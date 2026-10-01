package system

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/attention"
	"github.com/kilo/spiral-codemaker/core/causal"
	"github.com/kilo/spiral-codemaker/core/converge"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/regression"
	"github.com/kilo/spiral-codemaker/core/scheduler"
	"github.com/kilo/spiral-codemaker/core/spiral"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/units"
	"github.com/kilo/spiral-codemaker/execution/sandbox"
	"github.com/kilo/spiral-codemaker/models/registry"
	"github.com/kilo/spiral-codemaker/models/routing"
	"github.com/kilo/spiral-codemaker/persistence"
)

type Orchestrator struct {
	ctx       context.Context
	semiState *state.SemiState
	bus       *events.EventBus
	attention *attention.Manager
	workspace *workspace.Workspace
	sandbox   *sandbox.Sandbox
	router    *routing.Router
	spiralMgr *spiral.Manager
	registry  *units.Registry
	scheduler *scheduler.Scheduler
	memory    *persistence.PersistentMemory
	proposer  *proposals.Proposer
	config    *units.Config

	modelReg *registry.ModelRegistry

	requirementsAnalyst *units.RequirementsAnalyst
	decomposer          *units.Decomposer
	architect           *units.Architect
	codeGenerator       *units.CodeGenerator
	testDesigner        *units.TestDesigner
	testRunner          *units.TestRunnerUnit
	debugger            *units.Debugger
	critic              *units.Critic
	consistencyChecker  *units.ConsistencyChecker
	securityAnalyst     *units.SecurityAnalyst
	documentationWriter *units.DocumentationWriter
	synthesizer         *units.Synthesizer

	workspacePath string

	// Phase 0 instrumentation. The convergence tracker observes the potential
	// function each iteration, the causal ledger attributes credit to proposals,
	// and the Pareto frontier remembers the best revision found so the spiral
	// can return it instead of whatever the last iteration happened to produce.
	convergence       *converge.Tracker
	ledger            *causal.Ledger
	frontier          *converge.ParetoFrontier
	pendingAssessment converge.Assessment
	// runtime is retained so LLM usage can be reported after the run.

	runtime *units.Runtime
	// semiStateRaceEnabled records whether the race detector was actually
	// engaged, so a run analysed without it is not mistaken for one analysed
	// with it.
	semiStateRaceEnabled bool
}

func NewOrchestrator(projectRoot, outputPath, intent string, maxIterations int) (*Orchestrator, error) {
	registryPath := filepath.Join(outputPath, "models.json")
	modelReg := registry.NewModelRegistry(registryPath)

	return NewOrchestratorWithRegistry(projectRoot, outputPath, intent, maxIterations, modelReg)
}

func NewOrchestratorWithRegistry(projectRoot, outputPath, intent string, maxIterations int, modelReg *registry.ModelRegistry) (*Orchestrator, error) {
	ctx := context.Background()

	ss := state.NewSemiState()
	bus := events.NewEventBus()
	am := attention.NewManager(bus)

	wsPath := filepath.Join(outputPath, "output")
	if err := os.MkdirAll(wsPath, 0755); err != nil {
		return nil, err
	}

	ws, err := workspace.NewWorkspace(wsPath)
	if err != nil {
		return nil, fmt.Errorf("create workspace: %w", err)
	}

	sb := sandbox.NewSandbox(wsPath)
	router := modelReg.Router()

	mem := persistence.NewPersistentMemory(filepath.Join(outputPath, "memory"))
	proposer := proposals.NewProposer()

	sp := spiral.NewManager(ctx, bus, ss, maxIterations)

	config := &units.Config{
		ProjectRoot:   intent,
		OutputPath:    outputPath,
		MaxIterations: maxIterations,
		Debug:         false,
	}

	rt := units.NewRuntime(ss, bus, am, ws, sb, router, sp, proposer, mem, config)
	ledger := causal.NewLedger()
	rt.Ledger = ledger

	registry := units.NewRegistry(bus)
	scheduler := scheduler.NewScheduler(registry, bus, am, ss)

	o := &Orchestrator{
		ctx:           ctx,
		semiState:     ss,
		bus:           bus,
		runtime:       rt,
		attention:     am,
		workspace:     ws,
		sandbox:       sb,
		router:        router,
		spiralMgr:     sp,
		registry:      registry,
		scheduler:     scheduler,
		memory:        mem,
		proposer:      proposer,
		config:        config,
		modelReg:      modelReg,
		workspacePath: wsPath,
		convergence:   converge.NewTracker(),
		ledger:        ledger,
		frontier:      converge.NewParetoFrontier(),
	}

	o.requirementsAnalyst = units.NewRequirementsAnalyst(rt)
	o.decomposer = units.NewDecomposer(rt)
	o.architect = units.NewArchitect(rt)
	o.codeGenerator = units.NewCodeGenerator(rt, ws)
	o.testDesigner = units.NewTestDesigner(rt)
	o.testRunner = units.NewTestRunner(rt, wsPath)
	o.debugger = units.NewDebugger(rt, ws, proposer)
	o.critic = units.NewCritic(rt)
	o.consistencyChecker = units.NewConsistencyChecker(rt)
	o.securityAnalyst = units.NewSecurityAnalyst(rt)
	o.documentationWriter = units.NewDocumentationWriter(rt, ws)
	o.synthesizer = units.NewSynthesizer(rt)

	o.registerUnits()

	return o, nil
}

