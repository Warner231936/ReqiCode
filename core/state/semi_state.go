package state

import (
	"sync"
)

type SemiState struct {
	mu sync.RWMutex

	Revision int `json:"revision"`

	Hypotheses        []Hypothesis          `json:"hypotheses"`
	Evidence          []Evidence            `json:"evidence"`
	Contradictions    []Conflict            `json:"contradictions"`
	Unresolved        []string              `json:"unresolved"`
	Proposals         []CodeProposal        `json:"proposals"`
	TestResults       []TestResult          `json:"test_results"`
	AttentionMap      map[string]float64    `json:"attention_map"`
	ActiveUnits       []UnitMeta            `json:"active_units"`
	BackgroundUnits   []UnitMeta            `json:"background_units"`
	Confidence        Confidence            `json:"confidence"`
	History           []RevisionRecord      `json:"history"`
	Decisions         []Decision            `json:"decisions"`
	Findings          []FindingRecord       `json:"findings"`

	Requirements      []Requirement         `json:"requirements"`
	ArchitecturePlan  *ArchitecturePlan    `json:"architecture_plan,omitempty"`
	GeneratedFiles    map[string]FileEntry  `json:"generated_files"`
	Objections        []Objection           `json:"objections"`
}

type FileEntry struct {
	Path       string      `json:"path"`
	Content    string      `json:"content"`
	ProposalID string      `json:"proposal_id,omitempty"`
	Provenance Provenance  `json:"provenance"`
}

type Requirement struct {
	ID         string     `json:"id"`
	Content    string     `json:"content"`
	Type       string     `json:"type"`
	Priority   string     `json:"priority"`
	Provenance Provenance `json:"provenance"`
}

type ArchitecturePlan struct {
	Description   string          `json:"description"`
	Components    []ComponentSpec `json:"components"`
	Dependencies  []DepSpec       `json:"dependencies"`
	Endpoints     []EndpointSpec  `json:"endpoints"`
	Confidence    Confidence      `json:"confidence"`
	Provenance    Provenance      `json:"provenance"`
	AlternativeID string          `json:"alternative_id,omitempty"`
}

type ComponentSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
}

type DepSpec struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Type    string `json:"type"`
}

type EndpointSpec struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Desc   string `json:"desc"`
}

type Decision struct {
	ID         string     `json:"id"`
	Question   string     `json:"question"`
	Decision   string     `json:"decision"`
	Rationale  string     `json:"rationale"`
	Provenance Provenance `json:"provenance"`
	Supersedes string     `json:"supersedes,omitempty"`
}

type FindingRecord struct {
	Claim           Claim         `json:"claim"`
	PreviousStatus  FindingStatus `json:"previous_status"`
	NewStatus       FindingStatus `json:"new_status"`
	RevisionNotes   string        `json:"revision_notes,omitempty"`
	Provenance      Provenance    `json:"provenance"`
}

type Objection struct {
	SourceUnit string     `json:"source_unit"`
	Target     string     `json:"target"`
	Content    string     `json:"content"`
	Severity   string     `json:"severity"`
	Provenance Provenance `json:"provenance"`
	Resolved   bool       `json:"resolved"`
}

type RevisionRecord struct {
	Revision      int            `json:"revision"`
	Timestamp     string         `json:"timestamp"`
	Changes       []Change       `json:"changes"`
	Evidence      []Evidence     `json:"evidence"`
	Decisions     []Decision     `json:"decisions"`
	Confidence    Confidence     `json:"confidence"`
	Summary       string         `json:"summary"`
}

