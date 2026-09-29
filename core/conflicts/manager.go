package conflicts

import (
	"fmt"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Manager struct {
	mu        sync.RWMutex
	conflicts map[string]state.Conflict
	bus       *events.EventBus
	counter   int64
}

func NewManager(bus *events.EventBus) *Manager {
	return &Manager{
		conflicts: make(map[string]state.Conflict),
		bus:       bus,
	}
}

func (m *Manager) RegisterConflict(subject string, claims []state.Claim, evidence []state.Evidence, files []string, severity state.ConflictSeverity, participants []string) state.Conflict {
	m.mu.Lock()
	defer m.mu.Unlock()

	conflict := state.Conflict{
		ID:              fmt.Sprintf("conflict-%d-%d", m.counter, time.Now().UnixNano()),
		Subject:         subject,
		CompetingClaims: claims,
		Evidence:        evidence,
		AffectedFiles:   files,
		Severity:        severity,
		Participants:    participants,
		Provenance:      state.NewProvenance("conflict-manager"),
	}
	m.conflicts[conflict.ID] = conflict
	m.counter++
	m.bus.Publish(events.EventConflictDetected, "conflict-manager", map[string]interface{}{
		"conflict": conflict,
	})

	return conflict
}

func (m *Manager) AddEvidence(conflictID string, ev state.Evidence) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conflicts[conflictID]
	if !ok {
		return false
	}
	c.Evidence = append(c.Evidence, ev)
	m.conflicts[conflictID] = c
	return true
}

func (m *Manager) Resolve(conflictID, resolution string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.conflicts[conflictID]
	if !ok {
		return false
	}
	c.Resolution = resolution
	c.Resolved = true
	m.conflicts[conflictID] = c
	return true
}

func (m *Manager) Get(conflictID string) (state.Conflict, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.conflicts[conflictID]
	return c, ok
}

func (m *Manager) All() []state.Conflict {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]state.Conflict, 0, len(m.conflicts))
	for _, c := range m.conflicts {
		result = append(result, c)
	}
	return result
}

func (m *Manager) Active() []state.Conflict {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make([]state.Conflict, 0)
	for _, c := range m.conflicts {
		if !c.Resolved {
			result = append(result, c)
		}
	}
	return result
}

func (m *Manager) SyncToSemiState(s *state.SemiState) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s.Contradictions = make([]state.Conflict, 0, len(m.conflicts))
	for _, c := range m.conflicts {
		if !c.Resolved {
			s.Contradictions = append(s.Contradictions, c)
		}
	}
}
