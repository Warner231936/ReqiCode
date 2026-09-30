package canary_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/core/canary"
	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/execution/tests"
)

// This is the test that matters most in the package. Every other canary test
// uses a synthetic state, which means they verify the decision logic but not
// whether the harness can actually observe an edit. A canary that approves
// everything because it measured nothing would pass all of them.
//
// So this runs the real pipeline, applies a real edit to the real generated
// workspace, and re-tests with the real runner.

const canaryScenario = "Build a small HTTP service that stores TODO items."

func realHarness(t *testing.T, gateDir, scenario string) *canary.Harness {
	t.Helper()
	runner := func(dir string) (*canary.BranchResult, error) {
		orch, err := system.NewOrchestrator("", dir, scenario, 1)
		if err != nil {
			return nil, err
		}
		if err := orch.Run(); err != nil {
			return nil, err
		}
		return &canary.BranchResult{State: orch.SemiState(), Workspace: orch.WorkspacePath()}, nil
	}
	tester := func(ws string) (state.TestResult, error) {
		tr := tests.NewTestRunner(ws).WithRace(false).WithCoverage(false).RunAll(context.Background())
		return tr, nil
	}
	return canary.NewHarness(gateDir, t.TempDir(), runner).WithWorkspaceTester(tester)
}

func captureScenarioBaseline(t *testing.T, scenario, gateDir string) {
	t.Helper()
	dir := t.TempDir()
	orch, err := system.NewOrchestrator("", dir, scenario, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := orch.Run(); err != nil {
		t.Fatal(err)
	}
	reg := regression.NewGate(gateDir)
	if err := reg.Capture(regression.Project(scenario, orch.SemiState())); err != nil {
		t.Fatal(err)
	}
}

func TestEndToEndCanaryRejectsEditThatBreaksTheBuild(t *testing.T) {
	gateDir := t.TempDir()
	captureScenarioBaseline(t, canaryScenario, gateDir)

	h := realHarness(t, gateDir, canaryScenario)
	base, err := h.Baseline(canaryScenario)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if base.PassRate != 1 {
		t.Fatalf("precondition: baseline must pass, got pass=%.2f (%v)", base.PassRate, base.TestOutcomes)
	}

	// A test that dereferences a nil pointer: the workspace builds, then panics.
	m, err := h.Trial(canary.Edit{
		Name: "introduce-panic",
		Apply: func(ws string) error {
			path := filepath.Join(ws, "internal", "store", "panic_test.go")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(
				"package store\n\nimport \"testing\"\n\nfunc TestPanics(t *testing.T) {\n\tvar s *Store\n\ts.Get(\"1\")\n}\n"), 0o644)
		},
	}, canaryScenario)
	if err != nil {
		t.Fatal(err)
	}

	if !m.RetrunPerformed {
		t.Fatal("the candidate must be re-tested against the edited workspace")
	}
	if m.PassRate == 1 {
		t.Errorf("a panicking test must not report a passing suite; outcomes=%v", m.TestOutcomes)
	}
	v := canary.Compare(base, m)
	if v.Promote {
		t.Fatalf("a canary that promotes a broken build is worse than no canary; delta=%+f", v.Delta)
	}
}

func TestEndToEndCanaryPromotesEditThatAddsPassingTest(t *testing.T) {
	gateDir := t.TempDir()
	captureScenarioBaseline(t, canaryScenario, gateDir)

	h := realHarness(t, gateDir, canaryScenario)
	base, err := h.Baseline(canaryScenario)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}

	m, err := h.Trial(canary.Edit{
		Name: "add-passing-test",
		Apply: func(ws string) error {
			path := filepath.Join(ws, "internal", "store", "added_test.go")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte(
				"package store\n\nimport \"testing\"\n\nfunc TestAdditionalCoverage(t *testing.T) {\n\ts := NewStore()\n\tif err := s.Create(StoreItem{ID: \"x\", Data: \"y\"}); err != nil {\n\t\tt.Fatalf(\"create: %v\", err)\n\t}\n\tif _, ok := s.Get(\"x\"); !ok {\n\t\tt.Error(\"expected the written item to be retrievable\")\n\t}\n}\n"), 0o644)
		},
	}, canaryScenario)
	if err != nil {
		t.Fatal(err)
	}

	if m.PassRate != 1 {
		t.Fatalf("precondition: the added test must pass; outcomes=%v", m.TestOutcomes)
	}
	if !strings.Contains(strings.Join(m.TestOutcomes, ","), "PASSED") {
		t.Errorf("expected a PASSED outcome, got %v", m.TestOutcomes)
	}
	v := canary.Compare(base, m)
	if !v.Promote {
		t.Errorf("a valid added test should be promoted: %s", v.Reason)
	}
}

func TestEndToEndCanaryDetectsCompileError(t *testing.T) {
	gateDir := t.TempDir()
	captureScenarioBaseline(t, canaryScenario, gateDir)

	h := realHarness(t, gateDir, canaryScenario)
	base, err := h.Baseline(canaryScenario)
	if err != nil {
		t.Fatal(err)
	}

	m, err := h.Trial(canary.Edit{
		Name: "introduce-compile-error",
		Apply: func(ws string) error {
			path := filepath.Join(ws, "internal", "store", "badsyntax_test.go")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			return os.WriteFile(path, []byte("package store\nthis is not valid go\n"), 0o644)
		},
	}, canaryScenario)
	if err != nil {
		t.Fatal(err)
	}
	if m.PassRate == 1 {
		t.Error("a file that does not compile must not yield a passing suite")
	}
	if v := canary.Compare(base, m); v.Promote {
		t.Error("a compile error must never be promoted")
	}
}

func TestEndToEndCanaryCleanUpRemovesScratchByDefault(t *testing.T) {
	gateDir := t.TempDir()
	captureScenarioBaseline(t, canaryScenario, gateDir)

	scratch := t.TempDir()
	runner := func(dir string) (*canary.BranchResult, error) {
		orch, err := system.NewOrchestrator("", dir, canaryScenario, 1)
		if err != nil {
			return nil, err
		}
		if err := orch.Run(); err != nil {
			return nil, err
		}
		return &canary.BranchResult{State: orch.SemiState(), Workspace: orch.WorkspacePath()}, nil
	}
	tester := func(ws string) (state.TestResult, error) {
		return tests.NewTestRunner(ws).WithRace(false).WithCoverage(false).RunAll(context.Background()), nil
	}
	h := canary.NewHarness(gateDir, scratch, runner).WithWorkspaceTester(tester)

	if _, err := h.Trial(canary.Edit{Name: "noop", Apply: func(string) error { return nil }}, canaryScenario); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(scratch)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("trial directories must be removed by default, found %d leftover", len(entries))
	}
}
