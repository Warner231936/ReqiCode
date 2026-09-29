package state

import (
	"reflect"
	"strings"
	"testing"
)

// These tests live in the internal package because the property they verify —
// that retroactive history editing is detected — can only be exercised by
// writing to the unexported chain. That the field is unexported is itself part
// of the design: it stops ordinary callers from editing history at all, and this
// file is the only way to prove the detection still works if someone changes
// that.

func TestVerifyChainDetectsRewrittenIntermediateHash(t *testing.T) {
	ss := NewSemiState()
	ss.AppendChained("one") // links at revisions 0, 1, 2 below
	ss.IncrementRevision()
	ss.AppendChained("two")
	ss.IncrementRevision()
	ss.AppendChained("three")

	// Retroactive edit: rewrite the middle link's recorded hash without
	// recomputing its successor, which still claims the old value.
	ss.chain[1].Hash.Hash = strings.Repeat("f", 64)

	v := ss.VerifyChain()
	if v.Valid {
		t.Fatal("a rewritten intermediate hash must break the chain")
	}
	// The successor is the first link whose recorded predecessor no longer
	// matches the running head, so that is where the tamper surfaces.
	if v.BrokenAt != 2 {
		t.Errorf("break should surface at revision 2 (the first link after the edit), got %d", v.BrokenAt)
	}
	if !strings.Contains(v.Reason, "history was altered") {
		t.Errorf("reason should point at the alteration, got %q", v.Reason)
	}
}

func TestVerifyChainDetectsForgedPrevOnFirstLink(t *testing.T) {
	ss := NewSemiState()
	ss.AppendChained("one")
	ss.AppendChained("two")

	ss.chain[0].Hash.Prev = "deadbeef"

	v := ss.VerifyChain()
	if v.Valid {
		t.Fatal("a forged genesis link must be detected")
	}
	if v.BrokenAt != 0 {
		t.Errorf("break should be at revision 0, got %d", v.BrokenAt)
	}
}

func TestVerifyChainDetectsNonMonotonicRevision(t *testing.T) {
	ss := NewSemiState()
	ss.AppendChained("one")
	ss.AppendChained("two") // no increment between
	v := ss.VerifyChain()
	if v.Valid {
		t.Fatal("non-monotonic revisions must be rejected")
	}
	if !strings.Contains(v.Reason, "does not advance") {
		t.Errorf("reason should mention revision ordering, got %q", v.Reason)
	}
}

func TestVerifyChainDetectsTruncatedChain(t *testing.T) {
	ss := NewSemiState()
	for i := 0; i < 4; i++ {
		ss.AppendChained("s")
		ss.IncrementRevision()
	}
	// Deleting history must invalidate verification: the head no longer matches
	// what a full walk would produce.
	full := ss.ChainHead()
	ss.chain = ss.chain[:2]

	if ss.ChainHead() == full {
		t.Error("truncating the chain must change the head, otherwise edits are undetectable")
	}
	// A self-consistent truncated prefix still verifies structurally; the
	// detection of the removal comes from comparing heads, which is what the
	// regression baseline and persisted artifacts do.
	if !ss.VerifyChain().Valid {
		t.Error("a self-consistent prefix should still verify structurally")
	}
}

func TestChainFieldIsNotExported(t *testing.T) {
	// The enforcement is the lowercase field name itself: no package outside
	// this one can reference it. This test pins the name so a future refactor
	// that exports it is caught.
	f, ok := reflect.TypeOf(SemiState{}).FieldByName("chain")
	if !ok {
		t.Fatal("SemiState must retain an unexported chain field")
	}
	if f.IsExported() {
		t.Error("the chain field must not be exported; it would let callers rewrite history")
	}
}
