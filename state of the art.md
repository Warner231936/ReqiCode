# State of the Art: Spiral CodeMaker

## Where This System Already Sits on the Frontier

### The Core Novelty: A Typed Epistemic State Machine for Software Construction

Every AI coding system today shares one assumption: **code is the artifact**. Prompt in, code out, test, retry. The LLM is stateless between attempts. The context window is the memory. This is the ceiling of the entire current generation — Cursor, Copilot, Devin, SWE-agent, Aider, all of them are variations on prompt-in-code-out with a retry loop.

Spiral CodeMaker breaks that assumption. Its artifact is a **typed epistemic state** (`core/state/semi_state.go`), and code is merely one projection of that state.

The state machine carries a full epistemic lifecycle that has no equivalent in production AI coding tools:

```
FindingUnverified → FindingSupported
                  → FindingContradicted
                  → FindingResolved
                  → FindingRejected
```

This is the load-bearing idea. When a unit asserts something — "this architecture is correct", "this store is thread-safe", "this endpoint satisfies the requirement" — that assertion enters the state as a `Claim` with:

- `Status` (unverified / supported / contradicted / resolved / rejected)
- `Confidence` as a **named type**, not a bare float — `ConfidenceLow(0.25)`, `ConfidenceMedium(0.50)`, `ConfidenceHigh(0.75)`, `ConfidenceCertain(0.95)`
- `SupportingEvidence[]` and `ContradictingEvidence[]` — evidence is *bidirectional*, it can confirm or destroy
- `Provenance{UnitID, CreatedAt, Revision}` — who asserted it, when, and in which state revision
- `Revision` — the claim's own version, so beliefs have history independent of state snapshots

No current system does this. Claude Code writes files. Cursor writes files. Devin writes files and keeps a scratchpad of notes. None of them maintain **contradicting evidence** as a first-class, queryable, confidence-weighted field. None of them let a belief die.

The consequence: this system can *disagree with itself across time and be wrong about why*. That is the precondition for self-correction, and self-correction is what separates a code generator from a code *engineer*.

---

### Already Implemented and Genuinely Ahead of the Field

#### 1. Attention-Based Unit Scheduling Instead of a Fixed Pipeline

`core/attention/manager.go` implements decaying attention weights with event-driven boosts:

```go
type Manager struct {
    weights      map[string]state.Attention
    decayRate    float64  // 0.02
    minThreshold float64  // 0.05
}
// subscribed to: EvidenceAdded, ConflictDetected, TestFailed,
//                 CodeProposed, HypothesisChanged, UnitCompleted
```

Fifteen units (`core/units/`) do not run in a fixed sequence. They compete for attention. When the test runner records a failure, attention shifts toward `debugger` and `critic`. When evidence is added, it shifts toward `decomposer` and `architect`. When conflicts appear, resolution work gets scheduled.

Each unit declares its own `CadenceType` (Slow/Medium/Fast) and `ActivationType` (Manual/OnEvent). The scheduler in `core/scheduler/scheduler.go` infers cadence from interval and gates execution on attention threshold.

**Why this matters:** the dominant architecture in AI coding agents is a hardcoded tool graph — a plan is made, then tools are called in sequence. That graph is fixed at design time and does not adapt to what actually went wrong. Attention-based scheduling makes the *control flow itself* a function of the epistemic state. This is closer to how actual teams triage: nobody follows a fixed script when production is on fire.

#### 2. Proposal Lifecycle with Graded Conflict Detection

Code changes are never written directly. They become `CodeProposal` objects:

```go
type CodeProposal struct {
    ID              string
    File            string
    Operation       CodeOperation  // CREATE / MODIFY / DELETE / MOVE / RENAME
    Before, After   string
    Reason          string
    OriginatingUnit string
    Confidence      Confidence
    ExpectedEffect  string
    Provenance      Provenance
    Status          ProposalStatus  // PROPOSED / APPROVED / REJECTED / APPLIED / FAILED
    Dependencies    []string
}
```

`core/conflicts/manager.go` grades conflicts by `ConflictSeverity` and blocks application on high/critical. `code/validation.go` validates before apply. `code/patcher.go` applies atomically.

**Why this matters:** every current agent writes files optimistically and hopes. A failed write is a corrupted workspace. A successful-but-wrong write is a silent regression with no record of intent. The proposal pipeline makes every mutation auditable, reversible in principle, and attributable. It also enables the `ExpectedEffect` field — the system declares what it *intends* the change to accomplish, which is the precondition for verifying that it actually did.

#### 3. Capability-Based Model Routing

