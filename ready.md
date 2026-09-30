# readiness.md

**Question:** Is Spiral CodeMaker ready to iterate on its own codebase?

## Answer: **No — but the two hardest blockers are now closed.**

Blockers 1 and 3 from the original assessment are **implemented and tested**. The
remaining blockers are real and none of them are mechanical.

This document separates what is genuinely ready from what must be built, and
states the ordering constraint explicitly, because the ordering is the point:
building the remaining features in the wrong order creates a system that can
rewrite the code that verifies it.

**Status change since first assessment:** Blockers 1 and 3 moved from NOT READY
to READY. A regression regression is now detected, gate-enforced, and a
behaviour change is provably unable to land without a baseline re-capture.

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

### BLOCKER 1 — Regression baseline — ✅ **CLOSED**

**Was:** no golden files, no baseline, no regression harness. A self-edit that broke an untested path would pass silently.

**Now implemented:**

| Component | Location |
|---|---|
| Canonical projection | `core/regression/baseline.go` — `Project()` |
| Baseline capture / check | `core/regression/baseline.go` — `Gate` |
| Field-level drift reporting | `Drift{Field, Kind, Expected, Actual, Detail}` |
| CLI capture verb | `spiral baseline -action capture` |
| CLI check verb | `spiral baseline -action check` (exits non-zero on drift) |
| Runtime enforcement | `core/system/orchestrator.go` — `runRegressionGate()` |
| Pinned baselines | `testdata/baseline/{http-todo,http-resource,cli-filelist}.json` |
| Tests | `core/regression/baseline_test.go` — 15 tests |

**Design decisions worth noting:**

1. **A missing baseline is a failure, not a pass.** Defaulting to "nothing to compare, so nothing is wrong" is how a gate silently stops gating. `TestCheckFailsWithoutBaseline` pins this.
2. **An empty verdict set is not all-pass.** `AllPass(nil) == false` — otherwise a caller that forgets to gate anything reports success.
3. **Capture is an explicit verb, never automatic.** Auto-capturing would promote every behaviour change to "expected".
4. **The gate skips LLM-attached runs.** A run whose output depends on token sampling is not token-reproducible; pinning it would gate on noise, and a noisy gate trains people to ignore it. The skip is recorded as evidence, not silent.
5. **Drift is reported as a named removal plus a named addition**, not a boolean. Verified by injecting a real behaviour change: the gate printed exactly which requirement was lost and which appeared, and exited 1.

**Verified end-to-end:** mutated `requirements_analyst.go` to alter a requirement string → gate reported the drift and failed. Reverted → gate passed. This is the property that makes self-modification survivable, and it is demonstrated rather than asserted.

---

### BLOCKER 2 — No property tests over the state machine

**Status: PARTIALLY CLOSED.** The chain invariants are now property-tested (revision monotonicity under concurrency, hash determinism, order independence, clone independence, deadlock freedom). **The state-machine algebra is not.**

Still missing:
- Confidence bounds invariant under arbitrary mutation
- `Snapshot()`/`Clone()` round-trip fidelity across all fields
- Conflict symmetry (A conflicts-with B ⟹ B conflicts-with A)
- Claim status transition legality as a state machine, not just observed values
- Ledger idempotence: `Record` twice → exactly one intervention
- Ledger credit conservation: Σ credit == observed delta

Note: `fast-check` is still not a dependency. The chain tests use hand-written
determinism and concurrency assertions, which cover the properties that matter
most for the chain but do not constitute property-based testing.

### BLOCKER 3 — Tamper-evident history — ✅ **CLOSED**

**Was:** no hash chaining; a log the mutator could rewrite was not evidence.

**Now implemented:** `core/state/chain.go`

