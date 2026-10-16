package api

import (
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

type TaskLifecycleStatus string

const (
	TaskStatusSubmitted TaskLifecycleStatus = "SUBMITTED"
	TaskStatusApproved  TaskLifecycleStatus = "APPROVED"
	TaskStatusRunning   TaskLifecycleStatus = "RUNNING"
	TaskStatusVerified  TaskLifecycleStatus = "VERIFIED"
	TaskStatusFailed    TaskLifecycleStatus = "FAILED"
	TaskStatusBlocked   TaskLifecycleStatus = "BLOCKED"
	TaskStatusReviewed  TaskLifecycleStatus = "REVIEWED"
	TaskStatusClosed    TaskLifecycleStatus = "CLOSED"
	TaskStatusCanceled  TaskLifecycleStatus = "CANCELED"
)

type FileChange struct {
	Path        string `json:"path"`
	Operation   string `json:"operation"`
	ContentSize int    `json:"content_size,omitempty"`
}

type CommandExec struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
	Duration int64  `json:"duration_ms"`
}

type BenchmarkResult struct {
	Name     string  `json:"name"`
	Value    float64 `json:"value"`
	Unit     string  `json:"unit"`
	Baseline float64 `json:"baseline,omitempty"`
}

type TestRequirements struct {
	UnitTests     bool `json:"unit_tests"`
	Integration   bool `json:"integration"`
	Coverage      bool `json:"coverage"`
	Benchmark     bool `json:"benchmark"`
	RaceDetection bool `json:"race_detection"`
}

type ExecutionBudget struct {
	MaxTokens    int `json:"max_tokens,omitempty"`
	TimeLimitSec int `json:"time_limit_sec,omitempty"`
	MaxRetries   int `json:"max_retries,omitempty"`
}

type Permissions struct {
	AllowFileSystemWrite bool     `json:"allow_fs_write"`
	AllowNetwork         bool     `json:"allow_network"`
	AllowedPaths         []string `json:"allowed_paths,omitempty"`
	RestrictedCommands   []string `json:"restricted_commands,omitempty"`
}

type EngineeringTask struct {
	TaskID                 string            `json:"task_id"`
	InvestigationID        string            `json:"investigation_id,omitempty"`
	Objective              string            `json:"objective"`
	Rationale              string            `json:"rationale,omitempty"`
	Repository             string            `json:"repository,omitempty"`
	WorkspacePath          string            `json:"workspace_path,omitempty"`
	RelevantFiles          []string          `json:"relevant_files,omitempty"`
	ContextualEvidence     string            `json:"contextual_evidence,omitempty"`
	ProposedChanges        []FileChange      `json:"proposed_changes,omitempty"`
	ExpectedBehavior       string            `json:"expected_behavior,omitempty"`
	AcceptanceCriteria     []string          `json:"acceptance_criteria,omitempty"`
	TestRequirements       *TestRequirements `json:"test_requirements,omitempty"`
	Budget                 *ExecutionBudget  `json:"budget,omitempty"`
	Permissions            *Permissions      `json:"permissions,omitempty"`
	RollbackRequirements   string            `json:"rollback_requirements,omitempty"`
}

type TaskResult struct {
	TaskID                   string               `json:"task_id"`
	Status                   TaskLifecycleStatus  `json:"status"`
	InvestigationID          string               `json:"investigation_id,omitempty"`
	Objective                string               `json:"objective"`
	FilesChanged             []FileChange         `json:"files_changed,omitempty"`
	CommandsExecuted         []CommandExec        `json:"commands_executed,omitempty"`
	TestResults              []state.TestResult   `json:"test_results,omitempty"`
	BenchmarkResults         []BenchmarkResult    `json:"benchmark_results,omitempty"`
	Logs                     string               `json:"logs,omitempty"`
	ErrorSummary             string               `json:"error_summary,omitempty"`
	VerificationStatus       string               `json:"verification_status"`
	KnownLimitations         string               `json:"known_limitations,omitempty"`
	ArtifactLocations        []string             `json:"artifact_locations,omitempty"`
	Reproducibility          string               `json:"reproducibility,omitempty"`
	OutstandingUncertainties []string             `json:"outstanding_uncertainties,omitempty"`
	StartedAt                time.Time            `json:"started_at,omitempty"`
	CompletedAt              time.Time            `json:"completed_at,omitempty"`
	DurationMs               int64                `json:"duration_ms"`
}

type EngineeringRunResult struct {
	RunResult
	Result *TaskResult `json:"result,omitempty"`
}

type EngineeringSummary struct {
	Revision       int                  `json:"revision"`
	Confidence       float64              `json:"confidence"`
	TestResults    []state.TestResult   `json:"test_results"`
	FilesGenerated []string             `json:"files_generated"`
	Iterations     int                  `json:"iterations"`
	EvidenceCount  int                  `json:"evidence_count"`
	ProposalCount  int                  `json:"proposal_count"`
	ChainValid     bool                 `json:"chain_valid"`
}
