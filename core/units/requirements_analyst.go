package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type RequirementsAnalyst struct {
	*BaseUnit
	rt *Runtime
}

func NewRequirementsAnalyst(rt *Runtime) *RequirementsAnalyst {
	return &RequirementsAnalyst{
		BaseUnit: NewBaseUnit("unit-requirements-analyst", state.RoleRequirementsAnalyst, "RequirementsAnalyst", state.CadenceSlow, state.ActivationManual),
		rt:       rt,
	}
}

func (u *RequirementsAnalyst) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	if len(semiState.GetRequirements()) > 0 {
		return nil, nil
	}

	reqText := u.rt.Config.ProjectRoot
	if reqText == "" {
		reqText = "Build a small HTTP service that stores TODO items."
	}

	reqs := parseRequirements(reqText)
	var evts []events.Event

	for _, r := range reqs {
		semiState.AppendRequirement(r)
		u.rt.Memory.StoreRequirement(r)
	}

	for _, r := range reqs {
		ev := events.Event{
			Type:     events.EventRequirementAdded,
			SourceID: u.ID(),
			Payload:  events.PayloadForRequirement(r),
		}
		evts = append(evts, ev)
	}

	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("parsed %d requirements from user intent", len(reqs)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	u.rt.Attention.Boost("decomposer", "requirements parsed, decomposition needed", 0.3)
	u.rt.Attention.Boost("architect", "requirements parsed, architecture needed", 0.2)

	return evts, nil
}

func parseRequirements(text string) []state.Requirement {
	if text == "" {
		return []state.Requirement{}
	}

	var reqs []state.Requirement
	lower := strings.ToLower(text)

	if strings.Contains(lower, "http") || strings.Contains(lower, "service") || strings.Contains(lower, "api") ||
		strings.Contains(lower, "endpoint") || strings.Contains(lower, "server") {
		reqs = append(reqs, state.Requirement{
			ID:        "req-001",
			Content:   "HTTP API endpoints for resource management",
			Type:      "functional",
			Priority:  "high",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	if strings.Contains(lower, "todo") {
		reqs = append(reqs, state.Requirement{
			ID:        "req-002",
			Content:   "Store TODO items with create/read/update/delete operations",
			Type:      "functional",
			Priority:  "high",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	if strings.Contains(lower, "file") || strings.Contains(lower, "directory") || strings.Contains(lower, "list") ||
		strings.Contains(lower, "scan") {
		reqs = append(reqs, state.Requirement{
			ID:        fmt.Sprintf("req-%03d", len(reqs)+1),
			Content:   fmt.Sprintf("File system traversal and listing: %s", text),
			Type:      "functional",
			Priority:  "high",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	if strings.Contains(lower, "store") || strings.Contains(lower, "persist") || strings.Contains(lower, "database") {
		reqs = append(reqs, state.Requirement{
			ID:        fmt.Sprintf("req-%03d", len(reqs)+1),
			Content:   "Persistent storage for data items",
			Type:      "functional",
			Priority:  "medium",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	if strings.Contains(lower, "test") {
		reqs = append(reqs, state.Requirement{
			ID:        fmt.Sprintf("req-%03d", len(reqs)+1),
			Content:   "Automated tests for core functionality",
			Type:      "non-functional",
			Priority:  "medium",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	if len(reqs) == 0 {
		reqs = append(reqs, state.Requirement{
			ID:        "req-001",
			Content:   text,
			Type:      "functional",
			Priority:  "high",
			Provenance: state.NewProvenance("requirements-analyst"),
		})
	}

	return reqs
}