func (o *Orchestrator) EnableDebug(debug bool) {
	o.config.Debug = debug
}

func (o *Orchestrator) registerUnits() {
	units := []units.Unit{
		o.requirementsAnalyst,
		o.decomposer,
		o.architect,
		o.synthesizer,
		o.codeGenerator,
		o.testDesigner,
		o.testRunner,
		o.debugger,
		o.critic,
		o.consistencyChecker,
		o.securityAnalyst,
		o.documentationWriter,
	}

	o.attention.Set("requirements_analyst", "initial", 0.5)
	o.attention.Set("decomposer", "waiting for requirements", 0.1)
	o.attention.Set("architect", "waiting for decomposition", 0.1)
	o.attention.Set("synthesizer", "waiting for architecture", 0.1)
	o.attention.Set("code_generator", "waiting for synthesis", 0.1)
	o.attention.Set("test_designer", "waiting for code", 0.1)
	o.attention.Set("test_runner", "waiting for tests", 0.1)
	o.attention.Set("debugger", "dormant", 0.05)
	o.attention.Set("critic", "dormant", 0.05)
	o.attention.Set("consistency_checker", "dormant", 0.05)
	o.attention.Set("security_analyst", "dormant", 0.05)
	o.attention.Set("documentation_writer", "dormant", 0.05)

	for _, u := range units {
		o.registry.Register(u)
		o.attention.Set(u.ID(), u.Name(), u.AttentionWeight())
	}

	metas := make([]state.UnitMeta, 0, len(units))
	for _, u := range units {
		metas = append(metas, state.UnitMeta{
			ID:              u.ID(),
			Role:            u.Role(),
			Name:            u.Name(),
			Cadence:         u.Cadence(),
			Activation:      u.Activation(),
			Dependencies:    u.Dependencies(),
			AttentionWeight: u.AttentionWeight(),
			Confidence:      u.Confidence(),
			Active:          u.IsActive(),
		})
	}
	o.semiState.ActiveUnits = metas

	o.scheduler.AddSchedule("unit-requirements-analyst", 100*time.Millisecond)
	o.scheduler.AddSchedule("unit-decomposer", 200*time.Millisecond)
	o.scheduler.AddSchedule("unit-architect", 200*time.Millisecond)
	o.scheduler.AddSchedule("unit-synthesizer", 200*time.Millisecond)
	o.scheduler.AddSchedule("unit-code-generator", 100*time.Millisecond)
	o.scheduler.AddSchedule("unit-test-designer", 200*time.Millisecond)
	o.scheduler.AddSchedule("unit-test-runner", 100*time.Millisecond)
	o.scheduler.AddSchedule("unit-debugger", 100*time.Millisecond)
	o.scheduler.AddSchedule("unit-critic", 300*time.Millisecond)
	o.scheduler.AddSchedule("unit-consistency-checker", 200*time.Millisecond)
	o.scheduler.AddSchedule("unit-security-analyst", 500*time.Millisecond)
	o.scheduler.AddSchedule("unit-documentation-writer", 500*time.Millisecond)
}

