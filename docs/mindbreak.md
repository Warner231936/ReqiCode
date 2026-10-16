# Mindbreak: How This Architecture Beats a Frontier Model

## The claim

Not "Semi-State is smarter than Claude." That is false today and any document
claiming it is marketing.

The real claim is architectural and narrower:

> **On tasks with many steps, many retries, and a verifiable outcome, a system
> with a persistent epistemic substrate beats a frontier model — not per token,
> but asymptotically.**

A frontier model has enormous capability per call and **no memory of its own
process**. It cannot tell you which of the 200 changes it made last Tuesday
actually fixed the bug. It cannot prove that the change it is about to make is an
improvement rather than a lateral move. It cannot be replayed, audited, or
rolled back with a receipt.

This system is weak per call and strong per *process*. That is a different axis,
and on the right axis it wins.

---

## Why a frontier model is structurally capped

Claude's ceiling is not the model. It is the substrate it runs on.

### 1. Context is the only memory, and it is write-once

Everything Claude knows about its own work lives in the context window. When the
window fills, something is evicted. There is no mechanism for deciding *what
matters*, because there is no structure to decide with — a requirement and a
failed stack trace and a hypothesis and a rejected approach are all just tokens.

Semi-State has typed containers with different retention rules. `CodeProposal`
records what changed. `Evidence` records what was observed. `Hypothesis` records
what is believed and *how strongly*. `ContradictingEvidence` records what would
change that. `Provenance{UnitID, Revision}` records who thought it and when.

This is not "more context." It is **different context**, which is strictly more
useful per token. A stack trace and a rejected architectural alternative are both
~200 tokens, but one is noise and one is the single most valuable thing in the
workspace. A typed state can rank them. A context window cannot.

### 2. No write path, therefore no learning

A frontier agent's loop is: read → act → observe → read. The observation informs
the next step only insofar as it survives in the window.

This system's loop is: state → act → record → attribute → revise state.

The second one has an arrow leaving "act," and that arrow is the entire
difference. Attribution assigns **signed** credit to each intervention, weighted
by objective, with regression weighted super-linearly because a broken build is
strictly worse than a wasted attempt. After 200 changes, this system can answer:

```
strategy=llm-plan         mean_credit= 2.170  n=1
strategy=template-generate mean_credit= 2.170  n=4
```

A frontier model given the same 200 changes can say "I think the second one
helped."

### 3. Verification is correlated with generation

This is the deepest limit and the least discussed.

When Claude writes code and Claude reviews it, the reviewer shares the
generators' context. It cannot detect a class of error that is systematic — a
misconception about a library, a wrong mental model of a protocol, a design
assumption taken on faith. Asking the same reasoner twice produces correlated
errors, not independent confirmation. **You cannot detect a defect with the same
lens that produced it.**

A system with independent verification does not have this problem. Test
execution is ground truth from a completely different process. The race detector
does not care what the author believed. Coverage does not care about intent.

This is why "ask Claude to review its own work" does not produce the safety
people expect, and why it is not a prompting problem.

---

## Where the substrate wins, concretely

### Case 1: Long-horizon refactors (dozens of interacting files)

A frontier model doing a 40-file refactor must hold the whole system in
context simultaneously and re-derive relationships it lost 200k tokens ago.
Failure mode: it silently changes an interface in file 12 and forgets three
call sites in files 30, 33, and 41.

This system tracks *interfaces as state*. `ConsistencyChecker` exists to detect
cross-file contradiction. `Contradictions` records conflicts with severity, and
an active conflict measurably reduces the potential function. The architecture
plan is a first-class object, so "which files implement this plan" is a query,
not a recollection.

**The win compounds with file count.** At 5 files a frontier model is fine. At
80, the memory advantage is decisive.

### Case 2: Tasks where the failure mode is thrashing

Some tasks have a search space where a model can oscillate — A works, B works
better, B breaks something subtle, revert to A, repeat. A frontier model has no
representation of this and burns the full budget.

