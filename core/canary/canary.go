// Package canary implements the trial-before-promote mechanism that must exist
// before the system is allowed to modify its own source.
//
// A self-edit that passes the test suite can still regress the pipeline
// non-deterministically: LLM sampling, attention-decay ordering, and timing all
// make a single green run weak evidence. The canary addresses this by running
// the proposed change on an isolated branch and promoting it only when the
// measured outcome is strictly better than the incumbent on the incumbent's own
// terms.
//
// Two design rules are non-negotiable and are enforced structurally rather than
// by convention:
//
//  1. The canary holds no privileges. It cannot write to the real workspace.
//     Everything happens in a scratch directory that is deleted afterwards, so
//     a canary that goes wrong costs a temporary directory, not the codebase.
//
//  2. The canary cannot promote on a tie. "No worse" is not "better", and a
//     promotion rule that accepts ties will eventually promote a regression that
//     happened to cancel out a different regression.
package canary

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/kilo/spiral-codemaker/core/converge"
	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/state"
)

// Measurement is the comparable outcome of one branch.
type Measurement struct {
	Label string `json:"label"`
	// Potential is the Lyapunov objective from core/converge.
	Potential float64 `json:"potential"`
	// PassRate is the fraction of test results that passed.
	PassRate float64 `json:"pass_rate"`
	// Coverage is parsed from the test runner's coverage output when present.
	Coverage float64 `json:"coverage"`
	// GatePassed is whether the regression gate accepted this branch.
	GatePassed bool `json:"gate_passed"`
	// ChainValid is whether the branch's own tamper chain verifies.
	ChainValid bool `json:"chain_valid"`
	// TestOutcomes is the raw pass/fail set, kept for human inspection.
	TestOutcomes []string `json:"test_outcomes"`
	// ChainHead lets two branches be compared for divergence.
	ChainHead string `json:"chain_head"`
	// RetrunPerformed records whether the edited workspace was actually
	// re-tested. A candidate measured without a re-test cannot be trusted to
	// differ from its baseline, and this flag makes that visible in the report.
	RetrunPerformed bool `json:"retrun_performed"`
	// TestOutput is the re-test's captured output. A canary that blocks without
	// saying why is unactionable, and an operator who cannot see the compiler
	// error will either disable the canary or trust it wrongly.
	TestOutput string `json:"test_output,omitempty"`
	// FailureClass is the classified re-test failure, for quick reading.
	FailureClass string `json:"failure_class,omitempty"`
}

// Comparable scores a measurement for promotion decisions. Higher is better.
//
// Gate and chain status are folded in as hard gates rather than weighted terms.
// A branch that fails the regression gate is not "slightly worse" — it is
// disqualified, and letting a sufficiently good potential function outvote a
// broken gate would reintroduce exactly the failure the gate exists to prevent.
func (m Measurement) Comparable() float64 {
	return m.Potential
}

// Qualified reports whether the branch is even eligible for promotion.
func (m Measurement) Qualified() (bool, string) {
	if !m.GatePassed {
		return false, "regression gate rejected this branch; promotion is blocked regardless of potential"
	}
	if !m.ChainValid {
		return false, "tamper chain does not verify; history cannot be trusted"
	}
	return true, ""
}

// Verdict is the outcome of comparing a candidate branch against the incumbent.
type Verdict struct {
	Promote  bool   `json:"promote"`
	Reason   string `json:"reason"`
	Delta    float64 `json:"delta"`
	Baseline Measurement `json:"baseline"`
	Candidate Measurement `json:"candidate"`
}

// ImprovementThreshold is how much better a candidate must be before promotion.
//
// Non-zero by design. Accepting any positive delta would promote a change that
// improved the potential by 0.0001, which is inside the noise of a system whose
// potential includes an evidence-density term that moves with every iteration.
// The threshold is set just above ComputePotential's Epsilon.
const ImprovementThreshold = converge.Epsilon * 2

// BranchResult is what a pipeline run yields: its epistemic state and the
// workspace it actually populated.
//
// The workspace path is returned rather than discovered. Discovery by probing
// for a "output" subdirectory silently misfires when the probe runs before the
// workspace exists, and the failure mode is that an edit lands somewhere the
// test suite never reads — which makes the canary score every candidate as
// identical to its baseline. A wrong-but-confident measurement is worse than no
// measurement.
type BranchResult struct {
	State     *state.SemiState
	Workspace string
}