`models/routing/router.go` routes by *capability*, never by model name:

```go
CapFast, CapReasoning, CapSpecialize, CapClassification, CapEmbed
```

Units declare `LLM.GenerateCode(ctx, routing.CapReasoning, prompt)`. The router resolves which concrete model serves `CapReasoning` today. Providers (`gguf.go`, `huggingface.go`, `ollama.go`, `mock.go`) are interchangeable.

**Why this matters:** it makes the system substrate-independent at the reasoning level, not the API level. A 1.1B local model and a frontier API model are interchangeable for the architect role. This is the correct abstraction and most agent frameworks get it wrong by hardcoding model names into tool definitions.

#### 4. Fully Local, Fully Offline Operation

`models/provider/gguf.go` + `models/registry/llamacpp.go` + `registry/downloader.go` implement complete local inference: binary download, archive extraction, CUDA backend detection, model download, GGUF conversion via `convert_hf_to_gguf.py`, server lifecycle management, and a runner that auto-starts and auto-terminates `llama-server.exe` per run.

Verified working end-to-end: TinyLlama-1.1B-Chat-v1.0.Q4_K_M on CUDA 12.4, ~48 tok/s, with `spiral run --local-model <path>` handling the full lifecycle automatically.

**Why this matters:** every frontier agent today requires network egress to a model API. That is a hard dependency on a third party, a per-token cost that scales with project size, and an IP disclosure obligation. This system generates, tests, and iterates with zero network dependency after initial download. For regulated industries, air-gapped environments, and anything touching proprietary code, this is not a convenience feature — it is the difference between deployable and not.

#### 5. Provenance on Every Artifact

`Provenance{UnitID, CreatedAt, Revision}` is embedded in `Requirement`, `Evidence`, `Hypothesis`, `Decision`, `CodeProposal`, `FileEntry`, and `Finding`. Nothing exists in the state without knowing who made it, when, and at which revision.

**Why this matters:** this is the substrate for a replayable, debuggable, auditable agent. When a system makes a decision you disagree with, the question is always "why did it do that" — and this answers it exactly, not approximately. Combined with `revision` counters, you can also ask "how many times did it change its mind, and what moved it", which is a far more informative metric than pass rate.

#### 6. Multi-Role Epistemic Separation

The fifteen units are not prompt variations of one agent. They hold structurally different epistemic positions:

| Unit | Epistemic stance |
|---|---|
| `requirements-analyst` | Interprets user intent into structure |
| `decomposer` | Partitions the problem |
| `architect` | Proposes competing designs |
| `code-generator` | Emits proposals |
| `test-designer` | Defines correctness criteria |
| `test-runner` | Executes and reports ground truth |
| `debugger` | Diagnoses failure |
| `critic` | Reviews and raises objections |
| `consistency-checker` | Detects cross-file contradiction |
| `security-analyst` | Adversarial review on security axis |
| `documentation-writer` | Externalizes understanding |
| `synthesizer` | Consolidates findings |

**Why this matters:** a single agent asked to "write code and check it" is structurally incapable of genuine self-criticism — the same context that produced the error is the context evaluating it. Separation into distinct units with distinct state access creates the conditions for disagreement, which is the mechanism by which errors actually get caught.

---

## Honest Assessment: Where It Actually Stands

Being rigorous about this matters more than being flattering.

**What is genuinely ahead of the field:** the typed epistemic state machine, the claim lifecycle with bidirectional evidence, attention-based scheduling, the proposal-conflict pipeline, and full local operation. These are not incremental. They are a different architecture.

**What is not yet ahead of the field:**

- **Verification depth.** Tests are generated by the same template family as the code. `test/designer.go` and `code_generator.go` share assumptions, so tests can confirm a bug rather than catch one. This is the single largest gap.
- **Convergence.** The spiral has no termination proof, no Lyapunov-style descent guarantee, and no oscillation detection. `maxIterations` is a hard stop, not a principled one.
- **Causal attribution.** The system knows *what* changed and *what* the test said, but cannot compute *which change caused which outcome*. Evidence is recorded but not weighed causally.
- **Model capability.** TinyLlama-1.1B cannot produce valid JSON reliably. The architect's LLM path fails on this model and falls back to templates — which is why the current working path is template-driven. This is a substrate limitation, not an architecture flaw, but it means the LLM paths are unexercised at the low end.
- **Scale.** No persistent memory across sessions is yet wired into the spiral loop. No benchmarks exist. No comparison against SWE-bench, HumanEval, or MBPP.

---

## The Lethal Program: Five Pillars

