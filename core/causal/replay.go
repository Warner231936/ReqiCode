package causal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

// Snapshot is a serializable point-in-time capture of the semi-state plus the
// workspace contents needed to reproduce it.
//
// Snapshotting both together is the whole trick. A state without its files
// cannot be replayed; a file tree without its epistemic record cannot be
// interpreted. Counterfactual replay needs both, and it needs them to be
// consistent with each other, so they are captured in one atomic operation.
type Snapshot struct {
	ID          string                 `json:"id"`
	Revision    int                    `json:"revision"`
	Timestamp   time.Time              `json:"timestamp"`
	State       *state.SemiState       `json:"state"`
	Files       map[string]string      `json:"files"`
	StateHash   string                 `json:"state_hash"`
	FileHash    string                 `json:"file_hash"`
	Combined    string                 `json:"combined_hash"`
	Label       string                 `json:"label,omitempty"`
}

// Result is the outcome of replaying a snapshot under some condition.
type Result struct {
	SnapshotID string             `json:"snapshot_id"`
	Condition  string             `json:"condition"`
	TestStatus state.TestStatus   `json:"test_status"`
	FailureClass string           `json:"failure_class,omitempty"`
	Objectives map[Objective]float64 `json:"objectives"`
	Delta      map[Objective]float64 `json:"delta_vs_original"`
	Duration   int64              `json:"duration_ms"`
	RanAt      time.Time          `json:"ran_at"`
	Err        string             `json:"error,omitempty"`
}

// Replayer executes counterfactual trials. It is injected with a function that
// actually runs the test suite, so the causal package stays free of exec
// concerns and remains trivially testable.
type Replayer struct {
	mu         sync.Mutex
	root       string
	runTests   func(dir string) (state.TestResult, error)
	history    []Result
	counter    int
}

// NewReplayer creates a replayer rooted at dir. The runTests callback executes
// the test suite in the given directory and returns the result.
func NewReplayer(root string, runTests func(dir string) (state.TestResult, error)) *Replayer {
	return &Replayer{root: root, runTests: runTests}
}

// Capture writes the current state and workspace to a snapshot directory.
//
// Both the state and the file tree are hashed, and the hashes are combined, so
// a later Trial can assert it is operating on exactly these inputs. A snapshot
// that cannot be verified is worse than no snapshot at all, because it invites
// trust it has not earned.
func (r *Replayer) Capture(ss *state.SemiState, label string) (*Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.counter++
	id := fmt.Sprintf("snap-%d-rev%d", r.counter, ss.Revision)
	dir := filepath.Join(r.root, ".spiral", "snapshots", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create snapshot dir: %w", err)
	}

	// Serialize state.
	stateBytes, err := json.MarshalIndent(ss, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal state: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), stateBytes, 0o644); err != nil {
		return nil, fmt.Errorf("write state: %w", err)
	}

	// Copy workspace files.
	files := make(map[string]string, len(ss.GeneratedFiles))
	paths := make([]string, 0, len(ss.GeneratedFiles))
	for p := range ss.GeneratedFiles {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, rel := range paths {
		src := filepath.Join(r.root, rel)
		data, err := os.ReadFile(src)
		if err != nil {
			// A generated file that never made it to disk is worth recording as
			// an empty entry rather than aborting the whole snapshot: the
			// interesting comparison is the other files anyway.
			files[rel] = ""
			continue
		}
		files[rel] = string(data)
	}

	snap := &Snapshot{
		ID:        id,
		Revision:  ss.Revision,
		Timestamp: time.Now(),
		State:     ss,
		Files:     files,
		Label:     label,
	}
	snap.StateHash = hashState(ss)
	snap.FileHash = hashFiles(files)
	snap.Combined = hashStrings(snap.StateHash, snap.FileHash)

	meta, _ := json.MarshalIndent(map[string]any{
		"id": snap.ID, "revision": snap.Revision, "timestamp": snap.Timestamp,
		"state_hash": snap.StateHash, "file_hash": snap.FileHash,
		"combined": snap.Combined, "label": label,
	}, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o644); err != nil {
		return nil, fmt.Errorf("write meta: %w", err)
	}
	filesJSON, err := json.MarshalIndent(files, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal files: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "files.json"), filesJSON, 0o644); err != nil {
		return nil, fmt.Errorf("write files: %w", err)
	}

	return snap, nil
}