// Runner executes a branch. Injected rather than hardcoded so the canary can be
// tested without running the real pipeline, and so a caller can supply a
// pipeline invocation appropriate to the change under trial.
type Runner func(trialDir string) (*BranchResult, error)

// WorkspaceTester re-runs the test suite against an already-built workspace.
//
// This is not optional. The pipeline regenerates its own files from its own
// state, so the state's test results describe the pipeline's run and are blind
// to any edit applied to the workspace afterwards. Measuring the state alone
// would score every candidate identical to its baseline, which is a canary that
// can never detect anything. The authoritative signal for an edit is whether the
// edited artifact still builds and passes.
type WorkspaceTester func(workspaceDir string) (state.TestResult, error)

// Harness runs candidates in isolation and decides promotion.
type Harness struct {
	mu sync.Mutex

	// gateRoot holds the regression baselines used to qualify branches.
	gateRoot string
	// run executes a branch in a scratch workspace.
	run Runner
	// testWorkspace re-tests an edited workspace. When nil, the harness falls
	// back to the pipeline's own results, which is correct for a no-edit
	// baseline and insufficient for a candidate.
	testWorkspace WorkspaceTester
	// scratchParent is where isolated branch directories are created. They are
	// removed after every trial, success or failure.
	scratchParent string
	// keepScratch retains trial directories for inspection. Off by default
	// because a failed trial's files are rarely what you want to read; the
	// measurement summary is the useful artefact.
	keepScratch bool
}

// NewHarness creates a canary harness without workspace re-testing.
func NewHarness(gateRoot, scratchParent string, run Runner) *Harness {
	return &Harness{
		gateRoot:     gateRoot,
		run:          run,
		scratchParent: scratchParent,
	}
}

// WithWorkspaceTester supplies the re-test function that makes candidate
// measurement meaningful. Without it, every candidate scores identically to its
// baseline.
func (h *Harness) WithWorkspaceTester(t WorkspaceTester) *Harness {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.testWorkspace = t
	return h
}

// KeepScratch retains trial directories for inspection.
func (h *Harness) KeepScratch(on bool) *Harness {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.keepScratch = on
	return h
}

// Edit is a proposed change to trial.
type Edit struct {
	// Name identifies the edit in the report.
	Name string
	// Apply mutates the populated workspace. It receives the path the pipeline
	// reported, not a guessed one, and it runs after the pipeline has built the
	// project. Applying before the build would write into a directory the build
	// then overwrites.
	Apply func(workspace string) error
}

// Trial runs the pipeline, then applies the edit to the resulting workspace, then
// re-tests it.
//
// The order is load-bearing: build, edit, re-test. Applying the edit first and
// building after would measure the pipeline's own regeneration rather than the
// edited artifact.
func (h *Harness) Trial(e Edit, scenario string) (Measurement, error) {
	dir, err := os.MkdirTemp(h.scratchParent, "canary-*")
	if err != nil {
		return Measurement{}, fmt.Errorf("create scratch dir: %w", err)
	}
	if !h.keepScratch {
		defer os.RemoveAll(dir)
	}

	if e.Apply == nil {
		return Measurement{}, fmt.Errorf("edit %q has no Apply function", e.Name)
	}

	res, err := h.run(dir)
	if err != nil {
		return Measurement{}, fmt.Errorf("edit %q: branch run failed: %w", e.Name, err)
	}
	if res == nil || res.Workspace == "" {
		return Measurement{}, fmt.Errorf("edit %q: pipeline did not report a workspace path", e.Name)
	}

	// Confirm the edit lands inside the workspace it was given. A runner that
	// reports the wrong directory would otherwise let the edit escape the build
	// silently, and the trial would pass without ever having been exercised.
	if !withinDir(res.Workspace, dir) {
		return Measurement{}, fmt.Errorf("edit %q: reported workspace %q is outside the trial directory %q",
			e.Name, res.Workspace, dir)
	}

	if err := e.Apply(res.Workspace); err != nil {
		return Measurement{}, fmt.Errorf("edit %q failed to apply: %w", e.Name, err)
	}

	m := h.measure(e.Name, res.State, scenario)

	if h.testWorkspace != nil {
		tr, terr := h.testWorkspace(res.Workspace)
		if terr != nil {
			return m, fmt.Errorf("edit %q: workspace re-test failed: %w", e.Name, terr)
		}
		m.applyReTest(tr)
		m.GatePassed = h.gatePasses(res.State, scenario)
		m.RetrunPerformed = true
	}
	return m, nil
}

