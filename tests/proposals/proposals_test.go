package proposals_test

import (
	"testing"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/core/state"
)

func TestCreateProposal(t *testing.T) {
	p := proposals.NewProposer()
	prop := p.CreateProposal(
		state.OpCreate,
		"main.go",
		"package main\n\nfunc main() {}",
		"initial implementation",
		"test-unit",
		state.ConfidenceHigh,
		"run the service",
		nil,
	)

	if prop.File != "main.go" {
		t.Errorf("expected main.go, got %s", prop.File)
	}
	if prop.Operation != state.OpCreate {
		t.Errorf("expected CREATE, got %s", prop.Operation)
	}
	if prop.Confidence != state.ConfidenceHigh {
		t.Errorf("expected high confidence, got %f", prop.Confidence)
	}
	if prop.Status != state.ProposalProposed {
		t.Errorf("expected PROPOSED, got %s", prop.Status)
	}
}

func TestValidateProposal(t *testing.T) {
	v := proposals.NewValidator()
	prop := state.CodeProposal{
		File:            "test.go",
		Operation:       state.OpCreate,
		After:           "content",
		OriginatingUnit: "test",
		Confidence:      state.ConfidenceHigh,
		Status:          state.ProposalProposed,
		Provenance:      state.NewProvenance("test"),
	}

	err := v.Validate(prop)
	if err != nil {
		t.Errorf("unexpected validation error: %v", err)
	}
}

func TestValidateProposalMissingFile(t *testing.T) {
	v := proposals.NewValidator()
	prop := state.CodeProposal{
		File:            "",
		Operation:       state.OpCreate,
		Confidence:      state.ConfidenceHigh,
		Status:          state.ProposalProposed,
		Provenance:      state.NewProvenance("test"),
	}

	err := v.Validate(prop)
	if err == nil {
		t.Error("expected validation error for missing file")
	}
}

func TestValidateProposalInvalidOperation(t *testing.T) {
	v := proposals.NewValidator()
	prop := state.CodeProposal{
		File:      "test.go",
		Operation: "INVALID",
		Confidence: state.ConfidenceHigh,
		Status:      state.ProposalProposed,
		Provenance:  state.NewProvenance("test"),
	}

	err := v.Validate(prop)
	if err == nil {
		t.Error("expected validation error for invalid operation")
	}
}

func TestValidateAndCheck(t *testing.T) {
	p := proposals.NewProposer()
	prop := p.CreateProposal(
		state.OpCreate,
		"main.go",
		"content",
		"test",
		"test-unit",
		state.ConfidenceHigh,
		"test",
		nil,
	)

	validated, conflicts, err := p.ValidateAndCheck(prop, nil)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected no conflicts, got %d", len(conflicts))
	}
	if validated.ID == "" {
		t.Error("expected non-empty ID")
	}
}

func TestDetectConflicts(t *testing.T) {
	cd := proposals.NewConflictDetector()
	existing := []state.CodeProposal{
		{
			ID:              "prop-1",
			File:            "main.go",
			Operation:       state.OpCreate,
			Status:          state.ProposalApplied,
			OriginatingUnit: "unit-1",
			Provenance:      state.NewProvenance("unit-1"),
		},
	}

	newProp := state.CodeProposal{
		ID:              "prop-2",
		File:            "main.go",
		Operation:       state.OpCreate,
		OriginatingUnit: "unit-2",
		Provenance:      state.NewProvenance("unit-2"),
	}

	conflicts := cd.DetectConflicts(newProp, existing)
	if len(conflicts) != 1 {
		t.Fatalf("expected 1 conflict, got %d", len(conflicts))
	}
	if conflicts[0].Severity != state.ConflictHigh {
		t.Errorf("expected HIGH severity, got %s", conflicts[0].Severity)
	}
}