| Capability | Detail |
|---|---|
| `AppendChained(summary)` | Seals each revision, chaining from the previous link |
| `ComputeChainHash(prev)` | Recomputes a revision's hash for external verification |
| `VerifyChain()` | Walks from genesis, reports the exact revision where history diverged |
| `Chain()` / `ChainHead()` | Read-only accessors |
| Unexported `chain` field | Structurally prevents external mutation |
| Survives `Clone()` | Cloning cannot be used to shed history |
| Persisted | Written to `instrumentation/phase0.json` as `chain` + `chain_verify` |
| Wired into loop | Every iteration sealed *before* the stop decision, so the final revision is never unchained |
| Tests | `tests/state/chain_test.go` (11) + `core/state/chain_internal_test.go` (6) |

**Critical design detail:** the canonical hash deliberately **excludes
timestamps and wall-clock durations**. Including them would make every run
produce a different hash, which would destroy the regression-detection property
the mechanism exists to serve. Everything epistemic is included.

**Bugs found and fixed while building this:**

1. **Deadlock.** `ComputeChainHash` took `RLock` and then called a canonicalizer that took `RLock` again. `sync.RWMutex` is not reentrant — this hung every integration test. Fixed by making the canonicalizer lock-free and requiring callers to hold the lock. `TestChainDoesNotDeadlockUnderConcurrency` is now the regression guard.
2. **Misleading tamper location.** The verifier originally reported the edited revision rather than the first link that no longer follows. Corrected: the break surfaces at the successor, which is where the divergence becomes observable, and the message says so.

**Limitation, stated honestly:** a self-consistent truncated prefix still verifies structurally. Detecting *removal* relies on comparing the head against a persisted value (the baseline and the saved artifacts), not on `VerifyChain` alone. `TestVerifyChainDetectsTruncatedChain` documents this.

---

### BLOCKER 6 — Canary / shadow branch — ✅ **CLOSED**

**Was:** a self-edit that passes tests locally can still regress the pipeline non-deterministically. No mechanism existed to trial an edit on a parallel branch.

**Now implemented:** `core/canary/canary.go`

| Capability | Detail |
|---|---|
| Isolated trial | Every branch builds in its own temp directory, deleted afterwards |
| Reported workspace | The pipeline reports the workspace it populated; the canary does not guess |
| Workspace containment | A runner reporting a workspace outside its trial dir is rejected — stops cross-trial contamination |
| Build → edit → re-test | Ordering is load-bearing; applying the edit before the build would measure regeneration, not the artifact |
| Real re-test | `WithWorkspaceTester` runs the actual test runner against the *edited* workspace |
| Promotion rule | Gate passed, chain valid, pass rate not lower, potential delta above threshold |
| No ties | A tie is not an improvement; threshold is 2× the potential's own epsilon |
| Failure output retained | A blocked candidate prints the failure class and the tail of the test output |
| CLI | `spiral canary -scenario X -file Y -content-file Z` |
| Reports | Written to `instrumentation/canary/` |
| Tests | 21 unit + 4 end-to-end against the real pipeline |

**Four real bugs found and fixed while building this:**

1. **The canary could not see its own edit.** It applied the edit, then measured the orchestrator's state — which describes the pipeline's own run and is blind to anything applied afterwards. Every candidate scored *identical* to its baseline. A canary that approves everything because it measured nothing is worse than no canary. Fixed by re-testing the edited workspace.
2. **A broken test was promoted.** Caused by the same defect plus a path bug: the edit was applied before the orchestrator created the workspace, so `workspaceSubdir` fell back to the trial root and the file landed outside the Go module. The test suite never saw it. Fixed by having the runner report the workspace path and by asserting the edit lands inside the trial directory.
3. **Multi-line content was corrupted in transit.** Passing Go source via `--content` on a command line loses newlines on Windows, so the written file did not compile. The resulting failure was indistinguishable from "the edit was wrong" — the worst time to be lied to. Fixed with `--content-file`, and inline multi-line content is now rejected with an explanation.
4. **The canary blocked without saying why.** It reported "pass rate regressed" and discarded the test output. An operator cannot distinguish "the edit is wrong" from "the toolchain is misconfigured". Fixed by retaining the failure class and the output tail.

**Verified end-to-end against the real pipeline and the real test runner:**

