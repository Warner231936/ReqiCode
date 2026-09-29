package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Decomposer struct {
	*BaseUnit
	rt *Runtime
}

func NewDecomposer(rt *Runtime) *Decomposer {
	return &Decomposer{
		BaseUnit: NewBaseUnit("unit-decomposer", state.RoleDecomposer, "Decomposer", state.CadenceMedium, state.ActivationOnEvent),
		rt:       rt,
	}
}

func (u *Decomposer) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	reqs := semiState.GetRequirements()

	if len(reqs) == 0 {
		return nil, fmt.Errorf("no requirements to decompose")
	}

	var subtasks []Subtask
	for _, req := range reqs {
		subtasks = append(subtasks, decomposeRequirement(req)...)
	}

	for _, st := range subtasks {
		semiState.AddUnresolved(st.Description)
	}

	var evts []events.Event
	for _, st := range subtasks {
		evts = append(evts, events.Event{
			Type:     events.EventUnresolvedAdded,
			SourceID: u.ID(),
			Payload: map[string]interface{}{
				"description": st.Description,
				"type":        st.Type,
			},
		})
	}

	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("decomposed %d requirements into %d subtasks", len(reqs), len(subtasks)),
		Strength: state.ConfidenceMedium,
		Provenance: state.NewProvenance(u.ID()),
	})

	u.rt.Attention.Boost("architect", "decomposition complete, architecture design needed", 0.3)

	return evts, nil
}

type Subtask struct {
	ID          string   `json:"id"`
	Description string   `json:"description"`
	Type        string   `json:"type"`
	Priority    string   `json:"priority"`
	DependsOn   []string `json:"depends_on"`
}

func decomposeRequirement(req state.Requirement) []Subtask {
	content := strings.ToLower(req.Content)

	var subtasks []Subtask

	if strings.Contains(content, "http") || strings.Contains(content, "api") || strings.Contains(content, "endpoint") {
		subtasks = append(subtasks, Subtask{
			ID:          req.ID + "-sub-01",
			Description: "Design HTTP API endpoints and routes",
			Type:        "design",
			Priority:    req.Priority,
			DependsOn:   []string{req.ID},
		})
	}

	if strings.Contains(content, "store") || strings.Contains(content, "persist") || strings.Contains(content, "todo") {
		subtasks = append(subtasks, Subtask{
			ID:          req.ID + "-sub-02",
			Description: "Design data model and in-memory storage",
			Type:        "design",
			Priority:    req.Priority,
			DependsOn:   []string{req.ID},
		})
	}

	if strings.Contains(content, "http") || strings.Contains(content, "api") {
		subtasks = append(subtasks, Subtask{
			ID:          req.ID + "-sub-03",
			Description: "Implement HTTP server with CRUD handlers",
			Type:        "implementation",
			Priority:    req.Priority,
			DependsOn:   []string{req.ID + "-sub-01"},
		})
	}

	if strings.Contains(content, "store") || strings.Contains(content, "todo") {
		subtasks = append(subtasks, Subtask{
			ID:          req.ID + "-sub-04",
			Description: "Implement storage layer",
			Type:        "implementation",
			Priority:    req.Priority,
			DependsOn:   []string{req.ID + "-sub-02"},
		})
	}

	subtasks = append(subtasks, Subtask{
		ID:          req.ID + "-sub-05",
		Description: "Write unit tests and integration tests",
		Type:        "testing",
		Priority:    "high",
		DependsOn:   []string{req.ID + "-sub-03", req.ID + "-sub-04"},
	})

	subtasks = append(subtasks, Subtask{
		ID:          req.ID + "-sub-06",
		Description: "Write documentation",
		Type:        "documentation",
		Priority:    "medium",
		DependsOn:   []string{req.ID + "-sub-03", req.ID + "-sub-04"},
	})

	return subtasks
}