// withinDir reports whether path is inside root. Used to catch a runner handing
// back a workspace outside its own trial directory, which would let one trial
// contaminate another.
func withinDir(path, root string) bool {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// gatePasses runs the regression gate against a branch's state.
func (h *Harness) gatePasses(ss *state.SemiState, scenario string) bool {
	if ss == nil {
		return false
	}
	h.mu.Lock()
	root := h.gateRoot
	h.mu.Unlock()
	return regression.NewGate(root).Check(regression.Project(scenario, ss)).Pass
}

// applyReTest folds a fresh test result into the measurement, replacing whatever
// the pipeline reported. The edited artifact is what matters, not the run that
// produced it.
func (m *Measurement) applyReTest(tr state.TestResult) {
	status := string(tr.Status)
	m.TestOutcomes = []string{status + "|" + tr.FailureClass}
	m.FailureClass = tr.FailureClass
	m.TestOutput = tail(tr.Stdout+tr.Stderr, outputTailLimit)

	if tr.Status == state.TestPassed {
		m.PassRate = 1
		m.Potential += potentialTestPassBonus
	} else {
		m.PassRate = 0
		m.Potential -= potentialTestFailPenalty
	}
	if m.Potential < 0 {
		m.Potential = 0
	}
}

// outputTailLimit bounds how much test output is retained. Test output can be
// enormous, and the useful part — the compile error or the failing assertion —
// is at the end, not the beginning.
const outputTailLimit = 4000

func tail(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return "..." + s[len(s)-limit:]
}

// potentialTestPassBonus and potentialTestFailPenalty are the adjustments a
// fresh test result applies to the potential. Passing earns credit and failing
// costs more than it earns, because a broken artifact is strictly worse than an
// unmeasured one.
const (
	potentialTestPassBonus = 0.10
	potentialTestFailPenalty = 0.35
)

// chainHead is not tracked on Measurement itself; the gate directory belongs to
// the Harness, which is why gatePasses is a method there rather than here.

// Measure scores a completed branch.
func (h *Harness) measure(label string, ss *state.SemiState, scenario string) Measurement {
	m := Measurement{Label: label}

	if ss == nil {
		return m
	}

	p := converge.ComputePotential(ss)
	m.Potential = p.Value

	results := ss.GetTestResults()
	if len(results) > 0 {
		passed := 0
		for _, r := range results {
			if r.Status == state.TestPassed {
				passed++
			}
			m.TestOutcomes = append(m.TestOutcomes, string(r.Status)+"|"+r.FailureClass)
		}
		m.PassRate = float64(passed) / float64(len(results))
	}
	sort.Strings(m.TestOutcomes)

	m.ChainValid = ss.VerifyChain().Valid
	m.ChainHead = ss.ChainHead()

	// A branch that produced no test results has not been verified at all.
	// Scoring it on potential alone would let an inert branch win by producing
	// no evidence, which is the opposite of an improvement.
	if len(results) == 0 {
		m.GatePassed = false
		return m
	}

	gate := regression.NewGate(h.gateRoot)
	m.GatePassed = gate.Check(regression.Project(scenario, ss)).Pass
	return m
}

// Baseline runs the unmodified pipeline to establish the incumbent.
func (h *Harness) Baseline(scenario string) (Measurement, error) {
	dir, err := os.MkdirTemp(h.scratchParent, "canary-base-*")
	if err != nil {
		return Measurement{}, fmt.Errorf("create scratch dir: %w", err)
	}
	if !h.keepScratch {
		defer os.RemoveAll(dir)
	}

	ss, err := h.run(dir)
	if err != nil {
		return Measurement{}, fmt.Errorf("baseline run failed: %w", err)
	}
	if ss == nil || ss.State == nil {
		return Measurement{}, fmt.Errorf("baseline run produced no state")
	}
	m := h.measure("baseline", ss.State, scenario)
	if len(ss.State.GetTestResults()) == 0 {
		// A baseline that cannot verify itself cannot arbitrate. Returning a
		// failed measurement here is deliberate: it blocks every subsequent
		// promotion, which is the correct outcome for a harness that cannot
		// measure.
		return m, fmt.Errorf("baseline produced no test results; the harness cannot measure candidates reliably")
	}
	return m, nil
}

// Compare decides whether a candidate should replace the incumbent.
//
// Promotion requires, in order: both branches qualified, the candidate's
// potential strictly exceeding the incumbent's by more than the threshold, and
// the candidate's pass rate not being worse. Each is checked separately so the
// rejection reason names the actual cause.
func Compare(baseline, candidate Measurement) Verdict {
	v := Verdict{Baseline: baseline, Candidate: candidate}
	v.Delta = candidate.Comparable() - baseline.Comparable()

	if ok, why := baseline.Qualified(); !ok {
		v.Reason = "cannot promote: the incumbent itself is unqualified — " + why
		return v
	}
	if ok, why := candidate.Qualified(); !ok {
		v.Reason = "cannot promote: " + why
		return v
	}

	if candidate.PassRate < baseline.PassRate {
		v.Reason = fmt.Sprintf("cannot promote: pass rate regressed from %.2f to %.2f",
			baseline.PassRate, candidate.PassRate)
		return v
	}

	if v.Delta <= ImprovementThreshold {
		v.Reason = fmt.Sprintf("cannot promote: delta %+.4f does not exceed the improvement threshold %.4f "+
			"(ties and noise are not improvements)", v.Delta, ImprovementThreshold)
		return v
	}

	v.Promote = true
	v.Reason = fmt.Sprintf("promote: potential %+.4f (%s)", v.Delta, candidate.ChainHead[:min(12, len(candidate.ChainHead))])
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// Report is a serializable record of a canary session, written to disk so a
// promotion decision can be audited after the fact rather than trusted.
type Report struct {
	Scenario  string       `json:"scenario"`
	Baseline  Measurement  `json:"baseline"`
	Candidate Measurement  `json:"candidate"`
	Verdict   Verdict      `json:"verdict"`
	Applied   string       `json:"applied_edit,omitempty"`
	Timestamp string       `json:"timestamp"`
}

// Write persists a report.
func Write(dir string, r Report) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	name := "canary-" + sanitize(r.Candidate.Label) + ".json"
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
		return fmt.Errorf("write report: %w", err)
	}
	return nil
}

