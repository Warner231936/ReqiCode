package verify

import (
	"sort"
	"strings"
)

// FailureKind classifies why a command failed.
type FailureKind string

const (
	FailureNone             FailureKind = "none"
	FailureCompile          FailureKind = "compile_error"
	FailureTest             FailureKind = "test_failure"
	FailureLint             FailureKind = "lint_failure"
	FailureMissingTool      FailureKind = "missing_tool"
	FailureMissingDependency FailureKind = "missing_dependency"
	FailureTimeout          FailureKind = "timeout"
	FailurePermission       FailureKind = "permission_denied"
	FailureNetwork          FailureKind = "network"
	FailurePanic            FailureKind = "panic_or_crash"
	FailureUnknown          FailureKind = "unknown"
)

// Failure is a classified failure with guidance for the repair loop.
type Failure struct {
	Kind FailureKind `json:"kind"`
	// Message is the single most diagnostic line, extracted from the output.
	Message string `json:"message"`
	// Retryable says whether the identical command might succeed on a second
	// attempt. A compile error is not retryable; a network timeout is.
	Retryable bool `json:"retryable"`
	// Advice tells the agent what class of change is needed.
	Advice string `json:"advice,omitempty"`
	// Details carries the focused output slice handed to the model. Full output
	// stays in the log; the model gets the part that names a defect, because a
	// truncated build log is mostly noise that displaces the actual error.
	Details string `json:"details,omitempty"`
}

// IsHard reports whether the failure requires a code change to resolve.
func (f Failure) IsHard() bool {
	switch f.Kind {
	case FailureCompile, FailureTest, FailureLint, FailurePanic, FailureMissingDependency:
		return true
	default:
		return false
	}
}

// ExtractDiagnostics pulls the lines that actually name a defect out of build or
// test output.
//
// This is what makes the failure/retry loop workable with a small local model. A
// full `go build ./...` failure on a large module can be tens of kilobytes; a
// 7B model given that produces noise. The lines with a file, line number, or a
// test name are the ones a repair needs, and they are usually under 4 KB.
func ExtractDiagnostics(output string, maxBytes int) string {
	if maxBytes <= 0 {
		maxBytes = 4000
	}
	lines := strings.Split(output, "\n")

	var (
		keep      []string
		keepFlags = make([]bool, len(lines))
	)

	mark := func(i int) {
		if i >= 0 && i < len(lines) {
			keepFlags[i] = true
		}
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		switch {
		case looksDiagnostic(trimmed):
			// Include the preceding line: for several toolchains the useful
			// context ("# package", "In function", the failing source line) is on
			// the line above the diagnostic itself.
			mark(i - 1)
			mark(i)
			mark(i + 1)
		}
	}

	for i, f := range keepFlags {
		if f {
			keep = append(keep, lines[i])
		}
	}
	if len(keep) == 0 {
		// Nothing matched: return the head, which for most tools is the error
		// block, rather than nothing at all.
		keep = lines
		if len(keep) > 40 {
			keep = keep[:40]
		}
	}
	out := strings.Join(keep, "\n")
	if len(out) > maxBytes {
		out = out[:maxBytes] + "\n... [diagnostics truncated]"
	}
	return out
}

func looksDiagnostic(line string) bool {
	lower := strings.ToLower(line)
	switch {
	case strings.HasPrefix(lower, "error"),
		strings.HasPrefix(lower, "warning:"),
		strings.HasPrefix(lower, "undefined"),
		strings.HasPrefix(lower, "cannot find"),
		strings.HasPrefix(lower, "no such file"),
		strings.Contains(line, ".go:"),
		strings.Contains(line, ".py:"),
		strings.Contains(line, ".rs:"),
		strings.Contains(line, ".cs:"),
		strings.Contains(line, ".java:"),
		strings.Contains(line, ".ts:"),
		strings.Contains(line, ".c:"),
		strings.Contains(line, ".cpp:"),
		strings.HasPrefix(lower, "--- fail:"),
		strings.HasPrefix(lower, "fail"),
		strings.HasPrefix(lower, "panic:"),
		strings.HasPrefix(lower, "traceback"),
		strings.Contains(lower, "assertionerror"),
		strings.Contains(lower, "undefined reference"),
		strings.Contains(lower, "undefined symbol"),
		strings.Contains(lower, "expected "),
		strings.Contains(lower, "syntaxerror"),
		strings.Contains(lower, "is not defined"),
		strings.Contains(lower, "cannot find name"),
		strings.Contains(lower, "cannot find module"),
		strings.Contains(lower, "does not implement"),
		strings.Contains(lower, "not assignable"),
		strings.Contains(lower, "mismatched types"),
		strings.Contains(lower, "no matching function"),
		strings.HasPrefix(lower, "thread '"),
		strings.HasPrefix(lower, "at "):
		return true
	}
	return false
}

// Summarize produces a short human-readable summary of a verification run.
func Summarize(results []RunResult) string {
	if len(results) == 0 {
		return "no verification commands were run"
	}
	var b strings.Builder
	pass, fail := 0, 0
	for _, r := range results {
		status := "PASS"
		switch {
		case r.Skipped != "":
			status = "SKIP"
		case r.Success:
			pass++
		default:
			fail++
			status = "FAIL"
		}
		fmtLine(&b, r, status)
	}
	return strings.TrimRight(b.String(), "\n")
}

func fmtLine(b *strings.Builder, r RunResult, status string) {
	dur := r.Duration.Round(1e6)
	switch status {
	case "SKIP":
		b.WriteString("SKIP  " + r.Line() + "  (" + r.Skipped + ")\n")
	case "PASS":
		b.WriteString("PASS  " + r.Line() + "  (" + dur.String() + ")\n")
	default:
		b.WriteString("FAIL  " + r.Line() + "  (" + dur.String() + ", " + string(r.Failure.Kind) + ")\n")
		if r.Failure.Message != "" {
			b.WriteString("      " + r.Failure.Message + "\n")
		}
	}
}

// HasFailure reports whether any result failed.
func HasFailure(results []RunResult) bool {
	for _, r := range results {
		if !r.Success && r.Skipped == "" {
			return true
		}
	}
	return false
}

// FirstFailure returns the earliest failing result.
func FirstFailure(results []RunResult) (RunResult, bool) {
	for _, r := range results {
		if !r.Success && r.Skipped == "" {
			return r, true
		}
	}
	return RunResult{}, false
}

// FailureKinds lists the distinct failure kinds present, for the run report.
func FailureKinds(results []RunResult) []string {
	seen := map[string]bool{}
	for _, r := range results {
		if r.Success || r.Skipped != "" {
			continue
		}
		seen[string(r.Failure.Kind)] = true
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}