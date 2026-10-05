package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type ConsistencyChecker struct {
	*BaseUnit
	rt *Runtime
}

func NewConsistencyChecker(rt *Runtime) *ConsistencyChecker {
	return &ConsistencyChecker{
		BaseUnit: NewBaseUnit("unit-consistency-checker", state.RoleConsistencyChecker, "ConsistencyChecker", state.CadenceFast, state.ActivationOnEvent),
		rt:       rt,
	}
}

func (u *ConsistencyChecker) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	plan := semiState.GetArchitecturePlan()
	if plan == nil {
		return nil, nil
	}

	if !semiState.HasGeneratedFiles() {
		return nil, nil
	}

	var issues []ConsistencyIssue

	for _, comp := range plan.Components {
		if !semiState.FileExistsInGenerated(comp.Path) {
			issues = append(issues, ConsistencyIssue{
				Component: comp.Name,
				Issue:     fmt.Sprintf("component %s not found at expected path %s", comp.Name, comp.Path),
				Severity:  "medium",
			})
		}
	}

	files := semiState.GetGeneratedFiles()
	for path, entry := range files {
		if !isGoFile(path) {
			continue
		}
		if !strings.Contains(entry.Content, "package ") {
			issues = append(issues, ConsistencyIssue{
				Component: path,
				Issue:     "missing package declaration",
				Severity:  "high",
			})
		}
	}

	for _, tr := range semiState.GetTestResults() {
		if tr.Status == state.TestFailed && (strings.Contains(tr.Stdout, "panic") || strings.Contains(tr.Stdout, "RACE")) {
			issues = append(issues, ConsistencyIssue{
				Component: "runtime",
				Issue:     "panic or race condition detected in tests - runtime consistency failure",
				Severity:  "high",
			})
		}
	}

	var evts []events.Event
	for _, issue := range issues {
		semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    fmt.Sprintf("consistency: %s", issue.Issue),
			Strength:   state.ConfidenceHigh,
			Provenance: state.NewProvenance(u.ID()),
		})
		evts = append(evts, events.Event{
			Type:     events.EventReviewRequested,
			SourceID: u.ID(),
			Payload: map[string]interface{}{
				"target":      issue.Component,
				"issue":       issue.Issue,
				"severity":    issue.Severity,
				"consistency": true,
			},
		})
	}

	if len(issues) == 0 {
		semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    "consistency check passed: all components present and code well-formed",
			Strength:   state.ConfidenceHigh,
			Provenance: state.NewProvenance(u.ID()),
		})
		u.rt.Attention.Boost("documentation_writer", "consistency verified, documentation ready", 0.2)
	} else {
		u.rt.Attention.Boost("critic", fmt.Sprintf("consistency issues found: %d", len(issues)), 0.3)
	}

	return evts, nil
}

type ConsistencyIssue struct {
	Component string `json:"component"`
	Issue     string `json:"issue"`
	Severity  string `json:"severity"`
}
