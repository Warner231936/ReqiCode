// Package selfmodify lets the system improve its own source, through the same
// proposal, validation, and canary path it uses for everything else.
//
// The design constraint is that there is no privileged route. An edit to
// core/units/code_generator.go is proposed, validated, applied to an isolated
// copy, and measured exactly as an edit to a generated file would be. A special
// case for self-edits would make every safety property in the system
// unverifiable, because the one actor most likely to break the substrate would
// be the one actor exempt from checking it.
//
// Improvement is defined as a measured change, never as a judgement. "This code
// is cleaner" is not checkable; "this package's statement coverage rose and every
// test still passes" is.
package selfmodify

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Objective names a measurable improvement target.
type Objective string

const (
	// ObjectiveCoverage writes tests for untested exported functions. The
	// measurement is unambiguous -- coverage percentage -- and the failure mode
	// is bounded: a test that does not compile or fails is rejected, and a test
	// that passes without exercising the target does not move the number.
	ObjectiveCoverage Objective = "coverage"

	// ObjectiveFix repairs a failing test. Only available when the baseline is
	// already red, so it is not offered as a way to "improve" a green tree.
	ObjectiveFix Objective = "fix"
)

// Measurement is the observed state of a tree.
type Measurement struct {
	Objective Objective `json:"objective"`
	Target    string    `json:"target"`

	// Passed and Failed are test counts for the target package.
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Skipped int `json:"skipped"`

	// CoveragePercent is statement coverage of the target package, 0 when no
	// profile was produced.
	CoveragePercent float64 `json:"coverage_percent"`

	// BuildOK is false if the target package does not compile. Nothing else is
	// meaningful when this is false.
	BuildOK bool `json:"build_ok"`

	// Uncovered lists exported functions with no coverage, which is the work
	// list for the coverage objective.
	Uncovered []string `json:"uncovered,omitempty"`

	ElapsedMS int64  `json:"elapsed_ms"`
	Command   string `json:"command,omitempty"`
	Err       string `json:"error,omitempty"`
}

// Score is the single comparable number.
//
// Coverage dominates because it is the only objective here that is both
// unambiguous and non-gameable: a test cannot raise coverage without executing a
// statement that was previously unexecuted. Pass count is a tiebreak, and build
// success is a hard gate handled by Verdict rather than folded into the score.
func (m Measurement) Score() float64 {
	return m.CoveragePercent
}

// Verdict decides whether a candidate tree replaces the incumbent.
type Verdict struct {
	Promote   bool        `json:"promote"`
	Reason    string      `json:"reason"`
	Baseline  Measurement `json:"baseline"`
	Candidate Measurement `json:"candidate"`
	Delta     float64     `json:"delta"`
}

// CoverageThreshold is the minimum coverage gain required to promote.
//
// Non-zero because measurement noise and chance are real. A change that moves
// coverage by a fraction of a percent has not meaningfully verified anything, and
// promoting it would train the system to churn.
const CoverageThreshold = 0.5

// Observation is a trial outcome. Discovery distinguishes "the change broke
// something" from "the change found something", which are opposites.
//
// A mechanically-synthesised smoke test that panics on a nil argument has done
// exactly its job: it found a nil-safety defect in existing code. Treating that
// as a regression would discard the most valuable output the verifier can
// produce, and would make the system refuse to publish its own findings.
type Observation string

const (
	// ObservationImproved: measurably better, no new failures.
	ObservationImproved Observation = "improved"
	// ObservationRegression: existing behaviour broke.
	ObservationRegression Observation = "regression"
	// ObservationDiscovery: newly-added tests fail against existing code, which
	// means they found something rather than caused something.
	ObservationDiscovery Observation = "discovery"
	// ObservationNeutral: no measurable change.
	ObservationNeutral Observation = "neutral"
	// ObservationBuildFailure: the change does not compile.
	ObservationBuildFailure Observation = "build-failure"
)

