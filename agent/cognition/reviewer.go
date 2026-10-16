package cognition

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// ReviewRequest is what the reviewer is asked to judge.
type ReviewRequest struct {
	OriginalRequest string
	Diff            string
	ChangedFiles    []string
	Verification    string
	PriorFindings   []Finding
	// ModelIsAvailable reports whether a language model is reachable. When it is
	// not, the reviewer runs its mechanical checks only and says so.
	ModelIsAvailable bool
}

// ReviewOutcome is the reviewer's report.
type ReviewOutcome struct {
	Result       ReviewResult  `json:"result"`
	Mechanical   []Finding     `json:"mechanical"`
	Rounds       int           `json:"rounds"`
	Accepted     bool          `json:"accepted"`
	Degraded     bool          `json:"degraded"`
	DegradedNote string        `json:"degraded_note,omitempty"`
	Duration     time.Duration `json:"duration"`
}

// Reviewer evaluates a change before it is accepted.
type Reviewer struct {
	Router    *llm.Router
	Role      string
	MaxRounds int
	// Mechanical checks run regardless of model availability.
	Diff    *DiffInspector
	Project *verify.Project
}

// Review inspects a change and returns a verdict.
func (r *Reviewer) Review(ctx context.Context, req ReviewRequest) ReviewOutcome {
	start := time.Now()
	out := ReviewOutcome{Rounds: 1}

	// Mechanical checks first: they are cheap, deterministic, and catch the class
	// of problem a model most often misses, which is a change that touches more
	// files than the request required.
	if r.Diff != nil {
		out.Mechanical = r.Diff.Inspect(req.OriginalRequest, req.Diff, req.ChangedFiles)
	}

	if !req.ModelIsAvailable {
		out.Degraded = true
		out.DegradedNote = "no language model backend reachable; only mechanical review was performed"
		out.Result = mechanicalVerdict(req, out.Mechanical)
		out.Accepted = out.Result.Accepted()
		out.Duration = time.Since(start)
		return out
	}

	prior := ""
	if len(req.PriorFindings) > 0 {
		prior = RenderFindings(req.PriorFindings)
	}
	system, user := ReviewPrompt(req.OriginalRequest, req.Diff,
		strings.Join(req.ChangedFiles, "\n"), req.Verification, prior)

	msgs := []llm.Message{{Role: llm.RoleSystem, Content: system}, {Role: llm.RoleUser, Content: user}}

	resp, err := r.Router.Chat(ctx, r.Role, msgs, llm.Request{JSONMode: true, Temperature: 0.1})
	if err != nil {
		out.Degraded = true
		out.DegradedNote = "reviewer model call failed: " + err.Error()
		out.Result = mechanicalVerdict(req, out.Mechanical)
		out.Accepted = out.Result.Accepted()
		out.Duration = time.Since(start)
		return out
	}

	result, err := ParseReview(resp.Content)
	if err != nil {
		out.Degraded = true
		out.DegradedNote = "reviewer output could not be parsed: " + err.Error()
		out.Result = mechanicalVerdict(req, out.Mechanical)
		out.Accepted = out.Result.Accepted()
		out.Duration = time.Since(start)
		return out
	}

	// A mechanical finding of critical severity overrides an accepting model.
	for _, f := range out.Mechanical {
		if f.Severity == "critical" {
			result.Verdict = "reject"
			result.Findings = append([]Finding{f}, result.Findings...)
			break
		}
	}
	out.Result = result
	out.Accepted = result.Accepted()
	out.Duration = time.Since(start)
	return out
}

// mechanicalVerdict derives a verdict from the deterministic checks alone.
//
// It defaults to accept when the model is unavailable and no mechanical problem
// was found, because refusing everything would make an offline agent useless. It
// rejects on any critical or high finding, which is the conservative direction
// and the one that protects against a silent regression.
func mechanicalVerdict(req ReviewRequest, findings []Finding) ReviewResult {
	res := ReviewResult{Verdict: "accept", Findings: findings}
	for _, f := range findings {
		if f.Severity == "critical" || f.Severity == "high" {
			res.Verdict = "reject"
			break
		}
	}
	if res.Verdict == "accept" {
		res.Summary = "mechanical review only: no blocking finding detected. " +
			"This is not evidence that the change is correct; it is evidence that nothing " +
			"was checked that would have detected a problem."
	}
	if req.Verification != "" && strings.Contains(strings.ToLower(req.Verification), "fail") {
		res.Verdict = "reject"
		res.Findings = append(res.Findings, Finding{
			Severity: "critical",
			Problem:  "verification did not pass, so the change is not proven correct",
			Fix:      "fix the failure, or explain why it is unrelated to this change",
		})
		res.Summary = "verification failed"
	}
	return res
}

// DiffInspector performs deterministic checks on a diff.
type DiffInspector struct{}

