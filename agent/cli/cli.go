// Package cli implements the agent's command-line interface.
//
// Every command assembles the same dependency set from one place, assemble(), so
// that `agent models`, `agent doctor`, and `agent run` cannot disagree about which
// workspace, config, memory, or backend they are operating on.
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/config"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/obs"
	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/task"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// Env is the process environment the CLI operates in.
//
// Injected rather than read from globals so the CLI can be tested without
// touching the real terminal, the real working directory, or the real HOME.
type Env struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	Getenv func(string) string
	Args   []string
	// Interactive controls whether confirmation prompts are asked.
	Interactive bool
}

// bundle is the assembled dependency set.
type bundle struct {
	cfg     *config.Config
	router  *llm.Router
	inv     llm.Inventory
	warn    []string
	tools   *tools.Deps
	context *agentcontext.Manager
	memory  *memory.Store
	tasks   *task.Store
	policy  *safety.Policy
	approver safety.Approver
	logger  *obs.Logger
	embedder agentcontext.Embedder
}

// Options are the flags shared by every command.
type Options struct {
	Workspace   string
	ConfigPath  string
	Verbose     bool
	AutoYes     bool
	NoReview    bool
	NoVerify    bool
	ScanModels  bool
	TimeoutSec  int
	MaxAttempts int
	NoLog       bool
}

type optionsKey struct{}

// WithOptions attaches options to a context, so command functions receive them
// without threading an extra parameter through the dispatcher.
func WithOptions(ctx context.Context, o Options) context.Context {
	return context.WithValue(ctx, optionsKey{}, o)
}

func optionsFrom(ctx context.Context) Options {
	if o, ok := ctx.Value(optionsKey{}).(Options); ok {
		return o
	}
	return Options{}
}