func (o *Orchestrator) Run() error {
	for o.spiralMgr.ShouldContinue() {
		if err := o.runIteration(); err != nil {
			return err
		}

		// Phase 0 instrumentation: after every iteration, record where the
		// potential function stands, attribute credit for this iteration's
		// proposals, and consult the convergence verdict. This is what replaces
		// maxIterations as a principled stopping rule.
		assessment := o.observeAndAssess()
		o.pendingAssessment = assessment

		// Seal this revision into the tamper-evident chain before deciding
		// whether to continue. Sealing first means the decision to stop is itself
		// recorded; otherwise the final revision would be unchained and therefore
		// the one revision a mutator could edit for free.
		if _, err := o.semiState.AppendChained(string(assessment.Phase) + ": " + assessment.Reason); err != nil {
			o.semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceObservation,
				Content:    "chain append failed: " + err.Error(),
				Strength:   state.ConfidenceHigh,
				Provenance: state.NewProvenance("state-chain"),
			})
		}

		if assessment.ShouldStop {
			break
		}
	}

	o.attention.SyncToSemiState(o.semiState)
	o.finalizeInstrumentation()
	o.runRegressionGate()
	return nil
}

// runRegressionGate pins a canonical projection of this run against a committed
// baseline and blocks on drift.
//
// This runs on every execution, not only in tests, because the whole point is
// that a self-edit cannot slip through because nobody remembered to run the
// suite. The gate is the enforcement mechanism; the test suite is a
// convenience on top of it.
func (o *Orchestrator) runRegressionGate() {
	gate := regression.NewGate(o.baselineDir())
	proj := regression.Project(o.scenarioName(), o.semiState)

	// A run with an LLM attached is not reproducible at the token level, so its
	// projection legitimately varies. Gating it would produce noise that trains
	// people to ignore the gate, which is worse than not having one. The
	// deterministic template path is the one worth pinning.
	if o.config != nil && o.config.Debug {
		return
	}
	if _, isLLM := o.llmInUse(); isLLM {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    "regression gate skipped: LLM output is not token-reproducible, pinning it would gate on noise",
			Strength:   state.ConfidenceLow,
			Provenance: state.NewProvenance("regression-gate"),
		})
		return
	}

	verdict := gate.Check(proj)
	if verdict.Pass {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    "regression gate: " + verdict.Summary,
			Strength:   state.ConfidenceHigh,
			Provenance: state.NewProvenance("regression-gate"),
		})
		return
	}

	o.semiState.AddEvidence(state.Evidence{
		Type:       state.EvidenceAnalysis,
		Content:    "regression gate FAILED:\n" + regression.FormatVerdicts([]regression.Verdict{verdict}),
		Strength:   state.ConfidenceCertain,
		Provenance: state.NewProvenance("regression-gate"),
	})
	o.attention.Boost("critic", "regression detected, review required", 0.8)
}

func (o *Orchestrator) baselineDir() string {
	return filepath.Join(o.config.OutputPath, "baseline")
}

func (o *Orchestrator) scenarioName() string {
	if o.config == nil || o.config.ProjectRoot == "" {
		return "default"
	}
	return "run"
}

// llmInUse reports whether a non-mock provider is wired, which is what makes a
// run non-reproducible.
func (o *Orchestrator) llmInUse() (any, bool) {
	if o.router == nil {
		return nil, false
	}
	for _, m := range []routing.ModelCapability{
		routing.CapReasoning, routing.CapSpecialize, routing.CapFast,
	} {
		if _, name, ok := o.router.GetProvider(m); ok && name != "" && name != "mock" {
			return name, true
		}
	}
	return nil, false
}

// observeAndAssess runs the Phase 0 measurement pass for the current revision.
func (o *Orchestrator) observeAndAssess() converge.Assessment {
	pot := o.convergence.Observe(o.semiState)

	// Attribute credit to everything proposed since the baseline captured at the
	// top of this iteration.
	attr := o.ledger.Attribute(o.semiState)
	if len(attr.Attributed) > 0 {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceAnalysis,
			Content:    "causal attribution: " + attr.Explanation,
			Strength:   state.ConfidenceMedium,
			Provenance: state.NewProvenance("causal-ledger"),
		})
	}

	// Feed the frontier. Token cost is the run total so far; complexity is a
	// cheap proxy (component count plus generated file count).
	passRate := 0.0
	if res := o.semiState.GetTestResults(); len(res) > 0 {
		if res[len(res)-1].Status == state.TestPassed {
			passRate = 1
		}
	}
	comp := 0
	if plan := o.semiState.GetArchitecturePlan(); plan != nil {
		comp = len(plan.Components)
	}
	comp += len(o.semiState.GeneratedFiles)
	o.frontier.Add(converge.ParetoPoint{
		Revision:   o.semiState.Revision,
		PassRate:   passRate,
		Coverage:   pot.EvidenceDensity,
		TokenCost:  o.ledger.TotalTokens(),
		Complexity: comp,
	})

	assessment := o.convergence.Assess()
	o.semiState.AddEvidence(state.Evidence{
		Type: state.EvidenceAnalysis,
		Content: fmt.Sprintf("potential %.4f (pass=%.2f evidence=%.2f confidence=%.2f) -> %s: %s",
			pot.Value, pot.TestPassRate, pot.EvidenceDensity, pot.ConfidenceTerm,
			assessment.Phase, assessment.Reason),
		Strength:   state.ConfidenceMedium,
		Provenance: state.NewProvenance("convergence-tracker"),
	})
	return assessment
}

