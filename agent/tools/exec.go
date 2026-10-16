package tools

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// ExecConfig configures the command runner.
type ExecConfig struct {
	Workspace     *Workspace
	Policy        *safety.Policy
	Approver      safety.Approver
	Timeout       time.Duration
	MaxOutputBytes int
	// Shell selects how a command line is interpreted. Empty means "the platform
	// default", which is a real shell on both platforms and is what makes
	// `go test ./... && echo done` work as written.
	Shell string
	// AllowEnv lists environment variables to pass through in addition to the
	// inherited environment. Empty means inherit everything, which is the normal
	// case for local development.
	AllowEnv []string
	// History supplies recent failure summaries to an approval prompt, so an
	// operator is told what has already failed before approving a retry.
	History func() []string
}

// RunCommandTool executes a shell command under the safety policy.
type RunCommandTool struct{ Cfg ExecConfig }

func (t *RunCommandTool) Name() string  { return "run_command" }
func (t *RunCommandTool) Mutates() bool { return true }
func (t *RunCommandTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeTest, ModeRefactor}
}
func (t *RunCommandTool) Description() string {
	return "Run a shell command in the workspace. Returns exit code, stdout, stderr, and duration. Commands are classified by a safety policy; a confirmation-class command may require approval."
}
func (t *RunCommandTool) Params() []Param {
	return []Param{
		{Name: "command", Type: "string", Required: true, Description: "Command line to execute"},
		{Name: "timeout_sec", Type: "integer", Description: "Override the default timeout"},
	}
}

func (t *RunCommandTool) Run(ctx context.Context, args Args) Result {
	cmdLine := args.Str("command")
	if strings.TrimSpace(cmdLine) == "" {
		return FailMsg(t.Name(), "command is required")
	}

	verdict := t.Cfg.Policy.Classify(cmdLine)
	if verdict.Class == safety.Blocked {
		return Result{
			Tool: t.Name(), Success: false, Class: "blocked",
			Error: fmt.Sprintf("command blocked by safety policy (rule: %s): %s\nThe command was: %s",
				verdict.Rule, verdict.Reason, cmdLine),
			Hint: "do not retry this command; change the approach or ask the operator",
		}
	}

	if verdict.NeedsApproval() {
		var history []string
		if t.Cfg.History != nil {
			history = t.Cfg.History()
		}
		ap, err := t.Cfg.Approver.Approve(ctx, safety.Request{
			Command: cmdLine, Verdict: verdict, History: history,
		})
		if err != nil {
			return Result{Tool: t.Name(), Success: false, Class: "approval_error", Error: err.Error()}
		}
		if !ap.Granted {
			return Result{
				Tool: t.Name(), Success: false, Class: "not_approved",
				Error:   fmt.Sprintf("operator declined the command (%s): %s", ap.By, ap.Reason),
				Hint:    "choose a different approach that does not require this command",
				Stderr:  cmdLine,
			}
		}
		verdict.ApprovalBy = ap.By
		verdict.Executed = true
	}

	out, err := t.execute(ctx, cmdLine, args.Int("timeout_sec", 0))
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	return out
}

