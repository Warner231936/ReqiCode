package canary_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kilo/spiral-codemaker/core/canary"
	"github.com/kilo/spiral-codemaker/core/converge"
	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/state"
)

// goodState is a branch that builds, tests, and passes the gate.
func goodState(ss *state.SemiState) {
	ss.SetArchitecturePlan(&state.ArchitecturePlan{
		Description:   "Go HTTP server",
		AlternativeID: "plan-http",
		Components:    []state.ComponentSpec{{Name: "main", Path: "cmd/server/main.go"}},
		Endpoints:     []state.EndpointSpec{{Method: "GET", Path: "/resources"}},
	})
	ss.AddTestResult(state.TestResult{Command: "go test", Status: state.TestPassed})
	ss.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Content: "green", Strength: state.ConfidenceHigh})
	ss.GeneratedFiles["cmd/server/main.go"] = state.FileEntry{Path: "cmd/server/main.go"}
	ss.AppendChained("good")
}

func badTests(ss *state.SemiState) {
	ss.SetArchitecturePlan(&state.ArchitecturePlan{
		Description:   "Go HTTP server",
		AlternativeID: "plan-http",
		Components:    []state.ComponentSpec{{Name: "main", Path: "cmd/server/main.go"}},
		Endpoints:     []state.EndpointSpec{{Method: "GET", Path: "/resources"}},
	})
	ss.AddTestResult(state.TestResult{Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	ss.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Content: "green", Strength: state.ConfidenceHigh})
	ss.GeneratedFiles["cmd/server/main.go"] = state.FileEntry{Path: "cmd/server/main.go"}
	ss.AppendChained("bad")
}

func newGate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	g := regression.NewGate(dir)
	base := state.NewSemiState()
	goodState(base)
	if err := g.Capture(regression.Project("s", base)); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestTrialAppliesEditToReportedWorkspace(t *testing.T) {
	gateDir := newGate(t)
	var sawWorkspace string

	h := canary.NewHarness(gateDir, t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		// The pipeline reports the workspace it populated. The edit must land
		// there and nowhere else; an edit that escapes the workspace is measured
		// as a no-op and the canary would approve a change it never exercised.
		ws := filepath.Join(dir, "output")
		_ = os.MkdirAll(ws, 0o755)
		ss := state.NewSemiState()
		goodState(ss)
		return &canary.BranchResult{State: ss, Workspace: ws}, nil
	})

	m, err := h.Trial(canary.Edit{
		Name: "add-marker",
		Apply: func(ws string) error {
			sawWorkspace = ws
			return os.WriteFile(filepath.Join(ws, "workspace-marker"), []byte("x"), 0o644)
		},
	}, "s")
	if err != nil {
		t.Fatal(err)
	}
	if sawWorkspace == "" {
		t.Fatal("the edit must receive the workspace the pipeline reported")
	}
	if filepath.Base(sawWorkspace) != "output" {
		t.Errorf("edit should have targeted the reported workspace, got %q", sawWorkspace)
	}
	if !m.GatePassed {
		t.Errorf("a passing branch should satisfy the gate: %+v", m)
	}
	if !m.ChainValid {
		t.Error("a well-formed branch must have a valid chain")
	}
	if m.Potential <= 0 {
		t.Errorf("expected a positive potential, got %f", m.Potential)
	}
	if m.ChainHead == "" {
		t.Error("measurement must record the chain head for divergence comparison")
	}
}

func TestTrialRejectsWorkspaceOutsideTrialDir(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		goodState(ss)
		// Report a workspace outside the trial directory. Accepting it would let
		// one trial contaminate another.
		return &canary.BranchResult{State: ss, Workspace: t.TempDir()}, nil
	})
	_, err := h.Trial(canary.Edit{Name: "escape", Apply: func(string) error { return nil }}, "s")
	if err == nil {
		t.Fatal("a workspace outside the trial directory must be rejected")
	}
}

func TestTrialRequiresReportedWorkspace(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		goodState(ss)
		return &canary.BranchResult{State: ss, Workspace: ""}, nil
	})
	if _, err := h.Trial(canary.Edit{Name: "nows", Apply: func(string) error { return nil }}, "s"); err == nil {
		t.Fatal("a runner that reports no workspace must be rejected")
	}
}