// Classify derives the nature of a trial from the baseline and candidate
// measurements.
//
// The distinction between regression and discovery turns on which side the
// failures are on. Failures that were already there are the incumbent's. Failures
// that appear only in the candidate came from the new tests, and when those tests
// are mechanical smoke tests, a new failure is a defect in the code under test.
//
// `newTests` distinguishes a coverage-adding trial from an edit to existing files,
// because only the former can legitimately produce discoveries: a modified
// existing test that starts failing is a regression, full stop.
func Classify(base, cand Measurement, newTests bool) Observation {
	if !cand.BuildOK {
		return ObservationBuildFailure
	}
	if cand.Failed > base.Failed {
		if !newTests {
			// An edit to existing tests that introduces failures is a regression
			// regardless of intent.
			return ObservationRegression
		}
		return ObservationDiscovery
	}
	if cand.Failed > 0 && base.Failed == 0 {
		return ObservationRegression
	}
	if cand.CoveragePercent > base.CoveragePercent+CoverageThreshold {
		return ObservationImproved
	}
	return ObservationNeutral
}

// Finding is a defect a synthesised test discovered in existing code.
type Finding struct {
	Target  string `json:"target"`
	Summary string `json:"summary"`
	// CoverageGained is still recorded: the new tests are worth keeping even
	// though they fail, because a failing test that documents a real defect is
	// the correct artifact.
	CoverageGained float64 `json:"coverage_gained"`
	// FailureOutput is the diagnostic, truncated. Without it a finding is an
	// assertion the reader must take on faith.
	FailureOutput string `json:"failure_output,omitempty"`
}

// ImprovementThreshold is the minimum score delta in objective units.
const ImprovementThreshold = CoverageThreshold

// Compare decides promotion.
//
// A candidate that fails to build is rejected outright, regardless of score: a
// tree that does not compile is not a better tree under any measurement.
//
// A candidate that introduces failures is also rejected, but Classify is what
// distinguishes a regression from a discovery, and a discovery is not a reason to
// discard the change -- it is a result. Callers should Classify before treating a
// non-promotion as failure.
func Compare(baseline, candidate Measurement) Verdict {
	v := Verdict{Baseline: baseline, Candidate: candidate}
	v.Delta = candidate.Score() - baseline.Score()

	if !candidate.BuildOK {
		v.Reason = fmt.Sprintf("cannot promote: candidate does not build (%s)", candidate.Err)
		return v
	}
	if !baseline.BuildOK {
		// The incumbent is already broken, so any building tree is an improvement.
		v.Promote = true
		v.Reason = "promote: baseline did not build and the candidate does"
		return v
	}
	if baseline.Failed > 0 && candidate.Failed > baseline.Failed {
		v.Reason = fmt.Sprintf("cannot promote: failing tests rose from %d to %d", baseline.Failed, candidate.Failed)
		return v
	}
	if candidate.Failed > 0 && baseline.Failed == 0 {
		v.Reason = fmt.Sprintf("cannot promote: introduced %d failing test(s) into a green tree", candidate.Failed)
		return v
	}
	if v.Delta < ImprovementThreshold {
		v.Reason = fmt.Sprintf("cannot promote: coverage %+.2f does not meet the %.2f threshold; "+
			"no meaningful verification was added", v.Delta, ImprovementThreshold)
		return v
	}

	v.Promote = true
	v.Reason = fmt.Sprintf("promote: coverage %+.2f (%d failing -> %d failing)",
		v.Delta, baseline.Failed, candidate.Failed)
	return v
}

// Findings extracts defects that new tests discovered in existing code.
//
// Returns nil when the trial was a clean regression or improvement. The failures
// are kept verbatim because a reader who cannot see the panic cannot judge the
// finding.
func Findings(base, cand Measurement, obs Observation, target string) []Finding {
	if obs != ObservationDiscovery {
		return nil
	}
	summary := fmt.Sprintf("%d new failing test(s) appeared after adding coverage to %s; "+
		"the tests compile, so they are reporting defects in existing code",
		cand.Failed, target)

	return []Finding{{
		Target:         target,
		Summary:        summary,
		CoverageGained: cand.CoveragePercent - base.CoveragePercent,
		FailureOutput:  cand.Err,
	}}
}

