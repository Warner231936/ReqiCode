package causal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kilo/spiral-codemaker/core/state"
)

func prop(id, file string) state.CodeProposal {
	return state.CodeProposal{
		ID:              id,
		File:            file,
		Operation:       state.OpCreate,
		Reason:          "add " + file,
		OriginatingUnit: "unit-code-generator",
		Provenance:      state.NewProvenance("unit-code-generator"),
	}
}

func TestRecordIsIdempotentPerProposal(t *testing.T) {
	l := NewLedger()
	a := l.Record(prop("p1", "a.go"), "template", 0)
	b := l.Record(prop("p1", "a.go"), "template", 0)
	if a != b {
		t.Error("recording the same proposal twice must return the same intervention")
	}
	if len(l.Interventions()) != 1 {
		t.Errorf("expected 1 intervention, got %d", len(l.Interventions()))
	}
}

func TestRecordCapturesMetadata(t *testing.T) {
	l := NewLedger()
	iv := l.Record(prop("p1", "internal/store.go"), "template-generate", 128)
	if iv.ProposalID != "p1" || iv.Target != "internal/store.go" {
		t.Errorf("intervention did not capture proposal identity: %+v", iv)
	}
	if iv.Strategy != "template-generate" || iv.Tokens != 128 {
		t.Errorf("intervention did not capture strategy/tokens: %+v", iv)
	}
	if iv.Unit != "unit-code-generator" {
		t.Errorf("expected originating unit recorded, got %q", iv.Unit)
	}
}

func TestAttributeNoPendingIsNoOp(t *testing.T) {
	l := NewLedger()
	ss := state.NewSemiState()
	attr := l.Attribute(ss)
	if len(attr.Attributed) != 0 {
		t.Errorf("expected nothing attributed, got %v", attr.Attributed)
	}
}

func TestAttributeCreditsPassingChange(t *testing.T) {
	l := NewLedger()

	before := state.NewSemiState()
	before.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	l.ObserveBaseline(before)

	l.Record(prop("p1", "a.go"), "fix-build", 0)

	after := state.NewSemiState()
	after.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	attr := l.Attribute(after)

	if len(attr.Attributed) != 1 {
		t.Fatalf("expected 1 attributed intervention, got %d", len(attr.Attributed))
	}
	credit, ok := l.CreditFor("p1")
	if !ok {
		t.Fatal("no credit recorded for p1")
	}
	if credit <= 0 {
		t.Errorf("a change that turned failing tests green must earn positive credit, got %.3f", credit)
	}
}

func TestAttributeNegativeCreditForRegression(t *testing.T) {
	l := NewLedger()

	before := state.NewSemiState()
	before.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	l.ObserveBaseline(before)

	l.Record(prop("p1", "a.go"), "risky-change", 0)

	after := state.NewSemiState()
	after.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	l.Attribute(after)

	credit, _ := l.CreditFor("p1")
	if credit >= 0 {
		t.Errorf("a change that broke a passing suite must earn negative credit, got %.3f", credit)
	}
}

func TestConfidenceDiscountedByCohortSize(t *testing.T) {
	// Credit is the *observed* delta and must not be deflated by attribution
	// uncertainty; the uncertainty belongs in Confidence. Asserting otherwise
	// would bake a lie into the ledger: pretending we know less than we do.
	solo := NewLedger()
	solo.ObserveBaseline(state.NewSemiState())
	solo.Record(prop("p1", "a.go"), "s", 0)
	after := state.NewSemiState()
	after.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	solo.Attribute(after)

	many := NewLedger()
	many.ObserveBaseline(state.NewSemiState())
	for i := 0; i < 8; i++ {
		many.Record(prop(string(rune('a'+i))+".go", "x.go"), "s", 0)
	}
	many.Attribute(after)

	soloIv := solo.Interventions()[0]
	manyIv := many.Interventions()[0]

	if manyIv.Confidence >= soloIv.Confidence {
		t.Errorf("joint attribution must lower confidence: solo=%.3f cohort=%.3f",
			soloIv.Confidence, manyIv.Confidence)
	}
	if manyIv.NetCredit != soloIv.NetCredit {
		t.Errorf("observed credit must not be deflated: solo=%.3f cohort=%.3f",
			soloIv.NetCredit, manyIv.NetCredit)
	}
	if manyIv.Confidence > 0.5 {
		t.Errorf("confidence must never exceed the 0.5 joint-attribution cap, got %.3f", manyIv.Confidence)
	}
}

