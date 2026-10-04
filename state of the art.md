# State of the Art

**Status document. Everything below was measured, not estimated.**

Last revised after the independent-verification milestone. Where a claim is
unbenchmarked or unproven, it says so.

---

## The Position in One Paragraph

Every AI coding agent today shares one assumption: **code is the artifact**. Read
the repo, write files, run tests, retry. The LLM is stateless between attempts;
the context window is the only memory. That architecture has a ceiling which is
set by the substrate, not the model — no amount of model scale creates memory,
attribution, reversibility, or convergence.

This system replaced the substrate. The artifact is a **typed epistemic state**;
code is one projection of it. It now holds five of the six properties the
original design argued were missing from the field, and it has discovered that
the sixth — independent verification — is best obtained *by removing the model
from the loop entirely* rather than by prompting one more carefully.

---

## What Is Built And Measured

### 89 source files · 42 test packages · 335 tests · 10 commits

| Property | State | Evidence |
|---|---|---|
| Typed epistemic state with claim lifecycle | ✅ | `UNVERIFIED → SUPPORTED/CONTRADICTED → RESOLVED/REJECTED`, terminal states enforced in the state so no caller bypasses |
| Signed causal attribution | ✅ | Per-intervention credit, regressions weighted 1.5×, confidence discounted by cohort |
| Counterfactual replay | ✅ | Hash-verified snapshots; **refuses to run a trial against an unverifiable snapshot** |
| Tamper-evident history | ✅ | SHA-256 chain from genesis; break located at the first non-following link |
| Principled convergence | ✅ | Potential function + `productive`/`converged`/`oscillating`/`regressing` classification |
| Trial-before-promote | ✅ | Isolated branch, real compile, real tests, promotion only on numeric gain |
| **Self-modification** | ✅ **model-free** | 23→27 tests, 86.5%→87.1% coverage, no model involved |
| **Self-defect-discovery** | ✅ **two confirmed** | Found by the system about itself, in seconds, no model |
| Property tests over the state algebra | ✅ | 200 runs per invariant; two real bugs caught |
| Local, private, auditable | ✅ | No API keys, no external calls, everything inspectable |

---

## The Three Findings That Were Not Predicted

These came out of building it, and each is more interesting than the original
design hypotheses. They are what I would defend as novel.

### Finding 1 — Verification improves by removing the model, not by improving it

Asked to write a test for `regression.Project`, the system failed **20 times out
of 20**: a 1.1B model, a 7B model, then a 14B coder model at 426 tok/s with an
exact API listing extracted from the AST and placed directly in the prompt.

Better models narrowed the failure mode. None removed it. The failure was always
the same class — invented helpers, misjudged imports, guessed field names.

The conclusion is architectural:

> **Asking a model to write code it has been shown the signature of still requires
> the model to be right about the code.**

So the verification path was rebuilt with no model in it. `core/apiscan` extracts
a package's exported API using `go/ast`, rendering types back from the AST so
generics and pointer receivers are correct by construction. `core/verify`
synthesises tests from that API that **compile by construction** — only symbols
the parser confirmed exist, argument types taken from the declared signature.

Nothing was inferred, so nothing can be confidently wrong. This is the first
verification channel in the system with no hallucination surface, and it is the
part that actually works.

### Finding 2 — The system found two bugs its own 335-test suite missed

| Bug | Why it survived |
|---|---|
| `attention.NewManager(nil)` panics | Event bus dereferenced during construction, no nil check |
| `converge.ComputePotential(nil)` panics | The nil guard sat **after** the first dereference — dead code announcing an intent the function didn't honour |

The second is the more interesting defect, and it is the strongest argument for
the whole approach. It survives code review precisely *because* the guard is
visible in the source, looking correct, four lines too late. No amount of
reading the function catches it. Calling it with a nil argument catches it
instantly.

A verifier that finds a nil dereference in four seconds, having never been told
to look for one, is the capability the architecture was aiming at.

### Finding 3 — A regression and a discovery are opposites, and the harness must tell them apart

The first mechanical trial against `core/attention` produced a test that panicked
on `NewManager(nil)`. The harness classified it as "introduced 1 failing test" and
discarded the change.

That was wrong. The smoke test had *found a nil-safety defect in existing code* —
the highest-value output a verifier can produce. Treating it as a regression would
have made the system discard its own findings, which is the specific failure mode
that makes agents untrustworthy.

`Classify` now separates `regression` from `discovery`, and a discovery is reported
with its failure output attached. A finding a reader cannot check is an assertion.

---

## The Substrate Advantage, Stated Precisely

Let a task require *N* steps, of which *m* are verifiable.

| | Frontier agent | This system |
|---|---|---|
| Memory of its own process | O(1) — the window | O(N) — typed state |
| Which change helped | unanswerable | O(N) ledger, signed credit |
| Independent check | unavailable | O(m) executions + AST-derived synthesis |
| Reversibility | none | revision fork + hash-verified replay |
| Convergence | undefined | potential function + phase classification |
| Cost of step *k* | full context resend | O(1) incremental |
| Self-diagnosis | none | `modelcheck` judges its own model adequacy |