What follows is the ordered work required to move this from *novel architecture* to *demonstrably superior system*. Each pillar is a research-grade problem, not a feature request.

---

### PILLAR 1 — Causal Credit Assignment Across the Spiral

**The problem.** After eight iterations the state contains dozens of proposals, test results, and findings. The system cannot answer: *which of these 200 changes actually improved the outcome?* Every change looks equally plausible. Retrying a bad change costs a full cycle. Reverting a good change loses progress. This is the exploration/exploitation problem, but the reward signal is sparse, delayed, and multi-causal.

**What to build.**

1. **Intervention logging.** Every proposal becomes an explicit intervention with a timestamp, a target, and a predicted `ExpectedEffect`. The current `CodeProposal` already has the field — it needs to be load-bearing rather than decorative.

2. **Effect attribution.** After each test run, compute per-proposal deltas against the running baseline: did the test pass/fail state change, which assertions flipped, did coverage move, did the confidence value move. Store as `Evidence{RelatedTo: []string{proposalIDs}}`.

3. **A causal ledger.** A first-class `core/causal/` package that maintains, per objective (test pass, compile success, coverage, security score), a DAG of *intervention → effect* edges with signed weights. Weight estimation via a simple credit-assignment scheme over the spiral history — start with a Shapley-style approximation over the last N interventions, then upgrade.

4. **Counterfactual replay.** Because the state is fully versioned by `revision` and every artifact carries provenance, it becomes possible to *branch the state*, revert a specific intervention, re-run the test runner, and record what would have happened. This is the single highest-value capability in the entire roadmap and it is *only* possible because of the state design. Most agent frameworks physically cannot do this — they have no reversible history.

5. **A bandit over interventions.** Once credit is assigned, replace fixed iteration with a contextual bandit over proposal strategies. Learn which kinds of change tend to produce which kinds of evidence, conditioned on the current state embedding. Early version: Thompson sampling over a small discrete strategy space. Later: a learned policy.

**Why this is lethal.** The moment the system can prove that change *N* caused pass and change *M* did not, it stops burning cycles on random perturbation and starts doing directed search. This is the difference between a spiral that converges and a spiral that just spins.

---

### PILLAR 2 — Independent Verification: Breaking the Assumption Loop

**The problem.** Code and tests are generated from the same plan, by the same template family, with the same assumptions. This is circular verification. A `security-analyst` unit exists but reviews code with the same mental model that produced it. The current `test-designer` is a template selector, not a test oracle.

**What to build.**

1. **Property extraction.** From the natural-language requirements, extract executable properties — invariants, preconditions, postconditions, algebraic laws. This requires the LLM to do real work, and is a genuinely hard generation problem. It is also where a large model earns its cost: this is a reasoning task, not a formatting task.

2. **Property-based and generative testing.** Integrate `fast-check` (Go) for property-based testing. Properties are checked against hundreds of generated inputs, not three handpicked cases. This catches the class of bugs example-based tests structurally cannot.

3. **Metamorphic testing.** Derive metamorphic relations from the specification: `f(x)` before `transform(x)` should differ in a known way; `list(items)` should be a permutation-preserving operation; `delete` then `get` should 404. These test *behavioural invariants* without needing a reference implementation — which is exactly the situation when there is no oracle.

4. **Differential testing.** Where a reference implementation exists (stdlib functions, a legacy system, a previous version), differential-test the generated code against it. Automatically mine Go's stdlib as a differential oracle source.

5. **Coverage-guided generation.** Drive `go test -coverprofile` output back into the attention manager. Uncovered branches get boosted priority. This converts the attention mechanism from "react to failures" to "react to *absence* of evidence" — which is strictly more informative, because untested code is indistinguishable from correct code until it isn't.

6. **True adversarial units.** The `security-analyst` and `critic` must be architecturally prevented from sharing context with the generator on the axis they critique. Either context isolation, or — cheaper and probably better — a *different model* on a *different capability* that was never shown the rationale. Agreement between genuinely independent reasoners is evidence. Agreement between one reasoner asked twice is not.

7. **Sanitizer integration.** Wire `-race`, `-fsanitize=address`, and `-fsanitize=undefined` into the sandbox test runner. Memory errors, data races, and UB are currently invisible to the system.

**Why this is lethal.** Every competitor in this space generates code against its own understanding and then tests it against its own understanding. The failure mode is *correlated error* — you cannot detect with the same lens that produced the defect. Independent verification is the only structural cure. A system that generates code and verifies it with genuinely different machinery is in a different epistemic category from one that does not.

---

### PILLAR 3 — Convergence, Termination, and Cost-Aware Scheduling

