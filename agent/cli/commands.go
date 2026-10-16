package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/config"
	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/cognition"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/obs"
	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/task"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// embedderAdapter bridges the model router to the context manager's embedder.
type embedderAdapter struct {
	router *llm.Router
	// fallback produces vectors without a model, used when no backend supports
	// embedding. It is a lexical hash rather than a semantic embedding and is
	// never described as one.
	fallback func(text string) []float32
	note     string
}

// newEmbedder builds an embedder that prefers a real backend.
func newEmbedder(router *llm.Router) agentcontext.Embedder {
	return &embedderAdapter{
		router:   router,
		fallback: hashEmbed,
		note:     "llm/router embeddings, with a lexical hash fallback when no backend supports embedding",
	}
}

// Embed requests vectors for a role.
func (e *embedderAdapter) Embed(ctx context.Context, inputs []string) ([][]float32, error) {
	vecs, err := e.router.Embed(ctx, config.RoleEmbeddings, inputs)
	if err == nil && len(vecs) == len(inputs) {
		return vecs, nil
	}
	if err != nil && !llm.IsUnsupported(err) && !isNoBackend(err) {
		return nil, err
	}
	// Fall back rather than fail. Retrieval degrades from semantic to lexical,
	// which is a quality reduction the caller already handles.
	out := make([][]float32, len(inputs))
	for i, in := range inputs {
		out[i] = e.fallback(in)
	}
	return out, nil
}

func (e *embedderAdapter) Describe() string { return e.note }

func isNoBackend(err error) bool {
	return err != nil && (strings.Contains(err.Error(), "no available backend") ||
		strings.Contains(err.Error(), "no model available"))
}

// hashEmbed produces a deterministic lexical vector.
//
// It is not a semantic embedding. It gives retrieval a stable similarity signal
// for text that shares vocabulary, which is enough to make the hybrid path
// exercisable when no embedding model is present, and the context manager treats
// it as lexical evidence rather than as meaning.
func hashEmbed(text string) []float32 {
	const dim = 256
	vec := make([]float32, dim)
	fields := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '_' || r == '.' || r == '/' || r == '\\' || r == '-' || r == '+' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, f := range fields {
		h := uint32(2166136261)
		for i := 0; i < len(f); i++ {
			h ^= uint32(f[i])
			h *= 16777619
		}
		vec[h%dim] += 1
		vec[(h>>13)%dim] += 0.5
	}
	var sum float64
	for _, x := range vec {
		sum += float64(x) * float64(x)
	}
	if sum > 0 {
		inv := float32(1 / sqrtApprox(sum))
		for i := range vec {
			vec[i] *= inv
		}
	}
	return vec
}

func sqrtApprox(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 16; i++ {
		x = 0.5 * (x + f/x)
	}
	return x
}

func findExecutable(name string) (string, error) { return exec.LookPath(name) }

func modeTable() string { return cognition.ModeTable() }

