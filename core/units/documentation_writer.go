package units

import (
	"context"
	"fmt"

	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type DocumentationWriter struct {
	*BaseUnit
	rt *Runtime
	ws *workspace.Workspace
}

func NewDocumentationWriter(rt *Runtime, ws *workspace.Workspace) *DocumentationWriter {
	return &DocumentationWriter{
		BaseUnit: NewBaseUnit("unit-documentation-writer", state.RoleDocumentationWriter, "DocumentationWriter", state.CadenceSlow, state.ActivationManual),
		rt:       rt,
		ws:       ws,
	}
}

func (u *DocumentationWriter) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	plan := semiState.GetArchitecturePlan()
	if plan == nil {
		return nil, nil
	}

	files := semiState.GetGeneratedFiles()
	if len(files) == 0 {
		return nil, nil
	}

	for path := range files {
		if path == "README.md" {
			return nil, nil
		}
	}

	docContent := generateDocumentation(plan, files)

	prop := u.rt.Proposals.CreateProposal(
		state.OpCreate,
		"README.md",
		docContent,
		"document the generated TODO service",
		u.ID(),
		state.ConfidenceHigh,
		"provide user documentation",
		nil,
	)

	validated, _, err := u.rt.Proposals.ValidateAndCheck(prop, nil)
	if err != nil {
		return nil, err
	}
	validated.Status = state.ProposalApproved
	semiState.AddProposal(validated)
	semiState.AddFile(state.FileEntry{
		Path:       "README.md",
		Content:    docContent,
		ProposalID: validated.ID,
		Provenance: state.NewProvenance(u.ID()),
	})

	if err := u.ws.ApplyProposal(validated); err != nil {
		validated.Status = state.ProposalFailed
	}
	validated.Status = state.ProposalApplied

	semiState.AddEvidence(state.Evidence{
		Type:       state.EvidenceObservation,
		Content:    "documentation written: README.md",
		Strength:   state.ConfidenceMedium,
		Provenance: state.NewProvenance(u.ID()),
	})

	return nil, nil
}

func generateDocumentation(plan *state.ArchitecturePlan, files map[string]state.FileEntry) string {
	doc := fmt.Sprintf("# %s\n\n", plan.Description)
	doc += "## Overview\n\n"
	if len(plan.Endpoints) > 0 {
		doc += fmt.Sprintf("This project implements a Go application with %d components and HTTP endpoints.\n\n", len(plan.Components))
	} else {
		doc += fmt.Sprintf("This project implements a Go application with %d components.\n\n", len(plan.Components))
	}

	if len(plan.Endpoints) > 0 {
		doc += "## Endpoints\n\n"
		for _, ep := range plan.Endpoints {
			doc += fmt.Sprintf("- `%s %s` - %s\n", ep.Method, ep.Path, ep.Desc)
		}
		doc += "\n"
	}

	doc += "## Project Structure\n\n"
	doc += "```\n"
	for _, comp := range plan.Components {
		doc += fmt.Sprintf("%s - %s\n", comp.Path, comp.Description)
	}
	doc += "```\n\n"

	if len(plan.Endpoints) > 0 {
		doc += "## Running\n\n"
		doc += "```bash\ngo run cmd/server/main.go\n```\n\n"
		doc += "The server starts on `:8080`.\n\n"
	} else {
		doc += "## Running\n\n"
		doc += "```bash\ngo run main.go\n```\n\n"
	}

	doc += "## Testing\n\n"
	doc += "```bash\ngo test ./...\n```\n"

	return doc
}