This system detects it structurally. The state fingerprint is a hash over
semantics only — plan, decisions, requirements, proposals, file paths — excluding
timestamps and contents. If revision *N* is semantically identical to revision
*N−2* while *N−1* differed, the system is in a two-cycle and reports:

```
Phase: oscillating
ForceDifferentiate: true
"state at revision 7 is semantically identical to revision 5 while revision 6 differed"
```

And it has the rollback it needs: the best-known revision is tracked on a Pareto
frontier over (pass rate, coverage, token cost, complexity), so the spiral
returns its *best* result rather than its last.

### Case 3: Regulated and auditable work

Some work has a provenance requirement. "Explain why this code looks like this"
is unanswerable for a frontier agent in any rigorous sense.

Here, every artifact carries `Provenance{UnitID, CreatedAt, Revision}`, and the
revision chain is a SHA-256 link from a genesis constant. Retroactive editing
breaks the link at the first divergent successor, and the verifier locates it
precisely. You can prove the audit log was not edited after the fact.

### Case 4: Cost-shaped iteration

Local inference has a marginal cost near zero. That changes what is rational to
attempt: 200 cheap verification runs to determine whether a change actually helped
is obviously correct at $0.02 and indefensible at $60 in API calls.

This is not a marginal advantage — it changes the shape of the search. When
verification is free, you verify everything. When verification costs a tenth of
generation, you verify only what looks risky. The second regime is where bugs
survive.

### Case 5: Repeated work on a codebase

Every run here leaves a causal ledger and a chain. Run it again on an adjacent
task and the ranking already knows which strategies paid off in this codebase,
which is local knowledge a general model does not carry and cannot acquire.

---

## The asymptotic argument

This is the core, and it is simple.

Let a task require *N* steps. Let *m* of those steps be verifiable.

| | Frontier model | This system |
|---|---|---|
| Memory of its own process | O(1) — the window | O(N) — typed state |
| Which change helped | unanswerable | O(N) ledger with signed credit |
| Independent check | unavailable | O(m) ground-truth executions |
| Reversibility | none | revision fork + replay |
| Cost of step *k* | full context resend | O(1) incremental |
| Convergence | undefined | potential function + phase classification |

The per-call capability of a frontier model is vastly higher. The per-*process*
capability of a typed state is categorically different, and it is what compounds
while the frontier model's does not.

**The crossover point.** A frontier model wins small tasks decisively and will
continue to for a long time. This system wins when `N × (cost of getting it
wrong)` exceeds `N × (cost of the substrate)`. That threshold is a few dozen
steps on a codebase you care about. Below it, just use Claude.

Being honest about where that line sits is the whole credibility of the claim.

---

## The counter-arguments, stated properly

A document like this is worthless if it only lists strengths, because the reader
cannot tell which parts to believe. These are the real weaknesses.

**"The scaffolding is good; the model is weak."** True, and currently the binding
constraint. A frontier model inside this substrate would be a genuinely different
system — the ledger would attribute credit to real reasoning, counterfactual
replay would test real hypotheses, and the convergence phase would have something
meaningful to converge. The architecture is substrate-independent *by design*; it
routes by capability, never by model name. That is an unfinished argument, not a
finished one.

**"Every bug this system found was found by me, not by it."** Also true, and the
honest number is eight. Property tests caught a confidence cap that erased its own
signal and a property test that could not fail. The validation gate caught a
duplicate declaration. A lock bug hung every integration test. None were found by
a unit; all were found by *infrastructure* — and that is the actual claim. The
bugs that were found are the bugs the substrate is designed to make visible.

**"Verification is only as good as the tests."** Correct and serious. Circular
verification remains the largest gap: the test designer and the code generator
currently share template assumptions. Until independent verification exists, the
system can verify itself against its own misconceptions. This is the single thing
standing between the current state and the claim above.