// Run dispatches a CLI invocation and returns a process exit code.
func Run(ctx context.Context, env Env) int {
	if len(env.Args) == 0 {
		printUsage(env.Stdout)
		return 0
	}

	// Help is intercepted before flag parsing. The flag package would otherwise
	// print its own generated usage on --help, which lists internal flag names
	// rather than the commands an operator actually wants.
	for _, a := range env.Args {
		if a == "--help" || a == "-help" || a == "-h" {
			printUsage(env.Stdout)
			return 0
		}
	}

	// Global flags are parsed first so every command accepts them regardless of
	// position, which is what makes `agent --verbose run "fix"` work.
	fs := flag.NewFlagSet("agent", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	var opts Options
	fs.StringVar(&opts.Workspace, "workspace", "", "workspace directory (default: current directory)")
	fs.StringVar(&opts.ConfigPath, "config", "", "path to agent.json")
	fs.BoolVar(&opts.Verbose, "verbose", false, "verbose output")
	fs.BoolVar(&opts.AutoYes, "yes", false, "auto-approve confirmation-class commands (unsafe unattended)")
	fs.BoolVar(&opts.NoReview, "no-review", false, "skip the review stage")
	fs.BoolVar(&opts.NoVerify, "no-verify", false, "skip verification (not recommended)")
	fs.BoolVar(&opts.ScanModels, "scan", true, "scan for local models at startup")
	fs.IntVar(&opts.TimeoutSec, "timeout", 0, "overall run timeout in seconds")
	fs.IntVar(&opts.MaxAttempts, "attempts", 0, "maximum repair attempts")
	fs.BoolVar(&opts.NoLog, "no-log", false, "do not write a run log")

	if err := fs.Parse(env.Args); err != nil {
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 {
		printUsage(env.Stdout)
		return 0
	}

	optsCtx := WithOptions(ctx, opts)
	cmd := rest[0]
	args := rest[1:]

	switch cmd {
	case "run", "do":
		return cmdRun(optsCtx, env, args)
	case "plan":
		return cmdPlan(optsCtx, env, args)
	case "apply":
		return cmdApply(optsCtx, env, args)
	case "explore", "explain":
		return cmdExplore(optsCtx, env, args, cmd)
	case "review":
		return cmdReview(optsCtx, env, args)
	case "test":
		return cmdTest(optsCtx, env, args)
	case "inspect":
		return cmdInspect(optsCtx, env, args)
	case "status":
		return cmdStatus(optsCtx, env, args)
	case "resume":
		return cmdResume(optsCtx, env, args)
	case "tasks":
		return cmdTasks(optsCtx, env, args)
	case "memory":
		return cmdMemory(optsCtx, env, args)
	case "models":
		return cmdModels(optsCtx, env, args)
	case "doctor":
		return cmdDoctor(optsCtx, env, args)
	case "modes":
		fmt.Fprint(env.Stdout, modeTable())
		return 0
	case "tools":
		return cmdTools(optsCtx, env, args)
	case "policy":
		return cmdPolicy(optsCtx, env, args)
	case "config":
		return cmdConfig(optsCtx, env, args)
	case "init":
		return cmdInit(optsCtx, env, args)
	case "help", "--help", "-h":
		printUsage(env.Stdout)
		return 0
	case "version":
		fmt.Fprintf(env.Stdout, "agent %s\n", Version)
		return 0
	default:
		fmt.Fprintf(env.Stderr, "unknown command %q\n\n", cmd)
		printUsage(env.Stderr)
		return 2
	}
}

// Version is the agent version.
const Version = "1.0.0"

func printUsage(w io.Writer) {
	fmt.Fprint(w, `agent - local-first autonomous coding agent

Usage:
  agent <command> [flags] [arguments]

Doing work:
  run "<request>"          Plan, execute, verify, review, and iterate until the
                          change is proven by the project's own build and tests.
  plan "<request>"        Produce a plan and write it to .agent/plan.json
  apply [--plan FILE]     Execute a plan file's edits and verify them
  explore "<question>"    Locate relevant code without changing anything
  review                  Review the current diff for regressions
  test                    Run the detected test suite

Information:
  inspect                 Report project structure and verification commands
  status                  Show the latest task and run counters
  tasks                   List persisted tasks
  resume [id]             Resume an interrupted task
  memory [show|validate]  Show or re-check project memory
  models                  Inventory local models and show routing
  doctor                  Diagnose the environment
  tools [mode]            List tools available in a mode
  policy                  Show the command safety rules
  modes                   Show agent modes
  config                  Print the effective configuration
  init                    Create agent.json and .agent/ in this workspace

Global flags:
  --workspace <dir>   Workspace directory (default: current directory)
  --config <file>     Configuration file (default: <workspace>/agent.json)
  --verbose           Verbose output
  --yes               Auto-approve confirmation-class commands (unsafe unattended)
  --no-review         Skip the review stage
  --no-verify         Skip verification (not recommended)
  --timeout <sec>     Overall run timeout
  --attempts <n>      Maximum repair attempts
  --scan=true|false   Scan for local models at startup
  --no-log            Do not write a run log

Exit codes:
  0 success   1 failure   2 usage error   3 degraded (ran without a model)

Everything is local: local models, local tools, local memory, local project.
No paid API is required or contacted.
`)
}

// assemble builds the dependency set for a command.
func assemble(ctx context.Context, env Env, needTools bool) (*bundle, error) {
	opts := optionsFrom(ctx)

	ws := opts.Workspace
	if ws == "" {
		wd, err := os.Getwd()
		if err != nil {
			return nil, err
		}
		ws = wd
	}

	cfg, err := config.Load(ws, opts.ConfigPath)
	if err != nil {
		return nil, err
	}
	cfg.Discover()
	if opts.MaxAttempts > 0 {
		cfg.Agent.MaxRepairRounds = opts.MaxAttempts
		cfg.Limits.MaxAttempts = opts.MaxAttempts
	}
	if opts.TimeoutSec > 0 {
		cfg.Limits.CommandTimeout = opts.TimeoutSec
		cfg.Safety.TimeoutSec = opts.TimeoutSec
	}

	b := &bundle{cfg: cfg}

	router, inv, warn, err := llm.BuildRouter(ctx, cfg, opts.ScanModels)
	if err != nil {
		return nil, err
	}
	b.router = router
	b.inv = inv
	b.warn = warn

	policy := safety.NewPolicy(safety.Options{
		Workspace:     cfg.Workspace,
		WorkspaceOnly: cfg.Safety.WorkspaceOnly,
		AllowNetwork:  cfg.Safety.AllowNetwork,
	})
	b.policy = policy

	var approver safety.Approver
	if opts.AutoYes {
		approver = safety.NewAutoApprover(true)
	} else if env.Interactive {
		approver = &safety.ChainApprover{Approvers: []safety.Approver{
			safety.NewInteractiveApprover(), safety.NewAutoApprover(false),
		}}
	} else {
		approver = safety.NewAutoApprover(false)
	}
	b.approver = approver

	store, err := memory.NewStore(cfg.Memory.Dir)
	if err != nil {
		return nil, err
	}
	b.memory = store
	if err := store.EnsureDocs(nil); err != nil {
		return nil, err
	}

	tasks, err := task.NewStore(filepath.Join(cfg.Memory.Dir, "tasks"))
	if err != nil {
		return nil, err
	}
	b.tasks = tasks

	if needTools {
		deps, err := tools.New(tools.Options{
			WorkspacePath:  cfg.Workspace,
			IgnoreGlobs:    cfg.Context.IgnoreGlobs,
			Policy:         policy,
			Approver:       approver,
			Timeout:        cfg.CommandTimeout(),
			MaxOutputBytes: cfg.Safety.MaxOutputBytes,
			MaxReadBytes:   cfg.Context.MaxFileBytes,
			MaxResults:     cfg.Context.MaxSearchResults,
			RipgrepPath:    findRipgrep(env),
		})
		if err != nil {
			return nil, err
		}
		deps.Exec.History = func() []string {
			var out []string
			for _, f := range store.Failures(false) {
				out = append(out, fmt.Sprintf("[%s x%d] %s", f.Kind, f.Occurrences, f.Summary))
				if len(out) >= 5 {
					break
				}
			}
			return out
		}
		b.tools = deps

		var embedder agentcontext.Embedder
		if cfg.Context.UseEmbeddings {
			embedder = newEmbedder(router)
		}
		b.embedder = embedder

		mgr, err := agentcontext.New(agentcontext.Options{
			WorkspacePath: cfg.Workspace,
			IgnoreGlobs:   cfg.Context.IgnoreGlobs,
			MaxFileBytes:  cfg.Context.MaxFileBytes,
			MaxFiles:      4000,
			MaxResults:    cfg.Context.MaxFiles,
			IncludeTests:  cfg.Context.IncludeTests,
			MaxContextKB:  cfg.Memory.MaxContextKB,
			Embedder:      embedder,
			Memory:        store,
		})
		if err != nil {
			return nil, err
		}
		b.context = mgr
	}

	return b, nil
}

// openLogger creates a run log directory.
func openLogger(cfg *config.Config, opts Options, label string) (*obs.Logger, error) {
	if opts.NoLog {
		return nil, nil
	}
	dir := obs.RunDir(cfg.Logging.Dir, time.Now())
	logger, err := obs.New(dir, opts.Verbose)
	if err != nil {
		return nil, err
	}
	if _, perr := obs.Prune(cfg.Logging.Dir, cfg.Logging.KeepRuns); perr != nil {
		logger.Warn("start", "could not prune old run logs: %v", perr)
	}
	logger.Info("start", "run label: %s", label)
	return logger, nil
}

// findRipgrep locates ripgrep for reporting only.
func findRipgrep(env Env) string {
	for _, name := range []string{"rg"} {
		if p, err := findExecutable(name); err == nil {
			return p
		}
	}
	return ""
}

// progressWriter routes progress lines to stderr so stdout stays parseable.
func progressWriter(env Env) func(string) {
	var last string
	return func(msg string) {
		if msg == last {
			return
		}
		last = msg
		fmt.Fprintf(env.Stderr, "  %s\n", msg)
	}
}

// warn prints discovery warnings.
func warn(env Env, b *bundle) {
	for _, w := range b.warn {
		if w == "" {
			continue
		}
		fmt.Fprintf(env.Stderr, "warning: %s\n", w)
	}
}

// notAGitRepo reports whether the workspace is outside version control, which
// changes how much a run can claim about its own changes.
func notAGitRepo(p *verify.Project) bool { return !p.IsGit }

func humanDuration(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	return d.Round(100 * time.Millisecond).String()
}

func truncateText(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}