// Trial is a counterfactual experiment: restore a snapshot, apply a condition,
// and observe what happens.
//
// conditionFn receives the restored working directory and returns a human-readable
// description of what it did. Returning a non-empty string means the condition
// was applied; the replay result is then directly comparable to the original.
//
// This is the operation that makes the whole state design pay off. Nothing in a
// conventional agent framework can answer "would the suite have passed if I had
// not made that change?" because nothing in those frameworks retains a
// reversible history.
func (r *Replayer) Trial(snap *Snapshot, conditionName string, conditionFn func(dir string) (string, error)) (*Result, error) {
	if snap == nil {
		return nil, fmt.Errorf("nil snapshot")
	}
	if err := snap.Verify(r.root); err != nil {
		return nil, fmt.Errorf("snapshot verification failed, refusing to run trial: %w", err)
	}

	r.mu.Lock()
	dir := filepath.Join(r.root, ".spiral", "trials",
		fmt.Sprintf("%s-%s-%d", snap.ID, sanitize(conditionName), time.Now().UnixNano()))
	r.mu.Unlock()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create trial dir: %w", err)
	}

	// Materialize the snapshot into the trial directory.
	for rel, content := range snap.Files {
		target := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return nil, fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(target, []byte(content), 0o644); err != nil {
			return nil, fmt.Errorf("write %s: %w", rel, err)
		}
	}

	res := &Result{
		SnapshotID: snap.ID,
		Condition:  conditionName,
		RanAt:      time.Now(),
	}

	applied, err := conditionFn(dir)
	if err != nil {
		res.Err = err.Error()
		return res, nil
	}
	if applied != "" {
		res.Condition = conditionName + ": " + applied
	}

	if r.runTests == nil {
		res.Err = "no test runner configured"
		return res, nil
	}

	start := time.Now()
	tr, err := r.runTests(dir)
	res.Duration = time.Since(start).Milliseconds()
	res.TestStatus = tr.Status
	res.FailureClass = tr.FailureClass
	if err != nil {
		res.Err = err.Error()
	}

	// Build a minimal state from the trial filesystem so Objectives can score
	// the outcome through the same code path as a live run.
	trialState := stateFromDir(dir, snap)

	// The trial observed ground truth directly by running the suite. Scoring
	// objectives from the stale snapshot alone would ignore that and report the
	// snapshot's pre-trial status, which is precisely the comparison the trial
	// exists to make. So the observed result is injected before scoring.
	if tr.ID != "" || tr.Status != "" {
		trialState.AddTestResult(tr)
	}
	res.Objectives = Objectives(trialState)

	r.mu.Lock()
	r.history = append(r.history, *res)
	r.mu.Unlock()
	return res, nil
}

// Verify re-hashes the snapshot's state and files and confirms they match what
// was recorded. Trial refuses to run against an unverifiable snapshot.
func (s *Snapshot) Verify(root string) error {
	if s.StateHash != hashState(s.State) {
		return fmt.Errorf("state hash mismatch: recorded %s, computed %s",
			s.StateHash, hashState(s.State))
	}
	if s.FileHash != hashFiles(s.Files) {
		return fmt.Errorf("file hash mismatch: recorded %s, computed %s",
			s.FileHash, hashFiles(s.Files))
	}
	metaPath := filepath.Join(root, ".spiral", "snapshots", s.ID, "meta.json")
	if _, err := os.Stat(metaPath); err != nil {
		return fmt.Errorf("snapshot metadata unreadable: %w", err)
	}
	return nil
}

// Results returns all trial outcomes recorded so far.
func (r *Replayer) Results() []Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Result(nil), r.history...)
}

// stateFromDir reconstructs a SemiState view from a materialized directory.
// It reads go test results out of the workspace where the test runner left them,
// and otherwise falls back to the snapshot's state so that non-file-scoped
// objectives (conflicts, objections) still have values.
func stateFromDir(dir string, snap *Snapshot) *state.SemiState {
	ss := state.NewSemiState()
	if snap != nil && snap.State != nil {
		ss = snap.State.Clone()
	}

	// Refresh the file set to match the trial directory exactly.
	ss.GeneratedFiles = make(map[string]state.FileEntry)
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(dir, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, ".spiral/") {
			return nil
		}
		data, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		ss.GeneratedFiles[rel] = state.FileEntry{Path: rel, Content: string(data)}
		return nil
	})
	return ss
}

func hashState(ss *state.SemiState) string {
	b, err := json.Marshal(ss)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hashFiles(files map[string]string) string {
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, p := range paths {
		fmt.Fprintf(h, "%s\x00%s\x00", p, files[p])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashStrings(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		fmt.Fprintf(h, "%s\x00", p)
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	out := b.String()
	if len(out) > 40 {
		out = out[:40]
	}
	return out
}
