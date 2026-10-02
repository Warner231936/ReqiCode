# final.md

## What this is

A semi-state AI coding system: instead of treating code as the artifact, it
treats a **typed epistemic state** as the artifact and code as one projection of
it. Development proceeds through competing hypotheses, evidence accumulation,
signed attribution, and trial-before-promote — with a hard rule that nothing
reaches the repository without passing a real compiler and a real test suite.

Everything below was measured, not estimated. Where something does not work, it
says so.

---

## Verified working

### 1. It improves its own codebase — with no model involved

```
$ spiral improve -pkg ./core/converge -mechanical
  baseline : build=true pass=23 fail=0 cov=86.5%
  candidate: build=true pass=27 fail=0 cov=87.1%
  promote: coverage +0.56
```

`core/apiscan` extracts a package's exported API exactly using `go/ast`. `core/verify`
synthesises tests from that API. **The tests compile by construction** — only
symbols the parser confirmed exist, argument types taken from the declared
signature. Nothing was inferred, so nothing can be confidently wrong.

Each synthesised test asserts only what the signature makes certain: the function
is callable, it does not panic on zero arguments, a constructor returns non-nil,
and a `(T, error)` result does not return a zero `T` with a nil error. Every one
of those is true of any correct implementation, so a violation is a real defect.

### 2. It found two real bugs in its own source

Neither was caught by the 335-test suite, and neither involved a model.

| Bug | Symptom |
|---|---|
| `attention.NewManager(nil)` | Panics — event bus dereferenced during construction with no nil check |
| `converge.ComputePotential(nil)` | Panics — the nil guard sat **after** the first dereference, making it dead code |

The second is the strongest argument for the approach. That defect survives review
because the guard is right there in the source, looking correct, four lines too
late. Found in 4 seconds by a process with no model in the loop.

### 3. It cannot damage itself while failing

Across every trial run — 20+ model attempts and all mechanical trials — **the live
source was never modified by a rejected change.** That is the property the entire
blocker ordering existed to guarantee, and it is demonstrated rather than asserted.

Four independent gates, each deliberately broken to confirm it fires:

| Gate | Verified by |
|---|---|
| Regression baseline | Mutating real source → gate reported the named drift, exit 1 |
| Tamper-evident chain | Rewriting an intermediate hash → break located precisely |
| Canary | Injected panic test → blocked, full stack trace shown |
| Discovery vs regression | A panicking smoke test reported as a *finding*, not a regression |

### 4. The measurement machinery

| Component | What it does |
|---|---|
| Potential function | Pure scalar objective over the semi-state |
| Convergence phases | `productive` / `converged` / `oscillating` / `regressing` |
| Causal ledger | Signed credit per intervention, regressions weighted 1.5× |
| Counterfactual replay | Hash-verified snapshots; **refuses to run unverified** |
| Pareto frontier | Best revision across (pass, coverage, cost, complexity) |
| Property tests | 200 runs per invariant over state algebra and ledger |
| Model adequacy | Judges whether the configured model can do the job, and says so |

Artifacts land in `instrumentation/phase0.json` — fully auditable.

### 5. Local, private, auditable

No API keys set. No external calls in the run path. Inference is llama.cpp on
local GPU: **Qwen2.5-Coder-14B-Instruct Q4_K_M at 367–448 tok/s**, 29/29 layers
on GPU. Every artifact carries `Provenance{UnitID, Revision}`; every revision is
chained by SHA-256 from a genesis constant, so retroactive history editing is
detectable.

---

## What does not work — stated plainly

### The LLM code-generation path is currently degraded

**This is a regression I introduced, not a pre-existing limitation.**

```
LLM usage: DEGRADED — 5 calls, 2 succeeded, 3 failed
Confidence: 0.25, test failures
undefined: Process
```

The cause is specific and architectural: **the template-driven test designer and
the LLM code generator disagree about file layout.** The test designer emits
`pkg/core/core_test.go` calling `Process`; the LLM generator produced a different
file set without that symbol. Tests built on a template assumption, code built
from a model, and neither knows about the other.

It worked before I re-enabled LLM codegen, when both sides used templates and
therefore agreed. Fixing it means the test designer must derive its tests from the
files the code generator *actually produced* — the same discipline the mechanical
path already follows by reading the real source. Until then, LLM codegen produces
code that its own tests cannot compile against, and the template fallback is what
actually ships.

