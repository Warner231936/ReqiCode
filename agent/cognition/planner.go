package cognition

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// Plan is an ordered set of steps with its provenance.
type Plan struct {
	Request     string     `json:"request"`
	Mode        string     `json:"mode"`
	Steps       []PlanStep `json:"steps"`
	Source      string     `json:"source"` // model | heuristic | file
	GeneratedAt time.Time  `json:"generated_at"`
	Notes       []string   `json:"notes,omitempty"`
	// Degraded marks a plan produced without a working model, which the caller
	// must surface rather than presenting as a considered plan.
	Degraded bool `json:"degraded,omitempty"`
}

// Render formats a plan.
func (p *Plan) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan for: %s\n", p.Request)
	fmt.Fprintf(&b, "mode: %s   source: %s\n", p.Mode, p.Source)
	if p.Degraded {
		b.WriteString("NOTE: produced without a working language model; treat the file list as a\n")
		b.WriteString("      starting point and the steps as unverified.\n")
	}
	for i, s := range p.Steps {
		id := s.ID
		if id == 0 {
			id = i + 1
		}
		fmt.Fprintf(&b, "%d. %s\n", id, s.Description)
		if len(s.Files) > 0 {
			fmt.Fprintf(&b, "   files: %s\n", strings.Join(s.Files, ", "))
		}
		if s.Verify != "" {
			fmt.Fprintf(&b, "   verify: %s\n", s.Verify)
		}
		if s.Rationale != "" {
			fmt.Fprintf(&b, "   why: %s\n", s.Rationale)
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

// WriteJSON persists a plan.
func (p *Plan) WriteJSON(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	blob, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(blob, '\n'), 0o644)
}

// ReadPlanJSON loads a plan from disk.
func ReadPlanJSON(path string) (*Plan, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var p Plan
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse plan %s: %w", path, err)
	}
	for i := range p.Steps {
		if p.Steps[i].ID == 0 {
			p.Steps[i].ID = i + 1
		}
	}
	if p.Source == "" {
		p.Source = "file"
	}
	return &p, nil
}

// Planner produces a plan.
type Planner interface {
	Plan(ctx context.Context, request string, mode Mode, brief *agentcontext.Brief) (*Plan, error)
}

// ModelPlanner asks a model for a plan.
type ModelPlanner struct {
	Router    *llm.Router
	Role      string
	Catalogue string
	Workspace string
	Project   *verify.Project
}

// Plan produces a plan from the planner model.
//
// Two attempts are made at most. The second supplies the parse or validation error
// from the first, because the failure mode is nearly always a formatting or
// completeness problem that the model can fix once told what was wrong, and a
// third attempt on an unchanged prompt mostly produces the same malformed output.
func (p *ModelPlanner) Plan(ctx context.Context, request string, mode Mode, brief *agentcontext.Brief) (*Plan, error) {
	msgs := PlanOnly(mode, p.Catalogue, p.Workspace, request, brief)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content:
		"Reply with the plan JSON object now. No other text."})

	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		resp, err := p.Router.Chat(ctx, p.Role, msgs, llm.Request{JSONMode: true})
		if err != nil {
			lastErr = err
			continue
		}
		d := ParseDecision(resp.Content)
		if d.Action != ActionPlan && len(d.Plan) == 0 {
			// The model acted instead of planning. That is recoverable: its plan
			// steps are frequently embedded in the thought or summary text.
			if recovered := extractPlanFromProse(resp.Content); recovered != nil {
				return p.finish(request, mode, recovered, "model"), nil
			}
			lastErr = fmt.Errorf("planner returned action %q instead of a plan", d.Action)
			msgs = append(msgs,
				llm.Message{Role: llm.RoleAssistant, Content: truncate(resp.Content, 1500)},
				llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(
					"That was not a plan (%s). Reply with ONLY a JSON object of this exact shape:\n"+
						`{"thought":"...","action":"plan","plan":[{"description":"...","files":["path"],"verify":"how to check","rationale":"..."}]}`,
					lastErr)})
			continue
		}
		if len(d.Plan) == 0 {
			lastErr = fmt.Errorf("plan was empty")
			continue
		}
		if err := validatePlan(d.Plan, p.Project); err != nil {
			lastErr = err
			msgs = append(msgs,
				llm.Message{Role: llm.RoleAssistant, Content: truncate(resp.Content, 1500)},
				llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(
					"The plan is not usable: %v\nProduce a corrected plan. Every step must name an existing file path relative to the workspace root, "+
						"and the plan must be ordered so each step's inputs exist before it runs.", err)})
			continue
		}
		return p.finish(request, mode, d.Plan, "model"), nil
	}
	return nil, fmt.Errorf("planning failed after 2 attempts: %w", lastErr)
}

