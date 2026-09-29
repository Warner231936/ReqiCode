package tests

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

type TestRunner struct {
	workspacePath string
	// raceEnabled turns on the Go race detector. Data races are currently
	// invisible to the system otherwise, and they are the single most common
	// correctness defect in the concurrent code these templates generate
	// (the in-memory store is mutex-guarded, so a race here means the guard
	// was written wrong).
	raceEnabled bool
	// coverageEnabled requests a coverage profile, which Pillar 2 feeds back
	// into the attention manager so that untested code draws attention.
	coverageEnabled bool
}

func NewTestRunner(workspacePath string) *TestRunner {
	return &TestRunner{workspacePath: workspacePath}
}

// WithRace enables the race detector on subsequent runs.
func (t *TestRunner) WithRace(enabled bool) *TestRunner {
	t.raceEnabled = enabled
	return t
}

// WithCoverage enables coverage profile emission.
func (t *TestRunner) WithCoverage(enabled bool) *TestRunner {
	t.coverageEnabled = enabled
	return t
}

func (t *TestRunner) WorkspacePath() string {
	return t.workspacePath
}

// args builds the flag set for a test invocation. Kept in one place so the
// command string recorded in TestResult always matches what was actually run;
// a mismatch there would silently invalidate every causal attribution that
// depends on it.
//
// The race flag is conditional. `go test -race` requires cgo, and when the
// host has CGO_ENABLED=0 the flag makes the whole invocation fail before a
// single test runs. Emitting a race flag that cannot execute turns every clean
// project into a reported test failure, so it is only added when cgo can
// actually support it.
func (t *TestRunner) args(target string) []string {
	args := []string{"test", target, "-v", "-count=1"}
	if t.raceEnabled && cgoEnabled() {
		ensureCC()
		args = append(args, "-race")
	}
	if t.coverageEnabled {
		args = append(args, "-coverprofile=coverage.out")
	}
	return args
}

// cgoEnabled reports whether the Go toolchain can compile cgo, which is a
// prerequisite for the race detector.
func cgoEnabled() bool {
	v := os.Getenv("CGO_ENABLED")
	if v != "" {
		return v != "0"
	}
	// Env is unset. `go env` holds the authoritative answer, so ask it.
	out, err := exec.Command("go", "env", "CGO_ENABLED").Output()
	if err != nil {
		// No toolchain reachable; assume the conservative answer so we never
		// emit a race flag that cannot execute.
		return false
	}
	return strings.TrimSpace(string(out)) == "1"
}

// ensureCC makes sure a C compiler is reachable when the race detector is
// wanted. `go env -w CC=...` records the compiler but does not put it on PATH,
// and the linker still needs it there. This resolves the known MSYS2 layout so
// the race detector works from a plain shell without manual PATH surgery.
func ensureCC() {
	if runtime.GOOS != "windows" {
		return
	}
	if _, err := exec.LookPath(filepath.Base(os.Getenv("CC"))); err == nil {
		return
	}
	candidates := []string{
		`C:\tools\msys64\ucrt64\bin`,
		`C:\tools\msys64\mingw64\bin`,
		`C:\msys64\ucrt64\bin`,
		`C:\msys64\mingw64\bin`,
		`C:\mingw64\bin`,
	}
	for _, dir := range candidates {
		gcc := filepath.Join(dir, "gcc.exe")
		if _, err := os.Stat(gcc); err == nil {
			os.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			return
		}
	}
}

func (t *TestRunner) commandString(target string) string {
	return "go " + strings.Join(t.args(target), " ")
}

// CommandString exposes the exact command a full run will execute, so callers
// can log or publish it without duplicating flag construction.
func (t *TestRunner) CommandString() string {
	return t.commandString("./...")
}

func (t *TestRunner) RunAll(ctx context.Context) state.TestResult {
	tr, output, err := t.invoke(ctx, "./...")

	// If the race detector could not be engaged, retry without it rather than
	// reporting a failure that has nothing to do with the code under test.
	if err != nil && strings.Contains(string(output), "-race requires cgo") {
		t.raceEnabled = false
		tr, output, err = t.invoke(ctx, "./...")
		if tr.Stdout == "" {
			tr.Stdout = string(output)
		}
	}
	return tr
}

