package units

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/execution/tests"
)

type TestRunnerUnit struct {
	*BaseUnit
	rt         *Runtime
	testRunner *tests.TestRunner
}

func NewTestRunner(rt *Runtime, wsPath string) *TestRunnerUnit {
	return &TestRunnerUnit{
		BaseUnit:   NewBaseUnit("unit-test-runner", state.RoleTestRunner, "TestRunner", state.CadenceFast, state.ActivationOnEvent),
		rt:         rt,
		testRunner: tests.NewTestRunner(wsPath).WithRace(true).WithCoverage(true),
	}
}

func (u *TestRunnerUnit) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	if !semiState.HasGeneratedFiles() {
		return nil, nil
	}

	if !semiState.HasTestFiles() {
		return nil, nil
	}

	if semiState.TestsAlreadyRan() {
		return nil, nil
	}

	bus.Publish(events.EventTestStarted, u.ID(), map[string]interface{}{
		"command": u.testRunner.CommandString(),
	})

	tr := u.testRunner.RunAll(ctx)
	semiState.AddTestResult(tr)

	ev := state.Evidence{
		Type:       state.EvidenceObservation,
		Content:    fmt.Sprintf("tests completed: %s", tr.Status),
		Strength:   state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	}
	if tr.Status == state.TestFailed {
		ev.Type = state.EvidenceTestFailure
		ev.Content = fmt.Sprintf("tests failed: %s", tr.FailureClass)
	} else {
		ev.Type = state.EvidenceTestPass
	}
	semiState.AddEvidence(ev)

	var evts []events.Event
	if tr.Status == state.TestPassed {
		evts = append(evts, events.Event{
			Type:     events.EventTestPassed,
			SourceID: u.ID(),
			Payload:  events.PayloadForTestResult(tr),
		})
		u.rt.Attention.Boost("critic", "tests passed, ready for critique", 0.3)
	} else {
		evts = append(evts, events.Event{
			Type:     events.EventTestFailed,
			SourceID: u.ID(),
			Payload:  events.PayloadForTestResult(tr),
		})
		u.rt.Attention.Boost("debugger", "test failures detected, debugging needed", 0.5)
		u.rt.Attention.Boost("code_generator", "test failures require code revision", 0.3)
		u.rt.Attention.Boost("consistency_checker", "test failures affect consistency", 0.2)
	}

	return evts, nil
}

func (u *TestRunnerUnit) RunWithTimeout(timeout time.Duration) (state.TestResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return u.testRunner.RunAll(ctx), nil
}

func (u *TestRunnerUnit) ExtractFailureDetails(tr state.TestResult) []FailureDetail {
	var details []FailureDetail
	lines := splitLines(tr.Stdout)
	for _, line := range lines {
		if strings.Contains(line, "FAIL") || strings.Contains(line, "Error") || strings.Contains(line, "panic") {
			details = append(details, FailureDetail{
				Line:     line,
				Type:     classifyFailure(line),
				Severity: "high",
			})
		}
	}
	if tr.Status == state.TestFailed && tr.FailureClass != "" {
		details = append(details, FailureDetail{
			Line:     fmt.Sprintf("Failure class: %s", tr.FailureClass),
			Type:     "classified",
			Severity: "critical",
		})
	}
	return details
}

type FailureDetail struct {
	Line     string `json:"line"`
	Type     string `json:"type"`
	Severity string `json:"severity"`
}

func classifyFailure(line string) string {
	if strings.Contains(line, "panic") {
		return "panic"
	}
	if strings.Contains(line, "expected") {
		return "assertion"
	}
	if strings.Contains(line, "compile") {
		return "compilation"
	}
	if strings.Contains(line, "undefined") {
		return "reference"
	}
	return "unknown"
}

func splitLines(s string) []string {
	var lines []string
	current := ""
	for _, ch := range s {
		if ch == '\n' {
			lines = append(lines, current)
			current = ""
		} else {
			current += string(ch)
		}
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}