// Inspect returns mechanical findings about a diff.
//
// Two checks earn their place. A file outside the request's stated scope means
// the agent touched something it was not asked to touch, which is the most common
// silent failure in an autonomous edit loop. A diff with no context lines at all
// usually means a whole file was replaced, which hides whatever was there.
func (d *DiffInspector) Inspect(request, diff string, changedFiles []string) []Finding {
	var out []Finding

	if len(changedFiles) == 0 && strings.TrimSpace(diff) != "" {
		out = append(out, Finding{
			Severity: "high",
			Problem:  "a diff was produced but no changed files could be identified",
			Fix:      "verify the change was actually written to disk",
		})
		return out
	}

	if len(changedFiles) > 12 {
		out = append(out, Finding{
			Severity: "medium",
			File:     strings.Join(changedFiles, ", "),
			Problem: fmt.Sprintf("the change touched %d files, which is unusually many for one request "+
				"(%s). Each unrelated edit is a chance to introduce a regression that no test covers",
				len(changedFiles), request),
			Fix: "confirm every listed file was required, and revert the ones that were not",
		})
	}

	for _, f := range changedFiles {
		if isGeneratedFile(f) {
			out = append(out, Finding{
				Severity: "medium", File: f,
				Problem: "the change modifies a generated file, which will be overwritten on the next generation",
				Fix:      "edit the generator's input or template instead",
			})
		}
	}

	if strings.Count(diff, "@@") == 0 && strings.TrimSpace(diff) != "" && !strings.HasPrefix(strings.TrimSpace(diff), "diff ") {
		out = append(out, Finding{
			Severity: "medium",
			Problem:  "the diff has no unified-diff hunk headers, so its scope cannot be verified",
			Fix:      "regenerate the diff with git diff so hunks are present",
		})
	}

	if looksLikeFullFileReplacement(diff) {
		out = append(out, Finding{
			Severity: "medium",
			Problem:  "every hunk in the diff replaces an entire file, which removes reviewable context",
			Fix:      "prefer edit_file or a narrow patch so the diff shows what actually changed",
		})
	}

	if hasSecretLikeLiteral(diff) {
		out = append(out, Finding{
			Severity: "critical",
			Problem:  "the diff contains a literal that resembles a credential",
			Fix:      "remove it and load the value from configuration or the environment",
		})
	}

	if added, removed := diffCounts(diff); added > 0 && removed == 0 {
		out = append(out, Finding{
			Severity: "low",
			Problem:  fmt.Sprintf("the change only adds %d line(s) and removes none, so nothing was replaced or deleted", added),
			Fix:      "confirm the intent was purely additive",
		})
	}

	return out
}

func diffCounts(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			removed++
		}
	}
	return added, removed
}

func looksLikeFullFileReplacement(diff string) bool {
	var hunks, removed, added int
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			hunks++
		case strings.HasPrefix(line, "-"):
			removed++
		case strings.HasPrefix(line, "+"):
			added++
		}
	}
	if hunks == 0 {
		return false
	}
	// A small number of very large hunks is the signature of wholesale
	// replacement rather than a targeted edit.
	return removed >= 40 && added >= 40 && hunks <= 2
}

func isGeneratedFile(path string) bool {
	l := strings.ToLower(path)
	switch {
	case strings.HasSuffix(l, ".pb.go"), strings.HasSuffix(l, "_pb2.py"),
		strings.HasSuffix(l, ".generated.go"), strings.HasSuffix(l, ".g.dart"),
		strings.HasSuffix(l, ".designer.cs"):
		return true
	}
	return strings.Contains(l, "/node_modules/") || strings.Contains(l, "/vendor/") ||
		strings.Contains(l, "/dist/") || strings.Contains(l, "/.gen/")
}

// hasSecretLikeLiteral looks for high-entropy assignments to credential names.
//
// This is a heuristic and only ever reports a suspicion, never a confirmed secret.
// It is included because committing a key is the kind of mistake that cannot be
// undone after the fact, and a false positive costs one look while a false negative
// can cost a credential.
func hasSecretLikeLiteral(diff string) bool {
	patterns := []string{
		`(?i)(api[_-]?key|secret|password|passwd|token|private[_-]?key)\s*[:=]\s*["'][^"']{12,}["']`,
		`-----BEGIN [A-Z ]*PRIVATE KEY-----`,
		`(?i)(aws_secret_access_key|aws_access_key_id)\s*[:=]`,
		`(?i)gh[pousr]_[A-Za-z0-9]{20,}`,
		`(?i)sk-[A-Za-z0-9]{20,}`,
	}
	addedOnly := ""
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			addedOnly += line + "\n"
		}
	}
	if strings.TrimSpace(addedOnly) == "" {
		return false
	}
	for _, p := range patterns {
		if regexp.MustCompile(p).MatchString(addedOnly) {
			return true
		}
	}
	return false
}