package state_test

import (
	"sync"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

func timeAfterShort() <-chan time.Time {
	return time.After(10 * time.Second)
}

func chained() *state.SemiState {
	ss := state.NewSemiState()
	ss.AppendRequirement(state.Requirement{ID: "req-1", Content: "do a thing"})
	ss.SetArchitecturePlan(&state.ArchitecturePlan{
		Description:   "Go CLI",
		AlternativeID: "plan-a",
		Components:    []state.ComponentSpec{{Name: "main", Path: "cmd/main.go"}},
	})
	ss.AddHypothesis(state.Hypothesis{ID: "hyp-1", Content: "design works", Confidence: state.ConfidenceMedium})
	ss.AddDecision(state.Decision{ID: "dec-1", Question: "which", Decision: "this"})
	ss.AddTestResult(state.TestResult{Command: "go test", Status: state.TestPassed})
	ss.GeneratedFiles["main.go"] = state.FileEntry{Path: "main.go"}
	return ss
}

func TestAppendChainedProducesGenesisLinkedChain(t *testing.T) {
	ss := state.NewSemiState()
	h, err := ss.AppendChained("first")
	if err != nil {
		t.Fatal(err)
	}
	if h.Prev != state.GenesisHash {
		t.Errorf("first link should chain from genesis, got prev=%q", h.Prev)
	}
	if len(h.Hash) != 64 {
		t.Errorf("expected a sha256 hex digest, got %q", h.Hash)
	}
}

func TestChainLinksAreSequential(t *testing.T) {
	ss := state.NewSemiState()
	first, _ := ss.AppendChained("one")
	ss.IncrementRevision()
	second, _ := ss.AppendChained("two")

	if second.Prev != first.Hash {
		t.Errorf("second link must chain from first: got %q want %q", second.Prev, first.Hash)
	}
	if second.Revision != 1 {
		t.Errorf("expected revision 1, got %d", second.Revision)
	}
	if ss.ChainHead() != second.Hash {
		t.Error("chain head must be the most recent link")
	}
}

func TestVerifyChainPassesOnUntouchedChain(t *testing.T) {
	ss := state.NewSemiState()
	for i := 0; i < 5; i++ {
		ss.AppendChained("step")
		ss.IncrementRevision()
	}
	v := ss.VerifyChain()
	if !v.Valid {
		t.Errorf("untampered chain must verify: %s", v.Reason)
	}
	if v.Checked != 5 {
		t.Errorf("expected 5 links checked, got %d", v.Checked)
	}
}

func TestVerifyChainPassesOnEmptyChain(t *testing.T) {
	ss := state.NewSemiState()
	v := ss.VerifyChain()
	if !v.Valid || v.Checked != 0 {
		t.Errorf("empty chain should be trivially valid, got %+v", v)
	}
	if v.Head != state.GenesisHash {
		t.Errorf("empty chain head should be genesis, got %q", v.Head)
	}
}

func TestComputeChainHashDiffersFromRecordedHeadAfterChange(t *testing.T) {
	// The property that matters operationally: recomputing the hash after the
	// state has changed must not match the recorded head. That is exactly what
	// makes retroactive editing detectable.
	ss := state.NewSemiState()
	recorded, _ := ss.AppendChained("one")

	ss.AddEvidence(state.Evidence{
		Type:     state.EvidenceAnalysis,
		Content:  "new evidence",
		Strength: state.ConfidenceHigh,
	})
	ss.IncrementRevision()

	recomputed, err := ss.ComputeChainHash(recorded.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if recomputed.Hash == recorded.Hash {
		t.Error("recomputing after a state change must yield a different hash")
	}
}

func TestVerifyChainDetectsNonMonotonicRevision(t *testing.T) {
	ss := state.NewSemiState()
	ss.AppendChained("one")
	ss.AppendChained("two") // same revision, no increment
	v := ss.VerifyChain()
	if v.Valid {
		t.Fatal("non-monotonic revisions must be rejected")
	}
}

func TestChainHashIsDeterministic(t *testing.T) {
	build := func() string {
		ss := chained()
		h, err := ss.AppendChained("s")
		if err != nil {
			t.Fatal(err)
		}
		return h.Hash
	}
	if build() != build() {
		t.Error("identical state must produce an identical chain hash")
	}
}

func TestChainHashIgnoresEvidenceOrdering(t *testing.T) {
	build := func(order int) string {
		ss := state.NewSemiState()
		ss.AppendRequirement(state.Requirement{ID: "r1", Content: "a"})
		ss.AppendRequirement(state.Requirement{ID: "r2", Content: "b"})
		if order == 0 {
			ss.AddDecision(state.Decision{ID: "d1", Decision: "x"})
			ss.AddDecision(state.Decision{ID: "d2", Decision: "y"})
		} else {
			ss.AddDecision(state.Decision{ID: "d2", Decision: "y"})
			ss.AddDecision(state.Decision{ID: "d1", Decision: "x"})
		}
		h, _ := ss.AppendChained("s")
		return h.Hash
	}
	if build(0) != build(1) {
		t.Error("insertion order must not change the canonical hash")
	}
}

func TestChainHashChangesWithContent(t *testing.T) {
	a := chained()
	b := chained()
	b.AddTestResult(state.TestResult{Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})

	ha, _ := a.AppendChained("s")
	hb, _ := b.AppendChained("s")
	if ha.Hash == hb.Hash {
		t.Error("a changed test outcome must change the chain hash")
	}
}

func TestChainSurvivesClone(t *testing.T) {
	ss := state.NewSemiState()
	ss.AppendChained("one")
	ss.IncrementRevision()
	ss.AppendChained("two")

	clone := ss.Clone()
	if len(clone.Chain()) != 2 {
		t.Errorf("clone must retain the chain, got %d links", len(clone.Chain()))
	}
	if clone.ChainHead() != ss.ChainHead() {
		t.Error("clone chain head must match the original")
	}
	if !clone.VerifyChain().Valid {
		t.Error("cloned chain must still verify")
	}
}

func TestCloneChainIsIndependent(t *testing.T) {
	ss := state.NewSemiState()
	ss.AppendChained("one")

	clone := ss.Clone()
	clone.AppendChained("from-clone")

	if len(ss.Chain()) != 1 {
		t.Errorf("appending to a clone must not affect the original, original has %d links", len(ss.Chain()))
	}
}

func TestChainDoesNotDeadlockUnderConcurrency(t *testing.T) {
	// The canonicalizer reads state under a caller-held lock. This test exists
	// because an earlier version re-acquired the read lock inside the
	// canonicalizer and deadlocked every run. It is the cheapest possible
	// regression guard for that class of bug.
	ss := state.NewSemiState()
	done := make(chan struct{})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			ss.AppendChained("concurrent")
		}
		close(done)
	}()

	select {
	case <-done:
	case <-timeAfterShort():
		t.Fatal("AppendChained deadlocked: canonicalizer re-acquires the state lock")
	}
	wg.Wait()
}

func TestConcurrentReadsAndChainsDoNotRace(t *testing.T) {
	ss := state.NewSemiState()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			ss.AppendChained("writer")
			ss.IncrementRevision()
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			ss.VerifyChain()
		}
	}()
	wg.Wait()
}
