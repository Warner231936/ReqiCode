package attention

import (
	"context"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Manager struct {
	mu           sync.RWMutex
	weights      map[string]state.Attention
	eventBus     *events.EventBus
	decayRate    float64
	minThreshold float64
}

func NewManager(eventBus *events.EventBus) *Manager {
	m := &Manager{
		weights:      make(map[string]state.Attention),
		eventBus:     eventBus,
		decayRate:    0.02,
		minThreshold: 0.05,
	}
	m.subscribe()
	return m
}

func (m *Manager) subscribe() {
	m.eventBus.Subscribe(events.EventEvidenceAdded, m.onEvidenceAdded)
	m.eventBus.Subscribe(events.EventConflictDetected, m.onConflictDetected)
	m.eventBus.Subscribe(events.EventTestFailed, m.onTestFailed)
	m.eventBus.Subscribe(events.EventCodeProposed, m.onCodeProposed)
	m.eventBus.Subscribe(events.EventHypothesisChanged, m.onHypothesisChanged)
	m.eventBus.Subscribe(events.EventUnitCompleted, m.onUnitCompleted)
}

func (m *Manager) Boost(unitID, reason string, amount float64) {
	m.mu.Lock()
	defer m.mu.Unlock()

	current, exists := m.weights[unitID]
	if !exists {
		current = state.Attention{
			UnitID:    unitID,
			Weight:    0.1,
			Reason:    reason,
			Timestamp: time.Now().UTC(),
			Decay:     m.decayRate,
		}
	}

	current.Weight += amount
	if current.Weight > 1.0 {
		current.Weight = 1.0
	}
	current.Reason = reason
	current.Timestamp = time.Now().UTC()
	m.weights[unitID] = current

	m.eventBus.Publish(events.EventAttentionChanged, "attention-manager", map[string]interface{}{
		"unit_id": unitID,
		"weight":  current.Weight,
		"reason":  reason,
	})
}

func (m *Manager) Set(unitID, reason string, weight float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.weights[unitID] = state.Attention{
		UnitID:    unitID,
		Weight:    weight,
		Reason:    reason,
		Timestamp: time.Now().UTC(),
		Decay:     m.decayRate,
	}
}

func (m *Manager) Get(unitID string) state.Attention {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.weights[unitID]
}

func (m *Manager) GetTop(n int) []state.Attention {
	m.mu.RLock()
	defer m.mu.RUnlock()

	result := make([]state.Attention, 0, len(m.weights))
	for _, a := range m.weights {
		result = append(result, a)
	}
	sortByWeightDesc(result)
	if len(result) > n {
		result = result[:n]
	}
	return result
}

func (m *Manager) GetAll() []state.Attention {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]state.Attention, 0, len(m.weights))
	for _, a := range m.weights {
		result = append(result, a)
	}
	sortByWeightDesc(result)
	return result
}

func (m *Manager) ApplyDecay() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now().UTC()
	for unitID, a := range m.weights {
		elapsed := now.Sub(a.Timestamp).Seconds()
		decayed := a.Weight * (1.0 - a.Decay*elapsed/60.0)
		if decayed < 0 {
			decayed = 0
		}
		if decayed < m.minThreshold {
			decayed = m.minThreshold
		}
		a.Weight = decayed
		a.Timestamp = now
		m.weights[unitID] = a
	}
}

func (m *Manager) ResetToBaseline() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for unitID := range m.weights {
		a := m.weights[unitID]
		a.Weight = 0.1
		a.Reason = "reset to baseline"
		a.Timestamp = time.Now().UTC()
		m.weights[unitID] = a
	}
}

func (m *Manager) SyncToSemiState(s *state.SemiState) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s.AttentionMap = make(map[string]float64)
	for unitID, a := range m.weights {
		s.AttentionMap[unitID] = a.Weight
	}
}

func sortByWeightDesc(a []state.Attention) {
	for i := 0; i < len(a); i++ {
		for j := i + 1; j < len(a); j++ {
			if a[j].Weight > a[i].Weight {
				a[i], a[j] = a[j], a[i]
			}
		}
	}
}

func (m *Manager) onEvidenceAdded(_ context.Context, event events.Event) {
	evidence, ok := event.Payload["evidence"].(state.Evidence)
	if !ok {
		return
	}
	switch evidence.Type {
	case state.EvidenceTestFailure:
		m.Boost("test_runner", "new test failure evidence", 0.3)
		m.Boost("debugger", "new test failure evidence", 0.3)
		m.Boost("critic", "new test failure evidence", 0.2)
	case state.EvidenceAnalysis:
		m.Boost("architect", "new analysis evidence", 0.15)
	case state.EvidenceCodeReview:
		m.Boost("critic", "new code review evidence", 0.15)
	}
}

func (m *Manager) onConflictDetected(_ context.Context, event events.Event) {
	conflict, ok := event.Payload["conflict"].(state.Conflict)
	if !ok {
		return
	}
	severityBoost := map[state.ConflictSeverity]float64{
		state.ConflictLow:      0.1,
		state.ConflictMedium:   0.2,
		state.ConflictHigh:     0.3,
		state.ConflictCritical: 0.5,
	}
	boost := severityBoost[conflict.Severity]
	if boost == 0 {
		boost = 0.2
	}
	for _, participant := range conflict.Participants {
		m.Boost(participant, "conflict resolution needed", boost)
	}
	m.Boost("synthesizer", "conflict detected requiring synthesis", boost*0.8)
}

func (m *Manager) onTestFailed(_ context.Context, event events.Event) {
	m.Boost("test_runner", "tests failing - need investigation", 0.4)
	m.Boost("debugger", "test failures require debugging", 0.4)
	m.Boost("critic", "test failures detected", 0.3)
}

func (m *Manager) onCodeProposed(_ context.Context, event events.Event) {
	proposal, ok := event.Payload["proposal"].(state.CodeProposal)
	if !ok {
		return
	}
	m.Boost("consistency_checker", "new code proposal to check", 0.2)
	m.Boost("security_analyst", "new code proposal to review", float64(proposal.Confidence)*0.5)
}

func (m *Manager) onHypothesisChanged(_ context.Context, event events.Event) {
	m.Boost("synthesizer", "hypothesis changed - re-synthesis needed", 0.3)
	m.Boost("architect", "hypothesis changed - architecture review", 0.2)
}

func (m *Manager) onUnitCompleted(_ context.Context, event events.Event) {
	unitID, _ := event.Payload["unit_id"].(string)
	if unitID == "" {
		return
	}
	m.Boost("synthesizer", "unit completed - synthesis may be needed", 0.1)
}
