package cognition

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// Phase names a stage of the loop. Used as the phase label on every log event so a
// run can be read back and it is obvious which stage was reached.
type Phase string

const (
	PhaseUnderstand   Phase = "understand"
	PhaseDiscover     Phase = "discover"
	PhaseRetrieve     Phase = "retrieve"
	PhasePlan         Phase = "plan"
	PhaseExecute      Phase = "execute"
	PhaseVerify       Phase = "verify"
	PhaseDiagnose     Phase = "diagnose"
	PhaseFix          Phase = "fix"
	PhaseReview       Phase = "review"
	PhaseFinalise     Phase = "finalise"
)

// PhaseOrder renders the intended loop for documentation.
func PhaseOrder() string {
	return "UNDERSTAND -> DISCOVER -> RETRIEVE -> PLAN -> EXECUTE -> VERIFY -> " +
		"[fail -> DIAGNOSE -> FIX -> VERIFY]* -> REVIEW -> FINALISE"
}

// Request is one run.
type Request struct {
	Description string
	Mode        Mode
	// ResumeTaskID continues a stored task instead of starting a new one.
	ResumeTaskID string
	MaxAttempts  int
	// SkipReview disables the review stage, for a fast inner loop.
	SkipReview bool
	// DryRun plans and reports without allowing mutating tools.
	DryRun bool
	// MustInclude forces these paths into context regardless of ranking.
	MustInclude []string
	// ApproveAll auto-approves confirmation-class commands for this run.
	ApproveAll bool
	// NoVerify skips the verification stage.
	NoVerify bool
}

// Result is a run's outcome.
type Result struct {
	Task           *task.Task          `json:"task"`
	Status         string              `json:"status"`
	Summary        string              `json:"summary"`
	Plan           *Plan               `json:"plan,omitempty"`
	FilesChanged   []string            `json:"files_changed"`
	FilesRead      []string            `json:"files_read"`
	Verification   []verify.RunResult  `json:"verification"`
	Verified       bool                `json:"verified"`
	Review         *ReviewOutcome      `json:"review,omitempty"`
	Attempts       int                 `json:"attempts"`
	ToolCalls      int                 `json:"tool_calls"`
	ModelCalls     int                 `json:"model_calls"`
	Evidence       []string            `json:"evidence"`
	OpenIssues     []string            `json:"open_issues"`
	Degraded       bool                `json:"degraded"`
	DegradedReason string              `json:"degraded_reason,omitempty"`
	Duration       time.Duration       `json:"duration"`
	LogDir         string              `json:"log_dir"`
	Transcript     []Event             `json:"transcript,omitempty"`
}

// OK reports whether the run achieved its objective with evidence.
func (r *Result) OK() bool { return r.Status == string(task.StatusComplete) && r.Verified }