// execute runs a command line and returns the structured result.
//
// On Windows the command goes through the shell so that the operators the agent
// writes behave as written. On Unix an explicit shell is used too, rather than
// splitting on spaces, because splitting cannot handle quoting and would make
// `grep "two words"` a different command than intended.
func (t *RunCommandTool) execute(ctx context.Context, cmdLine string, timeoutOverride int) (Result, error) {
	timeout := t.Cfg.Timeout
	if v := timeoutOverride; v > 0 {
		timeout = time.Duration(v) * time.Second
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var c *exec.Cmd
	shell := t.Cfg.Shell
	if shell == "" {
		if runtime.GOOS == "windows" {
			shell = "cmd.exe"
			c = exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", cmdLine)
		} else {
			shell = "/bin/sh"
			c = exec.CommandContext(ctx, "/bin/sh", "-c", cmdLine)
		}
	} else if base := filepath.Base(shell); base == "cmd.exe" || base == "cmd" {
		c = exec.CommandContext(ctx, shell, "/d", "/s", "/c", cmdLine)
	} else {
		c = exec.CommandContext(ctx, shell, "-c", cmdLine)
	}

	c.Dir = t.Cfg.Workspace.Root
	if len(t.Cfg.AllowEnv) > 0 {
		c.Env = append(os.Environ(), t.Cfg.AllowEnv...)
	}

	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr

	start := time.Now()
	runErr := c.Run()
	dur := time.Since(start)

	exitCode := 0
	if runErr != nil {
		if ctx.Err() == context.DeadlineExceeded {
			// A timeout is a distinct failure from a non-zero exit: the command
			// may have been fine, so it must be labelled so the agent retries with
			// a longer budget instead of "fixing" a command that works.
			return Result{
				Tool: t.Name(), Success: false, Class: "timeout",
				ExitCode:   intPtr(-1),
				Stdout:     clipOutput(stdout.String(), t.Cfg.MaxOutputBytes),
				Stderr:     clipOutput(stderr.String(), t.Cfg.MaxOutputBytes),
				DurationMS: dur.Milliseconds(),
				Error:      fmt.Sprintf("command exceeded its %s timeout and was terminated", timeout),
				Hint:       "increase timeout_sec, or scope the command to less work",
			}, nil
		}
		var ee *exec.ExitError
		if asExit(runErr, &ee) {
			exitCode = ee.ExitCode()
		} else {
			return Result{}, fmt.Errorf("cannot execute %q: %w", cmdLine, runErr)
		}
	}

	res := CommandResult(t.Name(), cmdLine, exitCode,
		stdout.String(), stderr.String(), dur, t.Cfg.MaxOutputBytes)

	// Attach a failure classification so the agent knows whether a retry could
	// possibly help, without having to re-derive it from the text.
	if !res.Success {
		f := verify.ClassifyFailure(cmdLine, res.Stdout, res.Stderr, exitCode)
		res.Class = string(f.Kind)
		if f.Advice != "" {
			res.Hint = f.Advice + " (" + f.Message + ")"
		}
	}
	return res, nil
}

func intPtr(v int) *int { return &v }

func asExit(err error, target **exec.ExitError) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		*target = ee
		return true
	}
	return false
}

func clipOutput(s string, max int) string {
	if max <= 0 {
		max = 256 * 1024
	}
	if len(s) <= max {
		return s
	}
	head := max / 3
	tail := max - head
	return s[:head] + "\n... [truncated " + fmt.Sprint(len(s)-max) + " bytes] ...\n" + s[len(s)-tail:]
}

// BuildProjectTool runs the detected build command.
type BuildProjectTool struct {
	Cfg   ExecConfig
	Detected *verify.Project
}

func (t *BuildProjectTool) Name() string  { return "build_project" }
func (t *BuildProjectTool) Mutates() bool { return false }
func (t *BuildProjectTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeTest, ModeReview}
}
func (t *BuildProjectTool) Description() string {
	return "Run the project's detected build command. Returns the compiler output, which is the primary evidence that a change is correct."
}
func (t *BuildProjectTool) Params() []Param {
	return []Param{
		{Name: "kind", Type: "string", Enum: []string{"build", "test", "lint"}, Default: "build",
			Description: "Which detected command group to run"},
	}
}