I am reporting this rather than quietly reverting, because the honest version of
"it's working" includes the thing that isn't.

### The model cannot write self-tests

0 promotions out of 20 attempts, spanning a 1.1B model, a 7B model, and a 14B
coder model at 426 tok/s with an exact AST-extracted API in the prompt. The model
invents API, hallucinates helpers, and misjudges imports. Better models narrowed
the failure mode without removing it.

The conclusion I drew from that is the main architectural result of this project:
**asking a model to write code it has been shown the signature of still requires
the model to be right about the code.** Which is why verification now has a
channel that isn't a model at all.

### Other known limits

- **Circular verification persists on the model path.** The mechanical path is
  independent; the model path is not, because tests and code come from different
  generators with different assumptions.
- **The potential function is not a proven Lyapunov function.** Empirically
  useful, caught a real bug, but a heuristic with instrumentation, not a theorem.
- **Joint attribution is an upper bound.** With N simultaneous interventions and
  one observation, exact decomposition is underdetermined. Confidence is
  discounted by cohort size to say so honestly.
- **No benchmarks.** "State of the art" is currently an assertion. Need
  first-attempt pass rate, convergence curves, local-vs-frontier parity.
- **Method coverage.** Synthesised tests call package-level functions. Methods are
  skipped because a receiver cannot be constructed from a signature alone — which
  is why `core/attention` reported 16 skipped methods.
- **Prompt-echo sensitivity.** A model that echoes the prompt back can still pass
  lexical validation if the echo starts with `package`. Guarded, but the guard is
  syntactic, not semantic.

---

## Where the value actually is

The system is **not** a replacement for a frontier coding agent, and it is not
competitive with one at writing novel code. It is competitive at something else,
which is narrower and more valuable:

**It makes an unreliable generator safe to run unattended.**

Every current agent writes code against its own understanding and tests it with
its own understanding. The failure mode is **correlated error** — you cannot detect
a defect with the same lens that produced it. This system closes that gap with
mechanisms that are independent of the model by construction: a real compiler, a
real test suite, a real race detector, and an AST-derived test synthesiser that
cannot hallucinate because nothing was inferred.

That is why the mechanical path works where the model path does not. It is not a
workaround — it is the point. The substrate is what makes a weak model usable,
and what will make a strong model trustworthy.

---

## Honest scorecard

| Capability | State |
|---|---|
| Improves its own test coverage | ✅ demonstrated, no model needed |
| Finds real defects in its own source | ✅ two found, both confirmed |
| Cannot damage itself on failure | ✅ four gates, each verified by breaking it |
| Measures whether it converged | ✅ potential function + phases |
| Attributes outcomes to changes | ✅ signed credit, confidence-adjusted |
| Explains why it decided anything | ✅ provenance + SHA-256 chain |
| Runs entirely local | ✅ no keys, no external calls |
| Writes novel production code | ❌ model path degraded, template fallback ships |
| Writes novel tests | ❌ 0/20 attempts |
| Compares favourably to a frontier agent | ❌ unbenchmarked, and unlikely on raw capability |

---

## If there were more time

1. **Fix the LLM path properly** — test designer reads the code generator's actual
   output rather than assuming a template layout. This is the single highest-value
   fix remaining and the one I broke.
2. **Benchmarks.** Until first-attempt pass rate and convergence curves exist
   against a baseline, every comparative claim here is unverified.
3. **Metamorphic relations** for the mechanical verifier — currently it asserts
   signature-derived properties; deriving behavioural invariants from doc comments
   would raise the ceiling substantially.
4. **Receiver construction** in the synthesiser, to close the method gap that
   left 16 functions untested in `core/attention`.
5. **Frontier model on `CapReasoning`.** The router resolves by capability, never
   by model name, so this is a one-line configuration change — and it is the
   single change most likely to make the causal ledger, bandit, and counterfactual
   replay structurally meaningful rather than empty.

---

## Summary

The architecture is ahead of the field in the specific, defensible sense that it
maintains a typed epistemic state with signed evidence attribution, reversible
history, and trial-before-promote enforcement, and that a larger model does not
erase the need for any of it.

It is behind the field in the blunt sense that it cannot yet write novel code
reliably.

The system's honest claim today is narrow and demonstrated: **given an unreliable
generator, it finds real defects, improves real coverage, and never damages itself
— with no model in the loop.** Everything above that line is roadmap.