// Render formats a result for the terminal.
func (r *Result) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n=== RESULT ===\n")
	fmt.Fprintf(&b, "status        : %s\n", r.Status)
	if r.Degraded {
		fmt.Fprintf(&b, "degraded      : %s\n", r.DegradedReason)
	}
	fmt.Fprintf(&b, "verified      : %v\n", r.Verified)
	fmt.Fprintf(&b, "attempts      : %d\n", r.Attempts)
	fmt.Fprintf(&b, "tool calls    : %d\n", r.ToolCalls)
	fmt.Fprintf(&b, "model calls   : %d\n", r.ModelCalls)
	fmt.Fprintf(&b, "duration      : %s\n", r.Duration.Round(time.Millisecond))
	if r.LogDir != "" {
		fmt.Fprintf(&b, "log           : %s\n", r.LogDir)
	}
	if len(r.FilesRead) > 0 {
		show := r.FilesRead
		if len(show) > 12 {
			show = show[:12]
		}
		fmt.Fprintf(&b, "files read    : %s", strings.Join(show, ", "))
		if len(r.FilesRead) > len(show) {
			fmt.Fprintf(&b, " (+%d more)", len(r.FilesRead)-len(show))
		}
		b.WriteString("\n")
	}
	if len(r.FilesChanged) > 0 {
		fmt.Fprintf(&b, "files changed : %s\n", strings.Join(r.FilesChanged, ", "))
	} else {
		b.WriteString("files changed : (none)\n")
	}
	if len(r.Verification) > 0 {
		b.WriteString("\nverification:\n")
		for _, line := range strings.Split(verify.Summarize(r.Verification), "\n") {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	if r.Review != nil {
		b.WriteString("\nreview:\n")
		fmt.Fprintf(&b, "  verdict   : %s (round %d)\n", r.Review.Result.Verdict, r.Review.Rounds)
		if r.Review.Degraded {
			fmt.Fprintf(&b, "  degraded  : %s\n", r.Review.DegradedNote)
		}
		if r.Review.Result.Summary != "" {
			fmt.Fprintf(&b, "  summary   : %s\n", truncate(r.Review.Result.Summary, 400))
		}
		for i, f := range r.Review.Result.Findings {
			if i >= 6 {
				fmt.Fprintf(&b, "  ... %d more findings\n", len(r.Review.Result.Findings)-6)
				break
			}
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "  [%s] %s%s\n", f.Severity, f.Problem, locSuffix(loc))
		}
	}
	if len(r.Evidence) > 0 {
		b.WriteString("\nevidence:\n")
		for _, e := range r.Evidence {
			fmt.Fprintf(&b, "  - %s\n", truncate(e, 240))
		}
	}
	if len(r.OpenIssues) > 0 {
		b.WriteString("\nopen issues:\n")
		for _, o := range r.OpenIssues {
			fmt.Fprintf(&b, "  - %s\n", truncate(o, 300))
		}
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "\nsummary: %s\n", r.Summary)
	}
	return b.String()
}

func locSuffix(loc string) string {
	if loc == "" {
		return ""
	}
	return " (" + loc + ")"
}

// Agent runs the loop.
type Agent struct {
	Router    *llm.Router
	Tools     *tools.Deps
	Context   *agentcontext.Manager
	Memory    *memory.Store
	Tasks     *task.Store
	Log       *obs.Logger
	Approver  safety.Approver
	Policy    *safety.Policy
	Embedder  agentcontext.Embedder

	MaxSteps        int
	MaxToolCalls    int
	MaxAttempts     int
	RequireEvidence bool
	ReviewEnabled   bool
	ReviewRounds    int
	Timeout         time.Duration

	// Progress receives human-readable progress lines.
	Progress func(string)
	// FailureHistory supplies recent failure summaries to approval prompts and
	// to the repair prompt, so a repeated approach is visible to both the operator
	// and the model.
	FailureHistory func() []string

	// lastChanged records the paths this run changed. It is kept on the agent
	// rather than derived from git, because a file modified before the run began
	// would otherwise be attributed to the run that reviewed it.
	lastChanged []string
}

// NewAgent builds an agent from assembled dependencies.
func NewAgent(router *llm.Router, deps *tools.Deps, mgr *agentcontext.Manager,
	store *memory.Store, tasks *task.Store, logger *obs.Logger, approver safety.Approver) *Agent {
	a := &Agent{
		Router:          router,
		Tools:           deps,
		Context:         mgr,
		Memory:          store,
		Tasks:           tasks,
		Log:             logger,
		Approver:        approver,
		Policy:          deps.Policy,
		MaxSteps:        60,
		MaxToolCalls:    200,
		MaxAttempts:     3,
		RequireEvidence: true,
		ReviewEnabled:   true,
		ReviewRounds:    2,
		Timeout:         30 * time.Minute,
	}
	a.FailureHistory = func() []string {
		if store == nil {
			return nil
		}
		var out []string
		for _, f := range store.Failures(false) {
			out = append(out, fmt.Sprintf("[%s x%d] %s", f.Kind, f.Occurrences, firstSentence(f.Summary)))
			if len(out) >= 5 {
				break
			}
		}
		return out
	}
	return a
}

func (a *Agent) progress(format string, args ...any) {
	if a.Progress != nil {
		a.Progress(fmt.Sprintf(format, args...))
	}
}

func (a *Agent) modelAvailable() bool {
	return a.Router != nil && a.Router.HasBackends()
}

