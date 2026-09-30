package state_test

import (
	"testing"

	"github.com/kilo/spiral-codemaker/core/state"
)

// These are the direct counterexamples to the property tests. Each asserts that
// a specific illegal transition is actually refused, so a regression in the
// lifecycle rule cannot hide behind a property that happens not to generate the
// offending pair.

func TestTerminalRejectedCannotBeAccepted(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: state.HypothesisRejected})

	if err := ss.SetHypothesisStatus("h", state.HypothesisAccepted); err == nil {
		t.Fatal("a rejected hypothesis must not be resurrectable as accepted")
	}
	if got := ss.GetHypotheses()[0].Status; got != state.HypothesisRejected {
		t.Errorf("status must remain REJECTED after a refused transition, got %s", got)
	}
}

func TestTerminalSupersededCannotBeAccepted(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: state.HypothesisSuperseded})

	if err := ss.SetHypothesisStatus("h", state.HypothesisAccepted); err == nil {
		t.Fatal("a superseded hypothesis must not be resurrectable")
	}
}

func TestAcceptedCannotRevertToProposed(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: state.HypothesisAccepted})

	if err := ss.SetHypothesisStatus("h", state.HypothesisProposed); err == nil {
		t.Fatal("an accepted hypothesis must not silently revert to proposed")
	}
}

func TestLegalTransitionsAreAllowed(t *testing.T) {
	legal := []struct{ from, to state.HypothesisStatus }{
		{state.HypothesisProposed, state.HypothesisAccepted},
		{state.HypothesisProposed, state.HypothesisRejected},
		{state.HypothesisProposed, state.HypothesisSuperseded},
		{state.HypothesisAccepted, state.HypothesisRejected},
		{state.HypothesisAccepted, state.HypothesisSuperseded},
		{state.HypothesisAccepted, state.HypothesisAccepted},
		{state.HypothesisProposed, state.HypothesisProposed},
	}
	for _, c := range legal {
		ss := state.NewSemiState()
		ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: c.from})
		if err := ss.SetHypothesisStatus("h", c.to); err != nil {
			t.Errorf("transition %s -> %s should be legal: %v", c.from, c.to, err)
		}
	}
}

func TestIllegalTransitionsTable(t *testing.T) {
	illegal := []struct{ from, to state.HypothesisStatus }{
		{state.HypothesisRejected, state.HypothesisAccepted},
		{state.HypothesisRejected, state.HypothesisProposed},
		{state.HypothesisRejected, state.HypothesisSuperseded},
		{state.HypothesisSuperseded, state.HypothesisAccepted},
		{state.HypothesisSuperseded, state.HypothesisProposed},
		{state.HypothesisSuperseded, state.HypothesisRejected},
		{state.HypothesisAccepted, state.HypothesisProposed},
	}
	for _, c := range illegal {
		if state.LegalHypothesisTransition(c.from, c.to) {
			t.Errorf("transition %s -> %s must be illegal", c.from, c.to)
		}
		ss := state.NewSemiState()
		ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: c.from})
		if err := ss.SetHypothesisStatus("h", c.to); err == nil {
			t.Errorf("SetHypothesisStatus must refuse %s -> %s", c.from, c.to)
		}
		if got := ss.GetHypotheses()[0].Status; got != c.from {
			t.Errorf("after a refused %s -> %s the status changed to %s", c.from, c.to, got)
		}
	}
}

func TestUpdateHypothesisStatusEnforcesTheRule(t *testing.T) {
	// UpdateHypothesisStatus is the method every existing caller uses. If it
	// bypassed the rule the invariant would be decorative.
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: state.HypothesisRejected})

	_ = ss.UpdateHypothesisStatus("h", state.HypothesisAccepted)
	if got := ss.GetHypotheses()[0].Status; got != state.HypothesisRejected {
		t.Errorf("UpdateHypothesisStatus bypassed the lifecycle rule, status is %s", got)
	}
}

func TestTransitionErrorIsDescriptive(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "dead-design", Content: "c", Status: state.HypothesisRejected})

	err := ss.SetHypothesisStatus("dead-design", state.HypothesisAccepted)
	if err == nil {
		t.Fatal("expected an error")
	}
	te, ok := err.(*state.TransitionError)
	if !ok {
		t.Fatalf("expected a *TransitionError, got %T", err)
	}
	if te.ID != "dead-design" {
		t.Errorf("error must name the hypothesis, got %q", te.ID)
	}
	if te.From != state.HypothesisRejected || te.To != state.HypothesisAccepted {
		t.Errorf("error must record both endpoints, got %s -> %s", te.From, te.To)
	}
	if err.Error() == "" {
		t.Error("error message must not be empty")
	}
}

func TestTransitionOnUnknownIDIsNotAnError(t *testing.T) {
	ss := state.NewSemiState()
	if err := ss.SetHypothesisStatus("nonexistent", state.HypothesisAccepted); err != nil {
		t.Errorf("transitioning an unknown id should be a no-op, not an error: %v", err)
	}
}

func TestRefusedTransitionDoesNotBumpRevision(t *testing.T) {
	// A refused transition must not advance the claim's own revision, or the
	// history would record changes that never happened.
	ss := state.NewSemiState()
	ss.AddHypothesis(state.Hypothesis{ID: "h", Content: "c", Status: state.HypothesisRejected})
	before := ss.GetHypotheses()[0].Provenance.Revision

	_ = ss.SetHypothesisStatus("h", state.HypothesisAccepted)
	after := ss.GetHypotheses()[0].Provenance.Revision
	if after != before {
		t.Errorf("a refused transition must not bump revision (%d -> %d)", before, after)
	}
}