func TestTrialSurfacesApplyFailure(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		return &canary.BranchResult{State: state.NewSemiState(), Workspace: dir}, nil
	})
	if _, err := h.Trial(canary.Edit{Name: "bad", Apply: func(string) error {
		return os.ErrPermission
	}}, "s"); err == nil {
		t.Fatal("a failed edit must surface as an error, not a silent measurement")
	}
}

func TestTrialRejectsNilApply(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		return &canary.BranchResult{State: state.NewSemiState(), Workspace: dir}, nil
	})
	if _, err := h.Trial(canary.Edit{Name: "nil"}, "s"); err == nil {
		t.Fatal("an edit with no Apply function must be rejected")
	}
}

func TestUnverifiedBranchIsNotGatePassed(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		ss.SetArchitecturePlan(&state.ArchitecturePlan{Description: "d", AlternativeID: "a"})
		ss.AppendChained("no tests")
		return &canary.BranchResult{State: ss, Workspace: dir}, nil
	})
	m, err := h.Trial(canary.Edit{Name: "untested", Apply: func(string) error { return nil }}, "s")
	if err != nil {
		t.Fatal(err)
	}
	if m.GatePassed {
		t.Error("a branch that produced no test results must not qualify; it is unverified, not good")
	}
	if ok, why := m.Qualified(); ok {
		t.Errorf("an unverified branch must be unqualified: %s", why)
	}
}

func TestBaselineErrorsWhenUnverifiable(t *testing.T) {
	h := canary.NewHarness(newGate(t), t.TempDir(), func(dir string) (*canary.BranchResult, error) {
		return &canary.BranchResult{State: state.NewSemiState(), Workspace: dir}, nil
	})
	if _, err := h.Baseline("s"); err == nil {
		t.Fatal("a baseline that cannot verify itself must error; it cannot arbitrate")
	}
}

func TestCompareRejectsFailingCandidate(t *testing.T) {
	gateDir := newGate(t)
	passing := func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		goodState(ss)
		return &canary.BranchResult{State: ss, Workspace: dir}, nil
	}
	failing := func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		badTests(ss)
		return &canary.BranchResult{State: ss, Workspace: dir}, nil
	}

	baseH := canary.NewHarness(gateDir, t.TempDir(), passing)
	base, err := baseH.Baseline("s")
	if err != nil {
		t.Fatal(err)
	}

	candH := canary.NewHarness(gateDir, t.TempDir(), failing)
	cand, err := candH.Trial(canary.Edit{Name: "break-tests", Apply: func(string) error { return nil }}, "s")
	if err != nil {
		t.Fatal(err)
	}

	v := canary.Compare(base, cand)
	if v.Promote {
		t.Fatal("a branch that breaks tests must never be promoted")
	}
}

func TestCompareBlocksOnFailedGate(t *testing.T) {
	// A branch with excellent potential but a gate rejection must still be
	// blocked. Otherwise the gate is decorative.
	base := canary.Measurement{Potential: 0.4, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "a"}
	cand := canary.Measurement{Potential: 0.9, PassRate: 1, GatePassed: false, ChainValid: true, ChainHead: "b"}

	v := canary.Compare(base, cand)
	if v.Promote {
		t.Fatal("high potential must not outvote a failed gate")
	}
}

func TestCompareBlocksOnBrokenChain(t *testing.T) {
	base := canary.Measurement{Potential: 0.4, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "a"}
	cand := canary.Measurement{Potential: 0.9, PassRate: 1, GatePassed: true, ChainValid: false, ChainHead: "b"}

	v := canary.Compare(base, cand)
	if v.Promote {
		t.Fatal("an untrustworthy history must block promotion regardless of score")
	}
}

func TestCompareBlocksWhenBaselineUnqualified(t *testing.T) {
	base := canary.Measurement{Potential: 0.4, GatePassed: false, ChainValid: true}
	cand := canary.Measurement{Potential: 0.9, GatePassed: true, ChainValid: true}

	v := canary.Compare(base, cand)
	if v.Promote {
		t.Fatal("an unqualified incumbent cannot arbitrate a promotion")
	}
}

func TestCompareRejectsTie(t *testing.T) {
	m := canary.Measurement{Potential: 0.5, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "aaaa"}
	v := canary.Compare(m, m)
	if v.Promote {
		t.Fatal("an identical branch is not an improvement")
	}
	if v.Delta != 0 {
		t.Errorf("expected zero delta, got %f", v.Delta)
	}
}

