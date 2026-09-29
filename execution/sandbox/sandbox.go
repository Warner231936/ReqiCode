package sandbox

import (
	"context"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/execution/build"
	"github.com/kilo/spiral-codemaker/execution/tests"
)

type Sandbox struct {
	buildRunner *build.BuildRunner
	testRunner  *tests.TestRunner
}

func NewSandbox(workspacePath string) *Sandbox {
	return &Sandbox{
		buildRunner: build.NewBuildRunner(workspacePath),
		testRunner:  tests.NewTestRunner(workspacePath),
	}
}

func (s *Sandbox) BuildRunner() *build.BuildRunner {
	return s.buildRunner
}

func (s *Sandbox) TestRunner() *tests.TestRunner {
	return s.testRunner
}

func (s *Sandbox) RunCycle(ctx context.Context) (build.BuildResult, state.TestResult) {
	br := s.buildRunner.Build(ctx)
	var tr state.TestResult
	if br.Success {
		tr = s.testRunner.RunAll(ctx)
	} else {
		tr = state.TestResult{
			ID:           "skipped-build-failed",
			Command:      "skipped",
			Status:       state.TestSkipped,
			Stdout:       "build failed, tests skipped",
			FailureClass: "build_failure",
			Duration:     0,
		}
	}
	return br, tr
}

func (s *Sandbox) RunCycleWithTimeout(timeout time.Duration) (build.BuildResult, state.TestResult) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return s.RunCycle(ctx)
}

func (s *Sandbox) BuildAndTest(ctx context.Context) state.TestResult {
	br, tr := s.RunCycle(ctx)
	if !br.Success {
		tr.Status = state.TestFailed
		tr.Stderr = br.Error
	}
	return tr
}