func TestTopStrategiesRanksByMeanCredit(t *testing.T) {
	l := NewLedger()

	// Strategy "good" moves a failing suite to passing.
	before := state.NewSemiState()
	before.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	l.ObserveBaseline(before)
	l.Record(prop("p1", "a.go"), "good", 0)
	after := state.NewSemiState()
	after.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	l.Attribute(after)

	// Strategy "bad" moves a passing suite back to failing.
	l.ObserveBaseline(after)
	l.Record(prop("p2", "b.go"), "bad", 0)
	broken := state.NewSemiState()
	broken.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	l.Attribute(broken)

	scores := l.TopStrategies()
	if len(scores) != 2 {
		t.Fatalf("expected 2 strategy scores, got %d", len(scores))
	}
	if scores[0].Strategy != "good" {
		t.Errorf("expected 'good' ranked first, got %q (%.3f)", scores[0].Strategy, scores[0].MeanCredit)
	}
	if scores[1].Strategy != "bad" {
		t.Errorf("expected 'bad' ranked second, got %q", scores[1].Strategy)
	}
}

func TestFailedApproachesSurfacesNegatives(t *testing.T) {
	l := NewLedger()

	// Baseline must be a *working* state, otherwise there is no regression to
	// attribute and the delta is legitimately zero.
	passing := state.NewSemiState()
	passing.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	l.ObserveBaseline(passing)

	l.Record(prop("p1", "a.go"), "harmful", 0)

	broken := state.NewSemiState()
	broken.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	l.Attribute(broken)

	failed := l.FailedApproaches(0)
	if len(failed) != 1 || failed[0].Strategy != "harmful" {
		t.Errorf("expected 'harmful' to be flagged as a failed approach, got %+v", failed)
	}
}

func TestObjectivesReportsCompileVsAssertion(t *testing.T) {
	buildFail := state.NewSemiState()
	buildFail.AddTestResult(state.TestResult{Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	if got := Objectives(buildFail)[ObjCompile]; got != 0 {
		t.Errorf("build failure must score compile 0, got %v", got)
	}

	assertFail := state.NewSemiState()
	assertFail.AddTestResult(state.TestResult{Command: "go test", Status: state.TestFailed, FailureClass: "test_assertion_failure"})
	vec := Objectives(assertFail)
	if vec[ObjCompile] != 1 {
		t.Errorf("assertion failure means it compiled, got %v", vec[ObjCompile])
	}
	if vec[ObjTestPass] != 0 {
		t.Errorf("assertion failure must score test_pass 0, got %v", vec[ObjTestPass])
	}
}

func TestObjectivesNilState(t *testing.T) {
	vec := Objectives(nil)
	if len(vec) != 0 {
		t.Errorf("nil state must yield empty objectives, got %v", vec)
	}
}

func TestParseCoverageFromOutput(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddTestResult(state.TestResult{
		Command: "go test", Status: state.TestPassed,
		Stdout: "ok  \tpkg/core\tcoverage: 87.5% of statements",
	})
	if got := Objectives(ss)[ObjCoverage]; got < 0.87 || got > 0.88 {
		t.Errorf("expected coverage ~0.875, got %v", got)
	}
}

func TestParseCoverageAbsentIsZero(t *testing.T) {
	ss := state.NewSemiState()
	ss.AddTestResult(state.TestResult{Command: "go test", Status: state.TestPassed, Stdout: "ok"})
	if got := Objectives(ss)[ObjCoverage]; got != 0 {
		t.Errorf("missing coverage must score 0, not a passing value; got %v", got)
	}
}

func TestTotalTokensAccumulates(t *testing.T) {
	l := NewLedger()
	l.Record(prop("p1", "a.go"), "s", 100)
	l.Record(prop("p2", "b.go"), "s", 250)
	if got := l.TotalTokens(); got != 350 {
		t.Errorf("expected 350 tokens, got %d", got)
	}
}

func TestCreditForUnknownProposal(t *testing.T) {
	l := NewLedger()
	if _, ok := l.CreditFor("nope"); ok {
		t.Error("unknown proposal must report no credit")
	}
}

// --- Replay ---

func TestCaptureAndVerifyRoundTrip(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "output")
	_ = os.MkdirAll(ws, 0o755)
	_ = os.WriteFile(filepath.Join(ws, "main.go"), []byte("package main\n"), 0o644)

	r := NewReplayer(ws, func(string) (state.TestResult, error) {
		return state.TestResult{Status: state.TestPassed}, nil
	})

	ss := state.NewSemiState()
	ss.GeneratedFiles["main.go"] = state.FileEntry{Path: "main.go", Content: "package main\n"}

	snap, err := r.Capture(ss, "baseline")
	if err != nil {
		t.Fatalf("capture failed: %v", err)
	}
	if err := snap.Verify(ws); err != nil {
		t.Errorf("freshly captured snapshot must verify: %v", err)
	}
	if snap.FileHash == "" || snap.StateHash == "" || snap.Combined == "" {
		t.Error("snapshot must record all three hashes")
	}
}

func TestVerifyRejectsTamperedState(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "out")
	_ = os.MkdirAll(ws, 0o755)
	r := NewReplayer(ws, nil)

	ss := state.NewSemiState()
	snap, err := r.Capture(ss, "clean")
	if err != nil {
		t.Fatal(err)
	}

	snap.State.Revision = 999
	if err := snap.Verify(ws); err == nil {
		t.Error("tampered state must fail verification")
	}
}