// Run executes the loop for a request.
func (a *Agent) Run(ctx context.Context, req Request) (*Result, error) {
	if strings.TrimSpace(req.Description) == "" {
		return nil, fmt.Errorf("a request description is required")
	}
	if req.Mode == "" {
		req.Mode = Implement
	}
	if req.MaxAttempts <= 0 {
		req.MaxAttempts = a.MaxAttempts
	}
	if a.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, a.Timeout)
		defer cancel()
	}
	if req.ApproveAll {
		a.Approver = safety.NewAutoApprover(true)
	}

	start := time.Now()
	mode := req.Mode
	if req.DryRun {
		// Dry run is implemented as a mode restriction rather than a separate code
		// path: the same loop runs, and the only difference is that no mutating
		// tool is reachable. A parallel dry-run implementation would drift.
		mode = PlanMode
	}

	t := a.resolveTask(req, mode)

	res := &Result{
		Task: t, Status: string(task.StatusRunning),
		Transcript: []Event{},
	}
	if a.Log != nil {
		a.Log.SetTask(t.ID)
		a.Log.Event(obs.EventRunStart, string(PhaseUnderstand), req.Description,
			map[string]any{"mode": mode, "resume": req.ResumeTaskID, "dry_run": req.DryRun})
	}
	a.progress("task %s: %s (mode=%s)", t.ID, truncate(req.Description, 70), mode)

	t.Status = task.StatusRunning
	t.CurrentStep = 0
	a.save(t)

	// ---- UNDERSTAND + DISCOVER -------------------------------------------
	a.progress("discovering project structure...")
	proj := a.Tools.Project
	a.logInfo(PhaseDiscover, fmt.Sprintf("ecosystem=%s files=%d manifests=%v", proj.Ecosystem, proj.FileCount, proj.Manifests))
	a.recordProfile(proj)

	// ---- RETRIEVE ---------------------------------------------------------
	a.progress("retrieving relevant context...")
	brief, err := contextFor(ctx, a.Context, req.Description, agentcontext.BriefOptions{
		IncludeMemory: true,
		MustInclude:   req.MustInclude,
	})
	if err != nil {
		a.logWarn(PhaseRetrieve, "retrieval failed: %v", err)
		brief = &agentcontext.Brief{Request: req.Description}
	}
	a.progress("context: %d chunk(s), %d byte(s), %d dropped",
		len(strings.Split(brief.Code, "\n--- "))-1, brief.Bytes, brief.Dropped)

	// ---- PLAN -------------------------------------------------------------
	catalogue := toolCatalogue(a.Tools.Registry, mode)
	planner := a.plannerFor(mode)
	plan, perr := planner.Plan(ctx, req.Description, mode, brief)
	if perr != nil {
		a.logFailure(PhasePlan, "planning failed", perr.Error(), nil)
		plan = &Plan{Request: req.Description, Mode: string(mode), Source: "unavailable",
			GeneratedAt: time.Now(), Degraded: true,
			Notes: []string{"planning failed: " + perr.Error()}}
		res.Degraded = true
		res.DegradedReason = perr.Error()
	}
	res.Plan = plan
	if len(plan.Steps) > 0 {
		t.Steps = toTaskSteps(plan.Steps)
	}
	a.save(t)
	a.progress("plan: %d step(s) from %s", len(plan.Steps), plan.Source)

	if plan.Degraded && !a.modelAvailable() {
		res.Degraded = true
		res.DegradedReason = "no language model backend reachable; the run is deterministic"
		a.logWarn(PhasePlan, "running without a language model: %s", res.DegradedReason)
	}

	// ---- EXECUTE / VERIFY / DIAGNOSE loop ----------------------------------
	var (
		verifyResults []verify.RunResult
		verified      bool
		attempts      int
		priorAttempts []string
		// roundBrief is the repair context for the current round. It is scoped to
		// this run rather than held at package level, because a package-level
		// variable would leak one run's failure into the next run in the same
		// process.
		roundBrief string
	)

	maxAttempts := req.MaxAttempts
	if !mode.Spec().AllowsChanges && mode != Review && mode != Explain {
		// Read-only modes have nothing to verify and nothing to retry.
		maxAttempts = 1
	}

	executor := &executor{
		agent:     a,
		mode:      mode,
		request:   req.Description,
		brief:     brief,
		catalogue: catalogue,
		plan:      plan,
		result:    res,
		task:      t,
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		res.Attempts = attempt
		attemptRec := t.BeginAttempt("", "")
		_ = attemptRec

		// Execute.
		a.progress("--- attempt %d/%d: executing ---", attempt, maxAttempts)
		outcome, xerr := executor.run(ctx)
		if xerr != nil {
			a.logFailure(PhaseExecute, "execution error", xerr.Error(), nil)
			t.EndAttempt(false, xerr.Error())
			break
		}
		if outcome == execFinished && mode == PlanMode {
			// PLAN mode's "finish" is the deliverable.
			t.Status = task.StatusComplete
			t.Summary = res.Summary
			t.EndAttempt(true, "plan produced")
			break
		}
		if outcome == execAborted {
			t.Status = task.StatusBlocked
			t.Blockers = appendUnique(t.Blockers, executor.abortReason)
			t.EndAttempt(false, executor.abortReason)
			break
		}

		if req.NoVerify || mode == PlanMode {
			t.Status = task.StatusComplete
			t.Summary = res.Summary
			t.EndAttempt(true, "verification skipped by request")
			break
		}

		// Verify.
		a.progress("--- verifying ---")
		vr := a.verify(ctx, mode)
		verifyResults = append(verifyResults, vr...)
		for _, v := range vr {
			t.AddTest(task.TestOutcome{
				Command: v.Line(), Success: v.Success, ExitCode: v.ExitCode,
				Kind: string(v.Failure.Kind), Duration: v.Duration,
				Summary: truncate(v.Failure.Message, 200),
			})
		}
		res.Verification = verifyResults

		if !verify.HasFailure(vr) {
			verified = true
			t.EndAttempt(true, "verification passed")
			res.Evidence = append(res.Evidence, "verification passed: "+verify.Summarize(vr))
			a.logInfo(PhaseVerify, "verification passed")
			break
		}

		first, _ := verify.FirstFailure(vr)
		diag := verify.ExtractDiagnostics(first.Stdout+first.Stderr, 4000)
		roundBrief = FailureBrief(string(first.Failure.Kind), diag, first.Failure.Advice,
			a.Memory, priorAttempts)

		summary := fmt.Sprintf("%s: %s", first.Line(), first.Failure.Message)
		a.recordFailure(string(first.Failure.Kind), summary, diag)
		priorAttempts = append(priorAttempts, summary)

		t.EndAttempt(false, summary)
		if !first.Failure.Retryable && attempt == 1 && first.Failure.IsHard() {
			// A hard failure will not fix itself, but the repair round is still
			// worth attempting once: the model has not yet seen the error.
			a.progress("repairing: %s", first.Failure.Kind)
		}
		if attempt >= maxAttempts {
			a.logFailure(PhaseDiagnose, "verification still failing after max attempts", summary,
				map[string]any{"attempts": attempt, "kind": string(first.Failure.Kind)})
			break
		}
		a.progress("repair round %d: %s", attempt, first.Failure.Kind)
		executor.newRound(roundBrief)
	}

	res.Verified = verified
	res.ToolCalls = executor.toolCalls
	res.ModelCalls = executor.modelCalls
	res.FilesRead = executor.filesRead

	// ---- REVIEW -----------------------------------------------------------
	if a.shouldReview(req, res, verified) {
		a.progress("--- reviewing ---")
		var prior []Finding
		for r := 1; r <= a.ReviewRounds; r++ {
			diff := a.currentDiff(ctx)
			changed := a.changedFiles(ctx)
			outcomeReview := a.reviewer().Review(ctx, ReviewRequest{
				OriginalRequest:  req.Description,
				Diff:             diff,
				ChangedFiles:     changed,
				Verification:     verify.Summarize(res.Verification),
				PriorFindings:    prior,
				ModelIsAvailable: a.modelAvailable(),
			})
			outcomeReview.Rounds = r
			res.Review = &outcomeReview

			for _, st := range a.Router.Stats() {
				a.recordModelUsage(st)
			}

			if outcomeReview.Accepted || len(res.FilesChanged) == 0 {
				break
			}
			a.logFailure(PhaseReview, "review rejected the change",
				outcomeReview.Result.Summary, map[string]any{"findings": len(outcomeReview.Result.Findings)})

			if r >= a.ReviewRounds || !mode.Spec().AllowsChanges {
				break
			}
			prior = outcomeReview.Result.Blocking()
			if len(prior) == 0 {
				prior = outcomeReview.Result.Findings
			}
			repair := FailureBrief("review_rejected", outcomeReview.Result.Render(), "", a.Memory, priorAttempts)
			executor.newRound(repair)
			if xerr := executor.runOnce(ctx); xerr != nil {
				a.logWarn(PhaseReview, "repair after review failed: %v", xerr)
			}
			vr := a.verify(ctx, mode)
			res.Verification = append(res.Verification, vr...)
			for _, v := range vr {
				t.AddTest(task.TestOutcome{Command: v.Line(), Success: v.Success,
					ExitCode: v.ExitCode, Kind: string(v.Failure.Kind), Duration: v.Duration})
			}
			res.Verified = !verify.HasFailure(vr)
		}
		t.ReviewAccepted = res.Review != nil && res.Review.Accepted
	}

	// ---- FINALISE ---------------------------------------------------------
	res.Status = a.finalStatus(t, res, verified, req)
	t.Status = task.Status(res.Status)
	now := time.Now()
	t.FinishedAt = &now
	t.Summary = res.Summary
	t.Evidence = res.Evidence
	t.Attempts = attempts
	a.recordFinalMemory(req, res, verified)
	a.save(t)

	res.Duration = time.Since(start)
	if a.Log != nil {
		a.logInfo(PhaseFinalise, "status="+res.Status, map[string]any{
			"verified": verified, "attempts": attempts,
			"files_changed": len(res.FilesChanged), "degraded": res.Degraded,
		})
	}
	a.progress("done: %s (verified=%v, %s)", res.Status, verified, res.Duration.Round(time.Millisecond))
	return res, nil
}