// cmdRun executes a full task.
func cmdRun(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	mode := fs.String("mode", "", "mode: explore|plan|implement|debug|test|review|refactor|explain")
	dryRun := fs.Bool("dry-run", false, "plan only; do not allow mutating tools")
	include := fs.String("include", "", "comma-separated paths to force into context")
	resume := fs.String("resume", "", "resume this task id")
	savePlan := fs.String("save-plan", "", "also write the plan to this path")
	fs.Parse(args)

	request := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(request) == "" {
		fmt.Fprintln(env.Stderr, "Error: a request is required, e.g. agent run \"fix the failing test\"")
		return 2
	}

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	m, err := cognition.ParseMode(*mode)
	if err != nil {
		return failWith(env, "mode", err)
	}
	if m == "" {
		m = cognition.Implement
	}

	logger, err := openLogger(b.cfg, optionsFrom(ctx), "run")
	if err != nil {
		fmt.Fprintf(env.Stderr, "warning: could not open a run log: %v\n", err)
	}
	if logger != nil {
		defer logger.Close()
	}

	ag := cognition.NewAgent(b.router, b.tools, b.context, b.memory, b.tasks, logger, b.approver)
	applyAgentOptions(ag, b)

	req := cognition.Request{
		Description: request,
		Mode:        m,
		DryRun:      *dryRun,
		SkipReview:  optionsFrom(ctx).NoReview,
		NoVerify:    optionsFrom(ctx).NoVerify,
		ResumeTaskID: *resume,
		MaxAttempts: optionsFrom(ctx).MaxAttempts,
	}
	if *include != "" {
		for _, p := range strings.Split(*include, ",") {
			if p = strings.TrimSpace(p); p != "" {
				req.MustInclude = append(req.MustInclude, p)
			}
		}
	}

	fmt.Fprintf(env.Stdout, "workspace : %s\n", b.cfg.Workspace)
	fmt.Fprintf(env.Stdout, "mode      : %s\n", m)
	fmt.Fprintf(env.Stdout, "request   : %s\n\n", request)

	res, err := ag.Run(ctx, req)
	if err != nil {
		return failWith(env, "run", err)
	}

	if *savePlan != "" && res.Plan != nil {
		if err := res.Plan.WriteJSON(*savePlan); err != nil {
			fmt.Fprintf(env.Stderr, "warning: could not write the plan: %v\n", err)
		} else {
			fmt.Fprintf(env.Stdout, "plan written to %s\n", *savePlan)
		}
	}
	if res.Plan != nil && res.Plan.Degraded && res.Plan.Source == "heuristic" {
		fmt.Fprintf(env.Stdout, "\n%s\n", res.Plan.Render())
		fmt.Fprintf(env.Stdout,
			"\nNo language model backend is reachable. To make changes, write the intended\n"+
				"edits into a plan file and run:\n  agent apply --plan <file>\n")
	}

	fmt.Fprint(env.Stdout, res.Render())
	writeRunReport(b, res)
	return exitCodeFor(res, res.Degraded)
}

func applyAgentOptions(ag *cognition.Agent, b *bundle) {
	opts := b.cfg.Agent
	ag.MaxAttempts = b.cfg.Limits.MaxAttempts
	ag.MaxSteps = b.cfg.Limits.MaxSteps
	ag.MaxToolCalls = b.cfg.Limits.MaxToolCalls
	ag.RequireEvidence = opts.RequireEvidence
	ag.ReviewEnabled = opts.ReviewEnabled
	ag.ReviewRounds = opts.MaxRepairRounds
	ag.Timeout = b.cfg.CommandTimeout() * 3
}

// cmdPlan produces a plan and writes it.
func cmdPlan(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	out := fs.String("out", "", "write the plan JSON here (default: <memory dir>/plan.json)")
	fs.Parse(args)

	request := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(request) == "" {
		fmt.Fprintln(env.Stderr, "Error: a request is required, e.g. agent plan \"add authentication\"")
		return 2
	}

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	logger, err := openLogger(b.cfg, optionsFrom(ctx), "plan")
	if err == nil && logger != nil {
		defer logger.Close()
	}

	mode, _ := cognition.ParseMode(string(cognition.PlanMode))
	brief, berr := b.context.Build(ctx, request, agentcontext.BriefOptions{IncludeMemory: true})
	if berr != nil {
		brief = &agentcontext.Brief{Request: request}
	}

	var planner cognition.Planner
	if b.router.HasBackends() {
		planner = &cognition.ModelPlanner{
			Router:    b.router,
			Role:      config.RolePlanner,
			Workspace: b.cfg.Workspace,
			Project:   b.tools.Project,
			Catalogue: b.tools.Registry.RenderCatalogue(string(cognition.PlanMode)),
		}
	} else {
		planner = &cognition.HeuristicPlanner{
			Manager: b.context, Project: b.tools.Project,
			Root: b.cfg.Workspace, Store: b.memory,
		}
	}

	plan, perr := planner.Plan(ctx, request, mode, brief)
	if perr != nil {
		return failWith(env, "plan", perr)
	}

	path := *out
	if path == "" {
		path = filepath.Join(b.cfg.Memory.Dir, "plan.json")
	}
	if err := plan.WriteJSON(path); err != nil {
		return failWith(env, "write plan", err)
	}

	fmt.Fprint(env.Stdout, plan.Render())
	fmt.Fprintf(env.Stdout, "\nplan JSON: %s\n", path)
	if plan.Degraded {
		fmt.Fprintf(env.Stdout,
			"\nThis plan has no code in it because no language model is reachable.\n"+
				"Add the intended edits to the JSON and run: agent apply --plan %s\n", path)
		return 3
	}
	return 0
}

