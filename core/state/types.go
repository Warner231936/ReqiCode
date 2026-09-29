package state

import (
	"time"
)

type FindingStatus string

const (
	FindingUnverified  FindingStatus = "UNVERIFIED"
	FindingSupported   FindingStatus = "SUPPORTED"
	FindingContradicted FindingStatus = "CONTRADICTED"
	FindingResolved    FindingStatus = "RESOLVED"
	FindingRejected    FindingStatus = "REJECTED"
)

type Confidence float64

const (
	ConfidenceLow      Confidence = 0.25
	ConfidenceMedium   Confidence = 0.50
	ConfidenceHigh     Confidence = 0.75
	ConfidenceCertain  Confidence = 0.95
)

const AttentionThreshold float64 = 0.05

type Provenance struct {
	UnitID    string    `json:"unit_id"`
	CreatedAt time.Time `json:"created_at"`
	Revision  int       `json:"revision"`
}

func NewProvenance(unitID string) Provenance {
	return Provenance{
		UnitID:    unitID,
		CreatedAt: time.Now().UTC(),
		Revision:  0,
	}
}

func (p Provenance) WithRevision(rev int) Provenance {
	p.Revision = rev
	return p
}

type Claim struct {
	Content               string       `json:"content"`
	Source                string       `json:"source"`
	Status                FindingStatus `json:"status"`
	Confidence            Confidence   `json:"confidence"`
	SupportingEvidence    []string     `json:"supporting_evidence"`
	ContradictingEvidence []string     `json:"contradicting_evidence"`
	Provenance            Provenance   `json:"provenance"`
	Revision              int          `json:"revision"`
}

func NewClaim(content, source, unitID string, confidence Confidence) Claim {
	return Claim{
		Content:    content,
		Source:     source,
		Status:     FindingUnverified,
		Confidence: confidence,
		Provenance: NewProvenance(unitID),
		Revision:   0,
	}
}

func (c Claim) WithStatus(s FindingStatus) Claim {
	c.Status = s
	c.Revision++
	return c
}

func (c Claim) Strengthen(confidence Confidence) Claim {
	c.Confidence = confidence
	c.Revision++
	return c
}

func (c Claim) Weaken(confidence Confidence) Claim {
	c.Confidence = confidence
	c.Revision++
	return c
}

func (c Claim) AddSupporting(id string) Claim {
	c.SupportingEvidence = append(c.SupportingEvidence, id)
	return c
}

func (c Claim) AddContradicting(id string) Claim {
	c.ContradictingEvidence = append(c.ContradictingEvidence, id)
	return c
}

type EvidenceType string

const (
	EvidenceTestFailure  EvidenceType = "test_failure"
	EvidenceTestPass     EvidenceType = "test_pass"
	EvidenceCodeReview   EvidenceType = "code_review"
	EvidenceAnalysis     EvidenceType = "analysis"
	EvidenceObservation  EvidenceType = "observation"
	EvidenceRuntime      EvidenceType = "runtime"
	EvidenceExternal     EvidenceType = "external"
)

type Evidence struct {
	ID          string       `json:"id"`
	Type        EvidenceType `json:"type"`
	Content     string       `json:"content"`
	RelatedTo   []string     `json:"related_to"`
	Strength    Confidence   `json:"strength"`
	Provenance  Provenance   `json:"provenance"`
}

type HypothesisStatus string

const (
	HypothesisProposed   HypothesisStatus = "PROPOSED"
	HypothesisAccepted   HypothesisStatus = "ACCEPTED"
	HypothesisRejected   HypothesisStatus = "REJECTED"
	HypothesisSuperseded HypothesisStatus = "SUPERSEDED"
)

type Hypothesis struct {
	ID            string           `json:"id"`
	Content       string           `json:"content"`
	Status        HypothesisStatus `json:"status"`
	Confidence    Confidence       `json:"confidence"`
	Supporting    []string         `json:"supporting"`
	Contradicting []string         `json:"contradicting"`
	Provenance    Provenance       `json:"provenance"`
	RelatedClaims []string         `json:"related_claims"`
}

type CodeOperation string

const (
	OpCreate CodeOperation = "CREATE"
	OpModify CodeOperation = "MODIFY"
	OpDelete CodeOperation = "DELETE"
	OpMove   CodeOperation = "MOVE"
	OpRename CodeOperation = "RENAME"
)