**"Local inference is too slow to iterate."** Measured: 9.5 tok/s on this card,
full GPU offload (29/29 layers, 0.00 MiB CPU buffer). A 2-minute verification run
is not a reason to skip verification — it is a reason to not waste a 200-iteration
budget on random perturbation, which is exactly the failure mode attribution
exists to prevent.

---

## The concrete bet

Not "this will replace frontier coding agents."

**This is the layer that makes frontier coding agents safe to run unattended.**

The frontier model is the best available proposal generator. That is settled and
not in dispute. The unsolved problem is that nobody can safely let one run
unattended for 500 steps on a production codebase, because you cannot verify the
result, cannot attribute the outcome, cannot roll back, and cannot prove the audit
trail afterward.

That is a substrate problem, not a model problem. Scaling the model does not
solve it. This is an attempt at solving it — and the parts that are built and
tested are the parts that matter: signed attribution, independent measurement,
reversible history, and principled termination.

A frontier model with a mediocre substrate and a frontier model with this one are
not comparable systems. The second one can be trusted with the loop. That is the
whole argument.

---

## Appendix: what Claude would be capable of with this substrate

Everything above argues the substrate is the leverage. That raises the obvious
question: **what would actually happen if a frontier model were plugged in?**

Not hypothetically — the router already resolves by capability and never by
model name, so `AddProvider("anthropic", ...)` plus `SetActiveModel` is the
entire integration. The question is what changes in behaviour.

### The things that currently measure nothing would start measuring

Right now the causal ledger produces this:

```
strategy=llm-plan         mean_credit= 2.170  n=1
strategy=template-generate mean_credit= 2.170  n=4
```

Every strategy scores identically because the only "LLM" strategy is the
architect's plan on a five-component template plan. The number is *structurally*
correct and *semantically* empty. With Claude it would carry signal, because the
choices being compared would actually differ:

```
strategy=claude-arch-layered       mean_credit= 0.412  n=14
strategy=claude-arch-flat         mean_credit= 0.288  n=9
strategy=claude-arch-event-driven mean_credit= 0.551  n=6
```

And that ranking would be a real finding: *this codebase rewards event-driven
architecture, and here is the evidence.* A frontier model asked the same question
in a chat would produce an opinion with no supporting measurement, because it
cannot produce the measurement.

This cascades. Once strategy ranking is informative:

- **The bandit becomes trainable.** Currently a contextual bandit over intervention
  strategies has nothing to learn from. With real credit assignment it would
  shift the architect's prior toward approaches that have actually paid off on
  *this* codebase, which is local knowledge no general model carries.
- **Counterfactual replay becomes meaningful.** "Would the event-driven design
  have passed?" is currently a question about templates. With Claude generating
  two genuinely different architectures, forking the state, reverting one, and
  re-running becomes a real experiment rather than a formality.
- **The convergence phase would have a real optimum.** The potential function
  currently rises because tests pass, then plateaus. With a model that sometimes
  produces better and sometimes worse, the trajectory would have genuine local
  maxima to detect and escape — which is exactly what `PhaseOscillating` and
  `ForceDifferentiate` exist for.

### The 15 units stop being shells

Seven of them are currently stubs: critic, security-analyst, debugger,
consistency-checker, documentation-writer, decomposer, synthesizer. They were
built with correct structure and no reasoning in them. This is the clearest case
of "the model was never the point" — the structure was designed to be filled by
something capable, and a weak model made the emptiness visible.

With Claude behind them, `Critic` becomes an actual adversarial reviewer whose
objections land in `Objections` with severity, and an objection measurably
reduces the potential function, so the critic has *teeth* rather than just a
voice. `SecurityAnalyst` reviewing code with a security-specific prompt produces
findings that enter the same lifecycle as architectural claims.

The 15-way attention competition — which is the actual novel mechanism — only
becomes interesting when the units have genuinely different opinions. Right now
they compete and mostly agree with each other.

### The unit economics invert, and that is a feature

