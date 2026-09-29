package spiral_test

import (
	"context"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/spiral"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestPhaseString(t *testing.T) {
	tests := []struct {
		phase    spiral.Phase
		expected string
	}{
		{spiral.PhaseRequirement, "requirement"},
		{spiral.PhaseDecomposition, "decomposition"},
		{spiral.PhaseArchitecture, "architecture"},
		{spiral.PhaseSynthesis, "synthesis"},
		{spiral.PhaseCodeProposal, "code_proposal"},
		{spiral.PhaseCodeApplication, "code_application"},
		{spiral.PhaseBuild, "build"},
		{spiral.PhaseTest, "test"},
		{spiral.PhaseCritique, "critique"},
		{spiral.PhaseRevision, "revision"},
		{spiral.PhaseDocumentation, "documentation"},
		{spiral.PhaseFinalSynthesis, "final_synthesis"},
	}

	for _, tt := range tests {
		got := tt.phase.String()
		if got != tt.expected {
			t.Errorf("expected %s, got %s", tt.expected, got)
		}
	}
}

func TestIterationLifecycle(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	ctx := context.Background()
	ss := state.NewSemiState()

	var iterCompleted bool
	bus.Subscribe(events.EventSpiralIterationCompleted, func(_ context.Context, event events.Event) {
		iterCompleted = true
	})

	mgr := spiral.NewManager(ctx, bus, ss, 3)

	record := mgr.StartIteration()
	if record.ID != 1 {
		t.Errorf("expected iteration 1, got %d", record.ID)
	}

	mgr.UpdatePhase(spiral.PhaseRequirement)
	mgr.AddEvidence(state.Evidence{
		Type: state.EvidenceObservation,
		Content: "parsed requirements",
		Provenance: state.NewProvenance("test"),
	})
	mgr.AddDecision(state.Decision{
		ID: "dec-1",
		Question: "what to do?",
		Decision: "answer",
		Provenance: state.NewProvenance("test"),
	})

	completed := mgr.EndIteration("iteration complete")

	if completed.ID != 1 {
		t.Errorf("expected iteration 1, got %d", completed.ID)
	}
	if completed.Summary != "iteration complete" {
		t.Errorf("expected summary, got '%s'", completed.Summary)
	}
	if len(completed.NewEvidence) != 1 {
		t.Errorf("expected 1 evidence, got %d", len(completed.NewEvidence))
	}
	if len(completed.Decisions) != 1 {
		t.Errorf("expected 1 decision, got %d", len(completed.Decisions))
	}
	if completed.EndingState == nil {
		t.Error("expected non-nil ending state")
	}

	time.Sleep(100 * time.Millisecond)

	if !iterCompleted {
		t.Error("expected iteration completed event")
	}

	records := mgr.Records()
	if len(records) != 1 {
		t.Errorf("expected 1 record, got %d", len(records))
	}
}

func TestShouldContinue(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	ctx := context.Background()
	ss := state.NewSemiState()

	mgr := spiral.NewManager(ctx, bus, ss, 2)

	if !mgr.ShouldContinue() {
		t.Error("expected should continue on iteration 0")
	}

	mgr.StartIteration()
	mgr.EndIteration("first")

	if !mgr.ShouldContinue() {
		t.Error("expected should continue after iteration 1")
	}

	mgr.StartIteration()
	mgr.EndIteration("second")

	if mgr.ShouldContinue() {
		t.Error("expected should NOT continue after iteration 2")
	}
}

func TestIterationsCompleted(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	ctx := context.Background()
	ss := state.NewSemiState()

	mgr := spiral.NewManager(ctx, bus, ss, 5)

	if mgr.IterationsCompleted() != 0 {
		t.Errorf("expected 0, got %d", mgr.IterationsCompleted())
	}

	mgr.StartIteration()
	mgr.EndIteration("first")

	if mgr.IterationsCompleted() != 1 {
		t.Errorf("expected 1, got %d", mgr.IterationsCompleted())
	}
}

func TestFinalReport(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	ctx := context.Background()
	ss := state.NewSemiState()

	mgr := spiral.NewManager(ctx, bus, ss, 1)
	mgr.StartIteration()
	mgr.EndIteration("test iteration")

	report := mgr.FinalReport()
	if report == "" {
		t.Error("expected non-empty report")
	}
}