func (a *Agent) finalStatus(t *task.Task, res *Result, verified bool, req Request) string {
	switch {
	case res.Review != nil && !res.Review.Accepted && len(res.FilesChanged) > 0:
		return string(task.StatusFailed)
	case verified:
		return string(task.StatusComplete)
	case len(t.Blockers) > 0:
		return string(task.StatusBlocked)
	case len(res.FilesChanged) > 0:
		// Files were changed but nothing proved them correct. Reporting this as
		// failed is the whole point: an unverified change is not a completed task.
		return string(task.StatusFailed)
	default:
		return string(task.StatusComplete)
	}
}

func (a *Agent) shouldReview(req Request, res *Result, verified bool) bool {
	if req.SkipReview || !a.ReviewEnabled {
		return false
	}
	if len(res.FilesChanged) == 0 {
		// Nothing changed, so there is nothing to review. Reporting a rejection
		// here would send the loop into a repair round with no edit to make.
		return false
	}
	return true
}

func (a *Agent) plannerFor(mode Mode) Planner {
	modelPlanner := &ModelPlanner{
		Router:    a.Router,
		Role:      roleForPlan(mode),
		Workspace: a.Tools.WS.Root,
		Project:   a.Tools.Project,
		Catalogue: toolCatalogue(a.Tools.Registry, mode),
	}
	if a.modelAvailable() {
		return modelPlanner
	}
	return &HeuristicPlanner{
		Manager: a.Context,
		Project: a.Tools.Project,
		Root:    a.Tools.WS.Root,
		Store:   a.Memory,
	}
}

