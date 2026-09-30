package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/api"
	"github.com/kilo/spiral-codemaker/core/canary"
	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/execution/tests"
	"github.com/kilo/spiral-codemaker/models/registry"
	"github.com/kilo/spiral-codemaker/models/routing"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(0)
	}

	cmd := os.Args[1]
	args := os.Args[2:]

	switch cmd {
	case "run":
		cmdRun(args)
	case "baseline":
		cmdBaseline(args)
	case "canary":
		cmdCanary(args)
	case "dashboard":
		cmdDashboard(args)
	case "daemon":
		cmdDaemon(args)
	case "setup":
		cmdSetup(args)
	case "models":
		cmdModels(args)
	case "scan":
		cmdScan(args)
	case "hf-search":
		cmdHFSearch(args)
	case "help", "--help", "-h":
		printUsage()
	case "version":
		fmt.Println("Spiral CodeMaker v0.2.0")
	default:
		fmt.Printf("Unknown command: %s\n\n", cmd)
		printUsage()
		os.Exit(1)
	}
}

// cmdBaseline captures and checks regression baselines.
//
// Capture is deliberately an explicit CLI verb rather than something a run does
// automatically. Auto-capturing would silently promote every behaviour change to
// "expected", which is exactly how a gate stops gating.
func cmdBaseline(args []string) {
	fs := flag.NewFlagSet("baseline", flag.ExitOnError)
	action := fs.String("action", "check", "capture | check")
	dir := fs.String("dir", "testdata/baseline", "baseline directory")
	scenario := fs.String("scenario", "http-todo", "scenario name (http-todo | http-resource | cli-filelist)")
	fs.Parse(args)

	gate := regression.NewGate(*dir)

	switch *action {
	case "capture":
		ss := buildScenarioState(*scenario)
		proj := regression.Project(*scenario, ss)
		if err := gate.Capture(proj); err != nil {
			fmt.Printf("Error capturing baseline: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Captured baseline %q: %d files, %d decisions, %d test outcomes -> %s\n",
			*scenario, len(proj.Files), len(proj.Decisions), len(proj.TestOutcomes), gate.Path(*scenario))
		fmt.Println("This is now the expected behaviour. Review the diff before committing.")

	case "check":
		ss := buildScenarioState(*scenario)
		proj := regression.Project(*scenario, ss)
		verdict := gate.Check(proj)
		fmt.Print(regression.FormatVerdicts([]regression.Verdict{verdict}))
		if !verdict.Pass {
			os.Exit(1)
		}

	default:
		fmt.Println("unknown action; use capture or check")
		os.Exit(1)
	}
}

// buildScenarioState runs the deterministic template pipeline for a scenario,
// with no LLM attached so its projection is stable enough to pin.
func buildScenarioState(scenario string) *state.SemiState {
	dir, err := os.MkdirTemp("", "spiral-baseline-*")
	if err != nil {
		fmt.Printf("Error creating temp dir: %v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)

	orch, err := system.NewOrchestrator("", dir, scenarioIntent(scenario), 1)
	if err != nil {
		fmt.Printf("Error creating orchestrator: %v\n", err)
		os.Exit(1)
	}
	if err := orch.Run(); err != nil {
		fmt.Printf("Error running orchestrator: %v\n", err)
		os.Exit(1)
	}
	return orch.SemiState()
}

func scenarioIntent(scenario string) string {
	switch scenario {
	case "cli-filelist":
		return "Create a Go CLI tool that lists files in a directory recursively"
	case "http-resource":
		return "Create a Go HTTP service with REST API for managing resources"
	default:
		return "Build a small HTTP service that stores TODO items."
	}
}

// cmdCanary trials a proposed edit in isolation and reports whether it should be
// promoted.
//
// The edit is expressed as a file operation rather than a patch, because the
// point of the canary is to run the real operation, not a stand-in that could
// diverge from what would really be applied.
func cmdCanary(args []string) {
	fs := flag.NewFlagSet("canary", flag.ExitOnError)
	scenario := fs.String("scenario", "http-todo", "scenario to run (http-todo | http-resource | cli-filelist)")
	gateDir := fs.String("gate", "testdata/baseline", "regression baseline directory")
	target := fs.String("file", "", "file the edit writes (workspace-relative)")
	content := fs.String("content", "", "inline content for the edit; prefer --content-file for anything multi-line")
	contentFile := fs.String("content-file", "", "read the edit's content from this file")
	from := fs.String("from", "", "copy the content of this existing workspace file")
	name := fs.String("name", "", "edit name for the report")
	iterations := fs.Int("iterations", 1, "iterations per branch")
	scratch := fs.String("scratch", "", "scratch parent dir (default: system temp)")
	keep := fs.Bool("keep-scratch", false, "retain trial directories for inspection")
	fs.Parse(args)

	if *target == "" && *from == "" {
		fmt.Println("Error: provide --file (with --content) or --file (with --from)")
		os.Exit(1)
	}
	editName := *name
	if editName == "" {
		editName = "edit-" + filepath.Base(*target)
	}

	// Content is read from a file rather than accepted inline for anything
	// multi-line. Windows argument parsing does not preserve newlines in argv,
	// so `--content "package x\n\nimport ..."` arrives at the process with its
	// line structure mangled and the resulting file does not compile. That
	// failure is indistinguishable from "the edit was wrong", which is the worst
	// possible time to be lied to.
	var editContent []byte
	switch {
	case *contentFile != "":
		b, rerr := os.ReadFile(*contentFile)
		if rerr != nil {
			fmt.Printf("Error: cannot read --content-file: %v\n", rerr)
			os.Exit(1)
		}
		editContent = b
	case *content != "":
		if strings.Contains(*content, "\n") {
			fmt.Println("Error: --content spanning multiple lines is unreliable on Windows.")
			fmt.Println("       Write the content to a file and use --content-file instead.")
			os.Exit(1)
		}
		editContent = []byte(*content)
	}

	scratchParent := *scratch
	if scratchParent == "" {
		scratchParent = os.TempDir()
	}
	reportDir := filepath.Join("instrumentation", "canary")

	runner := func(dir string) (*canary.BranchResult, error) {
		orch, err := system.NewOrchestrator("", dir, scenarioIntent(*scenario), *iterations)
		if err != nil {
			return nil, err
		}
		if err := orch.Run(); err != nil {
			return nil, err
		}
		// Report the workspace the orchestrator actually populated rather than
		// assuming a layout. Assuming is what let an edit land outside the
		// module and the trial score it as a no-op.
		return &canary.BranchResult{
			State:     orch.SemiState(),
			Workspace: orch.WorkspacePath(),
		}, nil
	}

	h := canary.NewHarness(*gateDir, scratchParent, runner).KeepScratch(*keep)

	// Re-test the edited workspace with the real test runner. Without this the
	// candidate would be measured from the pipeline's own state, which is blind
	// to the edit and would score every candidate identical to its baseline.
	h = h.WithWorkspaceTester(func(ws string) (state.TestResult, error) {
		return tests.NewTestRunner(ws).WithRace(true).WithCoverage(true).RunAll(context.Background()), nil
	})

	fmt.Println("Running baseline branch (unmodified)...")
	base, err := h.Baseline(*scenario)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  potential=%.4f pass=%.2f gate=%v chain=%v\n\n",
		base.Potential, base.PassRate, base.GatePassed, base.ChainValid)

	edit := canary.Edit{
		Name: editName,
		Apply: func(ws string) error {
			// Paths are workspace-relative, so the caller names files the way
			// they appear in the generated project.
			data := editContent
			if *from != "" {
				b, rerr := os.ReadFile(filepath.Join(ws, filepath.FromSlash(*from)))
				if rerr != nil {
					return fmt.Errorf("read %s: %w", *from, rerr)
				}
				data = b
			}
			path := filepath.Join(ws, filepath.FromSlash(*target))
			if merr := os.MkdirAll(filepath.Dir(path), 0o755); merr != nil {
				return merr
			}
			return os.WriteFile(path, data, 0o644)
		},
	}

	fmt.Printf("Running candidate branch (edit: %s -> %s)...\n", editName, *target)
	cand, err := h.Trial(edit, *scenario)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("  potential=%.4f pass=%.2f gate=%v chain=%v\n\n",
		cand.Potential, cand.PassRate, cand.GatePassed, cand.ChainValid)

	verdict := canary.Compare(base, cand)
	fmt.Print(canary.FormatVerdict(verdict))

	rep := canary.Report{
		Scenario:  *scenario,
		Baseline:  base,
		Candidate: cand,
		Verdict:   verdict,
		Applied:   *target,
		Timestamp: time.Now().Format(time.RFC3339),
	}
	if err := canary.Write(reportDir, rep); err != nil {
		fmt.Printf("Warning: could not write canary report: %v\n", err)
	} else {
		fmt.Printf("report: %s\n", filepath.Join(reportDir, "canary-"+editName+".json"))
	}

	if !verdict.Promote {
		os.Exit(1)
	}
	fmt.Println("\nNOTE: promotion is authorised, not applied. Applying the edit is a separate, explicit step.")
}

func printUsage() {
	fmt.Println("Spiral CodeMaker - Self-modifying, spiral-iterating AI coding system")
	fmt.Println()
	fmt.Println("Usage: spiral <command> [flags]")
	fmt.Println()
	fmt.Println("Commands:")
	fmt.Println("  run            Run spiral iteration on an intent")
	fmt.Println("  dashboard      Start web dashboard")
	fmt.Println("  daemon         Start daemon mode (web UI for submitting runs)")
	fmt.Println("  setup          Setup local tools (llama.cpp with CUDA, GPU detection)")
	fmt.Println("  models         List/manage models and providers")
	fmt.Println("  scan <path>    Scan a path for GGUF files")
	fmt.Println("  hf-search      Search HuggingFace Hub")
	fmt.Println("  help           Show this help")
	fmt.Println()
	fmt.Println("Run: spiral run --help")
}

func cmdDaemon(args []string) {
	fs := flag.NewFlagSet("daemon", flag.ExitOnError)
	port := fs.String("port", ":8080", "Server address")
	output := fs.String("output", "./spiral-output", "Output directory for runs")
	fs.Parse(args)

	outputPath, _ := filepath.Abs(*output)
	if err := os.MkdirAll(outputPath, 0755); err != nil {
		fmt.Printf("Error creating output dir: %v\n", err)
		os.Exit(1)
	}

	configPath := filepath.Join(outputPath, "models.json")

	fmt.Printf("Spiral CodeMaker Daemon starting...\n")
	fmt.Printf("Dashboard: http://%s/dashboard/\n", *port)
	fmt.Printf("Output dir: %s\n", outputPath)
	fmt.Println()

	srv := api.NewDaemonServer(*port, configPath)
	if err := srv.Start(); err != nil {
		fmt.Printf("Error starting daemon: %v\n", err)
		os.Exit(1)
	}
}

func cmdRun(args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	intent := fs.String("intent", "", "The intent/goal for the spiral to achieve (required)")
	output := fs.String("output", "./spiral-output", "Output directory")
	iterations := fs.Int("iterations", 3, "Max spiral iterations")
	serve := fs.Bool("serve", false, "Start web dashboard after run")
	modelDir := fs.String("models-dir", "", "Models config directory (default: <output>/models.json)")
	hfToken := fs.String("hf-token", "", "HuggingFace API token")
	ggufServer := fs.String("gguf-server", "", "GGUF/llama.cpp server URL (default: auto-detect localhost:8080)")
	localModel := fs.String("local-model", "", "Auto-start llama.cpp server with this GGUF model file")
	enableHF := fs.Bool("enable-hf", false, "Enable HuggingFace provider")
	debug := fs.Bool("debug", false, "Debug mode (prints LLM responses)")

	fs.Usage = func() {
		fmt.Println("Usage: spiral run --intent \"<description>\" [flags]")
		fmt.Println()
		fmt.Println("Flags:")
		fs.PrintDefaults()
	}

	fs.Parse(args)

	if *intent == "" {
		fmt.Println("Error: --intent is required")
		fs.Usage()
		os.Exit(1)
	}

	outputPath, _ := filepath.Abs(*output)

	fmt.Printf("Spiral CodeMaker starting...\n")
	fmt.Printf("Intent: %s\n", *intent)
	fmt.Printf("Output: %s\n", outputPath)
	fmt.Printf("Iterations: %d\n\n", *iterations)

	startTime := time.Now()

	// When no model is named, pick the largest GGUF already in the cache. A 1.1B
	// model cannot emit valid JSON, so it silently forces every LLM path onto
	// the template fallback; preferring the biggest available model keeps the
	// LLM path exercised instead of degrading without telling anyone.
	if *localModel == "" {
		if best := bestLocalModel(); best != "" {
			*localModel = best
			fmt.Printf("Auto-selected local model: %s\n", best)
		}
	}

	if *ggufServer == "" {
		*ggufServer = "http://localhost:8080"
	}

	// A model is present, so a local provider is worth configuring even when no
	// other provider was requested.
	useProviders := *enableHF || *hfToken != "" || *ggufServer != "http://localhost:8080" || *localModel != ""

	var orch *system.Orchestrator
	if useProviders {
		registryPath := *modelDir
		if registryPath == "" {
			registryPath = filepath.Join(outputPath, "models.json")
		}
		modelReg := registry.NewModelRegistry(registryPath)
		if *enableHF && *hfToken != "" {
			modelReg.AddProvider("huggingface", "huggingface", "https://api-inference.huggingface.co", *hfToken, map[string]any{
				"model":     "gpt2",
				"cache_dir": filepath.Join(outputPath, "hf-cache"),
			})
		}
		if *localModel != "" {
			llamaMgr := registry.NewLlamaCppManager(registry.DefaultLlamaCppDir())
			status := llamaMgr.Status()
			if !status.Installed {
				fmt.Printf("Error: llama.cpp not installed. Run 'spiral setup' first.\n")
				os.Exit(1)
			}
			if !status.ServerRunning {
				fmt.Printf("Starting llama.cpp server with model: %s\n", *localModel)
				cmd, err := llamaMgr.StartServer(*localModel, 999)
				if err != nil {
					fmt.Printf("Error starting llama.cpp server: %v\n", err)
					os.Exit(1)
				}
				defer cmd.Process.Kill()
				time.Sleep(3 * time.Second)
			}
			providerName := "gguf-local"
			modelReg.AddProvider(providerName, "gguf", *ggufServer, "", map[string]any{
				"model_path": *localModel,
			})
			modelReg.AddModel("gguf-model", providerName, routing.CapFast, 2048, 0.7)
			modelReg.AddModel("gguf-model", providerName, routing.CapReasoning, 2048, 0.3)
			modelReg.AddModel("gguf-model", providerName, routing.CapSpecialize, 2048, 0.1)
			modelReg.SetActiveModel(routing.CapFast, "gguf-model")
			modelReg.SetActiveModel(routing.CapReasoning, "gguf-model")
			modelReg.SetActiveModel(routing.CapSpecialize, "gguf-model")
		} else if *ggufServer != "" {
			providerName := "gguf-local"
			modelReg.AddProvider(providerName, "gguf", *ggufServer, "", map[string]any{
				"model_path": filepath.Join(outputPath, "models"),
			})
			modelReg.AddModel("gguf-model", providerName, routing.CapFast, 2048, 0.7)
			modelReg.AddModel("gguf-model", providerName, routing.CapReasoning, 2048, 0.3)
			modelReg.AddModel("gguf-model", providerName, routing.CapSpecialize, 2048, 0.1)
			modelReg.SetActiveModel(routing.CapFast, "gguf-model")
			modelReg.SetActiveModel(routing.CapReasoning, "gguf-model")
			modelReg.SetActiveModel(routing.CapSpecialize, "gguf-model")
		}
		orch, err := system.NewOrchestratorWithRegistry("", outputPath, *intent, *iterations, modelReg)
		if err != nil {
			fmt.Printf("Error creating orchestrator: %v\n", err)
			os.Exit(1)
		}
		orch.EnableDebug(*debug)
		runOrchestrator(orch, startTime, outputPath, *serve)
	} else {
		var err error
		orch, err = system.NewOrchestrator("", outputPath, *intent, *iterations)
		if err != nil {
			fmt.Printf("Error creating orchestrator: %v\n", err)
			os.Exit(1)
		}
		runOrchestrator(orch, startTime, outputPath, *serve)
	}
}

// bestLocalModel returns the largest GGUF file in the model cache, or "" if
// there is none. Size is used as the proxy for capability because it is the
// only signal available without loading a model, and it is a good enough one:
// the practical failure mode is a small model being silently inadequate for
// structured output, and larger models reliably fix that.
func bestLocalModel() string {
	dirs := []string{"models-cache", filepath.Join("third_party", "models")}
	var best string
	var bestSize int64

	for _, d := range dirs {
		entries, err := os.ReadDir(d)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Size() > bestSize {
				bestSize = info.Size()
				best = filepath.Join(d, e.Name())
			}
		}
	}
	return best
}

func runOrchestrator(orch *system.Orchestrator, startTime time.Time, outputPath string, serve bool) {
	err := orch.Run()
	if err != nil {
		fmt.Printf("Error running spiral: %v\n", err)
		os.Exit(1)
	}
	elapsed := time.Since(startTime)

	report := orch.FinalReport()

	fmt.Println()
	fmt.Println(report)

	fmt.Printf("\nGenerated files location: %s\n", orch.WorkspacePath())
	fmt.Printf("Total time: %s\n", elapsed)

	if err := orch.SaveModelConfig(); err != nil {
		fmt.Printf("Warning: could not save model config: %v\n", err)
	}

	if !serve {
		os.Exit(0)
	}

	fmt.Println("\nStarting web dashboard on :8080...")
	srv := api.NewServerWithRegistry(orch, ":8080", orch.ModelRegistry())
	if err := srv.Start(); err != nil {
		fmt.Printf("Error starting dashboard: %v\n", err)
		os.Exit(1)
	}
}

func cmdDashboard(args []string) {
	fs := flag.NewFlagSet("dashboard", flag.ExitOnError)
	port := fs.String("port", ":8080", "Server address")
	output := fs.String("output", "./spiral-output", "Output directory from a previous run")
	registryPath := fs.String("models-dir", "", "Models config path")

	fs.Parse(args)

	outputPath, _ := filepath.Abs(*output)
	registryPathAbs := *registryPath
	if registryPathAbs == "" {
		registryPathAbs = filepath.Join(outputPath, "models.json")
	}

	modelReg := registry.NewModelRegistry(registryPathAbs)

	fmt.Printf("Starting dashboard on %s\n", *port)
	fmt.Printf("Output dir: %s\n", outputPath)

	orch, err := system.NewOrchestratorWithRegistry("", outputPath, "dashboard mode", 1, modelReg)
	if err != nil {
		fmt.Printf("Error creating orchestrator: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Providers: %v\n", modelReg.ListProviders())
	fmt.Printf("Models: %d configured\n", len(modelReg.ListModels()))

	srv := api.NewServerWithRegistry(orch, *port, modelReg)
	if err := srv.Start(); err != nil {
		fmt.Printf("Error starting dashboard: %v\n", err)
		os.Exit(1)
	}
}

func cmdModels(args []string) {
	fs := flag.NewFlagSet("models", flag.ExitOnError)
	output := fs.String("output", "./spiral-output", "Output directory")
	listProviders := fs.Bool("providers", false, "List configured providers")
	listModels := fs.Bool("list", false, "List configured models")
	addProvider := fs.String("add-provider", "", "Add a provider: <name>:<type>:<url>:<token>")
	activate := fs.String("activate", "", "Activate model for capability: <capability>:<model_name>")
	fs.Parse(args)

	registryPath := filepath.Join(*output, "models.json")
	modelReg := registry.NewModelRegistry(registryPath)

	if *listProviders {
		providers := modelReg.ListProviders()
		if len(providers) == 0 {
			fmt.Println("No providers configured")
		} else {
			fmt.Println("Providers:")
			for _, p := range providers {
				fmt.Printf("  - %s\n", p)
			}
		}
		return
	}

	if *listModels {
		models := modelReg.ListModels()
		if len(models) == 0 {
			fmt.Println("No models configured")
		} else {
			fmt.Println("Models:")
			for _, m := range models {
				fmt.Printf("  - %s (%s, %s, %s) [%s]\n", m.Name, m.Kind, m.Provider, m.ModelID, m.Status)
			}
		}
		return
	}

	if *addProvider != "" {
		parts := strings.SplitN(*addProvider, ":", 4)
		if len(parts) < 2 {
			fmt.Println("Format: <name>:<type>:<url>:<token>")
			os.Exit(1)
		}
		name := parts[0]
		ptype := parts[1]
		url := ""
		token := ""
		if len(parts) > 2 {
			url = parts[2]
		}
		if len(parts) > 3 {
			token = parts[3]
		}
		extra := map[string]any{}
		if ptype == "huggingface" {
			extra["model"] = "gpt2"
		}
		if ptype == "gguf" {
			extra["model_path"] = ""
		}
		if ptype == "ollama" {
			extra["server_url"] = "http://localhost:11434"
		}
		modelReg.AddProvider(name, ptype, url, token, extra)
		modelReg.SaveConfig()
		fmt.Printf("Added provider: %s (%s)\n", name, ptype)
		return
	}

	if *activate != "" {
		parts := strings.SplitN(*activate, ":", 2)
		if len(parts) != 2 {
			fmt.Println("Format: <capability>:<model_name>")
			os.Exit(1)
		}
		modelReg.SetActiveModel(routing.ModelCapability(parts[0]), parts[1])
		modelReg.SaveConfig()
		fmt.Printf("Activated model '%s' for capability '%s'\n", parts[1], parts[0])
		return
	}

	fmt.Println("spiral models - manage models and providers")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --output <path>     Output directory (default: ./spiral-output)")
	fmt.Println("  --providers         List configured providers")
	fmt.Println("  --list              List configured models")
	fmt.Println("  --add-provider      Add a provider (format: name:type:url:token)")
	fmt.Println("  --activate          Activate a model for a capability (format: capability:model_name)")
}

func cmdScan(args []string) {
	fs := flag.NewFlagSet("scan", flag.ExitOnError)
	recursive := fs.Bool("recursive", true, "Scan recursively")
	fs.Parse(args)
	_ = recursive

	if fs.NArg() == 0 {
		fmt.Println("Usage: spiral scan <path>")
		fmt.Println("Scans for .gguf files in the given path")
		os.Exit(1)
	}

	path := fs.Arg(0)
	info, err := os.Stat(path)
	if err != nil {
		fmt.Printf("Error: %v\n", err)
		os.Exit(1)
	}
	if !info.IsDir() {
		fmt.Println("Path must be a directory")
		os.Exit(1)
	}

	s := registry.NewGGUFScanner()
	ctx := context.Background()
	entries, err := s.Scan(ctx, path)
	if err != nil {
		fmt.Printf("Error scanning: %v\n", err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Printf("No .gguf files found in %s\n", path)
		return
	}

	fmt.Printf("Found %d GGUF models in %s:\n", len(entries), path)
	for _, e := range entries {
		fmt.Printf("  - %s (%d MB) [%s]\n", e.Name, e.SizeMB, e.Capability)
	}
}

func cmdSetup(args []string) {
	fmt.Println("Spiral CodeMaker Local Setup")
	fmt.Println()

	s := registry.NewLlamaCppManager("")
	status := s.Status()

	fmt.Printf("llama.cpp status:\n")
	fmt.Printf("  Installed: %v\n", status.Installed)
	fmt.Printf("  Path: %s\n", status.Path)
	if status.Version != "" {
		fmt.Printf("  Version: %s\n", status.Version)
	}
	if status.GPUBackend != "none" {
		fmt.Printf("  GPU Backend: %s\n", status.GPUBackend)
		if len(status.GPUDevices) > 0 {
			fmt.Printf("  GPU Devices:\n")
			for _, gpu := range status.GPUDevices {
				fmt.Printf("    - %s\n", gpu)
			}
		}
	}

	if status.ServerRunning {
		fmt.Printf("  Server: running on :8080\n")
	} else {
		fmt.Printf("  Server: not running\n")
	}

	if !status.Installed {
		fmt.Println()
		fmt.Println("llama.cpp not found. To install:")
		fmt.Printf("  %s\n", s.Install(context.Background()))
		fmt.Println()
		fmt.Println("After building llama.cpp with CUDA support, copy the 'server' binary to:")
		fmt.Printf("  %s\n", status.Path)
		fmt.Println()
		fmt.Println("Then start llama.cpp server with a model:")
		fmt.Printf("  %s --model <model.gguf> --n-gpu-layers 999 --port 8080\n", filepath.Join(status.Path, "server.exe"))
	}
}

func cmdHFSearch(args []string) {
	fs := flag.NewFlagSet("hf-search", flag.ExitOnError)
	token := fs.String("token", "", "HuggingFace API token")
	limit := fs.Int("limit", 20, "Max results")
	fs.Parse(args)

	if fs.NArg() == 0 {
		fmt.Println("Usage: spiral hf-search <query> [--token <token>] [--limit <n>]")
		os.Exit(1)
	}

	query := fs.Arg(0)
	lister := registry.NewHFModelLister(*token)
	ctx := context.Background()
	entries, err := lister.ListModels(ctx, query, *limit)
	if err != nil {
		fmt.Printf("Error searching: %v\n", err)
		os.Exit(1)
	}

	if len(entries) == 0 {
		fmt.Printf("No models found for '%s'\n", query)
		return
	}

	fmt.Printf("Found %d models for '%s':\n", len(entries), query)
	for _, e := range entries {
		fmt.Printf("  - %s [%s] (%s)\n", e.Name, e.Status, e.Description)
	}
}