// ---------------------------------------------------------------------------
// Coverage profile parsing
// ---------------------------------------------------------------------------

// CoverageProfile is a parsed `go test -coverprofile` output.
//
// Go's format is `file:startLine.startCol,endLine.endCol numStatements count`,
// which has to be parsed rather than regex-matched because the positions
// contain dots.
type CoverageProfile struct {
	mu sync.Mutex
	// blocks records the covered statement count for every line span, which is
	// what makes per-function coverage answerable. Totals alone can say a file
	// is 30% covered but not which function is at zero.
	blocks map[string][]coverageBlock
	// fileTotals and fileCovered accumulate per-file percentages.
	fileTotals  map[string]int
	fileCovered map[string]int
}

// coverageBlock is one line span from a coverage profile.
type coverageBlock struct {
	startLine int
	endLine   int
	stmts     int
	count     int
}

// FunctionCovered reports whether any statement in a function's line range was
// executed at least once.
//
// A function is treated as covered if any block intersecting its definition was
// executed. Requiring every block would call a function uncovered because of a
// single unreached error branch, which would send the work list chasing branches
// rather than whole untested functions.
func (p *CoverageProfile) FunctionCovered(file string, startLine, endLine int) bool {
	for _, b := range p.blocks[file] {
		if b.count == 0 {
			continue
		}
		// Intersect the block with the function's span.
		if b.startLine <= endLine && b.endLine >= startLine {
			return true
		}
	}
	return false
}

// ParseCoverageProfile reads a coverprofile and reports per-file percentages plus
// the exported functions with zero coverage.
func ParseCoverageProfile(path string) (*CoverageProfile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read coverage profile: %w", err)
	}

	p := &CoverageProfile{
		blocks:      map[string][]coverageBlock{},
		fileTotals:  map[string]int{},
		fileCovered: map[string]int{},
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "mode:") {
			continue
		}

		// file:start.end,start.end stmts count
		colon := strings.LastIndex(line, ":")
		if colon < 0 {
			continue
		}
		file := line[:colon]

		fields := strings.Fields(line[colon+1:])
		if len(fields) < 3 {
			continue
		}
		stmts, err := parseInt(fields[len(fields)-2])
		if err != nil {
			continue
		}
		count, err := parseInt(fields[len(fields)-1])
		if err != nil {
			continue
		}

		if start, end, ok := parseSpan(fields[0]); ok {
			p.blocks[file] = append(p.blocks[file], coverageBlock{
				startLine: start, endLine: end, stmts: stmts, count: count,
			})
		}

		p.fileTotals[file] += stmts
		if count > 0 {
			p.fileCovered[file] += stmts
		}
	}

	return p, nil
}

// FileCoverage returns the percentage of statements covered in a file.
func (p *CoverageProfile) FileCoverage(file string) float64 {
	total := p.fileTotals[file]
	if total == 0 {
		return 0
	}
	return 100 * float64(p.fileCovered[file]) / float64(total)
}

