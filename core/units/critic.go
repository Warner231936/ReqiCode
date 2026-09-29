package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type Critic struct {
	*BaseUnit
	rt *Runtime
}

func NewCritic(rt *Runtime) *Critic {
	return &Critic{
		BaseUnit: NewBaseUnit("unit-critic", state.RoleCritic, "Critic", state.CadenceMedium, state.ActivationOnEvent),
		rt:       rt,
	}
}

func (u *Critic) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	if !semiState.HasGeneratedFiles() {
		return nil, nil
	}

	plan := semiState.GetArchitecturePlan()
	if plan == nil {
		return nil, nil
	}

	testResults := semiState.GetTestResults()
	allTestsPassed := true
	hasAnyTests := false
	for _, tr := range testResults {
		hasAnyTests = true
		if tr.Status == state.TestFailed {
			allTestsPassed = false
		}
	}

	var evts []events.Event

	if allTestsPassed && hasAnyTests {
		semiState.AddEvidence(state.Evidence{
			Type:     state.EvidenceCodeReview,
			Content:  "critique: all tests pass, no immediate objections to correctness",
			Strength: state.ConfidenceHigh,
			Provenance: state.NewProvenance(u.ID()),
		})

		semiState.RecordFinding(state.FindingRecord{
			Claim: state.Claim{
				Content:    "implementation is provisionally correct based on passing tests",
				Source:     "critic",
				Status:     state.FindingSupported,
				Confidence: state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			},
			PreviousStatus: state.FindingUnverified,
			NewStatus:      state.FindingSupported,
			RevisionNotes:  "tests confirm basic correctness",
			Provenance:     state.NewProvenance(u.ID()),
		})
		return evts, nil
	}

	issues := u.critique(semiState)
	for _, issue := range issues {
		obj := state.Objection{
			SourceUnit: u.ID(),
			Target:     issue.Target,
			Content:    issue.Content,
			Severity:   issue.Severity,
			Provenance: state.NewProvenance(u.ID()),
		}
		semiState.AddObjection(obj)

		ev := events.Event{
			Type:     events.EventReviewRequested,
			SourceID: u.ID(),
			Payload: map[string]interface{}{
				"target":   issue.Target,
				"severity": issue.Severity,
				"content":  issue.Content,
			},
		}
		evts = append(evts, ev)
	}

	if len(issues) == 0 {
		issues = u.findSubtleIssues(semiState)
		for _, issue := range issues {
			obj := state.Objection{
				SourceUnit: u.ID(),
				Target:     issue.Target,
				Content:    issue.Content,
				Severity:   issue.Severity,
				Provenance: state.NewProvenance(u.ID()),
			}
			semiState.AddObjection(obj)

			evts = append(evts, events.Event{
				Type:     events.EventReviewRequested,
				SourceID: u.ID(),
				Payload: map[string]interface{}{
					"target":   issue.Target,
					"severity": issue.Severity,
					"content":  issue.Content,
					"subtle":   true,
				},
			})
		}
	}

	u.rt.Attention.Boost("synthesizer", fmt.Sprintf("critique complete: %d issues found", len(issues)), 0.3)

	if u.rt.LLM != nil && u.rt.LLM.HasProvider(routing.CapSpecialize) {
		llmIssues := u.llmReview(ctx, semiState)
		issues = append(issues, llmIssues...)
	}

	return evts, nil
}

type CritiqueIssue struct {
	Target        string
	ComponentPath string
	Content       string
	Severity      string
}

func (u *Critic) critique(s *state.SemiState) []CritiqueIssue {
	var issues []CritiqueIssue

	for _, tr := range s.GetTestResults() {
		if tr.Status == state.TestFailed {
			issues = append(issues, CritiqueIssue{
				Target:   tr.ID,
				Content:  fmt.Sprintf("test failure detected: %s. Failure class: %s", tr.Stdout, tr.FailureClass),
				Severity: "high",
			})
		}
	}

	for _, obj := range s.GetObjections() {
		if !obj.Resolved {
			issues = append(issues, CritiqueIssue{
				Target:   obj.Target,
				Content:  fmt.Sprintf("unresolved objection: %s", obj.Content),
				Severity: obj.Severity,
			})
		}
	}

	return issues
}

func (u *Critic) findSubtleIssues(s *state.SemiState) []CritiqueIssue {
	var issues []CritiqueIssue

	plan := s.GetArchitecturePlan()
	if plan != nil {
		for _, comp := range plan.Components {
			if !s.FileExistsInGenerated(comp.Path) {
				issues = append(issues, CritiqueIssue{
					ComponentPath: comp.Path,
					Target:        comp.Name,
					Content:       fmt.Sprintf("planned component not yet generated: %s", comp.Path),
					Severity:      "medium",
				})
			}
		}
	}

	for _, tr := range s.GetTestResults() {
		if strings.Contains(tr.Stdout, "race") || strings.Contains(tr.Stdout, "RACE") {
			issues = append(issues, CritiqueIssue{
				Target:   "store",
				Content:  "potential race condition detected in test output",
				Severity: "high",
			})
		}
	}

	if s.GetConfidence() < state.ConfidenceMedium {
		issues = append(issues, CritiqueIssue{
			Target:   "overall",
			Content:  "system confidence is below acceptable threshold",
			Severity: "high",
		})
	}

	return issues
}

func (u *Critic) llmReview(ctx context.Context, s *state.SemiState) []CritiqueIssue {
	var issues []CritiqueIssue

	files := s.GetGeneratedFiles()
	for _, f := range files {
		if strings.HasSuffix(f.Path, "_test.go") || strings.HasSuffix(f.Path, "go.mod") || len(f.Content) < 20 {
			continue
		}

		review, err := u.rt.LLM.GenerateReview(ctx, routing.CapSpecialize, f.Content, f.Path)
		if err != nil {
			continue
		}

		for _, issue := range review.Issues {
			if issue.Severity == "high" || issue.Severity == "critical" {
				issues = append(issues, CritiqueIssue{
					Target:   f.Path,
					Content:  fmt.Sprintf("LLM review [%s]: %s", issue.Severity, issue.Message),
					Severity: issue.Severity,
				})
			}
		}
	}

	return issues
}
