package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Debugger struct {
	*BaseUnit
	rt       *Runtime
	ws       *workspace.Workspace
	proposer *proposals.Proposer
}

func NewDebugger(rt *Runtime, ws *workspace.Workspace, prop *proposals.Proposer) *Debugger {
	return &Debugger{
		BaseUnit: NewBaseUnit("unit-debugger", state.RoleDebugger, "Debugger", state.CadenceFast, state.ActivationOnEvent),
		rt:       rt,
		ws:       ws,
		proposer: prop,
	}
}

func (u *Debugger) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	testResults := semiState.GetTestResults()

	var latestFailure *state.TestResult
	for i := len(testResults) - 1; i >= 0; i-- {
		if testResults[i].Status == state.TestFailed {
			latestFailure = &testResults[i]
			break
		}
	}

	if latestFailure == nil {
		return nil, nil
	}

	details := u.analyzeFailure(latestFailure)
	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceAnalysis,
		Content:  fmt.Sprintf("analyzed test failure: %d issues found", len(details)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	var evts []events.Event
	for _, d := range details {
		if d.Type == "compilation" || d.Type == "reference" {
			fix := u.generateFix(d, semiState)
			if fix != nil {
				validated, conflicts, err := u.proposer.ValidateAndCheck(*fix, semiState.GetProposals())
				if err == nil && len(conflicts) == 0 {
					validated.Status = state.ProposalApproved
					semiState.AddProposal(validated)

					if err := u.ws.ApplyProposal(validated); err == nil {
						validated.Status = state.ProposalApplied
						evts = append(evts, events.Event{
							Type:     events.EventCodeChanged,
							SourceID: u.ID(),
							Payload:  events.PayloadForProposal(validated),
						})
						u.rt.Attention.Boost("test_runner", "debugger applied fixes, rerun tests", 0.5)
					}
				}
			}
		}
	}

	return evts, nil
}

func (u *Debugger) analyzeFailure(tr *state.TestResult) []FailureDetail {
	return (&TestRunnerUnit{}).ExtractFailureDetails(*tr)
}

func (u *Debugger) generateFix(d FailureDetail, semiState *state.SemiState) *state.CodeProposal {
	if d.Type == "reference" && strings.Contains(d.Line, "undefined") {
		missingImport := extractMissingSymbol(d.Line)
		if missingImport != "" {
			prop := u.proposer.CreateProposal(
				state.OpModify,
				"internal/handlers/todo.go",
				"",
				"add missing import",
				u.ID(),
				state.ConfidenceMedium,
				"fix compilation error by adding missing import",
				nil,
			)
			return &prop
		}
	}
	return nil
}

func extractMissingSymbol(line string) string {
	return ""
}