func roleForPlan(mode Mode) string {
	switch mode {
	case PlanMode, Explore, Explain:
		return "planner"
	case Debug:
		return "debugger"
	case Review:
		return "reviewer"
	default:
		return "planner"
	}
}

func (a *Agent) verify(ctx context.Context, mode Mode) []verify.RunResult {
	if !a.Tools.Project.Has("build") && !a.Tools.Project.Has("test") {
		a.logWarn(PhaseVerify, "no build or test command is available for this project, so nothing can be verified")
		return nil
	}
	runner := &verify.Runner{
		Project:  a.Tools.Project,
		Timeout:  a.Tools.Exec.Timeout,
		MaxBytes: a.Tools.Exec.MaxOutputBytes,
	}
	var kinds []string
	switch mode {
	case Test:
		kinds = []string{"test"}
	case Review:
		kinds = []string{"build", "test"}
	default:
		kinds = []string{"build", "test"}
	}
	results := runner.RunAll(ctx, kinds...)
	for _, r := range results {
		ev := obs.EventVerification
		if !r.Success && r.Skipped == "" {
			a.logFailure(PhaseVerify, r.Line()+" -> "+string(r.Failure.Kind),
				verify.ExtractDiagnostics(r.Stdout+r.Stderr, 2000),
				map[string]any{"exit_code": r.ExitCode})
		} else {
			a.logInfo(PhaseVerify, r.Line()+" -> ok")
		}
		a.Memory.RecordCommand(memory.CommandRecord{
			Command: r.Line(), ExitCode: r.ExitCode,
			Outcome: outcomeWord(r), UseCase: "verification",
		})
		_ = ev
	}
	return results
}

