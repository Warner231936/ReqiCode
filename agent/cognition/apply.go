package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/task"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// ApplyResult is the outcome of executing a plan file.
type ApplyResult struct {
	Plan         *Plan              `json:"plan"`
	Applied      []string           `json:"applied"`
	Skipped      []string           `json:"skipped"`
	Failed       []string           `json:"failed"`
	Verification []verify.RunResult `json:"verification"`
	Verified     bool               `json:"verified"`
	Attempts     int                `json:"attempts"`
	Diff         string             `json:"diff"`
	Review       *ReviewOutcome     `json:"review,omitempty"`
	Duration     time.Duration      `json:"duration"`
}

// Render formats an apply result for the terminal.
func (r *ApplyResult) Render() string {
	var b strings.Builder
	b.WriteString("\n=== APPLY RESULT ===\n")
	fmt.Fprintf(&b, "applied      : %d item(s)\n", len(r.Applied))
	for _, a := range r.Applied {
		fmt.Fprintf(&b, "  + %s\n", a)
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(&b, "skipped      : %d\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(&b, "  - %s\n", s)
		}
	}
	if len(r.Failed) > 0 {
		fmt.Fprintf(&b, "failed       : %d\n", len(r.Failed))
		for _, f := range r.Failed {
			fmt.Fprintf(&b, "  ! %s\n", f)
		}
	}
	fmt.Fprintf(&b, "verified     : %v\n", r.Verified)
	if len(r.Verification) > 0 {
		b.WriteString("\nverification:\n")
		for _, line := range strings.Split(verify.Summarize(r.Verification), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	if r.Review != nil {
		fmt.Fprintf(&b, "\nreview       : %s\n", r.Review.Result.Verdict)
		for i, f := range r.Review.Result.Findings {
			if i >= 5 {
				break
			}
			fmt.Fprintf(&b, "  [%s] %s\n", f.Severity, truncate(f.Problem, 200))
		}
	}
	fmt.Fprintf(&b, "duration     : %s\n", r.Duration.Round(time.Millisecond))
	return b.String()
}

// OK reports whether every step applied and verification passed.
func (r *ApplyResult) OK() bool {
	return len(r.Failed) == 0 && r.Verified
}

// ApplyPlan executes the steps of a plan that name a concrete edit.
//
// This exists so the agent is genuinely runnable with no language model at all. A
// plan file plus verification is a complete, auditable path from intent to a tested
// change, and it is the same code path the model-driven loop uses for edits, so
// there is only one implementation of "apply an edit and check it".
//
// Only steps that name an edit are executed. Inspection steps are already done by
// the time a plan file exists, and re-running them would cost time without
// producing evidence.
func (a *Agent) ApplyPlan(ctx context.Context, plan *Plan, opts ApplyOptions) (*ApplyResult, error) {
	start := time.Now()
	out := &ApplyResult{Plan: plan}

	if plan == nil || len(plan.Steps) == 0 {
		return out, fmt.Errorf("the plan contains no steps")
	}
	mode := Implement
	if plan.Mode != "" {
		if m, err := ParseMode(plan.Mode); err == nil {
			mode = m
		}
	}

	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	out.Attempts = maxAttempts

	t := task.NewTask(plan.Request, string(mode), maxAttempts)
	t.Status = task.StatusRunning
	a.save(t)

	a.progress("applying %d plan step(s) from %s", len(plan.Steps), plan.Source)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		for i, step := range plan.Steps {
			if err := ctx.Err(); err != nil {
				return out, err
			}
			args, ok := editArgs(step)
			if !ok {
				out.Skipped = append(out.Skipped,
					fmt.Sprintf("step %d (%s): no concrete edit named", stepID(step, i), step.Description))
				continue
			}
			toolName := step.Tool
			if toolName == "" || toolName == "read_file" || toolName == "inspect_project" {
				toolName = inferEditTool(step)
			}
			res := a.Tools.Registry.Invoke(ctx, toolName, tools.NewArgs(args))
			toolSummary := fmt.Sprintf("step %d: %s -> %s", stepID(step, i), toolName, step.Description)
			a.Log.Tool(string(PhaseExecute), toolName, args, res.Success, toolSummary, 0, res.ExitCode)

			if !res.Success {
				out.Failed = append(out.Failed,
					fmt.Sprintf("%s: %s", toolSummary, firstNonEmpty(res.Error, truncate(res.Stderr, 200))))
				a.logFailure(PhaseExecute, toolSummary+" failed", firstNonEmpty(res.Error, res.Stderr), nil)
				continue
			}
			out.Applied = append(out.Applied, toolSummary)
			for _, p := range changedPaths(Decision{Args: args}) {
				out.Applied = append(out.Applied, p)
				t.AddFileChanged(p)
			}
			a.progress("  %s", toolSummary)
		}

		out.Diff = a.currentDiff(ctx)
		vr := a.verify(ctx, mode)
		out.Verification = vr
		out.Verified = !verify.HasFailure(vr)
		if out.Verified {
			break
		}
		if attempt >= maxAttempts {
			first, _ := verify.FirstFailure(vr)
			out.Failed = append(out.Failed, fmt.Sprintf("verification: %s: %s",
				first.Line(), first.Failure.Message))
			break
		}
		a.progress("verification failed; retrying (attempt %d/%d)", attempt, maxAttempts)
		a.progress("  %s", firstNonEmpty(firstFailureLine(vr), "failure"))
	}

	// Review a plan-applied change on the same terms as a model-applied one.
	if a.ReviewEnabled && opts.Review && len(changedPathsOf(out)) > 0 {
		review := a.reviewer().Review(ctx, ReviewRequest{
			OriginalRequest:  plan.Request,
			Diff:             out.Diff,
			ChangedFiles:     changedPathsOf(out),
			Verification:     verify.Summarize(out.Verification),
			ModelIsAvailable: a.modelAvailable(),
		})
		out.Review = &review
	}

	now := time.Now()
	t.FinishedAt = &now
	t.Status = task.StatusComplete
	if !out.OK() {
		t.Status = task.StatusFailed
	}
	t.Summary = summarizeApply(out)
	a.save(t)

	out.Duration = time.Since(start)
	return out, nil
}

// ApplyOptions configures plan application.
type ApplyOptions struct {
	MaxAttempts int
	Review      bool
}

// editArgs extracts the arguments of a concrete edit from a step.
//
// Three shapes are accepted because all three are what people and models actually
// write: a file plus full content, a file plus an exact replacement, and a unified
// diff. Anything else is reported as having no concrete edit rather than guessed
// at, because a guessed edit to a source file is worse than no edit.
func editArgs(step PlanStep) (map[string]any, bool) {
	if len(step.Files) == 0 {
		return nil, false
	}
	target := step.Files[0]

	if content, ok := stepFields(step, "content", "new_content", "full_content", "text", "body"); ok && content != "" {
		return map[string]any{"path": target, "content": content}, true
	}
	oldText, hasOld := stepFields(step, "old_text", "old", "before", "find", "original", "old_string")
	newText, hasNew := stepFields(step, "new_text", "new", "after", "replace", "replacement", "new_string")
	if hasOld && hasNew {
		args := map[string]any{"path": target, "old_text": oldText, "new_text": newText}
		if v, ok := stepFields(step, "replace_all", "all"); ok && truthy(v) {
			args["replace_all"] = true
		}
		return args, true
	}
	if diff, ok := stepFields(step, "diff", "patch", "unified_diff"); ok && strings.Contains(diff, "---") {
		return map[string]any{"diff": diff}, true
	}
	// A step whose description explicitly says to create the file, with the body
	// carried in the description, is still executable.
	lower := strings.ToLower(step.Description)
	if strings.Contains(lower, "creat") && strings.Contains(step.Description, "\n") {
		parts := strings.SplitN(step.Description, "\n", 2)
		return map[string]any{"path": target, "content": parts[1]}, true
	}
	return nil, false
}

// stepFields reads a scalar field from a step.
//
// Plan steps arrive as JSON objects whose values may be strings, numbers, or
// booleans, and the natural language keys vary. Reading them through one function
// keeps the extraction rules in a single place.
func stepFields(step PlanStep, keys ...string) (string, bool) {
	raw := step.raw()
	for _, k := range keys {
		if v, ok := raw[k]; ok {
			s := toString(v)
			if s != "" {
				return s, true
			}
		}
	}
	return "", false
}

func truthy(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true", "yes", "1", "y", "on":
		return true
	}
	return false
}

