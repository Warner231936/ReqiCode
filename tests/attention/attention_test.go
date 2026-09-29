package attention_test

import (
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/core/attention"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestBoost(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Boost("unit-1", "test reason", 0.3)
	att := m.Get("unit-1")

	if att.Weight != 0.4 {
		t.Errorf("expected weight 0.4 (0.1 base + 0.3 boost), got %f", att.Weight)
	}
	if att.Reason != "test reason" {
		t.Errorf("expected 'test reason', got '%s'", att.Reason)
	}
}

func TestBoostCappedAtOne(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Set("unit-1", "initial", 0.5)
	m.Boost("unit-1", "capped", 1.0)
	att := m.Get("unit-1")

	if att.Weight != 1.0 {
		t.Errorf("expected capped at 1.0, got %f", att.Weight)
	}
}

func TestSet(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Set("unit-1", "manual set", 0.75)
	att := m.Get("unit-1")

	if att.Weight != 0.75 {
		t.Errorf("expected 0.75, got %f", att.Weight)
	}
}

func TestGetTop(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Set("unit-1", "reason", 0.3)
	m.Set("unit-2", "reason", 0.8)
	m.Set("unit-3", "reason", 0.5)

	top := m.GetTop(2)
	if len(top) != 2 {
		t.Fatalf("expected 2, got %d", len(top))
	}
	if top[0].UnitID != "unit-2" {
		t.Errorf("expected unit-2 first, got %s", top[0].UnitID)
	}
	if top[1].UnitID != "unit-3" {
		t.Errorf("expected unit-3 second, got %s", top[1].UnitID)
	}
}

func TestApplyDecay(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Boost("unit-1", "high attention", 0.8)
	attBefore := m.Get("unit-1")

	time.Sleep(2 * time.Second)
	m.ApplyDecay()
	attAfter := m.Get("unit-1")

	if attAfter.Weight >= attBefore.Weight {
		t.Error("expected decay to reduce weight")
	}
}

func TestSyncToSemiState(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	m.Set("unit-1", "reason", 0.5)
	m.Set("unit-2", "reason", 0.3)

	s := state.NewSemiState()
	m.SyncToSemiState(s)

	if s.AttentionMap["unit-1"] != 0.5 {
		t.Errorf("expected 0.5, got %f", s.AttentionMap["unit-1"])
	}
	if s.AttentionMap["unit-2"] != 0.3 {
		t.Errorf("expected 0.3, got %f", s.AttentionMap["unit-2"])
	}
}

func TestOnEvidenceAdded(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()
	m := attention.NewManager(bus)

	bus.Publish(events.EventEvidenceAdded, "source", map[string]interface{}{
		"evidence": state.Evidence{
			Type:       state.EvidenceTestFailure,
			Provenance: state.NewProvenance("source"),
		},
	})

	time.Sleep(50 * time.Millisecond)

	testRunnerAtt := m.Get("test_runner")
	if testRunnerAtt.Weight <= 0.1 {
		t.Error("expected test_runner attention to be boosted by test failure")
	}
}