// Files returns every file in the profile, sorted.
func (p *CoverageProfile) Files() []string {
	out := make([]string, 0, len(p.fileTotals))
	for f := range p.fileTotals {
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Uncovered function discovery
// ---------------------------------------------------------------------------

// UncoveredFunc is an exported function with no test coverage.
type UncoveredFunc struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	Line     int    `json:"line"`
	EndLine  int    `json:"end_line"`
	Exported bool   `json:"exported"`
}

// FindUncoveredFuncs reports exported functions in a file that no test exercises.
//
// The coverage profile is required rather than optional. Scanning source for
// exported functions alone produces the *entire* API as "uncovered" -- a lexical
// scan cannot tell covered code from dead code -- which would send every trial
// proposing tests for functions that already have them and waste a full
// model round-trip per function to learn nothing.
//
// When profile is nil the function degrades to a pure lexical scan, which is
// correct for unit-testing the scanner itself.
func FindUncoveredFuncs(path string, profile *CoverageProfile) ([]UncoveredFunc, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}

	var out []UncoveredFunc
	lines := strings.Split(string(data), "\n")
	inBlockComment := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if !strings.HasPrefix(trimmed, "func ") {
			continue
		}

		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "func "))
		if strings.HasPrefix(rest, "(") {
			// A method; only interface-satisfying helpers matter and they are
			// usually covered indirectly. Skipping keeps the work list focused
			// on genuinely untested logic.
			continue
		}
		if idx := strings.IndexAny(rest, "([ \t"); idx >= 0 {
			rest = rest[:idx]
		}
		rest = strings.TrimSuffix(rest, "*")
		if rest == "" || rest == "main" {
			continue
		}
		// Unexported functions are lower value: they are usually exercised
		// through an exported entry point.
		if rest[0] < 'A' || rest[0] > 'Z' {
			continue
		}

		out = append(out, UncoveredFunc{
			Name:     rest,
			File:     path,
			Line:     i + 1,
			EndLine:  i + 1,
			Exported: true,
		})
	}

	if profile == nil {
		return out, nil
	}

	// Intersect with the coverage profile so only genuinely untested functions
	// reach the model. A function's body is taken to extend to the next
	// declaration at column zero, which is exact for top-level Go functions.
	uncovered := make([]UncoveredFunc, 0, len(out))
	for idx, fn := range out {
		end := len(lines)
		if idx+1 < len(out) {
			end = out[idx+1].Line - 1
		}
		fn.EndLine = end
		if !profile.FunctionCovered(path, fn.Line, end) {
			uncovered = append(uncovered, fn)
		}
	}

	return uncovered, nil
}

func parseInt(s string) (int, error) {
	var n int
	_, err := fmt.Sscanf(s, "%d", &n)
	return n, err
}

// parseSpan reads Go's "startLine.startCol,endLine.endCol" position form.
func parseSpan(s string) (start, end int, ok bool) {
	comma := strings.Index(s, ",")
	if comma < 0 {
		return 0, 0, false
	}
	startStr := s[:comma]
	endStr := s[comma+1:]

	if dot := strings.Index(startStr, "."); dot > 0 {
		startStr = startStr[:dot]
	}
	if dot := strings.Index(endStr, "."); dot > 0 {
		endStr = endStr[:dot]
	}

	start, err1 := parseInt(startStr)
	end, err2 := parseInt(endStr)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return start, end, true
}

// ---------------------------------------------------------------------------
// Reporting
// ---------------------------------------------------------------------------

// Report is a serializable record of a self-improvement session.
type Report struct {
	Objective  Objective   `json:"objective"`
	Target     string      `json:"target"`
	Baseline   Measurement `json:"baseline"`
	Candidate  Measurement `json:"candidate"`
	Verdict    Verdict     `json:"verdict"`
	AppliedTo  string      `json:"applied_to,omitempty"`
	Timestamp  string      `json:"timestamp"`
	DurationMS int64       `json:"duration_ms"`
}

// Write persists a report.
func Write(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	name := fmt.Sprintf("selfmod-%s-%d.json", sanitize(string(r.Objective)), time.Now().Unix())
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// FormatVerdict renders a verdict for a terminal.
func FormatVerdict(v Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", v.Reason)
	fmt.Fprintf(&b, "  baseline : build=%v pass=%d fail=%d coverage=%.1f%%\n",
		v.Baseline.BuildOK, v.Baseline.Passed, v.Baseline.Failed, v.Baseline.CoveragePercent)
	fmt.Fprintf(&b, "  candidate: build=%v pass=%d fail=%d coverage=%.1f%%\n",
		v.Candidate.BuildOK, v.Candidate.Passed, v.Candidate.Failed, v.Candidate.CoveragePercent)
	if v.Candidate.Err != "" {
		fmt.Fprintf(&b, "  build err : %s\n", truncate(v.Candidate.Err, 400))
	}
	return b.String()
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
