package units

import (
	"context"
	"fmt"

	"github.com/kilo/spiral-codemaker/code/validation"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type SecurityAnalyst struct {
	*BaseUnit
	rt *Runtime
	v  *validation.CodeValidator
}

func NewSecurityAnalyst(rt *Runtime) *SecurityAnalyst {
	return &SecurityAnalyst{
		BaseUnit: NewBaseUnit("unit-security-analyst", state.RoleSecurityAnalyst, "SecurityAnalyst", state.CadenceSlow, state.ActivationOnEvent),
		rt:       rt,
		v:        validation.NewCodeValidator(),
	}
}

func (u *SecurityAnalyst) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	files := semiState.GetGeneratedFiles()

	if len(files) == 0 {
		return nil, nil
	}

	var evts []events.Event
	issuesFound := 0

	for path, entry := range files {
		if !isGoFile(path) {
			continue
		}
		issues := u.v.SecurityScan(entry.Content)
		for _, issue := range issues {
			issuesFound++
			obj := state.Objection{
				SourceUnit: u.ID(),
				Target:     path,
				Content:    fmt.Sprintf("security: %s (%s)", issue.Message, issue.Rule),
				Severity:   issue.Severity,
				Provenance: state.NewProvenance(u.ID()),
			}
			semiState.AddObjection(obj)

			evts = append(evts, events.Event{
				Type:     events.EventReviewRequested,
				SourceID: u.ID(),
				Payload: map[string]interface{}{
					"target":   path,
					"severity": issue.Severity,
					"content":  obj.Content,
					"rule":     issue.Rule,
					"security": true,
				},
			})

			if issue.Severity == "critical" || issue.Severity == "high" {
				u.rt.Attention.Boost("critic", fmt.Sprintf("critical security issue in %s", path), 0.4)
			}
		}
	}

	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceAnalysis,
		Content:  fmt.Sprintf("security scan completed: %d issues found across %d files", issuesFound, len(files)),
		Strength: state.ConfidenceMedium,
		Provenance: state.NewProvenance(u.ID()),
	})

	return evts, nil
}

func isGoFile(path string) bool {
	return len(path) > 3 && path[len(path)-3:] == ".go"
}
