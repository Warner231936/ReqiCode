package regression_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/state"
)

func buildState() *state.SemiState {
	ss := state.NewSemiState()
	ss.AppendRequirement(state.Requirement{ID: "req-1", Content: "build a service"})
	ss.SetArchitecturePlan(&state.ArchitecturePlan{
		Description:   "Go HTTP server",
		AlternativeID: "plan-http",
		Components: []state.ComponentSpec{
			{Name: "main", Path: "cmd/server/main.go", Description: "entry"},
			{Name: "store", Path: "internal/store/store.go", Description: "storage"},
		},
		Endpoints: []state.EndpointSpec{{Method: "GET", Path: "/resources"}},
	})
	ss.AddDecision(state.Decision{ID: "dec-1", Decision: "plan-http"})
	ss.AddHypothesis(state.Hypothesis{ID: "hyp-1", Content: "design works", Status: state.HypothesisProposed})
	ss.AddTestResult(state.TestResult{Command: "go test", Status: state.TestPassed})
	ss.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Content: "green", Strength: state.ConfidenceHigh})
	ss.GeneratedFiles["cmd/server/main.go"] = state.FileEntry{Path: "cmd/server/main.go"}
	ss.GeneratedFiles["internal/store/store.go"] = state.FileEntry{Path: "internal/store/store.go"}
	return ss
}

func TestProjectionIsDeterministic(t *testing.T) {
	a := regression.Project("s", buildState())
	b := regression.Project("s", buildState())

	if len(a.Files) != len(b.Files) {
		t.Fatalf("file lists differ in length")
	}
	for i := range a.Files {
		if a.Files[i] != b.Files[i] {
			t.Errorf("file list not stable at %d: %q vs %q", i, a.Files[i], b.Files[i])
		}
	}
	if a.ChainHead != b.ChainHead {
		t.Error("chain head must be stable across identical runs")
	}
}

func TestProjectionIgnoresInsertionOrder(t *testing.T) {
	build := func(reverse bool) regression.Projection {
		ss := state.NewSemiState()
		if reverse {
			ss.AddDecision(state.Decision{ID: "d2", Decision: "second"})
			ss.AddDecision(state.Decision{ID: "d1", Decision: "first"})
		} else {
			ss.AddDecision(state.Decision{ID: "d1", Decision: "first"})
			ss.AddDecision(state.Decision{ID: "d2", Decision: "second"})
		}
		return regression.Project("s", ss)
	}
	if fmtS(build(false)) != fmtS(build(true)) {
		t.Error("insertion order must not change the canonical projection")
	}
}

func TestProjectionAggregatesEvidenceByKind(t *testing.T) {
	ss := state.NewSemiState()
	for i := 0; i < 3; i++ {
		ss.AddEvidence(state.Evidence{Type: state.EvidenceAnalysis, Content: "x", Strength: state.ConfidenceLow})
	}
	ss.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Content: "y", Strength: state.ConfidenceHigh})

	p := regression.Project("s", ss)
	if p.EvidenceKinds["analysis"] != 3 {
		t.Errorf("expected 3 analysis evidence, got %d", p.EvidenceKinds["analysis"])
	}
	if p.EvidenceKinds["test_pass"] != 1 {
		t.Errorf("expected 1 test_pass evidence, got %d", p.EvidenceKinds["test_pass"])
	}
}

func TestCheckFailsWithoutBaseline(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	v := g.Check(regression.Project("never-captured", buildState()))
	if v.Pass {
		t.Fatal("a missing baseline must fail, not silently pass")
	}
}

func TestCaptureThenCheckPasses(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	v := g.Check(regression.Project("s", buildState()))
	if !v.Pass {
		t.Errorf("captured baseline should pass: %s", v.Summary)
	}
}

func TestCheckDetectsChangedRequirement(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}

	changed := buildState()
	changed.Requirements[0].Content = "build something else"

	v := g.Check(regression.Project("s", changed))
	if v.Pass {
		t.Fatal("a changed requirement must be detected")
	}
	// A modified entry surfaces as a paired removal and addition rather than a
	// single in-place change, because the projection is a set of strings and set
	// membership is what can be compared. Both halves must be present for the
	// reader to see what came and what went.
	var removed, added bool
	for _, d := range v.Drifts {
		if d.Field != "requirements" {
			continue
		}
		for _, e := range d.Expected {
			if e == "req-1|build a service" {
				removed = true
			}
		}
		for _, a := range d.Actual {
			if a == "req-1|build something else" {
				added = true
			}
		}
	}
	if !removed {
		t.Error("drift must name the previous requirement")
	}
	if !added {
		t.Error("drift must name the new requirement")
	}
}

