package units

import (
	"context"
	"fmt"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Synthesizer struct {
	*BaseUnit
	rt *Runtime
}

func NewSynthesizer(rt *Runtime) *Synthesizer {
	return &Synthesizer{
		BaseUnit: NewBaseUnit("unit-synthesizer", state.RoleSynthesizer, "Synthesizer", state.CadenceMedium, state.ActivationOnEvent),
		rt:       rt,
	}
}

func (u *Synthesizer) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	plan := semiState.GetArchitecturePlan()
	if plan == nil {
		return nil, fmt.Errorf("no architecture plan to synthesize")
	}

	proposals := semiState.GetProposals()
	hypotheses := semiState.GetHypotheses()
	testResults := semiState.GetTestResults()
	hasTests := len(testResults) > 0

	var evts []events.Event

	// Accept the hypothesis that matches the plan actually in force. Matching on
	// plan.AlternativeID rather than a hardcoded identifier is what keeps this
	// correct when the plan came from the LLM or from any template variant: a
	// hardcoded ID silently stops matching the moment plan naming changes, and
	// the design is then never marked as chosen.
	selectedID := "hyp-arch-" + plan.AlternativeID
	hasAccepted := false
	for _, h := range hypotheses {
		if h.Status == state.HypothesisAccepted {
			hasAccepted = true
		}
	}

	if !hasAccepted {
		found := false
		for _, h := range hypotheses {
			if h.ID == selectedID {
				found = true
				break
			}
		}
		if found {
			semiState.UpdateHypothesisStatus(selectedID, state.HypothesisAccepted)

			semiState.AddEvidence(state.Evidence{
				Type:      state.EvidenceAnalysis,
				Content:   "synthesized: " + plan.AlternativeID + " accepted as working design",
				Strength:  state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
				RelatedTo:  []string{selectedID},
			})

			semiState.RecordFinding(state.FindingRecord{
				Claim: state.Claim{
					Content:     plan.Description + " is the correct design choice",
					Source:      "synthesis",
					Status:      state.FindingSupported,
					Confidence:  state.ConfidenceHigh,
					Provenance:  state.NewProvenance(u.ID()),
				},
				PreviousStatus: state.FindingUnverified,
				NewStatus:      state.FindingSupported,
				RevisionNotes:  "selected over the rejected alternative on simplicity and evidence",
				Provenance:     state.NewProvenance(u.ID()),
			})
		}
	}

	if hasTests {
		allPassed := true
		for _, tr := range testResults {
			if tr.Status == state.TestFailed {
				allPassed = false
			}
		}

		if allPassed {
			semiState.AddEvidence(state.Evidence{
				Type:     state.EvidenceObservation,
				Content:  "synthesis: all tests passed, implementation provisionally accepted",
				Strength: state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			})
			semiState.SetConfidence(state.ConfidenceHigh)

			semiState.RecordFinding(state.FindingRecord{
				Claim: state.Claim{
					Content:    "implementation is provisionally correct based on passing tests",
					Source:     "synthesis",
					Status:     state.FindingSupported,
					Confidence: state.ConfidenceHigh,
					Provenance: state.NewProvenance(u.ID()),
				},
				PreviousStatus: state.FindingUnverified,
				NewStatus:      state.FindingSupported,
				RevisionNotes:  "all tests pass",
				Provenance:     state.NewProvenance(u.ID()),
			})
		} else {
			semiState.SetConfidence(state.ConfidenceLow)
			u.rt.Attention.Boost("debugger", "tests failed, debugger needed", 0.4)
			u.rt.Attention.Boost("critic", "tests failed, critique needed", 0.3)

			semiState.RecordFinding(state.FindingRecord{
				Claim: state.Claim{
					Content:    "implementation has failing tests",
					Source:     "synthesis",
					Status:     state.FindingContradicted,
					Confidence: state.ConfidenceHigh,
					Provenance: state.NewProvenance(u.ID()),
				},
				PreviousStatus: state.FindingUnverified,
				NewStatus:      state.FindingContradicted,
				RevisionNotes:  "tests fail - implementation requires revision",
				Provenance:     state.NewProvenance(u.ID()),
			})
		}
	}

	bus.Publish(events.EventSynthesisRequested, u.ID(), map[string]interface{}{
		"plan_id":     plan.AlternativeID,
		"description": plan.Description,
	})

	if len(proposals) == 0 {
		u.rt.Attention.Boost("code_generator", "synthesis complete, code generation needed", 0.5)
	}

	return evts, nil
}
