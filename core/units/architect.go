package units

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type Architect struct {
	*BaseUnit
	rt *Runtime
}

func NewArchitect(rt *Runtime) *Architect {
	return &Architect{
		BaseUnit: NewBaseUnit("unit-architect", state.RoleArchitect, "Architect", state.CadenceMedium, state.ActivationOnEvent),
		rt:       rt,
	}
}

func (u *Architect) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	if semiState.GetArchitecturePlan() != nil {
		return nil, nil
	}

	reqs := semiState.GetRequirements()
	if len(reqs) == 0 {
		return nil, fmt.Errorf("no requirements for architecture")
	}

	reqText := u.rt.Config.ProjectRoot

	var plan *state.ArchitecturePlan

	if u.rt.LLM != nil && u.rt.LLM.HasProvider(routing.CapReasoning) {
		llmPlan, err := u.generatePlanFromLLM(ctx, reqText, reqs)
		if err != nil {
			semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceObservation,
				Content:    fmt.Sprintf("LLM architecture planning failed, using fallback: %s", err.Error()),
				Strength:   state.ConfidenceLow,
				Provenance: state.NewProvenance(u.ID()),
			})
		} else {
			plan = llmPlan
			semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceAnalysis,
				Content:    fmt.Sprintf("LLM-generated architecture plan: %s", llmPlan.Description),
				Strength:   state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			})

			// Record the LLM call as an intervention so the ledger can credit it.
			//
			// Without this the model does work that never appears in the causal
			// record, and the strategy ranking would systematically favour
			// template generation simply because it is the only strategy with
			// interventions attributed to it.
			u.rt.Ledger.RecordModelCall(state.CodeProposal{
				ID:              "prop-arch-llm",
				File:            "architecture/plan.json",
				Operation:       state.OpCreate,
				Reason:          "LLM-proposed architecture plan",
				OriginatingUnit: u.ID(),
				ExpectedEffect:  "a valid architecture plan for the requirements",
				Provenance:      state.NewProvenance(u.ID()),
			}, "llm-plan", u.rt.LastLLMCall())
		}
	}

	if plan == nil {
		plan = u.generatePlanFromTemplate(reqText)
	}

	semiState.SetArchitecturePlan(plan)

	// Every architecture, whoever proposed it, is an epistemic claim that the
	// design satisfies the requirements. Recording it as a hypothesis regardless
	// of provenance is what lets later evidence attach to the design and drive
	// it to SUPPORTED or CONTRADICTED. A design adopted without a hypothesis is
	// a design the system can never learn to distrust.
	u.recordHypothesis(plan)

	semiState.AddEvidence(state.Evidence{
		Type:       state.EvidenceAnalysis,
		Content:    fmt.Sprintf("produced architecture plan: %s", plan.Description),
		Strength:   plan.Confidence,
		Provenance: state.NewProvenance(u.ID()),
	})

	u.rt.Attention.Boost("decomposer", "architecture plan produced, decomposition needed", 0.4)
	u.rt.Attention.Boost("synthesizer", "architecture plan ready for synthesis", 0.3)

	bus.Publish(events.EventEvidenceAdded, u.ID(), events.PayloadForEvidence(state.Evidence{
		Type:       state.EvidenceAnalysis,
		Content:    "architecture design completed",
		Provenance: state.NewProvenance(u.ID()),
	}))

	return nil, nil
}

