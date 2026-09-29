package persistence

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/kilo/spiral-codemaker/core/state"
)

type PersistentMemory struct {
	mu       sync.RWMutex
	datafile string

	decisions   []state.Decision
	requirements []state.Requirement
	constraints  []Constraint
	pastFailures []FailureRecord
	solutions    map[string]state.ArchitecturePlan
	depInfo      map[string]DepInfo
	testHistory  []state.TestResult
	revisionHistory []state.RevisionRecord
}

type Constraint struct {
	ID       string `json:"id"`
	Rule     string `json:"rule"`
	Provenance state.Provenance `json:"provenance"`
}

type FailureRecord struct {
	ID          string   `json:"id"`
	FailureType string   `json:"failure_type"`
	Context     string   `json:"context"`
	Solution    string   `json:"solution,omitempty"`
	Provenance  state.Provenance `json:"provenance"`
}

type DepInfo struct {
	Name     string `json:"name"`
	Version  string `json:"version"`
	Type     string `json:"type"`
}

type MemoryFile struct {
	Decisions      []state.Decision     `json:"decisions"`
	Requirements   []state.Requirement  `json:"requirements"`
	Constraints    []Constraint         `json:"constraints"`
	PastFailures   []FailureRecord      `json:"past_failures"`
	Solutions      map[string]state.ArchitecturePlan `json:"solutions"`
	DepInfo        map[string]DepInfo   `json:"dep_info"`
	TestHistory    []state.TestResult   `json:"test_history"`
	RevisionHistory []state.RevisionRecord `json:"revision_history"`
}

func NewPersistentMemory(dataDir string) *PersistentMemory {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return &PersistentMemory{
			datafile: filepath.Join(dataDir, "memory.json"),
		}
	}

	pm := &PersistentMemory{
		datafile: filepath.Join(dataDir, "memory.json"),
		decisions: []state.Decision{},
		requirements: []state.Requirement{},
		constraints: []Constraint{},
		pastFailures: []FailureRecord{},
		solutions: make(map[string]state.ArchitecturePlan),
		depInfo: make(map[string]DepInfo),
		testHistory: []state.TestResult{},
		revisionHistory: []state.RevisionRecord{},
	}
	pm.load()
	return pm
}

func (pm *PersistentMemory) load() {
	data, err := os.ReadFile(pm.datafile)
	if err != nil {
		return
	}

	var mf MemoryFile
	if err := json.Unmarshal(data, &mf); err != nil {
		return
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.decisions = mf.Decisions
	pm.requirements = mf.Requirements
	pm.constraints = mf.Constraints
	pm.pastFailures = mf.PastFailures
	pm.solutions = mf.Solutions
	pm.depInfo = mf.DepInfo
	pm.testHistory = mf.TestHistory
	pm.revisionHistory = mf.RevisionHistory
}

func (pm *PersistentMemory) Save() error {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	mf := MemoryFile{
		Decisions:      pm.decisions,
		Requirements:   pm.requirements,
		Constraints:    pm.constraints,
		PastFailures:   pm.pastFailures,
		Solutions:      pm.solutions,
		DepInfo:        pm.depInfo,
		TestHistory:    pm.testHistory,
		RevisionHistory: pm.revisionHistory,
	}

	data, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(pm.datafile)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	return os.WriteFile(pm.datafile, data, 0644)
}

func (pm *PersistentMemory) StoreDecision(d state.Decision) {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	for i, existing := range pm.decisions {
		if existing.Question == d.Question {
			d.Supersedes = existing.ID
			pm.decisions[i] = d
			return
		}
	}
	pm.decisions = append(pm.decisions, d)
}

func (pm *PersistentMemory) GetDecision(topic string) (state.Decision, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	for _, d := range pm.decisions {
		if d.Question == topic {
			return d, true
		}
	}
	return state.Decision{}, false
}

func (pm *PersistentMemory) StoreRequirement(r state.Requirement) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	for _, existing := range pm.requirements {
		if existing.ID == r.ID {
			return
		}
	}
	pm.requirements = append(pm.requirements, r)
}

func (pm *PersistentMemory) StorePastSolution(key string, plan state.ArchitecturePlan) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	plan.AlternativeID = key
	pm.solutions[key] = plan
}

func (pm *PersistentMemory) GetPastSolution(key string) (state.ArchitecturePlan, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	plan, ok := pm.solutions[key]
	return plan, ok
}

func (pm *PersistentMemory) StoreFailure(f FailureRecord) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.pastFailures = append(pm.pastFailures, f)
}

func (pm *PersistentMemory) RecordTestResult(tr state.TestResult) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.testHistory = append(pm.testHistory, tr)
}

func (pm *PersistentMemory) RecordRevision(r state.RevisionRecord) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.revisionHistory = append(pm.revisionHistory, r)
}

func (pm *PersistentMemory) AllDecisions() []state.Decision {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return append([]state.Decision{}, pm.decisions...)
}

func (pm *PersistentMemory) AllRequirements() []state.Requirement {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return append([]state.Requirement{}, pm.requirements...)
}

func (pm *PersistentMemory) AllFailures() []FailureRecord {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return append([]FailureRecord{}, pm.pastFailures...)
}

func (pm *PersistentMemory) AllSolutions() map[string]state.ArchitecturePlan {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	result := make(map[string]state.ArchitecturePlan, len(pm.solutions))
	for k, v := range pm.solutions {
		result[k] = v
	}
	return result
}

func (pm *PersistentMemory) AllTestHistory() []state.TestResult {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return append([]state.TestResult{}, pm.testHistory...)
}

func (pm *PersistentMemory) AllRevisions() []state.RevisionRecord {
	pm.mu.RLock()
	defer pm.mu.RUnlock()
	return append([]state.RevisionRecord{}, pm.revisionHistory...)
}