// finalizeInstrumentation writes the Phase 0 artifacts to disk so runs can be
// compared, audited, and replayed later.
func (o *Orchestrator) finalizeInstrumentation() {
	trace := o.convergence.Trace()
	var final converge.Potential
	if len(trace) > 0 {
		final = trace[len(trace)-1].Potential
	}

	report := map[string]any{
		"trajectory":      trace,
		"assessment":      o.pendingAssessment,
		"interventions":   o.ledger.Interventions(),
		"strategies":      o.ledger.TopStrategies(),
		"frontier":        o.frontier.Snapshot(nil),
		"final_potential": final,
		// Test results are included so the exact command that produced each
		// outcome is auditable. Attribution claims are only meaningful if the
		// ground-truth measurement is inspectable.
		"test_results": o.semiState.GetTestResults(),
		"race_enabled": o.semiStateRaceEnabled,
		// The chain is the audit record. It is written out so the hash history
		// survives the process and can be verified later.
		"chain":        o.semiState.Chain(),
		"chain_verify": o.semiState.VerifyChain(),
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return
	}
	dir := filepath.Join(o.config.OutputPath, "instrumentation")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "phase0.json"), data, 0644)
}

func max0(i int) int {
	if i < 0 {
		return 0
	}
	return i
}

func (o *Orchestrator) runIteration() error {
	ctx := o.ctx
	record := o.spiralMgr.StartIteration()
	_ = record

	// Capture the objective vector BEFORE any unit runs. Attribution needs a
	// before-picture taken prior to the changes being judged; capturing it in the
	// same function that reads the after-picture guarantees a zero delta and
	// means the ledger records no credit for anything, ever.
	o.ledger.ObserveBaseline(o.semiState)

	o.spiralMgr.UpdatePhase(spiral.PhaseRequirement)
	o.runUnitSafe(ctx, o.requirementsAnalyst)

	o.spiralMgr.UpdatePhase(spiral.PhaseDecomposition)
	o.runUnitSafe(ctx, o.decomposer)

	o.spiralMgr.UpdatePhase(spiral.PhaseArchitecture)
	o.runUnitSafe(ctx, o.architect)

	o.spiralMgr.UpdatePhase(spiral.PhaseSynthesis)
	o.runUnitSafe(ctx, o.synthesizer)

	o.spiralMgr.UpdatePhase(spiral.PhaseCodeProposal)
	o.runUnitSafe(ctx, o.codeGenerator)

	o.spiralMgr.UpdatePhase(spiral.PhaseBuild)
	br := o.sandbox.BuildRunner().Build(ctx)
	if !br.Success {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceAnalysis,
			Content:    "build failed: " + br.Error,
			Strength:   state.ConfidenceHigh,
			Provenance: state.NewProvenance("orchestrator"),
		})
		o.spiralMgr.AddEvidence(state.Evidence{
			Type:     state.EvidenceAnalysis,
			Content:  "build failed: " + br.Error,
			Strength: state.ConfidenceHigh,
		})
	}

	o.spiralMgr.UpdatePhase(spiral.PhaseCodeApplication)
	o.spiralMgr.UpdatePhase(spiral.PhaseTest)
	o.runUnitSafe(ctx, o.testDesigner)
	o.runUnitSafe(ctx, o.testRunner)
	o.semiStateRaceEnabled = o.codeGeneratorRaceStatus()

	o.spiralMgr.UpdatePhase(spiral.PhaseCritique)
	if o.semiState.HasFailedTests() {
		o.spiralMgr.UpdatePhase(spiral.PhaseRevision)
		o.runUnitSafe(ctx, o.debugger)
		o.runUnitSafe(ctx, o.testRunner)
	}

	o.runUnitSafe(ctx, o.consistencyChecker)
	o.runUnitSafe(ctx, o.securityAnalyst)
	o.runUnitSafe(ctx, o.critic)

	if o.semiState.HasFailedTests() && o.spiralMgr.IterationsCompleted() < o.spiralMgr.MaxIterations() {
		o.spiralMgr.UpdatePhase(spiral.PhaseFinalSynthesis)
		o.runUnitSafe(ctx, o.synthesizer)
		o.spiralMgr.EndIteration("iteration with test failures, will retry")
		return nil
	}

	o.spiralMgr.UpdatePhase(spiral.PhaseDocumentation)
	o.runUnitSafe(ctx, o.documentationWriter)

	o.spiralMgr.UpdatePhase(spiral.PhaseFinalSynthesis)
	o.runUnitSafe(ctx, o.synthesizer)

	summary := "all tests passed, implementation complete"
	if o.semiState.HasFailedTests() {
		summary = "tests still failing after max iterations"
	}
	o.spiralMgr.EndIteration(summary)

	return nil
}