**The problem.** The spiral has no theory. It runs until `maxIterations`. A good iteration and a wasted iteration are indistinguishable to the scheduler. In a system with 15 units, adversarial reviews, property-based test generation, and 4096-token completions, the token cost per iteration is nontrivial and the failure mode — infinite oscillation between two architectures — is real and currently undetected.

**What to build.**

1. **A potential function on the state.** Define a scalar objective over the semi-state: test pass rate, coverage depth, conflict count, objection count, evidence-to-claim ratio, confidence spread, decision stability. This is the Lyapunov function. Monitor its trajectory across revisions.

2. **Convergence classification.** The potential function's trajectory is one of: increasing (productive), flat-saturated (converged — stop), oscillating (two-cycle — trigger forced differentiation), decreasing (regression — roll back to best-known revision).

3. **Oscillation detection.** Hash the architecture plan and decision set per revision. If revision *N* and revision *N−2* are semantically identical but the tests differ, the system is thrashing. The fix is forced exploration: mutate a design parameter deliberately rather than letting the LLM rediscover the same fork.

4. **Best-known-revision tracking.** Maintain a Pareto frontier over (test pass, token cost, complexity). The spiral should be able to stop at *any* point and return the best revision found, not the last one generated. This is a small change with an outsized effect on perceived reliability.

5. **Token and energy accounting.** Every `Evidence` records the completion tokens and wall time that produced it. Attribute cost to units. The attention manager should become cost-aware: a unit that costs 8000 tokens to produce zero actionable findings should be scheduled less. This makes the system *self-limiting* under budget pressure.

6. **Confidence-calibrated stopping.** Stop when the confidence *distribution* stops moving, not when a counter hits a ceiling. If the last three iterations produced no change in the confidence spread, the system has extracted everything it can and should surface the result rather than continue.

**Why this is lethal.** Every agent demo runs on an unbounded loop with a generous budget. A system that *proves* convergence, detects thrash, tracks the Pareto frontier, and stops at the right moment is operable as a real system. It is the difference between a research artifact and a tool.

---

### PILLAR 4 — Persisted, Branched, Searchable Project Memory

**The problem.** `persistence.PersistentMemory` exists and writes to disk, but the spiral does not *read* it. Every run starts from zero. There is no accumulation of architectural knowledge, no recognition of "we already tried this and it failed", and no capability to resume a half-finished effort.

**What to build.**

1. **Case-based retrieval.** Index every past spiral by its final state — architecture, decisions, objections raised, failures encountered, final confidence. On a new run with a similar intent, retrieve the nearest cases and inject them as *prior evidence* with explicit provenance. The architect's first hypothesis should be informed by what has already been tried.

2. **Failure memory as first-class.** A `FailureMemory` of approaches that provably did not work, with the conditions under which they failed. Inject as `Hypothesis{Status: Rejected, Contradicting: [...]}` into new runs. **This is the highest-leverage memory type** — avoiding known-bad approaches is worth more than exploring known-good ones, because exploration is already stochastic.

3. **State branching.** Because `revision` is tracked on every artifact, the semi-state can be serialized and forked. A user can inspect revision 4, branch, and try a different architecture from there. This makes the system inspectable in a way that makes the LLM's behavior *debatable* — a researcher can point at the exact revision where reasoning diverged and ask why.

4. **Reproducibility hashing.** A content hash over (state revision, unit set, provider, model, prompt templates) such that any past run can be replayed exactly. Without this, none of the memory above is trustworthy.

5. **Cross-project learning.** Aggregate failure modes across all projects. "Generated code with `-race` enabled fails 40% of the time" is a fact that should transfer.

**Why this is lethal.** Memory is the difference between a system that *can* do something and a system that *gets better at it*. A no-memory agent is a lottery ticket run repeatedly. A memory-bearing agent compounds.

---

### PILLAR 5 — Verification-Guarded Self-Modification

**The problem.** The system modifies code. Nothing verifies that the modification system itself is correct. A bug in `patcher.go` or `validation.go` silently corrupts every run, and the failure is indistinguishable from bad generated code.

**What to build.**

1. **Self-verification gates.** Before any unit's output enters the state, it passes through an independent check. Architect plans must parse as valid JSON with ≥2 components. Proposals must pass syntax validation. Evidence must reference existing state IDs. The current validation is a starting point; make it a mandatory pipeline stage with a hard reject path.

2. **Metamorphic tests of the state machine itself.** The semi-state has algebraic properties: appending evidence is monotonic, revision increments are consistent, confidence is bounded, conflict detection is symmetric. These are testable as properties of the *system*, not the code it generates. A bug here is catastrophic and currently unguarded.

