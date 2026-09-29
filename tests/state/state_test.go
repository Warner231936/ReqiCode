package state_test

import (
	"testing"

	"github.com/kilo/spiral-codemaker/core/state"
)

func TestNewSemiState(t *testing.T) {
	s := state.NewSemiState()
	if s == nil {
		t.Fatal("expected non-nil SemiState")
	}
	if s.Revision != 0 {
		t.Errorf("expected revision 0, got %d", s.Revision)
	}
	if len(s.Hypotheses) != 0 {
		t.Errorf("expected no hypotheses, got %d", len(s.Hypotheses))
	}
	if len(s.Evidence) != 0 {
		t.Errorf("expected no evidence, got %d", len(s.Evidence))
	}
	if len(s.AttentionMap) != 0 {
		t.Errorf("expected empty attention map, got %d", len(s.AttentionMap))
	}
	if s.Confidence != state.ConfidenceMedium {
		t.Errorf("expected confidence %f, got %f", state.ConfidenceMedium, s.Confidence)
	}
}

func TestIncrementRevision(t *testing.T) {
	s := state.NewSemiState()
	r1 := s.IncrementRevision()
	if r1 != 1 {
		t.Errorf("expected revision 1, got %d", r1)
	}
	r2 := s.IncrementRevision()
	if r2 != 2 {
		t.Errorf("expected revision 2, got %d", r2)
	}
}

func TestAddEvidence(t *testing.T) {
	s := state.NewSemiState()
	ev := state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  "test evidence",
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance("test-unit"),
	}
	s.AddEvidence(ev)

	if len(s.Evidence) != 1 {
		t.Errorf("expected 1 evidence, got %d", len(s.Evidence))
	}
	if s.Evidence[0].Content != "test evidence" {
		t.Errorf("expected 'test evidence', got '%s'", s.Evidence[0].Content)
	}
}

func TestAddHypothesis(t *testing.T) {
	s := state.NewSemiState()
	h := state.Hypothesis{
		ID:      "hyp-001",
		Content: "test hypothesis",
		Status:  state.HypothesisProposed,
		Provenance: state.NewProvenance("test-unit"),
	}
	s.AddHypothesis(h)

	if len(s.Hypotheses) != 1 {
		t.Errorf("expected 1 hypothesis, got %d", len(s.Hypotheses))
	}
}

func TestAddProposal(t *testing.T) {
	s := state.NewSemiState()
	p := state.CodeProposal{
		ID:              "prop-001",
		File:            "main.go",
		Operation:       state.OpCreate,
		Reason:          "test",
		OriginatingUnit: "test-unit",
		Confidence:      state.ConfidenceHigh,
		Status:          state.ProposalProposed,
		Provenance:      state.NewProvenance("test-unit"),
	}
	s.AddProposal(p)

	if len(s.Proposals) != 1 {
		t.Errorf("expected 1 proposal, got %d", len(s.Proposals))
	}
}

func TestAddTestResult(t *testing.T) {
	s := state.NewSemiState()
	tr := state.TestResult{
		ID:      "test-001",
		Command: "go test ./...",
		Status:  state.TestPassed,
		Provenance: state.NewProvenance("test-unit"),
	}
	s.AddTestResult(tr)

	if len(s.TestResults) != 1 {
		t.Errorf("expected 1 test result, got %d", len(s.TestResults))
	}
}

func TestSetAttention(t *testing.T) {
	s := state.NewSemiState()
	s.SetAttention("unit-1", 0.5)
	s.SetAttention("unit-2", 0.8)

	if s.AttentionMap["unit-1"] != 0.5 {
		t.Errorf("expected 0.5, got %f", s.AttentionMap["unit-1"])
	}
	if s.AttentionMap["unit-2"] != 0.8 {
		t.Errorf("expected 0.8, got %f", s.AttentionMap["unit-2"])
	}
}