// cmdApply executes a plan file.
func cmdApply(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	planPath := fs.String("plan", "", "plan JSON file (default: <memory dir>/plan.json)")
	attempts := fs.Int("attempts", 0, "maximum verification attempts")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	path := *planPath
	if path == "" {
		path = filepath.Join(b.cfg.Memory.Dir, "plan.json")
	}
	if _, statErr := os.Stat(path); statErr != nil {
		fmt.Fprintf(env.Stderr, "Error: no plan file at %s\n", path)
		fmt.Fprintf(env.Stderr, "       create one with: agent plan \"<request>\"\n")
		return 2
	}
	plan, err := cognition.ReadPlanJSON(path)
	if err != nil {
		return failWith(env, "read plan", err)
	}

	logger, err := openLogger(b.cfg, optionsFrom(ctx), "apply")
	if err == nil && logger != nil {
		defer logger.Close()
	}

	ag := cognition.NewAgent(b.router, b.tools, b.context, b.memory, b.tasks, logger, b.approver)
	applyAgentOptions(ag, b)

	if !optionsFrom(ctx).NoReview {
		ag.ReviewEnabled = true
	}

	res, err := ag.ApplyPlan(ctx, plan, cognition.ApplyOptions{
		MaxAttempts: *attempts,
		Review:      ag.ReviewEnabled,
	})
	if err != nil {
		return failWith(env, "apply", err)
	}
	fmt.Fprint(env.Stdout, res.Render())
	if res.OK() {
		return 0
	}
	return 1
}

// cmdExplore runs a read-only investigation.
func cmdExplore(ctx context.Context, env Env, args []string, verb string) int {
	fs := flag.NewFlagSet(verb, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	mode := fs.String("mode", verb, "mode override")
	fs.Parse(args)

	question := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(question) == "" {
		fmt.Fprintf(env.Stderr, "Error: a question is required, e.g. agent %s \"where is auth handled\"\n", verb)
		return 2
	}
	m, err := cognition.ParseMode(*mode)
	if err != nil {
		return failWith(env, "mode", err)
	}

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	logger, lerr := openLogger(b.cfg, optionsFrom(ctx), verb)
	if lerr == nil && logger != nil {
		defer logger.Close()
	}

	ag := cognition.NewAgent(b.router, b.tools, b.context, b.memory, b.tasks, logger, b.approver)
	applyAgentOptions(ag, b)
	ag.ReviewEnabled = false
	ag.Progress = progressWriter(env)

	res, err := ag.Run(ctx, cognition.Request{
		Description: question, Mode: m, SkipReview: true, NoVerify: true,
	})
	if err != nil {
		return failWith(env, verb, err)
	}
	fmt.Fprint(env.Stdout, res.Render())
	if res.Degraded {
		return 3
	}
	return 0
}

// cmdReview reviews the working tree.
func cmdReview(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	rounds := fs.Int("rounds", 1, "review rounds")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)

	diffRes := b.tools.Registry.Invoke(ctx, "git_diff", tools.NewArgs(map[string]any{}))
	diff := diffRes.Stdout
	if !diffRes.Success {
		fmt.Fprintf(env.Stdout, "no diff available: %s\n", diffRes.Error)
		return 0
	}

	statusRes := b.tools.Registry.Invoke(ctx, "git_status", tools.NewArgs(map[string]any{}))
	var changed []string
	for _, line := range strings.Split(statusRes.Stdout, "\n") {
		if len(line) > 3 && !strings.Contains(line, ":") && !strings.HasPrefix(line, "branch") {
			changed = append(changed, strings.TrimSpace(line[3:]))
		}
	}

	reviewer := &cognition.Reviewer{
		Router: b.router, Role: config.RoleReviewer,
		MaxRounds: *rounds, Diff: &cognition.DiffInspector{}, Project: b.tools.Project,
	}
	out := reviewer.Review(ctx, cognition.ReviewRequest{
		OriginalRequest:  firstNonEmptyString(fs.Args(), "the uncommitted working tree changes"),
		Diff:             diff,
		ChangedFiles:     changed,
		Verification:     "not run; review covers the diff only",
		ModelIsAvailable: b.router.HasBackends(),
	})

	fmt.Fprintf(env.Stdout, "\n=== REVIEW ===\n")
	fmt.Fprintf(env.Stdout, "verdict    : %s\n", out.Result.Verdict)
	fmt.Fprintf(env.Stdout, "files      : %d changed\n", len(changed))
	fmt.Fprintf(env.Stdout, "mechanical : %d finding(s)\n", len(out.Mechanical))
	fmt.Fprintf(env.Stdout, "model      : %d finding(s)\n", len(out.Result.Findings)-countOverlap(out))
	if out.Degraded {
		fmt.Fprintf(env.Stdout, "degraded   : %s\n", out.DegradedNote)
	}
	if out.Result.Summary != "" {
		fmt.Fprintf(env.Stdout, "summary    : %s\n", out.Result.Summary)
	}
	for i, f := range out.Result.Findings {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(env.Stdout, "  [%s] %s%s\n", f.Severity, f.Problem, suffixLoc(loc))
		if f.Fix != "" {
			fmt.Fprintf(env.Stdout, "      fix: %s\n", f.Fix)
		}
		if i >= 20 {
			fmt.Fprintf(env.Stdout, "  ... %d more\n", len(out.Result.Findings)-21)
			break
		}
	}
	if out.Accepted {
		return 0
	}
	return 1
}