func TestCompareRejectsNoiseImprovement(t *testing.T) {
	base := canary.Measurement{Potential: 0.500, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "a"}
	// An improvement far below the threshold, i.e. inside scheduler and
	// evidence-density noise.
	cand := canary.Measurement{Potential: 0.5005, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "b"}

	if canary.ImprovementThreshold <= 0.0005 {
		t.Fatalf("threshold %f must exceed the noise this test models", canary.ImprovementThreshold)
	}
	if v := canary.Compare(base, cand); v.Promote {
		t.Fatal("an improvement below the noise threshold must not be promoted")
	}
}

func TestComparePromotesRealImprovement(t *testing.T) {
	base := canary.Measurement{Potential: 0.40, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "aaaa"}
	cand := canary.Measurement{Potential: 0.70, PassRate: 1, GatePassed: true, ChainValid: true, ChainHead: "bbbb"}

	v := canary.Compare(base, cand)
	if !v.Promote {
		t.Fatalf("a substantial, qualified improvement must promote: %s", v.Reason)
	}
	if v.Delta <= 0 {
		t.Errorf("expected positive delta, got %f", v.Delta)
	}
}

func TestCompareRejectsPassRateRegression(t *testing.T) {
	base := canary.Measurement{Potential: 0.40, PassRate: 1.0, GatePassed: true, ChainValid: true, ChainHead: "a"}
	cand := canary.Measurement{Potential: 0.90, PassRate: 0.5, GatePassed: true, ChainValid: true, ChainHead: "b"}

	v := canary.Compare(base, cand)
	if v.Promote {
		t.Fatal("a higher potential must not excuse a pass-rate regression")
	}
}

func TestThresholdExceedsPotentialEpsilon(t *testing.T) {
	// The threshold is only meaningful if it sits above the noise floor the
	// potential function itself defines.
	if canary.ImprovementThreshold <= converge.Epsilon {
		t.Errorf("threshold %f must exceed potential epsilon %f", canary.ImprovementThreshold, converge.Epsilon)
	}
}

func TestReportWrittenToDisk(t *testing.T) {
	dir := t.TempDir()
	rep := canary.Report{
		Scenario:  "s",
		Candidate: canary.Measurement{Label: "my edit/with:bad chars", ChainHead: "abc"},
		Verdict:   canary.Verdict{Promote: true},
	}
	if err := canary.Write(dir, rep); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected one report, got %d", len(entries))
	}
	if entries[0].Name() == "" || filepath.Ext(entries[0].Name()) != ".json" {
		t.Errorf("unexpected report filename %q", entries[0].Name())
	}
}

func TestKeepScratchRetainsDirectory(t *testing.T) {
	scratch := t.TempDir()
	marker := filepath.Join(scratch, "retained")

	h := canary.NewHarness(newGate(t), scratch, func(dir string) (*canary.BranchResult, error) {
		ss := state.NewSemiState()
		goodState(ss)

		return &canary.BranchResult{State: ss, Workspace: dir}, nil
	}).KeepScratch(true)

	if _, err := h.Trial(canary.Edit{
		Name:  "keep",
		Apply: func(dir string) error { return os.WriteFile(marker, []byte("x"), 0o644) },
	}, "s"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("KeepScratch must retain the trial directory for inspection")
	}
}

func TestFormatVerdictIncludesBothBranches(t *testing.T) {
	v := canary.Verdict{
		Reason:   "cannot promote: nope",
		Baseline: canary.Measurement{Potential: 0.1, PassRate: 0.5, GatePassed: true, ChainValid: true},
		Candidate: canary.Measurement{Potential: 0.9, PassRate: 1, GatePassed: true, ChainValid: true},
	}
	out := canary.FormatVerdict(v)
	for _, want := range []string{"nope", "baseline", "candidate", "0.1000", "0.9000"} {
		if !contains(out, want) {
			t.Errorf("verdict output missing %q:\n%s", want, out)
		}
	}
}

func contains(h, n string) bool {
	for i := 0; i+len(n) <= len(h); i++ {
		if h[i:i+len(n)] == n {
			return true
		}
	}
	return false
}
