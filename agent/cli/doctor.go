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

	"github.com/kilo/spiral-codemaker/agent/config"
	"github.com/kilo/spiral-codemaker/agent/cognition"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// cmdModels inventories local AI and shows routing.
func cmdModels(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("models", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	asJSON := fs.Bool("json", false, "emit the inventory as JSON")
	use := fs.Bool("use", false, "print commands to start a local server for a discovered model")
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	warn(env, b)

	if *asJSON {
		blob, err := json.MarshalIndent(b.inv, "", "  ")
		if err != nil {
			return failWith(env, "json", err)
		}
		fmt.Fprintln(env.Stdout, string(blob))
		return 0
	}

	fmt.Fprint(env.Stdout, b.inv.Format())
	fmt.Fprint(env.Stdout, "\n")
	fmt.Fprint(env.Stdout, b.router.Describe())

	if *use {
		fmt.Fprint(env.Stdout, "\nTo use a local model:\n")
		if best, ok := b.inv.Best(); ok {
			fmt.Fprintf(env.Stdout, "\n  # best chat model found: %s\n", best.Path)
			fmt.Fprintf(env.Stdout, "  %s --model %q --port 8090 --host 127.0.0.1 --n-gpu-layers 999\n",
				firstNonEmptyString([]string{b.cfg.Models.LlamaCppBinary}, "llama-server"), best.Path)
			fmt.Fprintf(env.Stdout,
				"  # then add to agent.json:\n"+
					"  #   {\"name\":\"llamacpp\",\"type\":\"openai\",\"base_url\":\"http://127.0.0.1:8090/v1\"}\n")
		}
		if emb, ok := b.inv.BestEmbedding(); ok {
			fmt.Fprintf(env.Stdout, "\n  # embedding model found: %s\n", emb.Path)
			fmt.Fprintf(env.Stdout, "  %s-embedding --model %q --port 8091\n",
				firstNonEmptyString([]string{b.cfg.Models.LlamaCppBinary}, "llama-server"), emb.Path)
		}
		if _, ollamaUp := hasReachableBackend(b.inv, "ollama"); !ollamaUp {
			fmt.Fprintf(env.Stdout, "\n  # Ollama is installed but not running. Start it with: ollama serve\n")
			if p, lerr := exec.LookPath("ollama"); lerr == nil {
				fmt.Fprintf(env.Stdout, "  # binary: %s\n", p)
			}
		}
	}
	return 0
}

func hasReachableBackend(inv llm.Inventory, name string) (llm.Health, bool) {
	for _, h := range inv.Backends {
		if h.Backend == name && h.Reachable {
			return h, true
		}
	}
	return llm.Health{}, false
}

// cmdDoctor diagnoses the environment.
func cmdDoctor(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Bool("verbose", false, "reserved for future per-check detail")
	fs.Parse(args)

	var (
		pass, fail, warn int
		report           []string
	)
	warnCheck := func(ok bool, name, detail string) {
		if ok {
			pass++
			report = append(report, fmt.Sprintf("  [PASS] %-28s %s", name, detail))
			return
		}
		warn++
		report = append(report, fmt.Sprintf("  [WARN] %-28s %s", name, detail))
	}
	failCheck := func(name, detail string) {
		fail++
		report = append(report, fmt.Sprintf("  [FAIL] %-28s %s", name, detail))
	}

	fmt.Fprintf(env.Stdout, "AGENT DOCTOR\n=============\n\n")

	// Runtime.
	report = append(report, "runtime:")
	warnCheck(true, "go", runtime.Version())
	warnCheck(true, "platform", runtime.GOOS+"/"+runtime.GOARCH)

	// Configuration.
	b, cfgErr := assemble(ctx, env, true)
	if cfgErr != nil {
		failCheck("configuration", cfgErr.Error())
		fmt.Fprint(env.Stdout, strings.Join(report, "\n"))
		fmt.Fprintf(env.Stdout, "\n\n%d passed, %d warning(s), %d failure(s)\n", pass, warn, fail)
		return 1
	}
	fmt.Fprint(env.Stdout, strings.Join(report, "\n"))
	report = report[:0]

	// Filesystem.
	fmt.Fprintf(env.Stdout, "\nfilesystem:\n")
	if werr := probeWritable(b.cfg.Workspace); werr != nil {
		failCheck("workspace writable", werr.Error())
	} else {
		warnCheck(true, "workspace writable", b.cfg.Workspace)
	}
	for _, sub := range []struct{ name, path string }{
		{"memory dir", b.cfg.Memory.Dir},
		{"log dir", b.cfg.Logging.Dir},
	} {
		if err := os.MkdirAll(sub.path, 0o755); err != nil {
			failCheck(sub.name, err.Error())
			continue
		}
		if perr := probeWritable(sub.path); perr != nil {
			failCheck(sub.name, "not writable: "+perr.Error())
		} else {
			warnCheck(true, sub.name, sub.path)
		}
	}
	fmt.Fprint(env.Stdout, strings.Join(report, "\n"))
	report = report[:0]

	// Memory database.
	fmt.Fprintf(env.Stdout, "\nproject memory:\n")
	if _, err := os.Stat(filepath.Join(b.cfg.Memory.Dir, "state.json")); err != nil {
		warnCheck(false, "state file", "not created yet; it is written on the first run")
	} else {
		warnCheck(true, "state file", filepath.Join(b.cfg.Memory.Dir, "state.json"))
	}
	rep := b.memory.Validate(b.cfg.Workspace)
	if rep.FactsStale > 0 {
		warnCheck(false, "freshness", fmt.Sprintf("%d of %d fact(s) are stale and will be excluded from context", rep.FactsStale, rep.FactsTotal))
	} else {
		warnCheck(true, "freshness", fmt.Sprintf("%d fact(s), all fresh", rep.FactsTotal))
	}

	// Git.
	fmt.Fprintf(env.Stdout, "\ngit:\n")
	if b.tools.Project.IsGit {
		out, err := exec.Command("git", "--version").Output()
		ver := strings.TrimSpace(string(out))
		if err != nil {
			failCheck("git", "detected a repository but git is not runnable")
		} else {
			warnCheck(true, "git", ver+" (root "+b.tools.Project.GitRoot+")")
		}
	} else {
		warnCheck(false, "git", "workspace is not a repository; diffs and history are unavailable")
	}

	// Search tools.
	fmt.Fprintf(env.Stdout, "\nsearch:\n")
	warnCheck(true, "native search", "built-in walker (no external dependency)")
	rg, rgErr := findExecutable("rg")
	if rgErr == nil {
		warnCheck(true, "ripgrep (optional)", rg+" (reported to the model as available)")
	} else {
		warnCheck(false, "ripgrep (optional)", "not on PATH; search uses the built-in walker, which is fully functional")
	}

	// Toolchains and test frameworks.
	fmt.Fprintf(env.Stdout, "\ntoolchains:\n")
	for _, tc := range toolchainChecks() {
		warnCheck(tc.present, tc.name, tc.detail)
	}

	// Project detection.
	fmt.Fprintf(env.Stdout, "\nproject:\n")
	warnCheck(true, "ecosystem", string(b.tools.Project.Ecosystem))
	warnCheck(true, "files indexed", fmt.Sprintf("%d file(s)", b.tools.Project.FileCount))
	if len(b.tools.Project.Build) > 0 {
		warnCheck(true, "build command", strings.Join(commandArgs(b.tools.Project.Build), "; "))
	} else {
		warnCheck(false, "build command", "none detected; the agent cannot verify a change")
	}
	if len(b.tools.Project.Test) > 0 {
		warnCheck(true, "test command", strings.Join(commandArgs(b.tools.Project.Test), "; "))
	} else {
		warnCheck(false, "test command", "none detected; the agent cannot verify a change")
	}

	// Safety.
	fmt.Fprintf(env.Stdout, "\nsafety policy:\n")
	warnCheck(b.cfg.Safety.WorkspaceOnly, "workspace boundary", "all writes and commands are confined to the workspace")
	warnCheck(!b.cfg.Safety.AutoApproveConfirm, "confirmation required",
		fmt.Sprintf("auto_approve_confirm=%v", b.cfg.Safety.AutoApproveConfirm))
	blocked := 0
	confirm := 0
	for _, r := range b.policy.Rules() {
		switch r.Class {
		case safety.Blocked:
			blocked++
		case safety.Confirm:
			confirm++
		}
	}
	warnCheck(true, "rules loaded", fmt.Sprintf("%d blocked, %d confirmation-class, plus read-only defaults", blocked, confirm))

	// Model backends.
	fmt.Fprintf(env.Stdout, "\nmodel backends:\n")
	reachable := 0
	if len(b.inv.Backends) == 0 {
		warnCheck(false, "backends configured", "none; the agent will run deterministically without a model")
	} else {
		for _, h := range b.inv.Backends {
			if h.Reachable {
				reachable++
				warnCheck(true, "backend "+h.Backend,
					fmt.Sprintf("kind=%s latency=%s models=%d", h.Kind, h.Latency.Round(1e6), h.ModelCount))
			} else {
				warnCheck(false, "backend "+h.Backend, "unreachable: "+truncateText(h.Detail, 80))
			}
		}
	}
	if len(b.inv.GGUF) > 0 {
		best, _ := b.inv.Best()
		warnCheck(true, "local weights", fmt.Sprintf("%d GGUF file(s); best=%s (%.1f GB)",
			len(b.inv.GGUF), best.Name, float64(best.SizeBytes)/(1<<30)))
	} else {
		warnCheck(false, "local weights", "no GGUF files found in the searched directories")
	}
	if b.cfg.Models.LlamaCppBinary != "" {
		warnCheck(true, "llama-server binary", b.cfg.Models.LlamaCppBinary)
	} else {
		warnCheck(false, "llama-server binary", "not found; Ollama or another configured backend is required")
	}

	// Routing.
	fmt.Fprintf(env.Stdout, "\nmodel routing:\n")
	for _, rt := range b.router.Routes() {
		warnCheck(true, "route "+rt.Role, fmt.Sprintf("%s model=%s temp=%.2f [%s]",
			rt.Backend, firstNonEmptyString([]string{rt.Model}, "-"), rt.Temperature, rt.Source))
	}

	fmt.Fprint(env.Stdout, strings.Join(report, "\n"))

	summary := fmt.Sprintf("\n%d passed, %d warning(s), %d failure(s)\n", pass, warn, fail)
	fmt.Fprint(env.Stdout, summary)

	if reachable == 0 {
		fmt.Fprint(env.Stdout, `
No model backend is reachable. The agent still works: it inspects the project,
searches, plans deterministically, and can apply a plan file you write, verifying
every change against the project's own build and tests. To enable model-driven
work, start a local server (see: agent models --use).
`)
	}
	if fail > 0 {
		return 1
	}
	return 0
}

func commandArgs(cmds []verify.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

type toolchainCheck struct {
	name    string
	present bool
	detail  string
}

func toolchainChecks() []toolchainCheck {
	out := []toolchainCheck{
		binaryCheck("go", "--version"),
		binaryCheck("python", "--version"),
		binaryCheck("node", "--version"),
		binaryCheck("cargo", "--version"),
		binaryCheck("dotnet", "--version"),
		binaryCheck("ollama", "--version"),
		binaryCheck("cmake", "--version"),
	}
	return out
}

func binaryCheck(name string, args ...string) toolchainCheck {
	path, err := exec.LookPath(name)
	if err != nil {
		return toolchainCheck{name: name, present: false,
			detail: "not installed; projects needing it cannot be verified"}
	}
	detail := path
	if out, err := exec.Command(path, args...).Output(); err == nil {
		line := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if line != "" {
			detail = line
		}
	}
	return toolchainCheck{name: name, present: true, detail: detail}
}

// probeWritable verifies a directory can be written to, by writing and removing a
// probe file. Stat alone would not detect a read-only mount or an ACL that permits
// listing but not writing.
func probeWritable(path string) error {
	probe := filepath.Join(path, ".agent-write-probe")
	if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
		return err
	}
	return os.Remove(probe)
}

// newMemoryStore opens the project memory store.
func newMemoryStore(dir string) (*memory.Store, error) { return memory.NewStore(dir) }

// cmdTools lists the tools available in a mode.
func cmdTools(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("tools", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	modeName := fs.String("mode", "implement", "mode whose tools to list")
	asJSON := fs.Bool("json", false, "emit the catalogue as JSON")
	fs.Parse(args)

	b, err := assemble(ctx, env, true)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	m, err := cognition.ParseMode(*modeName)
	if err != nil {
		return failWith(env, "mode", err)
	}

	if *asJSON {
		blob, _ := json.MarshalIndent(b.tools.Registry.CatalogueFor(string(m)), "", "  ")
		fmt.Fprintln(env.Stdout, string(blob))
		return 0
	}
	fmt.Fprintf(env.Stdout, "tools available in %s mode\n", m)
	fmt.Fprintf(env.Stdout, "(* = can modify the workspace)\n\n")
	for _, t := range b.tools.Registry.ForMode(string(m)) {
		marker := " "
		if t.Mutates() {
			marker = "*"
		}
		fmt.Fprintf(env.Stdout, "  %s %-16s %s\n", marker, t.Name(), t.Description())
		var req []string
		for _, p := range t.Params() {
			if p.Required {
				req = append(req, p.Name)
			}
		}
		if len(req) > 0 {
			fmt.Fprintf(env.Stdout, "      requires: %s\n", strings.Join(req, ", "))
		}
	}
	return 0
}

// cmdPolicy shows the command safety rules.
func cmdPolicy(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("policy", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	test := fs.String("test", "", "classify this command and exit")
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}

	if *test != "" {
		v := b.policy.Classify(*test)
		fmt.Fprintf(env.Stdout, "command : %s\n", v.Command)
		fmt.Fprintf(env.Stdout, "class   : %s\n", v.Class)
		fmt.Fprintf(env.Stdout, "rule    : %s\n", v.Rule)
		fmt.Fprintf(env.Stdout, "reason  : %s\n", v.Reason)
		if v.Segment != "" {
			fmt.Fprintf(env.Stdout, "segment : %s\n", v.Segment)
		}
		switch v.Class {
		case safety.Safe:
			return 0
		case safety.Confirm:
			return 3
		default:
			return 1
		}
	}

	fmt.Fprint(env.Stdout, "COMMAND SAFETY POLICY\n=====================\n\n")
	fmt.Fprintf(env.Stdout, "workspace bound : %s\n", b.cfg.Workspace)
	fmt.Fprintf(env.Stdout, "confirmation     : required for %d rule group(s)\n\n", countClass(b.policy, safety.Confirm))
	fmt.Fprint(env.Stdout, "Rules, most severe first:\n\n")
	for _, r := range b.policy.Rules() {
		fmt.Fprintf(env.Stdout, "  %-24s %-26s %s\n", r.Class, r.Rule, r.Reason)
	}
	fmt.Fprintf(env.Stdout, "\nA command not matched by any rule requires confirmation.\n")
	fmt.Fprintf(env.Stdout, "The command line is split on ; && || and | before classification, so a\n")
	fmt.Fprintf(env.Stdout, "safe prefix cannot smuggle a dangerous tail.\n")
	return 0
}

func countClass(p *safety.Policy, c safety.Classification) int {
	n := 0
	for _, r := range p.Rules() {
		if r.Class == c {
			n++
		}
	}
	return n
}

// cmdConfig prints the effective configuration.
func cmdConfig(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("config", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	write := fs.Bool("write", false, "write the effective configuration to agent.json")
	fs.Parse(args)

	b, err := assemble(ctx, env, false)
	if err != nil {
		return failWith(env, "assemble", err)
	}
	blob, err := json.MarshalIndent(b.cfg, "", "  ")
	if err != nil {
		return failWith(env, "json", err)
	}
	if *write {
		if err := os.WriteFile(filepath.Join(b.cfg.Workspace, config.FileName), append(blob, '\n'), 0o644); err != nil {
			return failWith(env, "write config", err)
		}
		fmt.Fprintf(env.Stdout, "wrote %s\n", filepath.Join(b.cfg.Workspace, config.FileName))
		return 0
	}
	fmt.Fprintln(env.Stdout, string(blob))
	return 0
}

// cmdInit creates the agent layout in a workspace.
func cmdInit(ctx context.Context, env Env, args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	force := fs.Bool("force", false, "overwrite an existing agent.json")
	fs.Parse(args)

	ws := optionsFrom(ctx).Workspace
	if ws == "" {
		wd, _ := os.Getwd()
		ws = wd
	}
	cfg, err := config.Load(ws, optionsFrom(ctx).ConfigPath)
	if err != nil {
		return failWith(env, "load config", err)
	}
	cfg.Discover()

	cfgPath := filepath.Join(cfg.Workspace, config.FileName)
	if _, err := os.Stat(cfgPath); err == nil && !*force {
		fmt.Fprintf(env.Stdout, "%s already exists; pass --force to overwrite\n", cfgPath)
	} else if err := cfg.Save(); err != nil {
		return failWith(env, "save config", err)
	} else {
		fmt.Fprintf(env.Stdout, "wrote %s\n", cfgPath)
	}

	store, err := newMemoryStore(cfg.Memory.Dir)
	if err != nil {
		return failWith(env, "memory", err)
	}
	if err := store.EnsureDocs(map[string]string{
		"project.md":      "Project",
		"architecture.md": "Architecture",
		"conventions.md":  "Conventions",
		"decisions.md":    "Decisions",
		"tasks.md":        "Tasks",
		"failures.md":     "Failures",
	}); err != nil {
		return failWith(env, "memory docs", err)
	}
	fmt.Fprintf(env.Stdout, "wrote %s/{%s}\n", cfg.Memory.Dir,
		strings.Join([]string{"project.md", "architecture.md", "conventions.md",
			"decisions.md", "tasks.md", "failures.md", "context", "cache", "logs"}, ", "))

	fmt.Fprintf(env.Stdout, "\nNext:\n  agent doctor\n  agent inspect\n  agent run \"<request>\"\n")
	return 0
}

var _ = tools.ModeExplore