Per-call capability, a frontier model is vastly better. Per-*process* capability,
this is a different category — and it is what compounds.

**The crossover is explicit:** this system wins when `N × (cost of getting it
wrong)` exceeds `N × (cost of the substrate)`. That threshold is a few dozen
steps on a codebase you care about. Below it, use the frontier agent. Being
precise about where that line sits is most of the credibility of the claim.

---

## What Is Honest About The Limits

### The model cannot write self-tests — and larger models did not fix it

0/20 promotions, across three model sizes, with an exact API in the prompt. This
is the central negative result, and it is why the model-free path exists.

### The LLM code-generation path is currently degraded

This is a **regression I introduced**, not a pre-existing limitation. The
template-driven test designer and the LLM code generator disagree about file
layout: tests assume `pkg/core/core_test.go` calling `Process`, the model produced
a different file set, and the result is `undefined: Process` at 2-of-5 calls
succeeded.

It worked before LLM codegen was re-enabled, because both sides used templates
and therefore agreed. The fix is for the test designer to read the code
generator's *actual* output — the discipline the mechanical path already follows.
Until then the template fallback is what actually ships.

### Remaining structural gaps

| Gap | Consequence |
|---|---|
| **Circular verification on the model path** | Model-written tests and model-written code come from different generators with different assumptions. Only the mechanical path is independent. |
| **Persistent memory is write-only** | `PersistentMemory` writes to disk and is never read back. No accumulation across runs, no failure memory, no case retrieval. |
| **Potential function is not a proven Lyapunov function** | Empirically useful, caught a real bug, but a heuristic with instrumentation around it, not a theorem. |
| **Joint attribution is an upper bound** | N simultaneous interventions and one observation make exact decomposition underdetermined. Confidence is discounted to admit it. |
| **Methods are unsynthesisable** | A receiver cannot be constructed from a signature alone — 16 functions skipped in `core/attention`. |
| **No benchmarks** | "State of the art" remains an assertion. No first-attempt pass rate, no convergence curve, no comparison against a baseline. |

### On the "state of the art" claim itself

Being precise about the two senses in which that phrase is used:

**Ahead of the field:** a typed epistemic state with signed attribution,
reversible history, principled convergence, trial-before-promote enforcement, and
model-independent verification. No production agent has any of these.

**Behind the field:** writing novel code. That is the actual product, and this
system is not competitive with a frontier model at it.

**Unmeasured:** anything comparative. Without benchmarks the ranking claim is a
hypothesis with a strong architecture behind it, not a result.

---

## The Honest Bet

Not "this replaces frontier coding agents."

**It is the layer that makes an unreliable generator safe to run unattended.**

The frontier model is the best available proposal generator. That is not in
dispute. The unsolved problem is that nobody can safely let one run for 500 steps
on a production codebase, because you cannot verify the result, cannot attribute
the outcome, cannot roll back, and cannot prove the audit trail afterward. That is
a substrate problem, and scaling the model does not solve it.

This system makes an unreliable generator *auditable and improvable without it*.
The evidence is narrow and real: it found two defects its own 335-test suite
missed, it improved its own coverage with no model in the loop, and across every
trial including 20 consecutive failures it never once modified its own source with
a rejected change.

A frontier model on this substrate would be a genuinely different system — the
causal ledger would attribute credit to real reasoning, the bandit would have
something to learn, and counterfactual replay would test real designs instead of
templates. That is an unfinished argument, not a finished one, and the honest
framing is that the substrate is ready for a model that isn't.

---

## Ordered Next Work

1. **Fix the model path** — test designer reads the code generator's actual
   output. This is the regression I introduced, and it is the first thing to fix.
2. **Benchmarks.** Nothing above becomes a result until first-attempt pass rate,
   convergence curve, and regression rate exist against a named baseline.
3. **Close the read-back loop on memory.** Failure memory is the highest-leverage
   type: avoiding known-bad approaches beats exploring known-good ones.
4. **Metamorphic relations.** The synthesiser asserts signature-derived
   properties; deriving behavioural invariants from doc comments would raise its
   ceiling substantially and is the natural extension of Finding 1.
5. **Receiver construction**, to close the method gap.
6. **A stronger model on `CapReasoning`** — the router resolves by capability, never
   by model name, so this is a configuration change, and it is the single change
   most likely to make the causal machinery structurally meaningful rather than
   empty.

---

## Summary

The architecture is ahead of the field in one specific, defensible sense: it
maintains typed epistemic state with signed attribution, reversible history,
principled termination, and — the part that turned out to matter most —
verification that does not require a model to be correct about code.

It is behind the field in the blunt sense that it cannot reliably write novel code.

The claim that survives both is the narrow one this document ends on:

> **Given an unreliable generator, this system finds real defects, improves real
> coverage, and never damages itself — with no model in the loop.**
>
> That is not a consolation prize. It is the property that makes the difference
> between a frontier agent you can and cannot let run unattended, and it is
> currently the only part that is finished.