func outcomeWord(r verify.RunResult) string {
	switch {
	case r.Skipped != "":
		return "skipped"
	case r.Success:
		return "success"
	default:
		return "failure: " + string(r.Failure.Kind)
	}
}

func (a *Agent) resolveTask(req Request, mode Mode) *task.Task {
	if req.ResumeTaskID != "" && a.Tasks != nil {
		if t, err := a.Tasks.Load(req.ResumeTaskID); err == nil {
			a.progress("resuming task %s (was %s)", t.ID, t.Status)
			return t
		}
	}
	t := task.NewTask(req.Description, string(mode), req.MaxAttempts)
	t.Context = task.ContextSnapshot{
		Ecosystem: string(a.Tools.Project.Ecosystem),
		Files:     a.Tools.Project.Manifests,
	}
	for _, c := range a.Tools.Project.Build {
		t.Context.BuildCmd = append(t.Context.BuildCmd, strings.Join(c.Args, " "))
	}
	for _, c := range a.Tools.Project.Test {
		t.Context.TestCmd = append(t.Context.TestCmd, strings.Join(c.Args, " "))
	}
	return t
}

func toTaskSteps(steps []PlanStep) []task.Step {
	out := make([]task.Step, 0, len(steps))
	for _, s := range steps {
		out = append(out, task.Step{
			ID:          s.ID,
			Description: s.Description,
			Tool:        s.Tool,
			Status:      task.StepPending,
		})
	}
	return out
}

// currentDiff returns the working tree diff, or an empty string.
func (a *Agent) currentDiff(ctx context.Context) string {
	reg := a.Tools.Registry
	res := reg.Invoke(ctx, "git_diff", tools.NewArgs(map[string]any{}))
	if res.Success {
		return res.Stdout
	}
	return ""
}

// changedFiles returns the paths the task reports as changed.
func (a *Agent) changedFiles(ctx context.Context) []string {
	return a.lastChanged
}

func (a *Agent) recordProfile(p *verify.Project) {
	if a.Memory == nil {
		return
	}
	prof := memory.ProjectProfile{
		Name:       filepathBase(p.Root),
		Root:       p.Root,
		Ecosystem:  string(p.Ecosystem),
		Languages:  p.Languages,
		ObservedAt: time.Now(),
		Valid:      true,
	}
	for _, c := range p.Build {
		prof.BuildCmd = append(prof.BuildCmd, strings.Join(c.Args, " "))
	}
	for _, c := range p.Test {
		prof.TestCmd = append(prof.TestCmd, strings.Join(c.Args, " "))
	}
	prof.EntryPoints = p.EntryPoints
	a.Memory.SetProfile(prof)
	a.Memory.AddFact(memory.Fact{
		Topic:   "verification commands",
		Content: fmt.Sprintf("build=%v test=%v", prof.BuildCmd, prof.TestCmd),
		Source:  "project detection",
	})
}

func (a *Agent) recordFailure(kind, summary, diagnostics string) {
	if a.Memory == nil {
		return
	}
	a.Memory.RecordFailure(memory.FailureRecord{
		Command: "", Kind: kind, Summary: summary, Context: truncate(diagnostics, 1500),
	})
}

func (a *Agent) recordModelUsage(st llm.RoleStats) {
	if a.Memory == nil {
		return
	}
	a.Memory.RecordModel(memory.ModelRecord{
		Role: st.Role, Backend: st.Backend, Model: st.Model,
		Calls: st.Calls, Failures: st.Failures,
	})
}