func countOverlap(out cognition.ReviewOutcome) int {
	n := 0
	for _, f := range out.Result.Findings {
		for _, m := range out.Mechanical {
			if m.Problem == f.Problem {
				n++
				break
			}
		}
	}
	return n
}

func suffixLoc(loc string) string {
	if loc == "" {
		return ""
	}
	return " (" + loc + ")"
}

// cmdTest runs the detected test suite.
func cmdTest(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	filter := fs.String("filter", "", "pass a filter argument to the test runner")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	targs := map[string]any{}
	if *filter != "" {
		targs["filter"] = *filter
	}
	res := b.tools.Registry.Invoke(ctx, "run_tests", tools.NewArgs(targs))
	fmt.Fprint(env.Stdout, res.Stdout)
	if !res.Success {
		fmt.Fprintf(env.Stdout, "\nFAILED: %s\n", res.Error)
		if res.Hint != "" {
			fmt.Fprintf(env.Stdout, "hint: %s\n", res.Hint)
		}
		if d, ok := res.Data["diagnostics"].(string); ok && d != "" {
			fmt.Fprintf(env.Stdout, "\n%s\n", d)
		}
		b.memory.RecordFailure(verifyFailureRecord("run_tests", res))
		_ = b.memory.Save()
		return 1
	}
	fmt.Fprintf(env.Stdout, "\nPASSED\n")
	return 0
}

// cmdInspect reports project structure.
func cmdInspect(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("inspect", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	detail := fs.Bool("full", false, "full detail")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)

	targs := map[string]any{"detail": "summary"}
	if *detail {
		targs["detail"] = "full"
	}
	res := b.tools.Registry.Invoke(ctx, "inspect_project", tools.NewArgs(targs))
	if !res.Success {
		return failWith(env, "inspect", fmt.Errorf("%s", res.Error))
	}
	fmt.Fprint(env.Stdout, res.Stdout)

	if notAGitRepo(b.tools.Project) {
		fmt.Fprintf(env.Stdout, "\nnote: this workspace is not a git repository, so the agent cannot show a diff\n")
		fmt.Fprintf(env.Stdout, "      or distinguish its changes from pre-existing ones. Run 'git init' to enable that.\n")
	}
	return 0
}

