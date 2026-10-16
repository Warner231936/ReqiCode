package cognition

import (
	"context"
	"strings"
	"testing"
	"time"

	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/obs"
	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/task"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

func mockAgent(t *testing.T, router *llm.Router, mode Mode, req Request) *Agent {
	t.Helper()
	tmp := t.TempDir()

	store, err := memory.NewStore(tmp)
	if err != nil {
		t.Fatalf("memory.NewStore: %v", err)
	}
	tasks, err := task.NewStore(tmp)
	if err != nil {
		t.Fatalf("task.NewStore: %v", err)
	}
	logger, err := obs.New(tmp, false)
	if err != nil {
		t.Fatalf("obs.New: %v", err)
	}

	ws := tools.NewWorkspace(tmp)
	project, _ := verify.Detect(tmp, nil)
	if project == nil {
		project = &verify.Project{Root: tmp, Ecosystem: verify.EcosystemNone}
	}

	policy := safety.NewPolicy(safety.Options{Workspace: tmp, WorkspaceOnly: true})
	reg := tools.NewRegistry()

	deps := &tools.Deps{
		WS:       ws,
		Registry: reg,
		Project:  project,
		Policy:   policy,
		Exec: tools.ExecConfig{
			Workspace:      ws,
			Policy:         policy,
			Approver:       safety.NewAutoApprover(false),
			Timeout:        5 * time.Minute,
			MaxOutputBytes: 256 * 1024,
		},
	}

	ctxMgr, err := agentcontext.New(agentcontext.Options{
		WorkspacePath: tmp,
		Memory:        store,
	})
	if err != nil {
		t.Fatalf("context.New: %v", err)
	}

	ag := NewAgent(router, deps, ctxMgr, store, tasks, logger, safety.NewAutoApprover(false))
	ag.MaxAttempts = req.MaxAttempts
	if ag.MaxAttempts <= 0 {
		ag.MaxAttempts = 3
	}
	ag.Timeout = 60 * time.Second
	ag.MaxToolCalls = 50
	ag.MaxSteps = 50
	return ag
}

func TestModelPlanner_PlanSuccess(t *testing.T) {
	mb := llm.NewMockBackend("planner-test")
	mb.Rule("palindrome", `{"thought":"I'll plan the palindrome checker","action":"plan","plan":[{"description":"Create main.go","files":["cmd/palindrome/main.go"],"verify":"go build ./...","rationale":"Entry point"}]}`)

	router := llm.NewRouter(llm.RouterOptions{Timeout: 30 * time.Second})
	router.AddBackend(mb)
	router.SetRoute(llm.Route{Role: "planner", Backend: "planner-test", Model: "mock-model"})

	ag := mockAgent(t, router, PlanMode, Request{Description: "create a palindrome checker"})
	defer ag.Log.Close()

	req := Request{
		Description:   "create a palindrome checker",
		Mode:          PlanMode,
		MaxAttempts:   1,
		SkipReview:    true,
		NoVerify:      true,
	}

	res, err := ag.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if res.Plan == nil {
		t.Fatal("expected a plan to be generated")
	}

	t.Logf("Plan source: %s, degraded: %v, steps: %d", res.Plan.Source, res.Plan.Degraded, len(res.Plan.Steps))
	for i, s := range res.Plan.Steps {
		t.Logf("Step %d: desc=%q files=%v tool=%q", i+1, s.Description, s.Files, s.Tool)
	}

	if res.Plan.Source != "model" {
		t.Errorf("expected source 'model', got %q", res.Plan.Source)
	}

	if len(res.Plan.Steps) != 1 {
		t.Errorf("expected 1 plan step, got %d (source=%s, degraded=%v)", len(res.Plan.Steps), res.Plan.Source, res.Plan.Degraded)
	}

	if len(res.Plan.Steps) > 0 && res.Plan.Steps[0].Description != "Create main.go" {
		t.Errorf("unexpected step description: %q", res.Plan.Steps[0].Description)
	}
}

func TestHeuristicPlanner_WhenNoModel(t *testing.T) {
	router := llm.NewRouter(llm.RouterOptions{Timeout: 30 * time.Second})
	ag := mockAgent(t, router, PlanMode, Request{Description: "inspect the project"})
	defer ag.Log.Close()

	req := Request{
		Description:   "inspect the project",
		Mode:          PlanMode,
		MaxAttempts:   1,
		SkipReview:    true,
		NoVerify:      true,
	}

	res, err := ag.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if res.Plan == nil {
		t.Fatal("expected a plan from heuristic planner")
	}

	if res.Plan.Source != "heuristic" {
		t.Errorf("expected source 'heuristic', got %q", res.Plan.Source)
	}

	if !res.Plan.Degraded {
		t.Error("expected plan to be degraded when no model available")
	}

	if len(res.Plan.Steps) < 1 {
		t.Error("expected at least 1 plan step")
	}
}

func TestAgent_RunEmptyDescription(t *testing.T) {
	router := llm.NewRouter(llm.RouterOptions{Timeout: 30 * time.Second})
	ag := mockAgent(t, router, Implement, Request{})
	defer ag.Log.Close()

	_, err := ag.Run(context.Background(), Request{Description: ""})
	if err == nil {
		t.Error("expected error for empty description")
	}
	if !strings.Contains(err.Error(), "description is required") {
		t.Errorf("expected 'description is required' error, got: %v", err)
	}
}

func TestAgent_RunPlanModeCompletes(t *testing.T) {
	router := llm.NewRouter(llm.RouterOptions{Timeout: 30 * time.Second})
	ag := mockAgent(t, router, PlanMode, Request{})
	defer ag.Log.Close()

	req := Request{
		Description: "plan a feature",
		Mode:        PlanMode,
		MaxAttempts: 1,
		SkipReview:  true,
		NoVerify:    true,
	}

	res, err := ag.Run(context.Background(), req)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// With no model backend, PlanMode uses the heuristic planner which still
	// produces a degraded plan. The status may be "complete" (plan produced)
	// or "blocked" (execution could not proceed without a model), but either
	// way the plan must exist.
	if res.Plan == nil {
		t.Fatal("expected a plan to be generated")
	}
	if res.Plan.Source != "heuristic" {
		t.Errorf("expected heuristic plan source, got %q", res.Plan.Source)
	}
}

func TestDecision_ValidWithUnknownTool(t *testing.T) {
	reg := tools.NewRegistry()

	d := Decision{
		Thought: "I should search for something",
		Action:  ActionTool,
		Tool:    "search_code",
	}

	err := d.Valid(reg, Implement)
	if err == nil {
		t.Error("expected error for unknown tool")
	}
	if !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' in error, got: %v", err)
	}
}