type CodeProposal struct {
	ID              string          `json:"id"`
	File            string          `json:"file"`
	Operation       CodeOperation   `json:"operation"`
	Location        string          `json:"location"`
	Before          string          `json:"before,omitempty"`
	After           string          `json:"after,omitempty"`
	Reason          string          `json:"reason"`
	OriginatingUnit string          `json:"originating_unit"`
	Confidence      Confidence      `json:"confidence"`
	ExpectedEffect  string          `json:"expected_effect"`
	Provenance      Provenance      `json:"provenance"`
	Status          ProposalStatus  `json:"status"`
	Dependencies    []string        `json:"dependencies,omitempty"`
}

type ProposalStatus string

const (
	ProposalProposed ProposalStatus = "PROPOSED"
	ProposalApproved ProposalStatus = "APPROVED"
	ProposalRejected ProposalStatus = "REJECTED"
	ProposalApplied  ProposalStatus = "APPLIED"
	ProposalFailed   ProposalStatus = "FAILED"
)

type TestStatus string

const (
	TestPassed  TestStatus = "PASSED"
	TestFailed  TestStatus = "FAILED"
	TestPending TestStatus = "PENDING"
	TestSkipped TestStatus = "SKIPPED"
)

type TestResult struct {
	ID           string     `json:"id"`
	Command      string     `json:"command"`
	Status       TestStatus `json:"status"`
	Stdout       string     `json:"stdout"`
	Stderr       string     `json:"stderr"`
	Duration     int64      `json:"duration_ms"`
	FailureClass string     `json:"failure_class,omitempty"`
	Evidence     []Evidence `json:"evidence"`
	Provenance   Provenance `json:"provenance"`
}

type ConflictSeverity string

const (
	ConflictLow      ConflictSeverity = "LOW"
	ConflictMedium   ConflictSeverity = "MEDIUM"
	ConflictHigh     ConflictSeverity = "HIGH"
	ConflictCritical ConflictSeverity = "CRITICAL"
)

type Conflict struct {
	ID              string           `json:"id"`
	Subject         string           `json:"subject"`
	CompetingClaims []Claim          `json:"competing_claims"`
	Evidence        []Evidence       `json:"evidence"`
	AffectedFiles   []string         `json:"affected_files"`
	Severity        ConflictSeverity `json:"severity"`
	Participants    []string         `json:"participants"`
	Resolution      string           `json:"resolution,omitempty"`
	Resolved        bool             `json:"resolved"`
	Provenance      Provenance       `json:"provenance"`
}

type Attention struct {
	UnitID    string    `json:"unit_id"`
	Weight    float64   `json:"weight"`
	Reason    string    `json:"reason"`
	Timestamp time.Time `json:"timestamp"`
	Decay     float64   `json:"decay"`
}

type UnitRole string

const (
	RoleOrchestrator         UnitRole = "orchestrator"
	RoleRequirementsAnalyst  UnitRole = "requirements_analyst"
	RoleDecomposer           UnitRole = "decomposer"
	RoleArchitect            UnitRole = "architect"
	RoleCodeGenerator        UnitRole = "code_generator"
	RoleTypeAnalyst          UnitRole = "type_analyst"
	RoleSecurityAnalyst      UnitRole = "security_analyst"
	RoleDependencyAnalyst    UnitRole = "dependency_analyst"
	RoleTestDesigner         UnitRole = "test_designer"
	RoleTestRunner           UnitRole = "test_runner"
	RoleDebugger             UnitRole = "debugger"
	RoleCritic               UnitRole = "critic"
	RoleConsistencyChecker   UnitRole = "consistency_checker"
	RoleDocumentationWriter  UnitRole = "documentation_writer"
	RoleSynthesizer          UnitRole = "synthesizer"
)

type CadenceType string

const (
	CadenceFast   CadenceType = "fast"
	CadenceMedium CadenceType = "medium"
	CadenceSlow   CadenceType = "slow"
	CadenceEvent  CadenceType = "event"
	CadenceManual CadenceType = "manual"
)

type ActivationType string

const (
	ActivationPeriodic      ActivationType = "periodic"
	ActivationOnEvent       ActivationType = "on_event"
	ActivationOnAttention   ActivationType = "on_attention"
	ActivationOnDependency  ActivationType = "on_dependency"
	ActivationManual        ActivationType = "manual"
)

type UnitMeta struct {
	ID              string           `json:"id"`
	Role            UnitRole         `json:"role"`
	Name            string           `json:"name"`
	Cadence         CadenceType      `json:"cadence"`
	Activation      ActivationType   `json:"activation"`
	Dependencies    []string         `json:"dependencies"`
	AttentionWeight float64          `json:"attention_weight"`
	Confidence      Confidence       `json:"confidence"`
	Active          bool             `json:"active"`
	LastRun         time.Time        `json:"last_run,omitempty"`
}
