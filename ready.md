# readiness.md

**Question:** Is Spiral CodeMaker ready to iterate on its own codebase?

## Answer: **No.**

Not "soon." Not "with caveats." The architecture is correct and the instrumentation exists, but self-modification requires a safety apparatus that is not merely unfinished — **it is absent**, and three of the four Pillar 5 gates do not exist at all.

This document separates what is genuinely ready from what must be built, and states the ordering constraint explicitly, because the ordering is the point: building the remaining features in the wrong order creates a system that can rewrite the code that verifies it.

---

## 0. The one-paragraph version

Semi-State can now *measure* itself honestly (potential function, causal ledger, counterfactual replay, convergence phases) and it can *generate* small Go projects that compile and pass their own tests. What it cannot do is safely edit `core/units/*.go`, because there is no golden-output baseline to detect a regression, no property tests over the state machine's algebraic invariants, no tamper-evident history, and no canary branch. A system that can rewrite its own verifier without a way to prove the verifier still works is not self-improving — it is a random walk with a compiler attached.

---

## 1. READY — verified working, no further work required

Everything in this section was checked by execution, not by reading intent.

### 1.1 Local LLM infrastructure — **READY**

| Item | Status |
|---|---|
| llama.cpp CUDA 12.4 | Installed, `third_party/llama-cpp` |
| TinyLlama-1.1B Q4_K_M | 638 MB, serving |
| Qwen2.5-7B-Instruct Q4_K_M | 4.68 GB, loaded and serving on `:8080` |
| Provider abstraction | GGUF, HuggingFace, Ollama, Mock |
| Capability routing | `CapFast/Reasoning/Specialize/Classification/Embed` |
| Model auto-selection | `bestLocalModel()` picks largest GGUF |
| Race detector | **Enabled** — cgo on, MSYS2 gcc 16.2.0 |
| Coverage profiling | `-coverprofile=coverage.out` on every run |

Confirmed: `go test ./... -race` passes. cgo was `CGO_ENABLED=0` with no reachable compiler; fixed via `go env -w` plus a runtime `ensureCC()` PATH resolver in `execution/tests/runner.go`.

### 1.2 Phase 0 measurement instrumentation — **READY**

This is the most advanced part of the system and it is complete and tested.

| Component | File | Tests |
|---|---|---|
| Lyapunov potential function | `core/converge/potential.go` | 23 |
| Convergence classification | `core/converge/convergence.go` | (shared) |
| Causal ledger | `core/causal/ledger.go` | 21 |
| Counterfactual replay | `core/causal/replay.go` | (shared) |
| Pareto frontier | `core/converge/convergence.go` | (shared) |

- Potential function is **pure** (same state in → same value out), which is what makes trajectory deltas attributable.
- Convergence phases: `productive` / `converged` / `oscillating` / `regressing`, with `ShouldStop`, `ShouldRollback`, `ForceDifferentiate`.
- State fingerprinting excludes timestamps and file contents, so two revisions are compared on *semantics*.
- Ledger assigns signed credit per intervention; regressions are weighted 1.5× super-linearly.
- Replay hash-verifies snapshots and **refuses to run a trial against an unverifiable snapshot**.
- Artifacts written to `instrumentation/phase0.json` — fully auditable.

### 1.3 Test suite — **READY (118 tests, all passing)**

```
core/causal            21      tests/attention        7
core/converge          23      tests/conflicts        4
tests/state            15      tests/events           6
tests/models           11      tests/integration     22
tests/proposals         6      tests/spiral           5
tests/units             4      tests/models/gguf      1
```

`go build ./...` clean, `go vet ./...` clean, `go test ./...` green.

### 1.4 Epistemic machinery — **READY**

- Claim lifecycle `UNVERIFIED → SUPPORTED/CONTRADICTED → RESOLVED/REJECTED`
- `ContradictingEvidence` as a first-class queryable field
- `Confidence` as a named type on claims *and* evidence
- `Provenance{UnitID, Revision}` on every artifact
- Attention manager: decaying weights, event-driven boosts, `minThreshold` gating
- Proposal pipeline with graded conflict detection and status transitions
- Rival architecture recorded as a live `PROPOSED` hypothesis (not pre-rejected — that would be an unearned claim)