func TestDecision_ValidNoThought(t *testing.T) {
	reg := tools.NewRegistry()

	d := Decision{
		Action: ActionTool,
		Tool:   "some_tool",
	}

	err := d.Valid(reg, Implement)
	if err == nil {
		t.Error("expected error for missing thought")
	}
	if !strings.Contains(err.Error(), "no thought") {
		t.Errorf("expected 'no thought' error, got: %v", err)
	}
}

func TestParseDecision_ValidJSON(t *testing.T) {
	input := `{"thought":"I need to read a file","action":"tool","tool":"read_file","args":{"path":"main.go"}}`
	d := ParseDecision(input)
	if d.ParseError != "" {
		t.Errorf("unexpected parse error: %s", d.ParseError)
	}
	if d.Thought != "I need to read a file" {
		t.Errorf("unexpected thought: %q", d.Thought)
	}
	if d.Action != ActionTool {
		t.Errorf("expected action 'tool', got %q", d.Action)
	}
	if d.Tool != "read_file" {
		t.Errorf("expected tool 'read_file', got %q", d.Tool)
	}
	if d.Args["path"] != "main.go" {
		t.Errorf("expected path 'main.go', got %v", d.Args["path"])
	}
}

func TestParseDecision_FinishAction(t *testing.T) {
	input := `{"thought":"done","action":"finish","summary":"all complete","evidence":["tests pass"]}`
	d := ParseDecision(input)
	if d.Action != ActionFinish {
		t.Errorf("expected action 'finish', got %q", d.Action)
	}
	if d.Summary != "all complete" {
		t.Errorf("unexpected summary: %q", d.Summary)
	}
	if len(d.Evidence) != 1 {
		t.Errorf("expected 1 evidence item, got %d", len(d.Evidence))
	}
}

func TestParseDecision_InvalidJSON(t *testing.T) {
	d := ParseDecision("this is not JSON")
	if d.ParseError == "" {
		t.Error("expected a parse error for invalid JSON")
	}
}

func TestParseMode(t *testing.T) {
	tests := []struct {
		input    string
		expected Mode
	}{
		{"explore", Explore},
		{"plan", PlanMode},
		{"implement", Implement},
		{"debug", Debug},
		{"test", Test},
		{"review", Review},
		{"refactor", Refactor},
		{"explain", Explain},
	}

	for _, tt := range tests {
		got, err := ParseMode(tt.input)
		if err != nil {
			t.Errorf("ParseMode(%q) returned error: %v", tt.input, err)
		}
		if got != tt.expected {
			t.Errorf("ParseMode(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestParseMode_Invalid(t *testing.T) {
	_, err := ParseMode("nonexistent")
	if err == nil {
		t.Error("expected error for invalid mode")
	}
	if !strings.Contains(err.Error(), "unknown mode") {
		t.Errorf("expected 'unknown mode' error, got: %v", err)
	}
}

func TestParseMode_Empty(t *testing.T) {
	m, err := ParseMode("")
	if err != nil {
		t.Errorf("ParseMode(\"\") returned error: %v", err)
	}
	if m != Explore {
		t.Errorf("expected Explore as default, got %q", m)
	}
}

func TestMode_Spec(t *testing.T) {
	spec := Implement.Spec()
	if !spec.AllowsChanges {
		t.Error("Implement mode should allow changes")
	}
	if !spec.RequiresVerification {
		t.Error("Implement mode should require verification")
	}

	spec = Explore.Spec()
	if spec.AllowsChanges {
		t.Error("Explore mode should not allow changes")
	}
}

func TestResult_OK(t *testing.T) {
	r := Result{
		Status:       string(task.StatusComplete),
		Verified:     true,
		FilesChanged: []string{"main.go"},
		Attempts:     1,
	}
	if !r.OK() {
		t.Error("OK() should return true for complete + verified")
	}
}

func TestResult_OK_NotVerified(t *testing.T) {
	r := Result{
		Status:       string(task.StatusComplete),
		Verified:     false,
		FilesChanged: []string{"main.go"},
		Attempts:     1,
	}
	if r.OK() {
		t.Error("OK() should return false when not verified")
	}
}