// invoke runs the suite once and assembles the result record.
func (t *TestRunner) invoke(ctx context.Context, target string) (state.TestResult, []byte, error) {
	start := time.Now()

	args := t.args(target)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = t.workspacePath
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	tr := state.TestResult{
		ID:       "go-test-all",
		Command:  t.commandString(target),
		Status:   state.TestPassed,
		Stdout:   string(output),
		Duration: duration.Milliseconds(),
	}

	if err != nil {
		tr.Status = state.TestFailed
		tr.FailureClass = classifyTestFailure(string(output))
		tr.Stderr = err.Error()
		return tr, output, err
	}

	if strings.Contains(string(output), "FAIL") {
		tr.Status = state.TestFailed
		tr.FailureClass = classifyTestFailure(string(output))
	}

	return tr, output, nil
}

// classifyTestFailure separates build errors from assertion failures from
// sanitizer reports. The distinction is load-bearing: compile failures, test
// failures, and data races call for different repair strategies, and the causal
// ledger weights them differently.
func classifyTestFailure(output string) string {
	switch {
	case strings.Contains(output, "DATA RACE"):
		return "data_race"
	case strings.Contains(output, "panic:"):
		return "panic"
	case strings.Contains(output, "build failed"),
		strings.Contains(output, "undefined:"),
		strings.Contains(output, "cannot use"),
		strings.Contains(output, "syntax error"),
		strings.Contains(output, "declared and not used"),
		strings.Contains(output, "imported and not used"),
		strings.Contains(output, "[build failed]"):
		return "build_error"
	case strings.Contains(output, "--- FAIL"):
		return "test_assertion_failure"
	case strings.Contains(output, "no Go files"):
		return "build_error"
	case strings.Contains(output, "setup failed"):
		return "build_error"
	default:
		return "test_error"
	}
}

func (t *TestRunner) RunGoTest(ctx context.Context, packagePath string) state.TestResult {
	start := time.Now()

	args := t.args(packagePath)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = t.workspacePath
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	tr := state.TestResult{
		ID:       "go-test-" + packagePath,
		Command:  t.commandString(packagePath),
		Status:   state.TestPassed,
		Stdout:   string(output),
		Duration: duration.Milliseconds(),
	}

	if err != nil {
		tr.Status = state.TestFailed
		tr.FailureClass = classifyTestFailure(string(output))
		tr.Stderr = err.Error()
	} else if strings.Contains(string(output), "FAIL") {
		tr.Status = state.TestFailed
		tr.FailureClass = classifyTestFailure(string(output))
	}

	return tr
}

func (t *TestRunner) Run(ctx context.Context, command string) state.TestResult {
	start := time.Now()

	parts := strings.Fields(command)
	if len(parts) == 0 {
		return state.TestResult{
			ID:      "empty-command",
			Command: command,
			Status:  state.TestFailed,
			Stderr:  "empty command",
		}
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = t.workspacePath
	output, err := cmd.CombinedOutput()
	duration := time.Since(start)

	tr := state.TestResult{
		ID:       "cmd-" + command,
		Command:  command,
		Status:   state.TestPassed,
		Stdout:   string(output),
		Duration: duration.Milliseconds(),
	}

	if err != nil {
		tr.Status = state.TestFailed
		tr.FailureClass = "command_error"
		tr.Stderr = err.Error()
	}

	return tr
}

func TestResultToEvidence(tr state.TestResult, unitID string) state.Evidence {
	evType := state.EvidenceTestPass
	content := "tests passed"
	if tr.Status == state.TestFailed {
		evType = state.EvidenceTestFailure
		content = "tests failed"
		if tr.FailureClass != "" {
			content += " (" + tr.FailureClass + ")"
		}
	}
	return state.Evidence{
		ID:       "",
		Type:     evType,
		Content:  content,
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(unitID),
	}
}

func TestResultJSON(tr state.TestResult) string {
	b, _ := json.Marshal(tr)
	return string(b)
}