### 1.5 Code generation — **READY for small projects, NOT for self-modification**

Verified end-to-end with both TinyLlama and Qwen2.5-7B:

- CLI tool intent → `cmd/cli/main.go` + `pkg/core/core.go` + tests → compiles, tests pass, runs correctly
- HTTP service intent → `cmd/server/main.go` + handlers/store/model → compiles, tests pass under `-race`
- 0.75 confidence, convergence phase reported

**Critical limitation:** code generation is **template-driven**, not LLM-driven. The LLM path is disabled in `code_generator.go` because small models cannot reliably emit the `---FILE:` format. Templates produce *template-shaped* code. They cannot produce the novel, plan-specific structure that self-modification requires.

---

## 2. NOT READY — hard blockers for self-modification

### BLOCKER 1 — No regression baseline **← FATAL**

**Verified:** no golden files, no baseline snapshots, no regression harness of any kind exists.

Self-modification means editing files that other units depend on. Without a recorded expected-output baseline, there is no way to distinguish "the system improved" from "the system changed and you cannot tell what broke." The 118 existing tests cover *current* behaviour, not *expected* behaviour — so a self-edit that breaks an untested path passes silently.

**Must build:**
1. Golden-output fixtures for the JSON shapes the system itself depends on: `SemiState` serialization, `TestResult`, `CodeProposal`, potential/ledger artifacts.
2. A `RegressionGate` that runs before any self-edit is applied and after.
3. Snapshot tests of the full pipeline output for a fixed seed and fixed model, so output drift is visible.

**Why fatal:** without this, a bad self-edit is undetectable. This is the single blocking item.

### BLOCKER 2 — No property tests over the state machine

**Verified:** `tests/state/state_test.go` has 15 example-based tests. No property-based tests. The `fast-check` dependency is not present.

The state machine's algebraic invariants are what self-modification would violate first. None are asserted:

- Revision monotonicity under concurrent `IncrementRevision`
- `Confidence` bounds invariant under arbitrary mutation
- `Snapshot()`/`Clone()` round-trip fidelity
- Conflict symmetry (if A conflicts-with B then B conflicts-with A)
- Claim status machine legality (illegal transitions must be impossible, not merely unobserved)
- Ledger idempotence: `Record` twice → one intervention
- Ledger credit conservation: Σ credit == observed delta

**Must build:** property-based tests over these invariants. A self-edit to `semi_state.go` that breaks revision monotonicity must be *caught by the machine*, not by inspection.

### BLOCKER 3 — No tamper-evident history

**Verified:** no hash chaining in `core/state/semi_state.go`.

If the system can rewrite its own source, it can also rewrite the records that justify the rewrite. A log that the mutator can edit is not evidence. Each revision must commit to the hash of the previous revision so retroactive history editing is detectable.

**Must build:** `StateHash` chained across revisions; `Snapshot.Verify` extended to the revision chain; a `VerifyChain()` that walks from genesis.

### BLOCKER 4 — No self-modification path exists

**Verified:** no `SelfModify` / `ModifySelf` / canary / shadow-branch code anywhere in `core/`.

This one is *not* a bug — it is correct that it does not exist yet. It is listed so the ordering is unambiguous: **it must be built last**, after Blockers 1–3, and it must route through the identical proposal → validate → conflict-check → apply pipeline as generated code. A privileged self-modification path would invalidate the entire verification argument, because the verifier would be exempt from the rules it enforces.

### BLOCKER 5 — Circular verification

**Verified by construction:** `test_designer.go` and `code_generator.go` share the same template family. The test for a store tests a store generated by the same template that wrote it.

This is tolerable when generating throwaway projects. It is **fatal for self-modification**: if unit A's bug is mirrored by unit B's test, the system's verification cannot detect A's class of error, and A is now editing the system's own logic.