func inferEditTool(step PlanStep) string {
	lower := strings.ToLower(step.Description)
	switch {
	case strings.Contains(lower, "creat") || strings.Contains(lower, "new file") || strings.Contains(lower, "add a file"):
		return "write_file"
	case strings.Contains(lower, "delet") || strings.Contains(lower, "remove the file"):
		return "delete_file"
	default:
		return "edit_file"
	}
}

func stepID(step PlanStep, idx int) int {
	if step.ID > 0 {
		return step.ID
	}
	return idx + 1
}

func changedPathsOf(out *ApplyResult) []string {
	seen := map[string]bool{}
	var paths []string
	for _, s := range out.Applied {
		if strings.Contains(s, "/") || strings.Contains(s, ".go") || strings.Contains(s, ".py") {
			if !seen[s] {
				seen[s] = true
				paths = append(paths, s)
			}
		}
	}
	return paths
}

func firstFailureLine(results []verify.RunResult) string {
	first, ok := verify.FirstFailure(results)
	if !ok {
		return ""
	}
	return first.Line() + ": " + first.Failure.Message
}

func summarizeApply(out *ApplyResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d step(s) applied, %d skipped, %d failed; verified=%v",
		len(out.Applied), len(out.Skipped), len(out.Failed), out.Verified)
	if out.Review != nil {
		fmt.Fprintf(&b, "; review=%s", out.Review.Result.Verdict)
	}
	return b.String()
}