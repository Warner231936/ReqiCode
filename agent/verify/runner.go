package verify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Runner executes verification commands against a detected project.
type Runner struct {
	Project  *Project
	Timeout  time.Duration
	MaxBytes int
	Env      []string
}

// RunResult is the structured outcome of one verification command.
type RunResult struct {
	Kind     string        `json:"kind"`
	Command  string        `json:"command"`
	Args     []string      `json:"args,omitempty"`
	Success  bool          `json:"success"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
	Duration time.Duration `json:"duration"`
	Failure  Failure       `json:"failure"`
	Skipped  string        `json:"skipped,omitempty"`
}

// Line renders the command that was run.
func (r RunResult) Line() string {
	if len(r.Args) == 0 {
		return r.Command
	}
	return r.Command + " " + strings.Join(r.Args, " ")
}

// Run executes one command inside the project.
//
// Every distinction the result makes is load-bearing for the repair loop: a
// timeout is not a non-zero exit, a missing tool is not a build failure, and a
// missing directory is not a failing test. Collapsing any of them would send the
// agent to fix a problem it does not have.
func (rn *Runner) Run(ctx context.Context, cmd Command) RunResult {
	res := RunResult{Kind: cmd.Name, Command: strings.Join(cmd.Args, " ")}

	dir := rn.Project.Root
	if cmd.Dir != "" {
		dir = filepath.Join(rn.Project.Root, filepath.FromSlash(cmd.Dir))
	}
	if _, err := os.Stat(dir); err != nil {
		res.Skipped = "directory does not exist: " + cmd.Dir
		res.Success = true
		return res
	}
	if len(cmd.Args) == 0 {
		res.Skipped = "command has no executable"
		res.Success = true
		return res
	}

	timeout := rn.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	c := exec.CommandContext(runCtx, cmd.Args[0], cmd.Args[1:]...)
	c.Dir = dir
	if len(rn.Env) > 0 {
		c.Env = append(os.Environ(), rn.Env...)
	}

	start := time.Now()
	var stdout, stderr strings.Builder
	c.Stdout = &stdout
	c.Stderr = &stderr
	runErr := c.Run()
	res.Duration = time.Since(start)

	maxBytes := rn.MaxBytes
	if maxBytes <= 0 {
		maxBytes = 256 * 1024
	}
	res.Stdout = truncate(stdout.String(), maxBytes)
	res.Stderr = truncate(stderr.String(), maxBytes)

	if runErr != nil {
		if runCtx.Err() == context.DeadlineExceeded {
			res.Failure = Failure{Kind: FailureTimeout,
				Message: fmt.Sprintf("command exceeded %s and was terminated", timeout),
				Advice:  "raise the timeout or scope the command to less work; this is not a defect in the code"}
		} else if ee, ok := runErr.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
			res.Success = false
		} else {
			res.Failure = Failure{Kind: FailureMissingTool, Message: runErr.Error(),
				Advice: "the executable could not be started; check that it is installed"}
		}
	} else {
		res.Success = true
	}

	if !res.Success {
		if res.Failure.Kind == "" {
			res.Failure = ClassifyFailure(res.Command, res.Stdout, res.Stderr, res.ExitCode)
		}
		// Some runners report failures in stdout while exiting zero. Trusting the
		// exit code alone would turn a red suite green, which is the one error this
		// layer must never make.
		if res.Success && strings.EqualFold(cmd.Name, "test") && looksLikeTestFailure(res.Stdout) {
			res.Success = false
			res.Failure = ClassifyFailure(res.Command, res.Stdout, res.Stderr, res.ExitCode)
		}
	}
	return res
}

func looksLikeTestFailure(stdout string) bool {
	return testSummaryF.MatchString(stdout) && !strings.Contains(stdout, "0 failed")
}

// RunAll executes build, then test, then lint, stopping after a failed build.
//
// Stopping matters: a test suite run against code that does not compile produces
// failures that say nothing about the code, and reading them spends the repair
// budget on noise.
func (rn *Runner) RunAll(ctx context.Context, kinds ...string) []RunResult {
	if len(kinds) == 0 {
		kinds = []string{"build", "test", "lint"}
	}
	var out []RunResult
	for _, kind := range kinds {
		for _, cmd := range rn.Project.All() {
			if cmd.Name != kind || cmd.Optional {
				continue
			}
			res := rn.Run(ctx, cmd)
			out = append(out, res)
			if !res.Success {
				if kind == "build" {
					// A build failure makes test results uninterpretable.
					return out
				}
				// A test failure does not stop lint: lint findings are independent
				// evidence and a fix that satisfies the tests may still violate the
				// project's style rules.
			}
		}
	}
	return out
}