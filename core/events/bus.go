package events

import (
	"context"
	"reflect"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

type EventType string

const (
	EventRequirementAdded          EventType = "RequirementAdded"
	EventHypothesisCreated         EventType = "HypothesisCreated"
	EventHypothesisChanged         EventType = "HypothesisChanged"
	EventEvidenceAdded             EventType = "EvidenceAdded"
	EventConflictDetected          EventType = "ConflictDetected"
	EventAttentionChanged          EventType = "AttentionChanged"
	EventCodeProposed             EventType = "CodeProposed"
	EventCodeChanged              EventType = "CodeChanged"
	EventCodeApplied              EventType = "CodeApplied"
	EventTestStarted              EventType = "TestStarted"
	EventTestPassed               EventType = "TestPassed"
	EventTestFailed               EventType = "TestFailed"
	EventDependencyChanged        EventType = "DependencyChanged"
	EventReviewRequested          EventType = "ReviewRequested"
	EventReviewCompleted          EventType = "ReviewCompleted"
	EventSynthesisRequested       EventType = "SynthesisRequested"
	EventSpiralIterationCompleted EventType = "SpiralIterationCompleted"
	EventUnitCompleted            EventType = "UnitCompleted"
	EventProposalApproved         EventType = "ProposalApproved"
	EventProposalRejected         EventType = "ProposalRejected"
	EventUnresolvedAdded          EventType = "UnresolvedAdded"
	EventObjectionRaised          EventType = "ObjectionRaised"
)

type Event struct {
	ID        string                 `json:"id"`
	Type      EventType              `json:"type"`
	SourceID  string                 `json:"source_id"`
	Timestamp time.Time              `json:"timestamp"`
	Payload   map[string]interface{} `json:"payload"`
}

type EventHandler func(ctx context.Context, event Event)

type EventBus struct {
	mu          sync.RWMutex
	handlers    map[EventType][]EventHandler
	queue       chan Event
	ctx         context.Context
	cancel      context.CancelFunc
	wg          sync.WaitGroup
}

func NewEventBus() *EventBus {
	ctx, cancel := context.WithCancel(context.Background())
	eb := &EventBus{
		handlers: make(map[EventType][]EventHandler),
		queue:    make(chan Event, 10000),
		ctx:      ctx,
		cancel:   cancel,
	}
	eb.Start()
	return eb
}

func (eb *EventBus) Start() {
	eb.wg.Add(1)
	go eb.dispatchLoop()
}

func (eb *EventBus) dispatchLoop() {
	defer eb.wg.Done()
	for {
		select {
		case <-eb.ctx.Done():
			return
		case event := <-eb.queue:
			eb.dispatch(event)
		}
	}
}

func (eb *EventBus) dispatch(event Event) {
	eb.mu.RLock()
	handlerList := eb.handlers[event.Type]
	eb.mu.RUnlock()

	for _, handler := range handlerList {
		handler(eb.ctx, event)
	}
}

func (eb *EventBus) Publish(eventType EventType, sourceID string, payload map[string]interface{}) {
	eb.PublishEvent(Event{
		ID:        generateEventID(eventType, sourceID),
		Type:      eventType,
		SourceID:  sourceID,
		Timestamp: time.Now().UTC(),
		Payload:   payload,
	})
}

func (eb *EventBus) PublishEvent(event Event) {
	eb.mu.RLock()
	hasHandlers := len(eb.handlers[event.Type]) > 0
	eb.mu.RUnlock()

	if !hasHandlers {
		return
	}

	select {
	case eb.queue <- event:
	case <-eb.ctx.Done():
	}
}

func (eb *EventBus) Subscribe(eventType EventType, handler EventHandler) {
	eb.mu.Lock()
	eb.handlers[eventType] = append(eb.handlers[eventType], handler)
	eb.mu.Unlock()
}

func (eb *EventBus) Unsubscribe(eventType EventType, handler EventHandler) {
	eb.mu.Lock()
	defer eb.mu.Unlock()
	target := reflect.ValueOf(handler).Pointer()
	handlers := eb.handlers[eventType]
	for i, h := range handlers {
		if reflect.ValueOf(h).Pointer() == target {
			eb.handlers[eventType] = append(handlers[:i], handlers[i+1:]...)
			return
		}
	}
}

func (eb *EventBus) HasSubscribers(eventType EventType) bool {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.handlers[eventType]) > 0
}

func (eb *EventBus) Close() {
	eb.cancel()
	eb.wg.Wait()
}

func generateEventID(eventType EventType, sourceID string) string {
	return string(eventType) + ":" + sourceID + ":" + time.Now().UTC().Format("150405.000000")
}

func PayloadForRequirement(r state.Requirement) map[string]interface{} {
	return map[string]interface{}{"requirement": r}
}

func PayloadForHypothesis(h state.Hypothesis) map[string]interface{} {
	return map[string]interface{}{"hypothesis": h}
}

func PayloadForEvidence(e state.Evidence) map[string]interface{} {
	return map[string]interface{}{"evidence": e}
}

func PayloadForConflict(c state.Conflict) map[string]interface{} {
	return map[string]interface{}{"conflict": c}
}

func PayloadForProposal(p state.CodeProposal) map[string]interface{} {
	return map[string]interface{}{"proposal": p}
}

func PayloadForTestResult(tr state.TestResult) map[string]interface{} {
	return map[string]interface{}{"test_result": tr}
}

func PayloadForAttention(a state.Attention) map[string]interface{} {
	return map[string]interface{}{"attention": a}
}

func PayloadForUnitCompletion(unitID string, output map[string]interface{}) map[string]interface{} {
	out := map[string]interface{}{"unit_id": unitID}
	for k, v := range output {
		out[k] = v
	}
	return out
}
