package spiral

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Phase int

const (
	PhaseRequirement Phase = iota + 1
	PhaseDecomposition
	PhaseArchitecture
	PhaseSynthesis
	PhaseCodeProposal
	PhaseCodeApplication
	PhaseBuild
	PhaseTest
	PhaseCritique
	PhaseRevision
	PhaseDocumentation
	PhaseFinalSynthesis
)

func (p Phase) String() string {
	switch p {
	case PhaseRequirement:
		return "requirement"
	case PhaseDecomposition:
		return "decomposition"
	case PhaseArchitecture:
		return "architecture"
	case PhaseSynthesis:
		return "synthesis"
	case PhaseCodeProposal:
		return "code_proposal"
	case PhaseCodeApplication:
		return "code_application"
	case PhaseBuild:
		return "build"
	case PhaseTest:
		return "test"
	case PhaseCritique:
		return "critique"
	case PhaseRevision:
		return "revision"
	case PhaseDocumentation:
		return "documentation"
	case PhaseFinalSynthesis:
		return "final_synthesis"
	default:
		return "unknown"
	}
}

type IterationRecord struct {
	ID             int                  `json:"iteration_id"`
	StartTime      time.Time            `json:"start_time"`
	EndTime        time.Time            `json:"end_time"`
	StartingState  *state.SemiState     `json:"starting_state"`
	EndingState    *state.SemiState     `json:"ending_state"`
	Changes        []state.Change       `json:"changes"`
	NewEvidence    []state.Evidence     `json:"new_evidence"`
	Contradictions []state.Conflict     `json:"contradictions"`
	Decisions      []state.Decision     `json:"decisions"`
	TestResults    []state.TestResult   `json:"tests"`
	Confidence     state.Confidence     `json:"confidence"`
	Summary        string               `json:"summary"`
	Phase          Phase                `json:"phase"`
}

type Manager struct {
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	bus         *events.EventBus
	semiState   *state.SemiState
	iteration   int
	records     []IterationRecord
	current     *IterationRecord
	maxIterations int
}

func NewManager(ctx context.Context, bus *events.EventBus, semiState *state.SemiState, maxIterations int) *Manager {
	return &Manager{
		ctx:           ctx,
		bus:           bus,
		semiState:     semiState,
		iteration:     0,
		records:       []IterationRecord{},
		maxIterations: maxIterations,
	}
}

func (m *Manager) StartIteration() *IterationRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.iteration++
	record := &IterationRecord{
		ID:             m.iteration,
		StartTime:      time.Now().UTC(),
		StartingState:  m.semiState.Snapshot(),
		Changes:        []state.Change{},
		NewEvidence:    []state.Evidence{},
		Contradictions: []state.Conflict{},
		Decisions:      []state.Decision{},
		TestResults:    []state.TestResult{},
		Phase:          PhaseRequirement,
	}
	m.current = record
	return record
}

func (m *Manager) UpdatePhase(phase Phase) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.Phase = phase
	}
}

func (m *Manager) AddChange(change state.Change) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.Changes = append(m.current.Changes, change)
	}
}

func (m *Manager) AddEvidence(ev state.Evidence) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.NewEvidence = append(m.current.NewEvidence, ev)
	}
}

func (m *Manager) AddDecision(d state.Decision) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.Decisions = append(m.current.Decisions, d)
	}
}

func (m *Manager) AddTestResult(tr state.TestResult) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.TestResults = append(m.current.TestResults, tr)
	}
}

func (m *Manager) AddContradiction(c state.Conflict) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.current != nil {
		m.current.Contradictions = append(m.current.Contradictions, c)
	}
}

func (m *Manager) EndIteration(summary string) IterationRecord {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current != nil {
		m.current.EndTime = time.Now().UTC()
		m.current.Summary = summary
		m.current.Confidence = m.semiState.Confidence
		m.current.EndingState = m.semiState.Snapshot()
		m.current.EndingState.Revision = m.semiState.IncrementRevision()
		m.records = append(m.records, *m.current)
	}

	record := IterationRecord{}
	if m.current != nil {
		record = *m.current
	}
	m.current = nil

	m.bus.Publish(events.EventSpiralIterationCompleted, "spiral-manager", map[string]interface{}{
		"iteration": record.ID,
		"summary":   summary,
	})

	return record
}

func (m *Manager) CurrentIteration() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.iteration
}

func (m *Manager) Records() []IterationRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.records
}

func (m *Manager) CurrentRecord() *IterationRecord {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *Manager) ShouldContinue() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.iteration < m.maxIterations
}

func (m *Manager) MaxIterations() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.maxIterations
}

func (m *Manager) IterationsCompleted() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.records)
}

func (m *Manager) FinalReport() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	report := "Spiral CodeMaker Report\n"
	report += fmt.Sprintf("Iterations completed: %d\n", len(m.records))
	report += fmt.Sprintf("Semi-State revision: %d\n", m.semiState.Revision)
	report += fmt.Sprintf("Confidence: %.2f\n", float64(m.semiState.Confidence))
	report += fmt.Sprintf("Hypotheses: %d\n", len(m.semiState.Hypotheses))
	report += fmt.Sprintf("Evidence items: %d\n", len(m.semiState.Evidence))
	report += fmt.Sprintf("Conflicts: %d (%d active)\n", len(m.semiState.Contradictions), m.countActiveConflicts())
	report += fmt.Sprintf("Proposals: %d\n", len(m.semiState.Proposals))
	report += fmt.Sprintf("Test results: %d\n", len(m.semiState.TestResults))
	report += fmt.Sprintf("Decisions: %d\n", len(m.semiState.Decisions))
	report += fmt.Sprintf("Files generated: %d\n", len(m.semiState.GeneratedFiles))
	report += fmt.Sprintf("Findings: %d\n", len(m.semiState.Findings))
	report += fmt.Sprintf("Objections: %d\n", len(m.semiState.Objections))

	for _, rec := range m.records {
		status := "PASS"
		for _, tr := range rec.TestResults {
			if tr.Status == state.TestFailed {
				status = "FAIL"
				break
			}
		}
		report += fmt.Sprintf("  Iteration %d: %s, %s, confidence %.2f\n", rec.ID, status, rec.Summary, float64(rec.Confidence))
	}

	return report
}

func (m *Manager) countActiveConflicts() int {
	count := 0
	for _, c := range m.semiState.Contradictions {
		if !c.Resolved {
			count++
		}
	}
	return count
}