func TestTrialRejectsNilSnapshot(t *testing.T) {
	r := NewReplayer(t.TempDir(), nil)
	if _, err := r.Trial(nil, "x", nil); err == nil {
		t.Error("nil snapshot must be rejected")
	}
}

func TestTrialRefusesUnverifiableSnapshot(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "out")
	_ = os.MkdirAll(ws, 0o755)
	called := false
	r := NewReplayer(ws, func(string) (state.TestResult, error) {
		called = true
		return state.TestResult{Status: state.TestPassed}, nil
	})

	ss := state.NewSemiState()
	snap, err := r.Capture(ss, "clean")
	if err != nil {
		t.Fatal(err)
	}
	snap.Files["injected.go"] = "package main"

	if _, err := r.Trial(snap, "noop", func(string) (string, error) { return "", nil }); err == nil {
		t.Error("trial must refuse an unverifiable snapshot")
	}
	if called {
		t.Error("tests must not run against an unverifiable snapshot")
	}
}

func TestTrialRunsAndRecordsResult(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "out")
	_ = os.MkdirAll(ws, 0o755)

	r := NewReplayer(ws, func(trialDir string) (state.TestResult, error) {
		if _, err := os.Stat(filepath.Join(trialDir, "go.mod")); err != nil {
			return state.TestResult{Status: state.TestFailed, FailureClass: "build_error"}, nil
		}
		return state.TestResult{Status: state.TestPassed}, nil
	})

	ss := state.NewSemiState()
	ss.GeneratedFiles["go.mod"] = state.FileEntry{Path: "go.mod", Content: "module x\n"}
	_ = os.WriteFile(filepath.Join(ws, "go.mod"), []byte("module x\n"), 0o644)

	snap, err := r.Capture(ss, "baseline")
	if err != nil {
		t.Fatal(err)
	}

	res, err := r.Trial(snap, "baseline-run", func(string) (string, error) { return "", nil })
	if err != nil {
		t.Fatalf("trial failed: %v", err)
	}
	if res.TestStatus != state.TestPassed {
		t.Errorf("expected passing trial, got %s (%s)", res.TestStatus, res.FailureClass)
	}
	if len(r.Results()) != 1 {
		t.Errorf("expected 1 recorded result, got %d", len(r.Results()))
	}
	if res.Objectives[ObjTestPass] != 1 {
		t.Errorf("expected test_pass objective 1, got %v", res.Objectives[ObjTestPass])
	}
}

func TestTrialAppliesCondition(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "out")
	_ = os.MkdirAll(ws, 0o755)

	r := NewReplayer(ws, func(string) (state.TestResult, error) {
		return state.TestResult{Status: state.TestPassed}, nil
	})

	ss := state.NewSemiState()
	ss.GeneratedFiles["a.txt"] = state.FileEntry{Path: "a.txt", Content: "x"}
	_ = os.WriteFile(filepath.Join(ws, "a.txt"), []byte("x"), 0o644)

	snap, _ := r.Capture(ss, "baseline")

	res, err := r.Trial(snap, "revert", func(trialDir string) (string, error) {
		_ = os.WriteFile(filepath.Join(trialDir, "a.txt"), []byte("y"), 0o644)
		return "wrote a.txt", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Condition != "revert: wrote a.txt" {
		t.Errorf("expected condition to be recorded, got %q", res.Condition)
	}
}

func TestCaptureHandlesMissingWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "out")
	_ = os.MkdirAll(ws, 0o755)
	r := NewReplayer(ws, nil)

	ss := state.NewSemiState()
	// Declared in state but never written to disk.
	ss.GeneratedFiles["ghost.go"] = state.FileEntry{Path: "ghost.go", Content: "package main"}

	snap, err := r.Capture(ss, "with-ghost")
	if err != nil {
		t.Fatalf("capture must tolerate a missing workspace file: %v", err)
	}
	if snap.Files["ghost.go"] != "" {
		t.Error("a missing file should be recorded as empty rather than aborting")
	}
}
