package events_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestEventBusPublishAndSubscribe(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	var received int32
	bus.Subscribe(events.EventTestPassed, func(_ context.Context, event events.Event) {
		atomic.AddInt32(&received, 1)
	})

	bus.Publish(events.EventTestPassed, "test-source", map[string]interface{}{
		"test_id": "test-001",
	})

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&received) != 1 {
		t.Errorf("expected 1 event received, got %d", atomic.LoadInt32(&received))
	}
}

func TestEventBusMultipleSubscribers(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	var count int32
	bus.Subscribe(events.EventTestFailed, func(_ context.Context, event events.Event) {
		atomic.AddInt32(&count, 1)
	})
	bus.Subscribe(events.EventTestFailed, func(_ context.Context, event events.Event) {
		atomic.AddInt32(&count, 1)
	})

	bus.Publish(events.EventTestFailed, "source", map[string]interface{}{
		"test_result": state.TestResult{ID: "test-001"},
	})

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&count) != 2 {
		t.Errorf("expected 2 handlers called, got %d", atomic.LoadInt32(&count))
	}
}

func TestEventBusNoSubscribers(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	bus.Publish(events.EventTestPassed, "source", map[string]interface{}{})
	// Should not block or panic
}

func TestEventBusUnsubscribe(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	var count int32
	handler := func(_ context.Context, event events.Event) {
		atomic.AddInt32(&count, 1)
	}

	bus.Subscribe(events.EventTestPassed, handler)
	bus.Publish(events.EventTestPassed, "src", nil)

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("expected 1 before unsubscribe, got %d", atomic.LoadInt32(&count))
	}

	bus.Unsubscribe(events.EventTestPassed, handler)
	bus.Publish(events.EventTestPassed, "src", nil)

	time.Sleep(50 * time.Millisecond)

	if atomic.LoadInt32(&count) != 1 {
		t.Errorf("expected 1 after unsubscribe, got %d", atomic.LoadInt32(&count))
	}
}

func TestEventPayloads(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	var receivedType state.TestStatus
	bus.Subscribe(events.EventTestPassed, func(_ context.Context, event events.Event) {
		tr, ok := event.Payload["test_result"].(state.TestResult)
		if ok {
			receivedType = tr.Status
		}
	})

	bus.Publish(events.EventTestPassed, "src", events.PayloadForTestResult(state.TestResult{
		ID:     "test-001",
		Status: state.TestPassed,
	}))

	time.Sleep(50 * time.Millisecond)

	if receivedType != state.TestPassed {
		t.Errorf("expected PASSED, got %s", receivedType)
	}
}

func TestHasSubscribers(t *testing.T) {
	bus := events.NewEventBus()
	defer bus.Close()

	if bus.HasSubscribers(events.EventTestPassed) {
		t.Error("expected no subscribers")
	}

	bus.Subscribe(events.EventTestPassed, func(_ context.Context, _ events.Event) {})

	if !bus.HasSubscribers(events.EventTestPassed) {
		t.Error("expected subscribers")
	}
}