func (p *ModelPlanner) finish(request string, mode Mode, steps []PlanStep, source string) *Plan {
	for i := range steps {
		if steps[i].ID == 0 {
			steps[i].ID = i + 1
		}
	}
	return &Plan{
		Request: request, Mode: string(mode), Steps: steps,
		Source: source, GeneratedAt: time.Now(),
	}
}

// validatePlan rejects a plan that cannot be executed.
//
// File existence is checked because a plan referring to a file that is not there
// cannot be carried out, and discovering that at execution time wastes the whole
// repair budget. It is not treated as fatal when every file is new, because
// creating new files is the normal case for a feature request.
func validatePlan(steps []PlanStep, project *verify.Project) error {
	if len(steps) == 0 {
		return fmt.Errorf("plan has no steps")
	}
	if len(steps) > 40 {
		return fmt.Errorf("plan has %d steps, which is too many to execute reliably; decompose the request first", len(steps))
	}
	var missing []string
	for _, s := range steps {
		if strings.TrimSpace(s.Description) == "" {
			return fmt.Errorf("a step has an empty description")
		}
		if s.Description == s.Verify && s.Verify != "" {
			return fmt.Errorf("step %q repeats its verification as its description; say what to do, not how to check",
				s.Description)
		}
		for _, f := range s.Files {
			if strings.TrimSpace(f) == "" {
				continue
			}
			if _, err := os.Stat(f); err == nil {
				continue
			}
			// A non-existent path is acceptable when the step is a create.
			lower := strings.ToLower(s.Description + " " + s.Tool)
			if strings.Contains(lower, "creat") || strings.Contains(lower, "add ") || strings.Contains(lower, "new file") {
				continue
			}
			missing = append(missing, f)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("these paths do not exist and the step does not say to create them: %s",
			strings.Join(missing, ", "))
	}
	return nil
}

// extractPlanFromProse pulls numbered steps out of prose.
func extractPlanFromProse(text string) []PlanStep {
	var out []PlanStep
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || len(t) < 8 {
			continue
		}
		if strings.HasPrefix(t, "```") {
			continue
		}
		t = strings.TrimLeft(t, "-*•0123456789.) \t")
		if t == "" {
			continue
		}
		if len(t) < 15 {
			continue
		}
		out = append(out, PlanStep{ID: len(out) + 1, Description: t})
		if len(out) >= 20 {
			break
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}

// HeuristicPlanner builds a plan without a language model.
//
// This is what makes the agent usable with no model at all. It cannot invent an
// implementation, and it does not pretend to: it produces a locate-then-act plan
// over real files, and it says so. A plan this honest is actionable by a human and
// executable by `agent apply`, which is a strictly better outcome than a plan that
// reads well and names files that do not exist.
type HeuristicPlanner struct {
	Manager *agentcontext.Manager
	Project *verify.Project
	Root    string
	Store   *memory.Store
}

// Plan produces a locate-first plan.
func (h *HeuristicPlanner) Plan(ctx context.Context, request string, mode Mode, brief *agentcontext.Brief) (*Plan, error) {
	p := &Plan{
		Request: request, Mode: string(mode), Source: "heuristic",
		GeneratedAt: time.Now(), Degraded: true,
	}

	p.Steps = append(p.Steps, PlanStep{ID: 1,
		Description: "Inspect the project to confirm its structure and verification commands",
		Tool:        "inspect_project",
		Rationale:   "The build and test commands determine what evidence a change must produce.",
	})

	if h.Project != nil {
		var build, test []string
		for _, c := range h.Project.All() {
			switch c.Name {
			case "build":
				build = append(build, strings.Join(c.Args, " "))
			case "test":
				test = append(test, strings.Join(c.Args, " "))
			}
		}
		p.Steps = append(p.Steps, PlanStep{ID: 2,
			Description: "Record the baseline verification result before changing anything",
			Tool:        "build_project",
			Verify:      strings.Join(append(build, test...), " ; "),
			Rationale:   "Without a baseline, a later failure cannot be attributed to the change.",
		})
	}

	var targets []string
	if h.Manager != nil {
		chunks, err := h.Manager.Retrieve(ctx, request, 8)
		if err == nil {
			for _, c := range chunks {
				if !contains(targets, c.Path) {
					targets = append(targets, c.Path)
				}
			}
		}
	}
	if len(targets) == 0 {
		targets = []string{}
	}

	if len(targets) > 0 {
		p.Steps = append(p.Steps, PlanStep{ID: 3,
			Description: "Read the code most relevant to the request",
			Files:       targets[:minInt(len(targets), 6)],
			Tool:        "read_file",
			Rationale:   "Files ranked by lexical and semantic match to the request text.",
		})
	}

	if mode.Spec().AllowsChanges {
		p.Steps = append(p.Steps, PlanStep{ID: len(p.Steps) + 1,
			Description: "Apply the change to " + describeTargets(targets),
			Files:       targets[:minInt(len(targets), 6)],
			Tool:        "edit_file",
			Verify:      "build_project; run_tests",
			Rationale:   "An edit must be followed by the project's own verification, not by inspection.",
		})
		p.Steps = append(p.Steps, PlanStep{ID: len(p.Steps) + 1,
			Description: "Review the diff for unrelated changes and regressions",
			Tool:        "git_diff",
			Rationale:   "Catching an unintended edit is cheaper here than after the change is accepted.",
		})
	}

	p.Notes = append(p.Notes,
		"No language model backend was reachable, so this plan was derived from repository structure and lexical ranking only.",
		"It names real files but proposes no code. Write the intended edit into a plan file and run 'agent apply --plan <file>' to execute and verify it.")
	if len(targets) == 0 {
		p.Notes = append(p.Notes,
			"No file matched the request text. The request may use different words than the code; try search_code with a concrete symbol name.")
	}

	// Surface what the project already learned about this area. A plan that
	// repeats an approach previously recorded as failed is worse than no plan.
	if prior := memoryTerms(h.Store, request); len(prior) > 0 {
		p.Notes = append(p.Notes, "Previously recorded about this area:")
		for _, n := range prior {
			p.Notes = append(p.Notes, "  "+n)
		}
	}
	return p, nil
}

func describeTargets(targets []string) string {
	if len(targets) == 0 {
		return "the files identified as relevant"
	}
	if len(targets) <= 3 {
		return strings.Join(targets, ", ")
	}
	return strings.Join(targets[:3], ", ") + fmt.Sprintf(" and %d more", len(targets)-3)
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// memoryTerms extracts the terms a request shares with stored memory, so a plan
// can note what the project already learned about this area.
func memoryTerms(store *memory.Store, request string) []string {
	if store == nil {
		return nil
	}
	terms := memory.ExtractTerms(request)
	facts := memory.SortFactsByRelevance(store.Facts(false), terms)
	var out []string
	for _, f := range facts {
		if len(out) >= 5 {
			break
		}
		if anyTermIn(terms, f.Topic+" "+f.Content) {
			out = append(out, fmt.Sprintf("[%s] %s", f.Topic, f.Content))
		}
	}
	return out
}

func anyTermIn(terms []string, s string) bool {
	lower := strings.ToLower(s)
	for _, t := range terms {
		if len(t) >= 4 && strings.Contains(lower, strings.ToLower(t)) {
			return true
		}
	}
	return false
}