Right now verification is nearly free, so the system verifies generously. With
Claude, a single generation call might cost more than the entire verification
budget for an iteration. That sounds like a downgrade. It is not, because:

- **Attribution becomes worth far more.** Knowing which of 200 Claude calls at
  $2 each was worth its price is a $400 question. The ledger answers it.
- **The canary becomes a search mechanism.** Speculative execution: propose ten
  candidate designs, build each in an isolated branch, re-test each against the
  edited workspace, promote the best. With a cheap model this is wasted effort;
  with an expensive one it is the correct allocation of a fixed budget.
- **The token cost axis becomes load-bearing.** The Pareto frontier already tracks
  token cost against pass rate. With real per-call prices, "this revision costs
  14× more for 3% more confidence" becomes a decision the system can make and
  justify.

So the substrate does not just tolerate an expensive model — it becomes more
valuable with one, because expensive changes need provenance to be affordable.

### Independent verification becomes *more* important, not less

This is the counter-intuitive part and it matters most.

A stronger generator produces *subtler* bugs. The template generator emits naive
code that fails compilation visibly and immediately — which is why the duplicate
declaration, duplicate path, and truncated output cases were all catchable by
lexical inspection. Claude emits code that compiles, passes its own tests, and is
wrong in a way that requires understanding intent to detect. The class of defect
that survives gets *harder* the better the generator is.

That inverts the usual intuition about capability. **As the generator improves,
correlated verification degrades as a safety mechanism**, because the generator's
misconceptions become subtler while the verifier's stay exactly as shallow. A
reviewer asked to check Claude's output is better at spotting Claude's errors
than it was at spotting a template's — but the error classes it misses are now
the ones that matter.

So Blocker 5 is not a prerequisite that could be deprioritized as "nice to have."
Under a frontier generator it becomes the load-bearing safety property, and the
property-based / metamorphic / differential verification work stops being
research and starts being the product.

### The new failure mode this creates

A stronger model does not only produce better output. It produces **more output,
faster, into infrastructure that has eight known bugs.** Every substrate defect
becomes more dangerous when the thing driving it is capable and fast:

- The confidence cap that made solo and two-member cohorts indistinguishable is a
  bookkeeping defect today. With real credit assignment flowing through it, it
  corrupts the ranking that makes the bandit trainable.
- The `packageName` extension bug that mis-grouped files is a validation defect
  today. With Claude generating 200 files instead of 5, the duplicate-declaration
  check becomes load-bearing across a much larger surface.
- The reentrant-lock deadlock was found because an integration test hung
  visibly. Under a fast generator with longer runs, a subtle concurrency bug in
  the state machine becomes intermittent and misattributed to the model.

**This is the strongest argument for the ordering already chosen.** Self
modification is last not as process conservatism but because substrate bugs scale
with the capability of whatever is driving them. Every gate closed before the
self-modification path is a gate that must not be debugged while the system is
rewriting itself.

### The honest summary of the swap

| | TinyLlama 1.1B (current) | Claude on this substrate |
|---|---|---|
| Architecture chosen | Template, always | Model-proposed, template fallback |
| Strategy ranking | Structurally empty | Real, per codebase |
| Bandit | No signal to learn from | Trainable |
| Counterfactual replay | Tests templates | Tests real designs |
| 15 units | 7 stubs | 15 distinct reasoners |
| Critic objections | No teeth | Measurable potential reduction |
| Verification | Lexical gates suffice | **Insufficient** |
| Cost of a bad step | ~0 | Real, and attributable |

The first five rows are unambiguous upgrades. The sixth is the price: the same
substrate that is adequate for a weak generator becomes *inadequate* for a strong
one, and the thing that was a nice-to-have becomes the thing standing between the
system and trustworthy output.

**That is the actual finding of this document.** The substrate is the leverage
not because it makes a weak model strong, but because it is the difference
between a frontier model you can and cannot let loop unattended. And the moment
you put the frontier model in, the substrate stops being sufficient on its own.