type Change struct {
	Kind   string `json:"kind"`
	Target string `json:"target"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	UnitID string `json:"unit_id"`
}

func NewSemiState() *SemiState {
	return &SemiState{
		Revision:        0,
		Hypotheses:      []Hypothesis{},
		Evidence:        []Evidence{},
		Contradictions:  []Conflict{},
		Unresolved:      []string{},
		Proposals:       []CodeProposal{},
		TestResults:     []TestResult{},
		AttentionMap:    make(map[string]float64),
		ActiveUnits:     []UnitMeta{},
		BackgroundUnits: []UnitMeta{},
		Confidence:      ConfidenceMedium,
		History:         []RevisionRecord{},
		Decisions:       []Decision{},
		Findings:        []FindingRecord{},
		Requirements:    []Requirement{},
		GeneratedFiles:  make(map[string]FileEntry),
		Objections:      []Objection{},
	}
}

func (s *SemiState) IncrementRevision() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Revision++
	return s.Revision
}

func (s *SemiState) AddEvidence(ev Evidence) {
	s.mu.Lock()
	s.Evidence = append(s.Evidence, ev)
	s.mu.Unlock()
}

func (s *SemiState) AddHypothesis(h Hypothesis) {
	s.mu.Lock()
	s.Hypotheses = append(s.Hypotheses, h)
	s.mu.Unlock()
}

func (s *SemiState) AddProposal(p CodeProposal) {
	s.mu.Lock()
	s.Proposals = append(s.Proposals, p)
	s.mu.Unlock()
}

func (s *SemiState) AddTestResult(tr TestResult) {
	s.mu.Lock()
	s.TestResults = append(s.TestResults, tr)
	s.mu.Unlock()
}

func (s *SemiState) AddConflict(c Conflict) {
	s.mu.Lock()
	s.Contradictions = append(s.Contradictions, c)
	s.mu.Unlock()
}

func (s *SemiState) SetAttention(unitID string, weight float64) {
	s.mu.Lock()
	s.AttentionMap[unitID] = weight
	s.mu.Unlock()
}

func (s *SemiState) AddUnresolved(q string) {
	s.mu.Lock()
	s.Unresolved = append(s.Unresolved, q)
	s.mu.Unlock()
}

func (s *SemiState) AddDecision(d Decision) {
	s.mu.Lock()
	s.Decisions = append(s.Decisions, d)
	s.mu.Unlock()
}

func (s *SemiState) RecordFinding(f FindingRecord) {
	s.mu.Lock()
	s.Findings = append(s.Findings, f)
	s.mu.Unlock()
}

func (s *SemiState) AddObjection(o Objection) {
	s.mu.Lock()
	s.Objections = append(s.Objections, o)
	s.mu.Unlock()
}

func (s *SemiState) AddFile(f FileEntry) {
	s.mu.Lock()
	s.GeneratedFiles[f.Path] = f
	s.mu.Unlock()
}

func (s *SemiState) SetArchitecturePlan(plan *ArchitecturePlan) {
	s.mu.Lock()
	s.ArchitecturePlan = plan
	s.mu.Unlock()
}

func (s *SemiState) GetArchitecturePlan() *ArchitecturePlan {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ArchitecturePlan
}

func (s *SemiState) AppendRequirement(r Requirement) {
	s.mu.Lock()
	s.Requirements = append(s.Requirements, r)
	s.mu.Unlock()
}

func (s *SemiState) GetRequirements() []Requirement {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Requirement{}, s.Requirements...)
}

func (s *SemiState) GetEvidence() []Evidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Evidence{}, s.Evidence...)
}

func (s *SemiState) GetDecisions() []Decision {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Decision{}, s.Decisions...)
}

func (s *SemiState) GetProposals() []CodeProposal {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]CodeProposal{}, s.Proposals...)
}

func (s *SemiState) GetHypotheses() []Hypothesis {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Hypothesis{}, s.Hypotheses...)
}

func (s *SemiState) UpdateHypothesisStatus(id string, status HypothesisStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.Hypotheses {
		if s.Hypotheses[i].ID == id {
			s.Hypotheses[i].Status = status
			s.Hypotheses[i].Provenance.Revision++
		}
	}
}

func (s *SemiState) GetTestResults() []TestResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]TestResult{}, s.TestResults...)
}

func (s *SemiState) GetGeneratedFiles() map[string]FileEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[string]FileEntry, len(s.GeneratedFiles))
	for k, v := range s.GeneratedFiles {
		result[k] = v
	}
	return result
}

func (s *SemiState) FileExistsInGenerated(path string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.GeneratedFiles[path]
	return ok
}

func (s *SemiState) SetConfidence(c Confidence) {
	s.mu.Lock()
	s.Confidence = c
	s.mu.Unlock()
}

func (s *SemiState) GetConfidence() Confidence {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.Confidence
}

func (s *SemiState) GetObjections() []Objection {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]Objection{}, s.Objections...)
}

func (s *SemiState) GetUnresolved() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string{}, s.Unresolved...)
}

func (s *SemiState) GetActiveUnits() []UnitMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]UnitMeta{}, s.ActiveUnits...)
}

func (s *SemiState) HasGeneratedFiles() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.GeneratedFiles) > 0
}

func (s *SemiState) HasTestFiles() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for path := range s.GeneratedFiles {
		if len(path) > 8 && path[len(path)-8:] == "_test.go" {
			return true
		}
	}
	return false
}

func (s *SemiState) HasPassedTests() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tr := range s.TestResults {
		if tr.Status == TestPassed {
			return true
		}
	}
	return false
}

func (s *SemiState) HasFailedTests() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tr := range s.TestResults {
		if tr.Status == TestFailed {
			return true
		}
	}
	return false
}

func (s *SemiState) LastTestResult() *TestResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.TestResults) == 0 {
		return nil
	}
	return &s.TestResults[len(s.TestResults)-1]
}

func (s *SemiState) TestsAlreadyRan() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, tr := range s.TestResults {
		if tr.ID == "go-test-all" {
			return true
		}
	}
	return false
}

func (s *SemiState) Clone() *SemiState {
	s.mu.RLock()
	defer s.mu.RUnlock()

	clone := &SemiState{
		Revision:        s.Revision,
		Hypotheses:      append([]Hypothesis{}, s.Hypotheses...),
		Evidence:        append([]Evidence{}, s.Evidence...),
		Contradictions:  append([]Conflict{}, s.Contradictions...),
		Unresolved:      append([]string{}, s.Unresolved...),
		Proposals:       append([]CodeProposal{}, s.Proposals...),
		TestResults:     append([]TestResult{}, s.TestResults...),
		AttentionMap:    make(map[string]float64),
		ActiveUnits:     append([]UnitMeta{}, s.ActiveUnits...),
		BackgroundUnits: append([]UnitMeta{}, s.BackgroundUnits...),
		Confidence:      s.Confidence,
		History:         append([]RevisionRecord{}, s.History...),
		Decisions:       append([]Decision{}, s.Decisions...),
		Findings:        append([]FindingRecord{}, s.Findings...),
		Requirements:    append([]Requirement{}, s.Requirements...),
		GeneratedFiles:  make(map[string]FileEntry),
		Objections:      append([]Objection{}, s.Objections...),
	}
	for k, v := range s.AttentionMap {
		clone.AttentionMap[k] = v
	}
	for k, v := range s.GeneratedFiles {
		clone.GeneratedFiles[k] = v
	}
	if s.ArchitecturePlan != nil {
		ap := *s.ArchitecturePlan
		clone.ArchitecturePlan = &ap
	}
	return clone
}

func (s *SemiState) Snapshot() *SemiState {
	return s.Clone()
}
