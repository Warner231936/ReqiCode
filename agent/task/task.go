// Package task holds the persistent state of an agent run.
//
// The state is written after every step, not at the end. That is what makes a
// crashed or interrupted run resumable: a process killed during a repair round
// leaves a record of what it was doing, what it had already changed, and which
// verification had failed. Without per-step persistence the only alternative is a
// log, and reconstructing task state from a log is guesswork.
package task

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Status is a lifecycle state.
type Status string

const (
	StatusPending   Status = "pending"
	StatusRunning   Status = "running"
	StatusComplete  Status = "complete"
	StatusFailed    Status = "failed"
	StatusBlocked   Status = "blocked"
	StatusAbandoned Status = "abandoned"
)

// StepStatus is a step lifecycle state.
type StepStatus string

const (
	StepPending  StepStatus = "pending"
	StepActive   StepStatus = "active"
	StepComplete StepStatus = "complete"
	StepFailed   StepStatus = "failed"
	StepSkipped  StepStatus = "skipped"
)

// Step is one unit of planned work.
type Step struct {
	ID          int        `json:"id"`
	Description string     `json:"description"`
	Status      StepStatus `json:"status"`
	Tool        string     `json:"tool,omitempty"`
	Attempts    int        `json:"attempts"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	Error       string     `json:"error,omitempty"`
	Result      string     `json:"result,omitempty"`
}

// Attempt records one repair round.
type Attempt struct {
	Number        int       `json:"number"`
	StartedAt     time.Time `json:"started_at"`
	EndedAt       *time.Time `json:"ended_at,omitempty"`
	Verified      bool      `json:"verified"`
	FailureKind   string    `json:"failure_kind,omitempty"`
	FailureDetail string    `json:"failure_detail,omitempty"`
	FilesChanged  []string  `json:"files_changed,omitempty"`
	ReviewVerdict string    `json:"review_verdict,omitempty"`
	ReviewNotes   []string  `json:"review_notes,omitempty"`
	Note          string    `json:"note,omitempty"`
}

// Task is a persisted unit of work.
type Task struct {
	ID          string    `json:"id"`
	Description string    `json:"description"`
	Mode        string    `json:"mode"`
	Status      Status    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	StartedAt   time.Time `json:"started_at,omitempty"`
	FinishedAt  *time.Time `json:"finished_at,omitempty"`

	Steps        []Step   `json:"steps"`
	CurrentStep  int      `json:"current_step"`
	Attempts     int      `json:"attempts"`
	MaxAttempts  int      `json:"max_attempts"`
	History      []Attempt `json:"history"`

	FilesChanged []string        `json:"files_changed"`
	FilesRead    []string        `json:"files_read"`
	Tests        []TestOutcome   `json:"tests"`
	Plan         []Step          `json:"-"`
	Summary      string          `json:"summary,omitempty"`
	Blockers     []string        `json:"blockers,omitempty"`
	Context      ContextSnapshot `json:"context,omitempty"`

	// ReviewAccepted records whether the reviewer approved the final result.
	ReviewAccepted bool `json:"review_accepted"`
	// Evidence records the observations that justify the final status.
	Evidence []string `json:"evidence,omitempty"`
}

// TestOutcome is one verification result.
type TestOutcome struct {
	Command  string        `json:"command"`
	Success  bool          `json:"success"`
	ExitCode int           `json:"exit_code"`
	Kind     string        `json:"kind,omitempty"`
	Duration time.Duration `json:"duration"`
	At       time.Time     `json:"at"`
	Summary  string        `json:"summary,omitempty"`
}

// ContextSnapshot records what the agent knew when the task began.
type ContextSnapshot struct {
	Ecosystem string   `json:"ecosystem,omitempty"`
	Files     []string `json:"files,omitempty"`
	BuildCmd  []string `json:"build_cmd,omitempty"`
	TestCmd   []string `json:"test_cmd,omitempty"`
}

// Active reports whether the task can still progress.
func (t *Task) Active() bool {
	return t.Status == StatusRunning || t.Status == StatusPending
}

// Resumable reports whether a task left unfinished work worth continuing.
func (t *Task) Resumable() bool {
	if t.Status == StatusComplete || t.Status == StatusAbandoned {
		return false
	}
	return t.CurrentStep < len(t.Steps) || t.Attempts < t.MaxAttempts
}

// RemainingSteps returns steps that are not complete.
func (t *Task) RemainingSteps() []Step {
	var out []Step
	for _, s := range t.Steps {
		if s.Status != StepComplete && s.Status != StepSkipped {
			out = append(out, s)
		}
	}
	return out
}

// BeginAttempt opens a repair round.
func (t *Task) BeginAttempt(kind, detail string) *Attempt {
	t.Attempts++
	t.History = append(t.History, Attempt{
		Number:       t.Attempts,
		StartedAt:    time.Now(),
		FailureKind:  kind,
		FailureDetail: detail,
	})
	return &t.History[len(t.History)-1]
}

// EndAttempt closes the current repair round.
func (t *Task) EndAttempt(verified bool, note string) {
	if len(t.History) == 0 {
		return
	}
	a := &t.History[len(t.History)-1]
	now := time.Now()
	a.EndedAt = &now
	a.Verified = verified
	a.Note = note
}

// AddFileChanged records a modified file.
func (t *Task) AddFileChanged(path string) {
	for _, p := range t.FilesChanged {
		if p == path {
			return
		}
	}
	t.FilesChanged = append(t.FilesChanged, path)
}

// AddFileRead records a file the agent consulted, so a resumed run does not
// re-read the entire tree from scratch.
func (t *Task) AddFileRead(path string) {
	for _, p := range t.FilesRead {
		if p == path {
			return
		}
	}
	t.FilesRead = append(t.FilesRead, path)
}

// AddTest records a verification outcome.
func (t *Task) AddTest(o TestOutcome) {
	if o.At.IsZero() {
		o.At = time.Now()
	}
	t.Tests = append(t.Tests, o)
}

// Store persists tasks under a directory.
type Store struct {
	dir string
	mu  sync.Mutex
}

// NewStore creates a task store.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create task dir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

func (s *Store) path(id string) string {
	return filepath.Join(s.dir, sanitizeID(id)+".json")
}

func sanitizeID(id string) string {
	var b strings.Builder
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	s := b.String()
	if len(s) > 96 {
		s = s[:96]
	}
	if s == "" {
		s = "task"
	}
	return s
}

// Save writes a task atomically.
func (s *Store) Save(t *Task) error {
	if t.ID == "" {
		t.ID = NewID(t.Description)
	}
	t.UpdatedAt = time.Now()
	if t.CreatedAt.IsZero() {
		t.CreatedAt = t.UpdatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	blob, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	path := s.path(t.ID)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(blob, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a task by id.
func (s *Store) Load(id string) (*Task, error) {
	data, err := os.ReadFile(s.path(id))
	if err != nil {
		return nil, err
	}
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return nil, fmt.Errorf("parse task %s: %w", id, err)
	}
	return &t, nil
}

// Delete removes a task.
func (s *Store) Delete(id string) error {
	err := os.Remove(s.path(id))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// List returns all stored tasks, most recently updated first.
func (s *Store) List() ([]*Task, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, err
	}
	var out []*Task
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		t, err := s.Load(strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

// Latest returns the most recently updated task.
func (s *Store) Latest() (*Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, os.ErrNotExist
	}
	return all[0], nil
}

// LatestResumable returns the most recent task that can still progress.
func (s *Store) LatestResumable() (*Task, error) {
	all, err := s.List()
	if err != nil {
		return nil, err
	}
	for _, t := range all {
		if t.Resumable() {
			return t, nil
		}
	}
	return nil, os.ErrNotExist
}

// NewTask builds a task with a fresh id.
func NewTask(description, mode string, maxAttempts int) *Task {
	now := time.Now()
	return &Task{
		ID:           NewID(description),
		Description:  description,
		Mode:         mode,
		Status:       StatusPending,
		CreatedAt:    now,
		UpdatedAt:    now,
		MaxAttempts:  maxAttempts,
		CurrentStep:  0,
	}
}

// NewID derives a stable, readable id from a description.
//
// The same request always produces the same id, so re-running an interrupted task
// reuses its state instead of creating a parallel history.
func NewID(description string) string {
	h := fnv64(description)
	stamp := time.Now().UTC().Format("20060102-150405")
	slug := slug(description, 40)
	return fmt.Sprintf("%s-%s-%s", stamp, slug, fmt.Sprintf("%04x", h&0xffff))
}

func slug(s string, max int) string {
	var b strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
		if b.Len() >= max {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "task"
	}
	return out
}

func fnv64(s string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= prime
	}
	return h
}

// RenderSummary produces a human-readable task report.
func (t *Task) RenderSummary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Task %s\n", t.ID)
	fmt.Fprintf(&b, "  description : %s\n", t.Description)
	fmt.Fprintf(&b, "  mode        : %s\n", t.Mode)
	fmt.Fprintf(&b, "  status      : %s\n", t.Status)
	fmt.Fprintf(&b, "  attempts    : %d/%d\n", t.Attempts, t.MaxAttempts)
	fmt.Fprintf(&b, "  steps       : %d/%d complete\n", t.completedSteps(), len(t.Steps))
	if len(t.FilesChanged) > 0 {
		fmt.Fprintf(&b, "  changed     : %s\n", strings.Join(t.FilesChanged, ", "))
	}
	if len(t.Tests) > 0 {
		pass, fail := 0, 0
		for _, te := range t.Tests {
			if te.Success {
				pass++
			} else {
				fail++
			}
		}
		fmt.Fprintf(&b, "  tests       : %d passed, %d failed\n", pass, fail)
	}
	if len(t.Blockers) > 0 {
		fmt.Fprintf(&b, "  blockers    :\n")
		for _, bl := range t.Blockers {
			fmt.Fprintf(&b, "    - %s\n", bl)
		}
	}
	if t.Summary != "" {
		fmt.Fprintf(&b, "  summary     : %s\n", t.Summary)
	}
	return b.String()
}

func (t *Task) completedSteps() int {
	n := 0
	for _, s := range t.Steps {
		if s.Status == StepComplete || s.Status == StepSkipped {
			n++
		}
	}
	return n
}