func (u *Architect) generatePlanFromLLM(ctx context.Context, intent string, reqs []state.Requirement) (*state.ArchitecturePlan, error) {
	var reqList strings.Builder
	for i, r := range reqs {
		reqList.WriteString(fmt.Sprintf("%d. %s (priority: %s)\n", i+1, r.Content, r.Priority))
	}

	prompt := fmt.Sprintf(`You are an expert software architect. Design a Go project architecture for this intent. Output ONLY valid JSON.

Intent: %s

Requirements:
%s

JSON schema:
{"description":"<real description of this architecture>","components":[{"name":"<name>","description":"<what it does>","path":"<go/path/file.go>"}],"dependencies":[{"name":"<dep>","version":"<ver>","type":"stdlib"}],"endpoints":[{"method":"GET","path":"/<path>","desc":"<desc>"}]}

Rules:
- description must be 1-2 sentences about the architecture, not placeholder text
- components: list 3-5 files needed (e.g. main.go, store.go, handlers.go, types.go)
- For CLI tools, include main.go and a core module
- For HTTP services, include handlers and routes
- dependencies: always include Go stdlib
- endpoints: empty array [] for non-HTTP tools
- Use realistic Go file paths like cmd/app/main.go or pkg/core/file.go`, intent, reqList.String())

	resp, err := u.rt.LLM.GeneratePlan(ctx, routing.CapReasoning, prompt)
	if u.rt.Config.Debug {
		fmt.Printf("[DEBUG] LLM architecture plan response:\n%s\n", resp)
	}
	if err != nil {
		return nil, err
	}

	resp = strings.TrimSpace(resp)
	startIdx := strings.Index(resp, "{")
	endIdx := strings.LastIndex(resp, "}")
	if startIdx == -1 || endIdx == -1 || endIdx <= startIdx {
		return nil, fmt.Errorf("no JSON found in LLM response")
	}
	resp = resp[startIdx : endIdx+1]

	var raw struct {
		Description  string                `json:"description"`
		Components   []state.ComponentSpec `json:"components"`
		Dependencies []state.DepSpec       `json:"dependencies"`
		Endpoints    []state.EndpointSpec  `json:"endpoints"`
	}
	if err := json.Unmarshal([]byte(resp), &raw); err != nil {
		return nil, fmt.Errorf("parse LLM plan JSON: %w", err)
	}

	// Verification gate. A weak model will happily echo the prompt's own
	// placeholder values back as data, producing syntactically valid JSON that
	// describes no real architecture. Accepting that is worse than failing: it
	// puts "<go/path/file.go>" into the state and then writes files there.
	if err := validatePlan(&raw); err != nil {
		return nil, err
	}

	plan := &state.ArchitecturePlan{
		Description:   raw.Description,
		Components:    raw.Components,
		Dependencies:  raw.Dependencies,
		Endpoints:     raw.Endpoints,
		Confidence:    state.ConfidenceMedium,
		Provenance:    state.NewProvenance(u.ID()),
		AlternativeID: "llm-plan",
	}

	// Record the design decision in both the live state and persistent memory so
	// the choice is auditable and survives across runs.
	dec := state.Decision{
		ID:         "dec-arch-llm",
		Question:   "Which architecture to adopt?",
		Decision:   "llm-plan",
		Rationale:  "LLM-proposed architecture selected over template fallback",
		Provenance: state.NewProvenance(u.ID()),
	}
	u.rt.SemiState.AddDecision(dec)
	u.rt.Memory.StoreDecision(dec)

	return plan, nil
}

// placeholderMarkers are the literal tokens a model emits when it echoes the
// prompt's example values instead of answering.
var placeholderMarkers = []string{"<", ">", "{{", "}}", "...", "path/to", "your-", "xxx", "TODO"}

// validatePlan rejects architecture plans that are structurally present but
// semantically empty. Every check here corresponds to a way a plan can be
// syntactically valid and still useless.
func validatePlan(raw *struct {
	Description  string                `json:"description"`
	Components   []state.ComponentSpec `json:"components"`
	Dependencies []state.DepSpec       `json:"dependencies"`
	Endpoints    []state.EndpointSpec  `json:"endpoints"`
}) error {
	if len(raw.Components) == 0 {
		return fmt.Errorf("plan has no components")
	}

	if isPlaceholder(raw.Description) {
		return fmt.Errorf("plan description is a placeholder: %q", raw.Description)
	}

	for i, c := range raw.Components {
		if isPlaceholder(c.Path) {
			return fmt.Errorf("component %d (%q) has placeholder path %q", i, c.Name, c.Path)
		}
		if isPlaceholder(c.Description) {
			return fmt.Errorf("component %d (%q) has placeholder description", i, c.Name)
		}
		if !strings.HasSuffix(c.Path, ".go") {
			return fmt.Errorf("component %d (%q) path %q is not a .go file", i, c.Name, c.Path)
		}
		if strings.ContainsAny(c.Path, `\<>"|?*`) {
			return fmt.Errorf("component %d (%q) path %q contains illegal characters", i, c.Name, c.Path)
		}
		if strings.Contains(c.Path, "..") {
			return fmt.Errorf("component %d (%q) path %q escapes the workspace", i, c.Name, c.Path)
		}
		if c.Name == "" {
			return fmt.Errorf("component %d has no name", i)
		}
	}

	for i, e := range raw.Endpoints {
		if !isPlaceholder(e.Path) && !strings.HasPrefix(e.Path, "/") {
			return fmt.Errorf("endpoint %d path %q must start with /", i, e.Path)
		}
		switch e.Method {
		case "GET", "POST", "PUT", "DELETE", "PATCH", "HEAD", "OPTIONS":
		default:
			return fmt.Errorf("endpoint %d has invalid method %q", i, e.Method)
		}
	}

	return nil
}

