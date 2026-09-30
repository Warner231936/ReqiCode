package state

import "fmt"

// Hypothesis lifecycle rules.
//
// A hypothesis is a belief the system holds about the world. The whole design
// rests on a belief being able to *die*: once evidence kills a hypothesis, the
// evidence that killed it must not be silently discardable. Without a transition
// rule, any unit can flip a REJECTED claim back to ACCEPTED and the record of
// why it was killed simply disappears.
//
// Terminal states are REJECTED and SUPERSEDED. Neither can be undone. ACCEPTED
// can be contradicted later, because a design that passed tests today can be
// disproved tomorrow; that is not resurrection, that is revision.

// LegalHypothesisTransition reports whether moving from one status to another is
// permitted.
//
// Exported because units across packages need to ask before attempting a change,
// and because the property tests assert against this single definition rather
// than a reimplementation of it.
func LegalHypothesisTransition(from, to HypothesisStatus) bool {
	// A no-op is always legal; callers use it to assert a state without change.
	if from == to {
		return true
	}

	switch from {
	case HypothesisProposed:
		// A proposal may be accepted, rejected, or overtaken by a better design.
		return to == HypothesisAccepted ||
			to == HypothesisRejected ||
			to == HypothesisSuperseded

	case HypothesisAccepted:
		// An accepted design stays accepted unless it is disproved or replaced.
		// It may not silently revert to PROPOSED: that would erase the fact
		// that it was ever chosen.
		return to == HypothesisRejected || to == HypothesisSuperseded

	case HypothesisRejected, HypothesisSuperseded:
		// Terminal. Nothing follows.
		return false
	}

	// An unrecognised source status cannot be trusted to have known rules.
	return false
}

// TransitionError explains why a transition was refused.
type TransitionError struct {
	ID   string
	From HypothesisStatus
	To   HypothesisStatus
}

func (e *TransitionError) Error() string {
	return fmt.Sprintf("hypothesis %q: illegal transition %s -> %s (terminal states cannot be revived)", e.ID, e.From, e.To)
}

// SetHypothesisStatus attempts a transition and reports whether it was applied.
//
// Rejected transitions are reported rather than silently ignored: a caller that
// believes it rejected a design must be able to tell whether that actually
// happened. Silently accepting the call and doing nothing is how a system ends
// up believing a design is live when it was killed two iterations ago.
func (s *SemiState) SetHypothesisStatus(id string, to HypothesisStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	for i := range s.Hypotheses {
		if s.Hypotheses[i].ID != id {
			continue
		}
		from := s.Hypotheses[i].Status
		if !LegalHypothesisTransition(from, to) {
			return &TransitionError{ID: id, From: from, To: to}
		}
		s.Hypotheses[i].Status = to
		s.Hypotheses[i].Provenance.Revision++
		return nil
	}
	return nil
}