// FormatVerdict renders a verdict for a terminal.
func FormatVerdict(v Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", v.Reason)
	fmt.Fprintf(&b, "  baseline : potential=%.4f pass=%.2f gate=%v chain=%v\n",
		v.Baseline.Potential, v.Baseline.PassRate, v.Baseline.GatePassed, v.Baseline.ChainValid)
	fmt.Fprintf(&b, "  candidate: potential=%.4f pass=%.2f gate=%v chain=%v\n",
		v.Candidate.Potential, v.Candidate.PassRate, v.Candidate.GatePassed, v.Candidate.ChainValid)

	// A blocked candidate with a re-test failure must show the reason. Suppressing
	// it leaves the operator unable to distinguish "the edit is wrong" from "the
	// toolchain is misconfigured", and the second case is far more common than
	// people expect.
	if v.Candidate.FailureClass != "" {
		fmt.Fprintf(&b, "  failure  : %s\n", v.Candidate.FailureClass)
		if v.Candidate.TestOutput != "" {
			fmt.Fprintf(&b, "  output   :\n%s\n", indent(v.Candidate.TestOutput))
		}
	}
	return b.String()
}

func indent(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	// Show only the tail; the head of a long failure is usually banner noise.
	if len(lines) > 40 {
		lines = lines[len(lines)-40:]
	}
	for i, l := range lines {
		lines[i] = "    " + l
	}
	return strings.Join(lines, "\n")
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
	if len(out) > 48 {
		out = out[:48]
	}
	return out
}