3. **The system writing its own improvements.** This is the recursive step, and it must come *last*, after everything above. The security-analyst should be able to propose improvements to the generator, those proposals go through the same conflict-checked pipeline as generated code, and the improvement ships only if it demonstrably increases the potential function on held-out projects. No special path. No privileged self-modification. **The system must use its own machinery to modify itself, or the whole verification argument is circular.**

4. **Tamper-evident state.** Cryptographic chaining of the state: each revision commits to the hash of the previous one. Any retroactive edit to history is detectable. This makes the audit log a *proof* rather than a log.

**Why this is lethal.** It closes the last remaining trust gap. A system that verifies its own output and can improve itself using the same verification machinery it applies to everything else has a coherent, non-circular foundation. Every prior agent framework fails this test — they improve with human-written patches, not with their own verified loop.

---

## Cross-Cutting: The Evaluation Problem

**None of the above is worth claiming without measurement.** The following benchmarks must be built, and results published, or "state of the art" is an assertion rather than a fact.

| Benchmark | Measures | Baseline |
|---|---|---|
| **First-Attempt Pass Rate** | % of tasks whose generated code compiles and passes tests on iteration 1 | Devin, SWE-agent, Cursor |
| **Convergence Curve** | Potential function vs. iteration count, and iterations-to-target | SWE-bench trajectories |
| **Causal Attribution Accuracy** | Given the ledger, can we correctly identify which intervention caused an outcome? (Measured on synthetic injected-bug tasks) | n/a — novel metric |
| **Regression Rate** | % of accepted changes that later introduced a defect | Cursor, Aider |
| **Token Efficiency** | Tokens to reach target confidence | All |
| **Local vs. Frontier Parity** | Confidence delta between TinyLlama-class local and GPT/Claude-class, on identical semi-states | n/a — novel |
| **Audit Replay Fidelity** | Does replay from revision *N* reproduce revision *N+1* exactly? | n/a — novel |
| **Adversarial Robustness** | Success rate against injected spec ambiguity, contradictory requirements, adversarial requirement text | n/a — novel |

The last three are genuinely novel metrics with no precedent in the literature. **Publishing them is itself a contribution.**

---

## Ordered Execution Roadmap

**Phase 0 — Instrument truth (2 weeks)**
Wire causal ledger. Add token accounting to every evidence record. Build the potential function and the oscillation detector. Add `-race` to the sandbox. *Nothing else can be evaluated until the instrumentation exists.*

**Phase 1 — Independent verification (4 weeks)**
Property extraction. `fast-check` integration. Metamorphic relation mining. Coverage-guided generation feedback into attention. This is the single highest-value phase — it is what makes the output trustworthy.

**Phase 2 — Credit assignment (4 weeks)**
Effect attribution. Counterfactual replay via state forking. Bandit over intervention strategies. Expect measurable reduction in iterations-to-target.

**Phase 3 — Memory and learning (3 weeks)**
Case-based retrieval. Failure memory injection. Reproducibility hashing. Cross-project aggregation.

**Phase 4 — Convergence and budget (2 weeks)**
Pareto frontier tracking. Confidence-calibrated stopping. Cost-aware attention. Forced exploration on detected oscillation.

**Phase 5 — Recursive self-improvement (open-ended, strictly last)**
State-machine property tests. Verification gates on unit output. Tamper-evident chaining. Then, and only then, self-modification through the identical pipeline.

---

## The Thesis

The current generation of AI coding systems is fundamentally prompt-engineering: better prompts, more context, more retries, on a stateless model. The ceiling is set by the architecture, not the model. Scaling a frontier model improves sample quality but does not create memory, does not create causal attribution, does not create independent verification, and does not create convergence.

Semi-State is a bet that the *substrate* is the leverage. Not the model. The substrate — because a substrate with typed claims, bidirectional evidence, provenance, revisions, and a proposal pipeline can do things that no amount of model scale enables: it can prove why it decided something, discover that it was wrong, revert specifically, branch a hypothesis, terminate on a principle rather than a timer, and improve itself using the same verification it applies to its output.

Most systems cannot even *state* the question "which change caused this result." This one can.

The five pillars are ordered by leverage. Pillar 1 (causal attribution) and Pillar 2 (independent verification) are the load-bearing pair — the first makes the search directed, the second makes the results trustworthy. Everything after is optimization.

The work is tractable. The architecture is already correct enough to build on. What remains is engineering rigor applied to an idea that is already ahead of the field.
