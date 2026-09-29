package conflicts_test

import (
	"context"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/core/conflicts"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestRegisterConflict(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	mgr := conflicts.NewManager(bus)

	var received bool
	bus.Subscribe(events.EventConflictDetected, func(_ context.Context, event events.Event) {
		received = true
	})

	conflict := mgr.RegisterConflict(
		"test conflict subject",
		[]state.Claim{
			{Content: "claim A", Provenance: state.NewProvenance("unit-1")},
			{Content: "claim B", Provenance: state.NewProvenance("unit-2")},
		},
		[]state.Evidence{},
		[]string{"file.go"},
		state.ConflictHigh,
		[]string{"unit-1", "unit-2"},
	)

	time.Sleep(50 * time.Millisecond)

	if !received {
		t.Error("expected conflict detected event")
	}
	if conflict.Subject != "test conflict subject" {
		t.Errorf("expected 'test conflict subject', got '%s'", conflict.Subject)
	}
	if conflict.Resolved {
		t.Error("expected unresolved conflict")
	}
}

func TestResolveConflict(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	mgr := conflicts.NewManager(bus)

	conflict := mgr.RegisterConflict(
		"subject",
		[]state.Claim{{Content: "A"}},
		[]state.Evidence{},
		[]string{"f.go"},
		state.ConflictMedium,
		[]string{"u1"},
	)

	ok := mgr.Resolve(conflict.ID, "resolved by evidence")
	if !ok {
		t.Error("expected resolve to return true")
	}

	resolved, _ := mgr.Get(conflict.ID)
	if !resolved.Resolved {
		t.Error("expected conflict to be resolved")
	}
	if resolved.Resolution != "resolved by evidence" {
		t.Errorf("expected resolution text, got '%s'", resolved.Resolution)
	}
}

func TestActiveConflicts(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	mgr := conflicts.NewManager(bus)

	mgr.RegisterConflict("c1", []state.Claim{}, []state.Evidence{}, []string{}, state.ConflictLow, []string{})
	mgr.RegisterConflict("c2", []state.Claim{}, []state.Evidence{}, []string{}, state.ConflictHigh, []string{})

	active := mgr.Active()
	if len(active) != 2 {
		t.Errorf("expected 2 active conflicts, got %d", len(active))
	}
}

func TestSyncToSemiState(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	mgr := conflicts.NewManager(bus)

	mgr.RegisterConflict("test", []state.Claim{}, []state.Evidence{}, []string{}, state.ConflictLow, []string{})

	s := state.NewSemiState()
	mgr.SyncToSemiState(s)

	if len(s.Contradictions) != 1 {
		t.Errorf("expected 1 contradiction in semi-state, got %d", len(s.Contradictions))
	}
}