// cmdStatus shows the latest task and run counters.
func cmdStatus(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	taskID := fs.String("task", "", "show a specific task")
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)

	var t *task.Task
	if *taskID != "" {
		t, err = b.tasks.Load(*taskID)
		if err != nil {
			return failWith(env, "task", err)
		}
	} else {
		t, err = b.tasks.Latest()
		if err != nil {
			fmt.Fprintln(env.Stdout, "no tasks recorded yet")
			return 0
		}
	}
	fmt.Fprint(env.Stdout, t.RenderSummary())

	if t.Resumable() {
		fmt.Fprintf(env.Stdout, "\nthis task is resumable: agent resume %s\n", t.ID)
	}

	st := b.memory.Stats()
	fmt.Fprintf(env.Stdout, "\nmemory: %d fact(s) (%d stale), %d decision(s), %d unsolved failure(s), %d fix(es)\n",
		st.Facts, st.Stale, st.Decisions, st.Unsolved, st.Fixes)

	if latest, err := latestRun(b); err == nil {
		if summary, err := obs.Summarize(latest); err == nil {
			fmt.Fprintf(env.Stdout, "\n%s\n", summary)
		}
	}
	return 0
}

func latestRun(b *bundle) (string, error) {
	entries, err := os.ReadDir(b.cfg.Logging.Dir)
	if err != nil {
		return "", err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "run-") {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) == 0 {
		return "", os.ErrNotExist
	}
	sortStrings(dirs)
	return filepath.Join(b.cfg.Logging.Dir, dirs[len(dirs)-1]), nil
}

// cmdResume continues an interrupted task.
func cmdResume(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("resume", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	latest := fs.Bool("latest", false, "resume the most recent resumable task")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)
	defer b.memory.Save()

	var t *task.Task
	switch {
	case fs.NArg() > 0:
		t, err = b.tasks.Load(fs.Arg(0))
	case *latest:
		t, err = b.tasks.LatestResumable()
	default:
		t, err = b.tasks.LatestResumable()
	}
	if err != nil {
		fmt.Fprintf(env.Stdout, "no resumable task found (%v)\n", err)
		return 0
	}
	fmt.Fprintf(env.Stdout, "resuming %s: %s\n\n", t.ID, t.Description)

	logger, lerr := openLogger(b.cfg, optionsFrom(ctx), "resume")
	if lerr == nil && logger != nil {
		defer logger.Close()
	}
	ag := cognition.NewAgent(b.router, b.tools, b.context, b.memory, b.tasks, logger, b.approver)
	applyAgentOptions(ag, b)
	ag.Progress = progressWriter(env)

	mode := cognition.Implement
	if t.Mode != "" {
		if parsed, perr := cognition.ParseMode(t.Mode); perr == nil {
			mode = parsed
		}
	}
	res, err := ag.Run(ctx, cognition.Request{
		Description: t.Description, Mode: mode, ResumeTaskID: t.ID,
	})
	if err != nil {
		return failWith(env, "resume", err)
	}
	fmt.Fprint(env.Stdout, res.Render())
	return exitCodeFor(res, res.Degraded)
}

// cmdTasks lists persisted tasks.
func cmdTasks(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("tasks", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	all, err := b.tasks.List()
	if err != nil {
		return failWith(env, "tasks", err)
	}
	if len(all) == 0 {
		fmt.Fprintln(env.Stdout, "no tasks recorded")
		return 0
	}
	fmt.Fprintf(env.Stdout, "%-34s %-11s %-4s %-20s %s\n", "ID", "STATUS", "ATT", "UPDATED", "DESCRIPTION")
	for _, t := range all {
		fmt.Fprintf(env.Stdout, "%-34s %-11s %-4s %-20s %s\n",
			t.ID, t.Status, fmt.Sprintf("%d/%d", t.Attempts, t.MaxAttempts),
			t.UpdatedAt.Format("2006-01-02 15:04"), truncateText(t.Description, 60))
	}
	return 0
}

func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}

