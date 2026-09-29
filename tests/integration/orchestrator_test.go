package integration_test

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestOrchestratorFullRun(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 3)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	ss := orch.SemiState()

	if len(ss.GetRequirements()) == 0 {
		t.Error("expected requirements to be parsed")
	}

	plan := ss.GetArchitecturePlan()
	if plan == nil {
		t.Fatal("expected architecture plan")
	}

	// The plan must match the intent. An HTTP-service intent has to yield an
	// HTTP plan; that is the invariant that replaced the old hardcoded
	// "always TODO" template, and it is what actually matters.
	if len(plan.Endpoints) == 0 {
		t.Error("HTTP service intent must produce a plan with endpoints")
	}
	if len(plan.Components) == 0 {
		t.Error("architecture plan must contain components")
	}
	for _, c := range plan.Components {
		if c.Path == "" {
			t.Errorf("component %q has no path", c.Name)
		}
		if filepath.Ext(c.Path) != ".go" {
			t.Errorf("component %q path %q is not a Go file", c.Name, c.Path)
		}
	}

	if len(ss.GetProposals()) == 0 {
		t.Error("expected code proposals")
	}

	testResults := ss.GetTestResults()
	if len(testResults) == 0 {
		t.Error("expected test results")
	}

	for _, tr := range testResults {
		if tr.Status != state.TestPassed {
			t.Errorf("expected test to pass, got %s (%s): %s", tr.Status, tr.FailureClass, tr.Stderr)
		}
	}

	records := orch.SpiralManager().Records()
	if len(records) == 0 {
		t.Error("expected at least one iteration record")
	}
}

func TestOrchestratorGeneratesValidGoCode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-gocode-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 1)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	ws := orch.Workspace()
	files, err := ws.ListFiles()
	if err != nil {
		t.Fatalf("failed to list files: %v", err)
	}

	// Assert on the shape of the output, not on specific file names. The
	// generator is free to lay out the project however the plan dictates; what
	// must hold is that a runnable Go module was produced.
	var goFiles []string
	for _, f := range files {
		f = filepath.ToSlash(f)
		if filepath.Ext(f) == ".go" {
			goFiles = append(goFiles, f)
		}
	}
	if len(goFiles) == 0 {
		t.Fatal("expected at least one generated Go file")
	}

	// Every generated Go file must declare a package. A file that does not is
	// dead weight that will break the build.
	for _, rel := range goFiles {
		abs := filepath.Join(ws.RootPath(), filepath.FromSlash(rel))
		content, err := os.ReadFile(abs)
		if err != nil {
			t.Errorf("failed to read %s: %v", rel, err)
			continue
		}
		if !bytes.Contains(content, []byte("package ")) {
			t.Errorf("%s has no package declaration", rel)
		}
	}

	// The go.mod module path must be a valid Go module path, not a reserved
	// word like "go".
	gomod := filepath.Join(ws.RootPath(), "go.mod")
	data, err := os.ReadFile(gomod)
	if err != nil {
		t.Fatalf("failed to read go.mod: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "module ") {
			mod := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			if mod == "" || mod == "go" || strings.ContainsAny(mod, " \\") {
				t.Errorf("invalid module path %q", mod)
			}
		}
	}
}

// TestOrchestratorPhase0Instrumentation verifies that the Pillar 0 measurement
// pass actually runs and produces the artifacts the causal and convergence
// analysis depend on. If this regresses, every downstream claim about
// attribution and convergence becomes unfounded.
func TestOrchestratorPhase0Instrumentation(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-phase0-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 2)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}
	if err := orch.Run(); err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	// Potential-function trajectory must have been recorded.
	trace := orch.Convergence().Trace()
	if len(trace) == 0 {
		t.Fatal("expected a potential-function trajectory")
	}
	for i, pt := range trace {
		if pt.Fingerprint == "" {
			t.Errorf("trajectory point %d has no fingerprint", i)
		}
		if pt.Potential.Value < 0 || pt.Potential.Value > 1.5 {
			t.Errorf("trajectory point %d has implausible potential %f", i, pt.Potential.Value)
		}
	}

	// Causal ledger must have recorded applied proposals.
	interventions := orch.CausalLedger().Interventions()
	if len(interventions) == 0 {
		t.Error("expected at least one recorded intervention")
	}
	for _, iv := range interventions {
		if iv.ProposalID == "" {
			t.Error("intervention is missing its proposal ID")
		}
		if iv.Target == "" {
			t.Errorf("intervention %s is missing a target file", iv.ID)
		}
	}

	// Pareto frontier must be populated.
	if orch.Frontier().Len() == 0 {
		t.Error("expected at least one point on the Pareto frontier")
	}

	// The instrumentation must be persisted so runs can be compared later.
	artifact := filepath.Join(outputPath, "instrumentation", "phase0.json")
	if _, err := os.Stat(artifact); os.IsNotExist(err) {
		t.Errorf("expected instrumentation artifact at %s", artifact)
	}
}

func TestOrchestratorRecordsRevisionHistory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-rev-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 2)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	records := orch.SpiralManager().Records()
	if len(records) < 2 {
		t.Fatalf("expected at least 2 iteration records, got %d", len(records))
	}

	for _, rec := range records {
		if rec.StartingState == nil {
			t.Errorf("iteration %d: expected non-nil starting state", rec.ID)
		}
		if rec.EndingState == nil {
			t.Errorf("iteration %d: expected non-nil ending state", rec.ID)
		}
		if rec.EndingState.Revision <= rec.StartingState.Revision {
			t.Logf("iteration %d: revision went from %d to %d", rec.ID, rec.StartingState.Revision, rec.EndingState.Revision)
		}
	}

	if records[0].StartingState.Revision != 0 {
		t.Errorf("expected first iteration to start at revision 0, got %d", records[0].StartingState.Revision)
	}
}

func TestOrchestratorAttentionMechanism(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-att-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 1)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	att := orch.Attention().GetAll()
	if len(att) == 0 {
		t.Error("expected attention values for units")
	}

	var hasNonZero bool
	for _, a := range att {
		if a.Weight > state.AttentionThreshold {
			hasNonZero = true
		}
	}
	if !hasNonZero {
		t.Error("expected at least one unit with non-zero attention")
	}
}

func TestOrchestratorPersistentMemory(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-mem-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")

	orch, err := system.NewOrchestrator("", outputPath, "Build a small HTTP service that stores TODO items.", 1)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	err = orch.Run()
	if err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if err := orch.SaveMemory(); err != nil {
		t.Fatalf("failed to save memory: %v", err)
	}

	decisions := orch.Memory().AllDecisions()
	if len(decisions) == 0 {
		t.Error("expected decisions to be stored in persistent memory")
	}

	requirements := orch.Memory().AllRequirements()
	if len(requirements) == 0 {
		t.Error("expected requirements to be stored in persistent memory")
	}

	memFile := filepath.Join(outputPath, "memory", "memory.json")
	if _, err := os.Stat(memFile); os.IsNotExist(err) {
		t.Error("expected memory.json to exist")
	}
}

func fileContains(path, expected string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	return contains(string(buf[:n]), expected)
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