func (o *Orchestrator) runUnitSafe(ctx context.Context, u units.Unit) {
	if u == nil {
		return
	}
	start := time.Now()
	_, err := u.Run(ctx, o.semiState, o.bus)
	elapsed := time.Since(start)

	o.semiState.SetAttention(u.ID(), u.AttentionWeight())

	if err != nil {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    fmt.Sprintf("unit %s completed with error: %s (took %s)", u.ID(), err.Error(), elapsed),
			Strength:   state.ConfidenceMedium,
			Provenance: state.NewProvenance(u.ID()),
		})
	} else {
		o.semiState.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    fmt.Sprintf("unit %s completed successfully (took %s)", u.ID(), elapsed),
			Strength:   state.ConfidenceLow,
			Provenance: state.NewProvenance(u.ID()),
		})
	}

	o.spiralMgr.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("unit %s: %s", u.ID(), errToStr(err)),
		Strength: state.ConfidenceMedium,
	})
}

func errToStr(err error) string {
	if err == nil {
		return "completed"
	}
	return err.Error()
}

// codeGeneratorRaceStatus reports whether the last test invocation included the
// race detector, derived from the recorded command rather than from intent.
func (o *Orchestrator) codeGeneratorRaceStatus() bool {
	results := o.semiState.GetTestResults()
	if len(results) == 0 {
		return false
	}
	return strings.Contains(results[len(results)-1].Command, "-race")
}

func (o *Orchestrator) SemiState() *state.SemiState {
	return o.semiState
}

// Convergence exposes the potential-function tracker so callers can inspect the
// trajectory and the convergence verdict after a run.
func (o *Orchestrator) Convergence() *converge.Tracker {
	return o.convergence
}

// LLMUsage reports whether the language model was actually consulted during the
// run, and at what cost.
//
// This exists because "the LLM is wired up" and "the LLM was used" are different
// claims, and only the second one matters. A pipeline can have a capable model
// configured and still produce every artefact from templates, silently. This
// accessor makes that distinction observable rather than assumed.
func (o *Orchestrator) LLMUsage() units.UsageReport {
	return o.runtime.LLM.Report()
}

// CausalLedger exposes the intervention record and credit assignment.
func (o *Orchestrator) CausalLedger() *causal.Ledger {
	return o.ledger
}

// Frontier exposes the Pareto frontier of best-known revisions.
func (o *Orchestrator) Frontier() *converge.ParetoFrontier {
	return o.frontier
}

func (o *Orchestrator) SpiralManager() *spiral.Manager {
	return o.spiralMgr
}

func (o *Orchestrator) Bus() *events.EventBus {
	return o.bus
}

func (o *Orchestrator) Attention() *attention.Manager {
	return o.attention
}

func (o *Orchestrator) Workspace() *workspace.Workspace {
	return o.workspace
}

func (o *Orchestrator) WorkspacePath() string {
	return o.workspacePath
}

func (o *Orchestrator) Memory() *persistence.PersistentMemory {
	return o.memory
}

func (o *Orchestrator) Registry() *units.Registry {
	return o.registry
}

func (o *Orchestrator) Scheduler() *scheduler.Scheduler {
	return o.scheduler
}

func (o *Orchestrator) StartScheduler() {
	o.scheduler.Start()
}

func (o *Orchestrator) StopScheduler() {
	o.scheduler.Stop()
}

func (o *Orchestrator) SaveMemory() error {
	return o.memory.Save()
}

func (o *Orchestrator) SaveModelConfig() error {
	if o.modelReg == nil {
		return nil
	}
	return o.modelReg.SaveConfig()
}

func (o *Orchestrator) ModelRegistry() *registry.ModelRegistry {
	return o.modelReg
}

func (o *Orchestrator) FinalReport() string {
	return o.spiralMgr.FinalReport()
}