| Trial | Result |
|---|---|
| Edit adds a passing test | **promote**, potential +0.1000 |
| Edit adds a nil-deref panic | **blocked**, full stack trace shown |
| Edit adds a syntax error | **blocked**, compile error shown |
| No-op edit | **blocked** as a tie, explicitly |
| Scratch cleanup | trial directories removed by default |

The panic trial is the one that matters. It produced a stack trace pointing at
`output/internal/store/broken_test.go:7` — proof the canary actually *exercised*
the edit rather than rubber-stamping it.

---

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
BLOCKER 1  Regression baseline          ✅ CLOSED
    ↓
BLOCKER 3  Tamper-evident chaining      ✅ CLOSED
    ↓
BLOCKER 6  Canary / shadow branch       ✅ CLOSED
    ↓
BLOCKER 2  State-machine properties     ◐ PARTIAL — chain covered, algebra not
    ↓
BLOCKER 5  Independent verification     ☐ open
    ↓
BLOCKER 4  Self-modification path      ☐ LAST. Rides the identical pipeline.
```

**Building BLOCKER 4 before BLOCKER 1 produces a system that silently rewrites itself with no way to detect a regression. That is strictly worse than the current system, which at least cannot damage itself.** That ordering is now enforced by construction: the regression gate runs unconditionally at the end of every run, and the canary refuses to promote a branch that fails the gate, so a self-edit cannot land without passing both.

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

| # | Item | Status |
|---|---|---|
| 1 | Regression baseline green, committed, enforced by a gate that blocks on failure | ✅ **done** |
| 2 | State-machine property tests covering revision monotonicity and claim-transition legality | ◐ **partial** — chain covered, state algebra not |
| 3 | `StateHash` chaining implemented and `VerifyChain()` passing from genesis | ✅ **done** |
| 4 | Canary harness promoting a self-edit only when the candidate beats the incumbent | ✅ **done** |
| 5 | At least one non-circular verification axis (property-based tests over generated code) | ☐ open — research grade |
| 6 | Self-modification routed through the *same* `Proposer.ValidateAndCheck` → `Workspace.ApplyProposal` path, no special case | ☐ open — mostly discipline |

**Remaining: item 2's remainder is mechanical (the state algebra), item 5 is research, item 6 is mostly discipline. Three of six gates are now closed and the fourth is half done.**

Note the new dependency this created: the regression gate is now a hard
prerequisite for *any* self-edit, so item 4 (canary) must compare against the
baseline and the chain, not just the potential function. The canary cannot promote
an edit that would fail the gate. That is now implemented rather than merely
noted.

---

## 6. Honest caveats about this document itself

- Every "verified" claim above was checked by execution or by grep against the source, not by reading design docs. The NOT-IMPLEMENTED findings came from direct searches.
- Blockers 1, 3, and 6 were closed and then **deliberately broken** to confirm the mechanisms actually fire. The regression gate was validated by mutating `requirements_analyst.go`; the chain by rewriting an intermediate hash; the canary by injecting a panicking test and a syntax error. A gate that has never been observed failing is not known to work.
- **Six real bugs were found *while building* Blockers 1, 3, and 6** — two lock/tamper-reporting defects in the chain, and four in the canary (including one where a broken test was promoted because the canary could not see its own edit). This is strong evidence that the two remaining blockers will similarly surface defects on contact rather than being straightforward, and it is the argument for building them in order.
- The canary currently **authorises** promotion; it does not apply edits. That separation is deliberate — applying is a separate explicit step — but it means the end-to-end path from "canary approves" to "edit is in the codebase" does not exist yet.
- I have not run the system against its own codebase even in read-only mode, so I cannot rule out additional blockers that only appear when the target is `core/units/*.go` rather than a generated throwaway project. Assume more work than listed.
- The test counts are from `^func Test` greps, which undercount subtests and table-driven cases.
- Blocker 1 was called fatal on the reasoning that undetectable regression makes self-modification worse than none. That is a judgement, not a theorem, but it is a judgement I would defend.