func (a *Agent) recordFinalMemory(req Request, res *Result, verified bool) {
	if a.Memory == nil {
		return
	}
	for _, f := range res.FilesChanged {
		a.Memory.AddFact(memory.Fact{
			Topic:    "recently changed file",
			Content:  fmt.Sprintf("modified while working on: %s", truncate(req.Description, 120)),
			Source:   f,
		})
	}
	if verified {
		a.Memory.RecordFix(memory.Fix{
			Files: res.FilesChanged, Summary: "verified change for: " + truncate(req.Description, 140),
			Verified: true, Evidence: verify.Summarize(res.Verification),
		})
	}
	a.Memory.RecordDecision(memory.Decision{
		Question:  "verification command for " + string(a.Tools.Project.Ecosystem),
		Decision:  strings.Join(firstCommand(a.Tools.Project.Test), " "),
		Reasoning: "detected from the project's own manifest and scripts",
	})
	if res.Plan != nil && len(res.Plan.Steps) > 0 {
		if _, werr := a.Memory.WriteDoc("tasks.md", appendTaskSection(a.Memory, req, res)); werr != nil {
			a.logWarn(PhaseFinalise, "could not record the task in memory: %v", werr)
		}
	}
}

func firstCommand(cmds []verify.Command) []string {
	if len(cmds) == 0 {
		return []string{"(none detected)"}
	}
	return cmds[0].Args
}

func appendTaskSection(store *memory.Store, req Request, res *Result) string {
	doc, err := store.ReadDoc("tasks.md")
	if err != nil {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n## %s — %s\n", res.Status, truncate(req.Description, 100))
	if res.Plan != nil {
		fmt.Fprintf(&b, "- plan: %d step(s) from %s\n", len(res.Plan.Steps), res.Plan.Source)
	}
	if len(res.FilesChanged) > 0 {
		fmt.Fprintf(&b, "- changed: %s\n", strings.Join(res.FilesChanged, ", "))
	}
	fmt.Fprintf(&b, "- verified: %v\n", res.Verified)
	if len(res.Verification) > 0 {
		fmt.Fprintf(&b, "- verification: %s\n", strings.TrimSpace(strings.ReplaceAll(verify.Summarize(res.Verification), "\n", " | ")))
	}
	if len(res.OpenIssues) > 0 {
		fmt.Fprintf(&b, "- open issues:\n")
		for _, o := range res.OpenIssues {
			fmt.Fprintf(&b, "  - %s\n", truncate(o, 200))
		}
	}
	return doc.Content + b.String()
}

func appendUnique(list []string, v string) []string {
	if v == "" {
		return list
	}
	for _, s := range list {
		if s == v {
			return list
		}
	}
	return append(list, v)
}

func filepathBase(p string) string {
	i := strings.LastIndexAny(p, `/\`)
	if i < 0 {
		return p
	}
	return p[i+1:]
}

func (a *Agent) save(t *task.Task) {
	if a.Tasks == nil {
		return
	}
	if err := a.Tasks.Save(t); err != nil {
		a.logWarn(PhaseFinalise, "could not persist task state: %v", err)
	}
	a.Memory.UpsertTask(memory.TaskRef{
		ID: t.ID, Description: t.Description, Status: string(t.Status),
		UpdatedAt: time.Now(),
	})
	if a.Memory != nil {
		_ = a.Memory.Save()
	}
}

func (a *Agent) logInfo(phase Phase, msg string, fields ...map[string]any) {
	if a.Log == nil {
		return
	}
	a.Log.Info(string(phase), "%s", msg)
	for _, f := range fields {
		if len(f) > 0 {
			a.Log.Event(obs.EventInfo, string(phase), msg, f)
		}
	}
}

func (a *Agent) logWarn(phase Phase, format string, args ...any) {
	if a.Log == nil {
		return
	}
	a.Log.Warn(string(phase), format, args...)
}

func (a *Agent) logFailure(phase Phase, summary, detail string, fields map[string]any) {
	if a.Log == nil {
		return
	}
	a.Log.Failure(string(phase), summary, detail, fields)
}

func (a *Agent) reviewer() *Reviewer {
	return &Reviewer{
		Router:    a.Router,
		Role:      "reviewer",
		MaxRounds: a.ReviewRounds,
		Diff:      &DiffInspector{},
		Project:   a.Tools.Project,
	}
}

var _ = os.Getenv
var _ = contextFor