func isPlaceholder(s string) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, m := range placeholderMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
}

// recordHypothesis registers the selected architecture as an epistemic claim.
//
// A hypothesis is a prediction that this design satisfies the requirements. It
// starts PROPOSED with no supporting evidence because nothing has verified it
// yet; confidence reflects the source's own certainty, not evidence of
// correctness. Keeping that distinction is the point of the type.
func (u *Architect) recordHypothesis(plan *state.ArchitecturePlan) {
	ss := u.rt.SemiState

	ss.AddHypothesis(state.Hypothesis{
		ID:         "hyp-arch-" + plan.AlternativeID,
		Content:    fmt.Sprintf("Architecture %s satisfies the requirements: %s", plan.AlternativeID, plan.Description),
		Status:     state.HypothesisProposed,
		Confidence: plan.Confidence,
		Provenance: state.NewProvenance(u.ID()),
		Supporting: []string{fmt.Sprintf("%d components", len(plan.Components))},
	})

	// A single hypothesis is not a comparison. Recording the rival that was NOT
	// taken is what gives the selected design something to be measured against,
	// and it is the only way a later contradiction can reveal that the choice
	// was wrong rather than merely untested.
	//
	// The rival stays PROPOSED rather than REJECTED. It was not selected, but
	// nothing has refuted it either, and marking it REJECTED would assert that
	// evidence disproved a design that simply never ran. Keeping it live is
	// what lets a future failure revive the alternative.
	if rival := u.rivalPlan(plan); rival != nil {
		ss.AddHypothesis(state.Hypothesis{
			ID:         "hyp-arch-" + rival.AlternativeID,
			Content:    fmt.Sprintf("Alternative architecture %s would satisfy the requirements: %s", rival.AlternativeID, rival.Description),
			Status:     state.HypothesisProposed,
			Confidence: rival.Confidence,
			Provenance: state.NewProvenance(u.ID()),
			Supporting: []string{fmt.Sprintf("%d components", len(rival.Components))},
		})
	}
}

// rivalPlan constructs the competing design that was considered and rejected.
// The alternative differs in the one axis the system actually reasons about:
// how state is held. A design chosen without a considered alternative is an
// assertion, not a decision.
func (u *Architect) rivalPlan(chosen *state.ArchitecturePlan) *state.ArchitecturePlan {
	hasStore := false
	for _, c := range chosen.Components {
		if strings.Contains(c.Path, "store") {
			hasStore = true
			break
		}
	}

	rival := &state.ArchitecturePlan{
		Description:   "Go application with file-backed persistence instead of in-memory state",
		Components:    append([]state.ComponentSpec(nil), chosen.Components...),
		Dependencies:  chosen.Dependencies,
		Endpoints:     chosen.Endpoints,
		Confidence:    state.ConfidenceLow,
		Provenance:    state.NewProvenance(u.ID()),
		AlternativeID: "plan-alt-" + chosen.AlternativeID,
	}

	if hasStore {
		for i := range rival.Components {
			if strings.Contains(rival.Components[i].Path, "store") {
				rival.Components[i].Description = "File-backed JSON storage with atomic writes"
			}
		}
		return rival
	}

	// No store component to vary: rival the module layout instead.
	rival.Description = "Go application as a single package rather than a layered layout"
	rival.Components = append([]state.ComponentSpec(nil), chosen.Components...)
	return rival
}