// cmdMemory shows or validates project memory.
func cmdMemory(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("memory", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	action := fs.String("action", "show", "show | validate | refresh | stats | doc")
	docName := fs.String("doc", "", "document to read or write (project|architecture|conventions|decisions|tasks|failures)")
	content := fs.String("set", "", "content to write into --doc")
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}

	switch *action {
	case "show":
		fmt.Fprint(env.Stdout, b.memory.Digest(20000, true))
		return 0

	case "validate":
		rep := b.memory.Validate(b.cfg.Workspace)
		fmt.Fprintf(env.Stdout, "memory validity report\n")
		fmt.Fprintf(env.Stdout, "  facts       : %d total, %d fresh, %d stale\n",
			rep.FactsTotal, rep.FactsFresh, rep.FactsStale)
		fmt.Fprintf(env.Stdout, "  profile     : valid=%v", rep.ProfileOK)
		if rep.ProfileNote != "" {
			fmt.Fprintf(env.Stdout, " (%s)", rep.ProfileNote)
		}
		fmt.Fprintf(env.Stdout, "\n")
		if len(rep.MissingDocs) > 0 {
			fmt.Fprintf(env.Stdout, "  missing docs: %s\n", strings.Join(rep.MissingDocs, ", "))
		}
		for _, f := range rep.StaleFacts {
			fmt.Fprintf(env.Stdout, "  stale: [%s] %s -- %s\n", f.Topic, f.Content, f.StaleNote)
		}
		if err := b.memory.Save(); err != nil {
			fmt.Fprintf(env.Stderr, "warning: could not persist staleness markers: %v\n", err)
		}
		return 0

	case "refresh":
		if b.cfg.Memory.StaleAfterDays <= 0 {
			fmt.Fprintln(env.Stdout, "memory.stale_after_days is 0; nothing to refresh")
			return 0
		}
		removed := b.memory.Refresh(time.Duration(b.cfg.Memory.StaleAfterDays) * 24 * time.Hour)
		_ = b.memory.Save()
		fmt.Fprintf(env.Stdout, "removed %d solved or old failure record(s)\n", removed)
		return 0

	case "stats":
		st := b.memory.Stats()
		blob, _ := json.MarshalIndent(st, "", "  ")
		fmt.Fprintln(env.Stdout, string(blob))
		fmt.Fprintf(env.Stdout, "\ndir: %s\n", b.cfg.Memory.Dir)
		return 0

	case "doc":
		if *docName == "" {
			fmt.Fprintln(env.Stderr, "Error: --doc <name> is required")
			return 2
		}
		if *content != "" {
			if _, err := b.memory.WriteDoc(*docName, *content); err != nil {
				return failWith(env, "write doc", err)
			}
			fmt.Fprintf(env.Stdout, "wrote %s.md\n", *docName)
			return 0
		}
		doc, err := b.memory.ReadDoc(*docName)
		if err != nil {
			return failWith(env, "read doc", err)
		}
		if doc.Content == "" {
			fmt.Fprintf(env.Stdout, "(%s.md is empty)\n", *docName)
			return 0
		}
		fmt.Fprint(env.Stdout, doc.Content)
		return 0
	}
	fmt.Fprintf(env.Stderr, "unknown --action %q; use show|validate|refresh|stats|doc\n", *action)
	return 2
}

func firstNonEmptyString(vals []string, fallback string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.Join(vals, " ")
		}
	}
	return fallback
}

// writeRunReport persists a machine-readable report beside the log.
func writeRunReport(b *bundle, res *cognition.Result) {
	if optionsFrom(context.Background()).NoLog {
		return
	}
	dir := b.cfg.Logging.Dir
	if latest, err := latestRun(b); err == nil {
		dir = latest
	}
	blob, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "result.json"), append(blob, '\n'), 0o644)
}

func verifyFailureRecord(source string, res tools.Result) memory.FailureRecord {
	return memory.FailureRecord{
		Kind:    firstNonEmptyString([]string{res.Class}, "test_failure"),
		Summary: fmt.Sprintf("%s: %s", source, firstNonEmptyString([]string{res.Error}, "failed")),
		Context: truncateText(res.Stdout, 1500),
	}
}

func exitCodeFor(res *cognition.Result, degraded bool) int {
	if res.Status == string(task.StatusComplete) && res.Verified {
		return 0
	}
	if degraded {
		return 3
	}
	return 1
}

func failWith(env Env, stage string, err error) int {
	fmt.Fprintf(env.Stderr, "Error (%s): %v\n", stage, err)
	return 2
}

var _ = llm.Route{}
var _ = safety.Classification("")
var _ = verify.EcosystemNone
var _ = runtime.GOOS