func TestCheckDetectsRemovedFile(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	changed := buildState()
	delete(changed.GeneratedFiles, "internal/store/store.go")

	v := g.Check(regression.Project("s", changed))
	if v.Pass {
		t.Fatal("a removed generated file must be detected")
	}
}

func TestCheckDetectsTestRegression(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	broken := buildState()
	broken.TestResults = []state.TestResult{{
		Command: "go test", Status: state.TestFailed, FailureClass: "build_error",
	}}

	v := g.Check(regression.Project("s", broken))
	if v.Pass {
		t.Fatal("a passing-to-failing change must be detected")
	}
}

func TestCheckDetectsAddedConflict(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	withConflict := buildState()
	withConflict.AddConflict(state.Conflict{ID: "c1", Subject: "storage", Severity: state.ConflictHigh})

	v := g.Check(regression.Project("s", withConflict))
	if v.Pass {
		t.Fatal("a new conflict must be detected")
	}
}

func TestCheckDetectsPlanRemoval(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	noPlan := state.NewSemiState()
	v := g.Check(regression.Project("s", noPlan))
	if v.Pass {
		t.Fatal("losing the architecture plan must be detected")
	}
}

func TestCheckDetectsSchemaVersionChange(t *testing.T) {
	dir := t.TempDir()
	g := regression.NewGate(dir)
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	// Rewrite the pinned baseline with a bumped schema version, simulating a
	// projection-format change that the baseline has not caught up with.
	p := regression.Project("s", buildState())
	p.SchemaVersion = 99
	if err := g.Capture(p); err != nil {
		t.Fatal(err)
	}

	v := g.Check(regression.Project("s", buildState()))
	if v.Pass {
		t.Fatal("a schema mismatch must fail rather than compare incompatible shapes")
	}
}

func TestCheckAllAndAllPass(t *testing.T) {
	dir := t.TempDir()
	g := regression.NewGate(dir)
	if err := g.Capture(regression.Project("a", buildState())); err != nil {
		t.Fatal(err)
	}
	if err := g.Capture(regression.Project("b", buildState())); err != nil {
		t.Fatal(err)
	}

	verdicts := g.CheckAll([]regression.Projection{
		regression.Project("a", buildState()),
		regression.Project("b", buildState()),
	})
	if !regression.AllPass(verdicts) {
		t.Errorf("both should pass, got %s", regression.FormatVerdicts(verdicts))
	}

	mixed := g.CheckAll([]regression.Projection{
		regression.Project("a", buildState()),
		regression.Project("missing", buildState()),
	})
	if regression.AllPass(mixed) {
		t.Error("a set containing a failure must not report all-pass")
	}
}

func TestAllPassOnEmptyIsFalse(t *testing.T) {
	if regression.AllPass(nil) {
		t.Error("an empty verdict set must not report all-pass; that would let a gate silently pass")
	}
}

func TestFormatVerdictsMentionsFields(t *testing.T) {
	g := regression.NewGate(t.TempDir())
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatal(err)
	}
	changed := buildState()
	changed.Requirements[0].Content = "different"

	v := g.Check(regression.Project("s", changed))
	out := regression.FormatVerdicts([]regression.Verdict{v})
	if out == "" {
		t.Fatal("formatting a failure must produce output")
	}
	if !contains(out, "requirements") {
		t.Errorf("output should name the drifting field, got:\n%s", out)
	}
}

func TestCaptureCreatesDirectory(t *testing.T) {
	nested := filepath.Join(t.TempDir(), "a", "b", "c")
	g := regression.NewGate(nested)
	if err := g.Capture(regression.Project("s", buildState())); err != nil {
		t.Fatalf("capture must create missing directories: %v", err)
	}
	if _, err := os.Stat(g.Path("s")); err != nil {
		t.Errorf("baseline file should exist: %v", err)
	}
}

func TestNilStateProjectsSafely(t *testing.T) {
	p := regression.Project("s", nil)
	if p.Name != "s" {
		t.Errorf("expected name preserved, got %q", p.Name)
	}
	if p.Plan != nil {
		t.Error("nil state must not produce a plan")
	}
}

func contains(haystack, needle string) bool {
	return strings.Contains(haystack, needle)
}

func fmtS(p regression.Projection) string {
	b, _ := json.Marshal(p)
	return string(b)
}