func TestSetArchitecturePlan(t *testing.T) {
	s := state.NewSemiState()
	plan := &state.ArchitecturePlan{
		Description: "test plan",
		Components: []state.ComponentSpec{
			{Name: "main", Description: "entry", Path: "main.go"},
		},
		Confidence: state.ConfidenceHigh,
		Provenance: state.NewProvenance("test-unit"),
	}
	s.SetArchitecturePlan(plan)

	got := s.GetArchitecturePlan()
	if got == nil {
		t.Fatal("expected non-nil plan")
	}
	if got.Description != "test plan" {
		t.Errorf("expected 'test plan', got '%s'", got.Description)
	}
}

func TestUpdateHypothesisStatus(t *testing.T) {
	s := state.NewSemiState()
	h := state.Hypothesis{
		ID:      "hyp-001",
		Content: "test",
		Status:  state.HypothesisProposed,
		Provenance: state.NewProvenance("test-unit"),
	}
	s.AddHypothesis(h)

	s.UpdateHypothesisStatus("hyp-001", state.HypothesisAccepted)

	hypotheses := s.GetHypotheses()
	if len(hypotheses) != 1 {
		t.Fatalf("expected 1 hypothesis, got %d", len(hypotheses))
	}
	if hypotheses[0].Status != state.HypothesisAccepted {
		t.Errorf("expected ACCEPTED, got %s", hypotheses[0].Status)
	}
}

func TestFileExistsInGenerated(t *testing.T) {
	s := state.NewSemiState()
	s.AddFile(state.FileEntry{Path: "main.go", Content: "test"})
	if !s.FileExistsInGenerated("main.go") {
		t.Error("expected file to exist")
	}
	if s.FileExistsInGenerated("nonexistent.go") {
		t.Error("expected file to not exist")
	}
}

func TestHasGeneratedFiles(t *testing.T) {
	s := state.NewSemiState()
	if s.HasGeneratedFiles() {
		t.Error("expected no files")
	}
	s.AddFile(state.FileEntry{Path: "main.go", Content: "test"})
	if !s.HasGeneratedFiles() {
		t.Error("expected files to exist")
	}
}

func TestHasTestFiles(t *testing.T) {
	s := state.NewSemiState()
	if s.HasTestFiles() {
		t.Error("expected no test files")
	}
	s.AddFile(state.FileEntry{Path: "main_test.go", Content: "test"})
	if !s.HasTestFiles() {
		t.Error("expected test file to be detected")
	}
}

func TestClone(t *testing.T) {
	s := state.NewSemiState()
	s.AddEvidence(state.Evidence{
		Type: state.EvidenceObservation,
		Content: "clone test",
		Provenance: state.NewProvenance("test"),
	})
	s.SetAttention("unit-1", 0.5)

	clone := s.Clone()

	if len(clone.Evidence) != 1 {
		t.Errorf("expected 1 evidence in clone, got %d", len(clone.Evidence))
	}
	if clone.AttentionMap["unit-1"] != 0.5 {
		t.Errorf("expected attention 0.5, got %f", clone.AttentionMap["unit-1"])
	}

	s.AddEvidence(state.Evidence{
		Type: state.EvidenceObservation,
		Content: "second",
		Provenance: state.NewProvenance("test"),
	})

	if len(clone.Evidence) != 1 {
		t.Error("clone should be independent of original")
	}
}

func TestClaimStrengthen(t *testing.T) {
	c := state.NewClaim("test", "source", "unit-1", state.ConfidenceLow)
	c = c.Strengthen(state.ConfidenceHigh)

	if c.Confidence != state.ConfidenceHigh {
		t.Errorf("expected high confidence, got %f", c.Confidence)
	}
	if c.Revision != 1 {
		t.Errorf("expected revision 1, got %d", c.Revision)
	}
}

func TestClaimWithStatus(t *testing.T) {
	c := state.NewClaim("test", "source", "unit-1", state.ConfidenceMedium)
	c = c.WithStatus(state.FindingSupported)

	if c.Status != state.FindingSupported {
		t.Errorf("expected SUPPORTED, got %s", c.Status)
	}
	if c.Revision != 1 {
		t.Errorf("expected revision 1, got %d", c.Revision)
	}
}
