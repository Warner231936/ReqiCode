package proposals

import (
	"fmt"

	"github.com/kilo/spiral-codemaker/core/state"
)

type Validator struct{}

func NewValidator() *Validator {
	return &Validator{}
}

func (v *Validator) Validate(proposal state.CodeProposal) error {
	if proposal.File == "" {
		return fmt.Errorf("proposal: file path is required")
	}
	if proposal.Operation != state.OpCreate && proposal.Operation != state.OpModify &&
		proposal.Operation != state.OpDelete && proposal.Operation != state.OpMove &&
		proposal.Operation != state.OpRename {
		return fmt.Errorf("proposal: invalid operation %s", proposal.Operation)
	}
	if proposal.Confidence < 0 || proposal.Confidence > 1 {
		return fmt.Errorf("proposal: confidence must be between 0 and 1")
	}
	return nil
}

type ConflictDetector struct{}

func NewConflictDetector() *ConflictDetector {
	return &ConflictDetector{}
}

func (d *ConflictDetector) DetectConflicts(newProposal state.CodeProposal, existing []state.CodeProposal) []state.Conflict {
	var conflicts []state.Conflict

	for _, existing := range existing {
		if existing.File == newProposal.File && existing.Status == state.ProposalApplied {
			if existing.Operation == state.OpCreate && newProposal.Operation == state.OpCreate {
				conflicts = append(conflicts, state.Conflict{
					ID:            fmt.Sprintf("conflict-%s-%s", existing.ID, newProposal.ID),
					Subject:       fmt.Sprintf("Duplicate file creation: %s", newProposal.File),
					CompetingClaims: []state.Claim{
						{Content: fmt.Sprintf("Create file %s", existing.File), Provenance: existing.Provenance},
						{Content: fmt.Sprintf("Create file %s", newProposal.File), Provenance: newProposal.Provenance},
					},
					AffectedFiles: []string{newProposal.File},
					Severity:      state.ConflictHigh,
					Participants:  []string{existing.OriginatingUnit, newProposal.OriginatingUnit},
					Provenance:    state.NewProvenance("proposal-validator"),
				})
			}
		}
	}

	return conflicts
}

type DependencyResolver struct{}

func NewDependencyResolver() *DependencyResolver {
	return &DependencyResolver{}
}

func (r *DependencyResolver) Resolve(proposal state.CodeProposal, existing []state.CodeProposal) []string {
	if len(proposal.Dependencies) > 0 {
		return proposal.Dependencies
	}
	return []string{}
}

type Proposer struct {
	validator    *Validator
	conflictDet  *ConflictDetector
	depResolver  *DependencyResolver
	nextID       int
}

func NewProposer() *Proposer {
	return &Proposer{
		validator:   NewValidator(),
		conflictDet: NewConflictDetector(),
		depResolver: NewDependencyResolver(),
		nextID:      1,
	}
}

func (p *Proposer) CreateProposal(op state.CodeOperation, file, after, reason, unitID string, conf state.Confidence, expectedEffect string, deps []string) state.CodeProposal {
	proposal := state.CodeProposal{
		ID:              fmt.Sprintf("prop-%d", p.nextID),
		File:            file,
		Operation:       op,
		After:           after,
		Reason:          reason,
		OriginatingUnit: unitID,
		Confidence:      conf,
		ExpectedEffect:  expectedEffect,
		Dependencies:    deps,
		Provenance:      state.NewProvenance(unitID),
		Status:          state.ProposalProposed,
	}
	p.nextID++
	return proposal
}

func (p *Proposer) ValidateAndCheck(proposal state.CodeProposal, existing []state.CodeProposal) (state.CodeProposal, []state.Conflict, error) {
	err := p.validator.Validate(proposal)
	if err != nil {
		return proposal, nil, err
	}

	conflicts := p.conflictDet.DetectConflicts(proposal, existing)
	deps := p.depResolver.Resolve(proposal, existing)
	proposal.Dependencies = deps

	return proposal, conflicts, nil
}