func (t *BuildProjectTool) Run(ctx context.Context, args Args) Result {
	kind := args.StrDefault("kind", "build")
	if t.Detected == nil {
		return FailMsg(t.Name(), "no project detection is available; run inspect_project first")
	}
	var cmds []verify.Command
	for _, c := range t.Detected.All() {
		if c.Name == kind && !c.Optional {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return Result{
			Tool: t.Name(), Success: false, Class: "no_command",
			Error: fmt.Sprintf("no %s command was detected for this project (ecosystem: %s)", kind, t.Detected.Ecosystem),
			Hint: "use run_command with an explicit command, or add a build manifest so detection can work",
		}
	}

	runner := &verify.Runner{
		Project:  t.Detected,
		Timeout:  t.Cfg.Timeout,
		MaxBytes: t.Cfg.MaxOutputBytes,
	}
	runner.Timeout = t.Cfg.Timeout

	var out strings.Builder
	var results []verify.RunResult
	for _, c := range cmds {
		res := runner.Run(ctx, c)
		results = append(results, res)
		fmt.Fprintf(&out, "$ %s\n%s\n", res.Line(), strings.TrimRight(capped(res.Stdout+res.Stderr, 20000), "\n"))
		if !res.Success {
			break
		}
	}

	first, hasFailure := verify.FirstFailure(results)
	if hasFailure {
		return Result{
			Tool: t.Name(), Success: false,
			ExitCode:   intPtr(first.ExitCode),
			Stdout:     out.String(),
			Stderr:     first.Stderr,
			DurationMS: first.Duration.Milliseconds(),
			Class:      string(first.Failure.Kind),
			Error:      first.Failure.Message,
			Hint:       first.Failure.Advice,
			Data: map[string]any{
				"diagnostics": verify.ExtractDiagnostics(first.Stdout+first.Stderr, 4000),
				"commands":    len(results),
			},
		}
	}
	return Result{
		Tool: t.Name(), Success: true, Stdout: out.String(),
		Data: map[string]any{"commands": len(results)},
	}
}

// RunTestsTool runs the detected test command and parses the outcome.
type RunTestsTool struct {
	Cfg      ExecConfig
	Detected *verify.Project
}

func (t *RunTestsTool) Name() string  { return "run_tests" }
func (t *RunTestsTool) Mutates() bool { return false }
func (t *RunTestsTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeTest, ModeReview}
}
func (t *RunTestsTool) Description() string {
	return "Run the project's detected test suite and return structured results including which tests failed."
}
func (t *RunTestsTool) Params() []Param {
	return []Param{
		{Name: "filter", Type: "string", Description: "Pass a filter argument to the test runner, e.g. a -run pattern"},
	}
}

func (t *RunTestsTool) Run(ctx context.Context, args Args) Result {
	if t.Detected == nil {
		return FailMsg(t.Name(), "no project detection is available; run inspect_project first")
	}
	var cmds []verify.Command
	for _, c := range t.Detected.All() {
		if c.Name == "test" && !c.Optional {
			cmds = append(cmds, c)
		}
	}
	if len(cmds) == 0 {
		return Result{
			Tool: t.Name(), Success: false, Class: "no_command",
			Error: "no test command was detected for this project",
			Hint:  "use run_command with an explicit test command",
		}
	}
	if filter := args.Str("filter"); filter != "" {
		for i := range cmds {
			cmds[i].Args = append(cmds[i].Args, strings.Fields(filter)...)
		}
	}

	runner := &verify.Runner{Project: t.Detected, Timeout: t.Cfg.Timeout, MaxBytes: t.Cfg.MaxOutputBytes}

	var out strings.Builder
	passed, failed := 0, 0
	var firstFail *verify.RunResult
	for _, c := range cmds {
		res := runner.Run(ctx, c)
		fmt.Fprintf(&out, "$ %s\n", res.Line())
		if res.Skipped != "" {
			fmt.Fprintf(&out, "  skipped: %s\n", res.Skipped)
			continue
		}
		if res.Success {
			passed++
			fmt.Fprintf(&out, "  PASS (%s)\n", res.Duration.Round(time.Millisecond))
		} else {
			failed++
			if firstFail == nil {
				r := res
				firstFail = &r
			}
			fmt.Fprintf(&out, "  FAIL (%s)\n%s\n", res.Failure.Kind, capped(verify.ExtractDiagnostics(res.Stdout+res.Stderr, 6000), 6000))
		}
	}

	if firstFail != nil {
		return Result{
			Tool: t.Name(), Success: false,
			ExitCode: intPtr(firstFail.ExitCode), Stdout: out.String(),
			Class: string(firstFail.Failure.Kind), Error: firstFail.Failure.Message,
			Hint: firstFail.Failure.Advice,
			Data: map[string]any{
				"passed": passed, "failed": failed,
				"diagnostics": verify.ExtractDiagnostics(firstFail.Stdout+firstFail.Stderr, 4000),
			},
		}
	}
	return Result{
		Tool: t.Name(), Success: true, Stdout: out.String(),
		Data: map[string]any{"passed": passed, "failed": 0},
	}
}

func capped(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... [truncated]"
}