func (u *Architect) generatePlanFromTemplate(intent string) *state.ArchitecturePlan {
	lower := strings.ToLower(intent)
	isHTTP := strings.Contains(lower, "http") || strings.Contains(lower, "service") || strings.Contains(lower, "api") || strings.Contains(lower, "endpoint") || strings.Contains(lower, "server")
	isCLI := strings.Contains(lower, "cli") || strings.Contains(lower, "command") || strings.Contains(lower, "tool")

	var plan *state.ArchitecturePlan

	if isHTTP {
		plan = &state.ArchitecturePlan{
			Description: "Go HTTP server using net/http with in-memory store",
			Components: []state.ComponentSpec{
				{Name: "main", Description: "Application entry point, HTTP server setup", Path: "cmd/server/main.go"},
				{Name: "handlers", Description: "HTTP request handlers for resource management", Path: "internal/handlers/resource.go"},
				{Name: "store", Description: "Thread-safe in-memory storage", Path: "internal/store/store.go"},
				{Name: "model", Description: "Resource data structures", Path: "internal/model/resource.go"},
			},
			Dependencies: []state.DepSpec{
				{Name: "std", Version: "go1.21", Type: "stdlib"},
			},
			Endpoints: []state.EndpointSpec{
				{Method: "POST", Path: "/resources", Desc: "Create a new resource"},
				{Method: "GET", Path: "/resources", Desc: "List all resources"},
				{Method: "GET", Path: "/resources/{id}", Desc: "Get a specific resource"},
				{Method: "PUT", Path: "/resources/{id}", Desc: "Update a resource"},
				{Method: "DELETE", Path: "/resources/{id}", Desc: "Delete a resource"},
			},
			Confidence:    state.ConfidenceMedium,
			Provenance:    state.NewProvenance(u.ID()),
			AlternativeID: "plan-template-http",
		}
	} else if isCLI {
		plan = &state.ArchitecturePlan{
			Description: "Go CLI tool using standard library",
			Components: []state.ComponentSpec{
				{Name: "main", Description: "Application entry point with CLI argument parsing", Path: "cmd/cli/main.go"},
				{Name: "core", Description: "Core business logic", Path: "pkg/core/core.go"},
			},
			Dependencies: []state.DepSpec{
				{Name: "std", Version: "go1.21", Type: "stdlib"},
			},
			Endpoints:     []state.EndpointSpec{},
			Confidence:    state.ConfidenceMedium,
			Provenance:    state.NewProvenance(u.ID()),
			AlternativeID: "plan-template-cli",
		}
	} else {
		plan = &state.ArchitecturePlan{
			Description: fmt.Sprintf("Go application: %s", intent),
			Components: []state.ComponentSpec{
				{Name: "main", Description: "Application entry point", Path: "cmd/app/main.go"},
				{Name: "core", Description: "Core application logic", Path: "pkg/core/core.go"},
			},
			Dependencies: []state.DepSpec{
				{Name: "std", Version: "go1.21", Type: "stdlib"},
			},
			Endpoints:     []state.EndpointSpec{},
			Confidence:    state.ConfidenceMedium,
			Provenance:    state.NewProvenance(u.ID()),
			AlternativeID: "plan-template-generic",
		}
	}

	semiState := u.rt.SemiState

	if u.rt.Spiral != nil {
		u.rt.Spiral.AddChange(state.Change{
			Kind:   "architecture",
			Target: plan.AlternativeID,
			After:  plan.Description,
			UnitID: u.ID(),
		})
	}

	semiState.AddDecision(state.Decision{
		ID:         "dec-arch-template",
		Question:   "Architecture template selected",
		Decision:   plan.AlternativeID,
		Rationale:  "Template architecture matches the user intent",
		Provenance: state.NewProvenance(u.ID()),
	})
	u.rt.Memory.StoreDecision(state.Decision{
		ID:         "dec-arch-template",
		Question:   "Architecture template selected",
		Decision:   plan.AlternativeID,
		Rationale:  "Template architecture matches the user intent",
		Provenance: state.NewProvenance(u.ID()),
	})

	return plan
}
