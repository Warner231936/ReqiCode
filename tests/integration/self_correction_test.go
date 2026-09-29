package integration_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/attention"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/spiral"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/units"
	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/execution/sandbox"
	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/routing"
	"github.com/kilo/spiral-codemaker/persistence"
)

func TestSelfCorrectionViaEvidence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-selfcorrect-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	wsPath := filepath.Join(tmpDir, "output")
	ws, err := workspace.NewWorkspace(wsPath)
	if err != nil {
		t.Fatal(err)
	}

	ss := state.NewSemiState()
	bus := events.NewEventBus()
	defer bus.Close()
	am := attention.NewManager(bus)
	sb := sandbox.NewSandbox(wsPath)
	router := routing.NewRouter()
	router.AddProvider("mock", provider.NewMockProvider("mock"))
	router.ConfigureDefaults()
	mem := persistence.NewPersistentMemory(filepath.Join(tmpDir, "memory"))
	prop := proposals.NewProposer()
	ctx := context.Background()
	sp := spiral.NewManager(ctx, bus, ss, 3)

	rt := units.NewRuntime(ss, bus, am, ws, sb, router, sp, prop, mem, &units.Config{
		ProjectRoot:   "Build a small HTTP service that stores TODO items.",
		MaxIterations: 3,
	})

	critic := units.NewCritic(rt)

	ss.SetArchitecturePlan(&state.ArchitecturePlan{
		Description:   "Test plan",
		Components:    []state.ComponentSpec{{Name: "main", Description: "test", Path: "cmd/server/main.go"}},
		Endpoints:     []state.EndpointSpec{{Method: "GET", Path: "/todos", Desc: "list"}},
		Confidence:    state.ConfidenceMedium,
		Provenance:    state.NewProvenance("test"),
		AlternativeID: "plan-A",
	})

	ss.AddFile(state.FileEntry{
		Path:        "cmd/server/main.go",
		Content:     "package main\n\nfunc main() {}",
		Provenance:  state.NewProvenance("test"),
	})
	ss.AddProposal(state.CodeProposal{
		ID:              "prop-1",
		File:            "cmd/server/main.go",
		Operation:       state.OpCreate,
		Status:          state.ProposalApplied,
		OriginatingUnit: "code_generator",
		Confidence:      state.ConfidenceHigh,
		Provenance:      state.NewProvenance("code_generator"),
	})

	ss.AddTestResult(state.TestResult{
		ID:          "go-test-all",
		Command:     "go test ./...",
		Status:      state.TestFailed,
		Stdout:      "",
		Stderr:      "compilation error: undefined: model.Todo",
		FailureClass: "compilation",
		Duration:    100,
		Provenance:  state.NewProvenance("test_runner"),
	})

	ss.AddEvidence(state.Evidence{
		Type:     state.EvidenceTestFailure,
		Content:  "test failure: compilation error",
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance("test_runner"),
	})

	initialObjectionCount := len(ss.GetObjections())
	initialConfidence := ss.GetConfidence()

	critic.Run(ctx, ss, bus)

	time.Sleep(50 * time.Millisecond)

	objections := ss.GetObjections()
	if len(objections) <= initialObjectionCount {
		t.Error("expected critic to add objections after detecting test failure")
	}

	firstObj := objections[initialObjectionCount]
	if firstObj.Severity != "high" {
		t.Errorf("expected high severity objection, got %s", firstObj.Severity)
	}

	if firstObj.Content == "" {
		t.Error("expected non-empty objection content")
	}

	synthesizer := units.NewSynthesizer(rt)
	synthesizer.Run(ctx, ss, bus)

	time.Sleep(50 * time.Millisecond)

	newConfidence := ss.GetConfidence()
	if newConfidence >= initialConfidence {
		t.Errorf("expected confidence to decrease after test failure, initial=%f, new=%f", float64(initialConfidence), float64(newConfidence))
	}

	findings := ss.Findings
	hasContradictedFinding := false
	for _, f := range findings {
		if f.NewStatus == state.FindingContradicted || f.NewStatus == state.FindingSupported {
			hasContradictedFinding = true
		}
	}
	if !hasContradictedFinding {
		t.Logf("findings: %+v", findings)
	}
}

func TestAttentionRedistributionOnTestFailure(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	am := attention.NewManager(bus)

	am.Set("critic", "initial", 0.1)
	am.Set("debugger", "dormant", 0.05)

	bus.Publish(events.EventTestFailed, "test_runner", map[string]interface{}{
		"test_result": state.TestResult{
			ID:      "go-test-all",
			Status:  state.TestFailed,
			Command: "go test",
			Stdout:  "",
			Stderr:  "FAIL",
		},
	})

	time.Sleep(100 * time.Millisecond)

	debuggerAtt := am.Get("debugger")
	if debuggerAtt.Weight <= 0.1 {
		t.Error("expected debugger attention to increase after test failure")
	}

	criticAtt := am.Get("critic")
	if criticAtt.Weight <= 0.1 {
		t.Error("expected critic attention to increase after test failure")
	}
}

func TestSpiralIterationChangesState(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-statechange-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 3)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	ss := orch.SemiState()

	if len(ss.GetRequirements()) != 0 {
		t.Error("expected no requirements initially")
	}

	initialRevision := ss.Revision

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if ss.Revision <= initialRevision {
		t.Error("expected revision to increase after run")
	}

	if len(ss.GetRequirements()) == 0 {
		t.Error("expected requirements to be added after run")
	}

	if ss.GetArchitecturePlan() == nil {
		t.Error("expected architecture plan after run")
	}

	if len(ss.GetEvidence()) == 0 {
		t.Error("expected evidence to accumulate")
	}

	if len(ss.GetTestResults()) == 0 {
		t.Error("expected test results after run")
	}

	if len(ss.GetHypotheses()) < 2 {
		t.Error("expected multiple hypotheses (competing designs)")
	}

	findings := ss.Findings
	hasSupported := false
	for _, f := range findings {
		if f.NewStatus == state.FindingSupported {
			hasSupported = true
		}
	}
	if !hasSupported {
		t.Error("expected at least one supported finding (self-correction)")
	}

	decisions := ss.GetDecisions()
	if len(decisions) == 0 {
		t.Error("expected decisions to be recorded")
	}

	records := orch.SpiralManager().Records()
	if len(records) != 3 {
		t.Errorf("expected 3 iteration records, got %d", len(records))
	}

	for _, rec := range records {
		if rec.StartingState == nil {
			t.Errorf("iteration %d: expected non-nil starting state", rec.ID)
		}
		if rec.EndingState == nil {
			t.Errorf("iteration %d: expected non-nil ending state", rec.ID)
		}
		if len(rec.Changes) == 0 {
			t.Logf("iteration %d: no changes recorded (may be expected)", rec.ID)
		}
	}
}

func TestCompetingHypothesesResolution(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-hyp-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 1)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	ss := orch.SemiState()
	hypotheses := ss.GetHypotheses()

	if len(hypotheses) < 2 {
		t.Fatalf("expected at least 2 hypotheses (competing designs), got %d", len(hypotheses))
	}

	hasProposed := false
	hasAccepted := false
	for _, h := range hypotheses {
		switch h.Status {
		case state.HypothesisProposed:
			hasProposed = true
		case state.HypothesisAccepted:
			hasAccepted = true
		}
	}

	if !hasAccepted {
		t.Error("expected at least one accepted hypothesis (synthesis selected a design)")
	}

	if !hasProposed {
		t.Error("expected at least one proposed hypothesis (competing design was not selected)")
	}
}