**Must build:** Pillar 2 — property extraction, `fast-check`, metamorphic relations, differential testing. Independent verification is the structural cure, and for self-modification it is mandatory rather than merely valuable.

### BLOCKER 6 — No canary / shadow branch

**Verified:** no canary, shadow, or branch isolation.

A self-edit that passes tests locally may still regress the pipeline non-deterministically (LLM sampling, timing, attention decay ordering). There is no mechanism to trial an edit on a parallel branch and compare potential function before/after.

**Must build:** state forking (the `Replayer` primitives already exist and are tested — this is the cheapest of the six blockers) plus a canary harness that runs the full pipeline on both branches and promotes only on improvement.

---

## 3. The ordering constraint

The blockers are **not** independent. Sequence matters:

```
BLOCKER 1  Regression baseline          ← nothing below is safe without it
    ↓
BLOCKER 3  Tamper-evident chaining     ← trust the history you are about to extend
    ↓
BLOCKER 2  State-machine properties     ← catch the edit the baseline misses
    ↓
BLOCKER 6  Canary / shadow branch      ← trial the edit before promoting it
    ↓
BLOCKER 5  Independent verification    ← the tests themselves must not be circular
    ↓
BLOCKER 4  Self-modification path      ← LAST. Rides the identical pipeline.
```

**Building BLOCKER 4 before BLOCKER 1 produces a system that silently rewrites itself with no way to detect a regression. That is strictly worse than the current system, which at least cannot damage itself.**

---

## 4. Additional gaps (not blocking, but required for the claim)

| Gap | Impact |
|---|---|
| No benchmarks | "State of the art" is currently an assertion. Need First-Attempt Pass Rate, Convergence Curve, Token Efficiency, Local-vs-Frontier Parity |
| No LLM-driven code gen | Templates only. Cannot produce novel structure. Re-enable behind validation gates once a capable model is the default |
| Joint attribution is an upper bound | Ledger cannot isolate credit among N simultaneous interventions. Exact attribution needs counterfactual replay per intervention — machinery exists, not yet scheduled |
| Potential function not proven Lyapunov | Empirically useful, caught a real bug, but no proof of strict descent. Treat as heuristic |
| No persistent memory read-back | `PersistentMemory` writes but the spiral never reads it. No accumulation across runs |
| No cost-aware attention | Token counts are captured but not used to down-weight expensive, unproductive units |
| `CodeProposal.ExpectedEffect` decorative | The field exists and is never checked against actual effect. This is the hook for counterfactual prediction — unused |

---

## 5. Minimum bar to flip the answer to "Yes"

1. Regression baseline green, committed, and **enforced by a gate that blocks on failure**
2. State-machine property tests in CI, covering revision monotonicity and claim-transition legality
3. `StateHash` chaining implemented and `VerifyChain()` passing from genesis
4. Canary harness promoting a self-edit only when potential function improves on the shadow branch
5. At least one non-circular verification axis (property-based tests over generated code)
6. Self-modification routed through the *same* `Proposer.ValidateAndCheck` → `Workspace.ApplyProposal` path as generated code, with **no special case**

Items 1–4 are mechanical. Item 5 is research. Item 6 is mostly discipline.

**Estimated: 3–4 weeks for 1–4 and 6, unbounded for 5.**

---

## 6. Honest caveats about this document itself

- Every "verified" claim above was checked by execution or by grep against the source, not by reading design docs. The four NOT-IMPLEMENTED findings came from direct searches.
- I have not run the system against its own codebase even in read-only mode, so I cannot rule out additional blockers that only appear when the target is `core/units/*.go` rather than a generated throwaway project. Assume more work than listed.
- The test counts are from `^func Test` greps, which undercount subtests and table-driven cases.
- Blocker 1 is called fatal on the reasoning that undetectable regression makes self-modification worse than none. That is a judgement, not a theorem, but it is a judgement I would defend.
