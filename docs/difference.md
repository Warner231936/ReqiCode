# Why Semi-State Is Not a RAG System or a Knowledge Graph

This document answers a specific objection that is raised constantly and answered badly. Semi-State *looks* like a RAG system: it has facts, it retrieves them, it feeds them to a model. It also *looks* like a knowledge graph: nodes, edges, relationships, a schema. Both analogies are wrong, and they are wrong in ways that matter — because both analogies imply a ceiling on what the system can do, and Semi-State's whole design exists to break through that ceiling.

I will make the case structurally: first by defining what RAG and knowledge graphs actually are, then by showing exactly where Semi-State diverges, and finally by identifying the *specific* failure mode that each analogy would impose on Semi-State if the analogy were taken seriously.

---

## Part 1: What RAG Actually Is

Retrieval-Augmented Generation, stated precisely:

> A **read-only** corpus of unstructured text is chunked, embedded, stored in a vector index, retrieved by similarity to a query, concatenated into the prompt, and the prompt is passed to a model that produces output. The corpus is never modified.

Four properties define it, and all four are structural rather than incidental:

**1. The knowledge is read-only.** Retrieval is the *only* permitted operation. There is no write path from generation back into the corpus. A RAG system learns nothing from its own output. If you ask it the same question twice with the same corpus, you get the same retrieval and (modulo sampling noise) the same answer. The system is a fixed function of its inputs.

**2. Retrieval is similarity, not consequence.** The retrieval step answers *"what text is lexically or semantically close to this query?"* It never answers *"what did my last action actually change?"* A chunk scoring 0.94 cosine similarity is retrieved whether it helped, hurt, or had nothing to do with the problem. Similarity is not evidence.

**3. The output is unconstrained prose.** Because the corpus is prose and the query is prose, the output is prose. RAG has no native representation for "this assertion is now false" or "this design decision was superseded." Those are not things you can express in a paragraph, so the system cannot represent them, so it cannot track them.

**4. There is no ground truth channel.** RAG has no notion of running the code and finding out. It retrieves documents *about* correctness. The correctness signal — a test failing, a race detector firing, a build breaking — is outside the system entirely.

**The load-bearing reduction:** RAG is a *stateless reader*. It widens the context window. It does not give the system memory of its own actions, and it cannot, because it has no write path.

---

## Part 2: What a Knowledge Graph Actually Is

A knowledge graph, stated precisely:

> A set of **typed entities** connected by **typed, explicitly asserted relationships**, stored so that the structure itself is queryable.

What it provides:

- **Structure.** You can ask "what depends on what" without an LLM.
- **Retrieval by relationship.** You can walk the graph, not just score it.
- **Inference.** Some systems can derive facts not explicitly stored.

**But here is the part that gets overlooked when people reach for this analogy:** a knowledge graph is also fundamentally a *representation of a static world*. It answers "what is true." It does not answer "what did I do, what happened when I did it, and was that a good idea."

A knowledge graph has no native concept of:
- **Temporal ordering** of its own construction (unless you bolt on bitemporal indexing)
- **Attribution** of a fact to a decision process
- **Confidence degradation** as evidence accumulates
- **Counterfactual reasoning** — "had I not made change *N*, what would hold?"
- **Reversal.** Knowledge graphs are typically append-and-index. Correcting a fact usually means adding a contradicting fact, leaving both live forever.

The distinguishing question is: **does the structure record only what the world looks like, or also what the system did and how that turned out?** Knowledge graphs answer the first. Semi-State needs the second.

---

## Part 3: Where Semi-State Actually Differs

### 3.1 The write path exists, and it is the whole point

This is the single most important difference, and everything else follows from it.

In Semi-State, generated code becomes a `CodeProposal` that is **validated, conflict-checked, applied, and recorded**. The architecture becomes a `Hypothesis` that moves `PROPOSED → ACCEPTED → CONTRADICTED` as evidence arrives. Decisions are recorded with rationale and a `Supersedes` pointer. Every one of these is a write into the state that changes what the system believes.

```
RAG:     query → retrieve → generate → output → (nothing)
SemiState: state → act → record → attribute → revise state → (loop)
```

A RAG system has no arrow leaving "output." Semi-State is defined by having one. **Without a write path, there is no learning, no self-correction, and no convergence — there is only retrieval.** This is not a feature Semi-State has and RAG lacks. It is the property that makes a loop possible at all.

### 3.2 The unit of knowledge is an *epistemic claim*, not a text chunk

This is the difference people miss, and it is the most technical.

A RAG chunk is a *span of text*. It is retrieved as a unit and it carries no metadata about its epistemic status. It is true or it is retained; there is no third state.

A Semi-State `Claim` is a typed object:

```go
type Claim struct {
    Content               string
    Source                string
    Status                FindingStatus  // UNVERIFIED → SUPPORTED
                                           //             → CONTRADICTED
                                           //             → RESOLVED | REJECTED
    Confidence            Confidence     // a named type, not a float
    SupportingEvidence    []string
    ContradictingEvidence []string       // ← RAG has no analogue for this
    Provenance            Provenance
    Revision              int
}
```

Three properties here are categorically absent from RAG:

**(a) `ContradictingEvidence` is a first-class, queryable field.** When a test fails against a design claim, the failure is attached to the claim as *contradicting* evidence. It does not merely add a new chunk that might or might not be retrieved. It changes the status of a specific existing belief. In RAG, the equivalent information is unrepresentable — you would have to write prose that says "this earlier statement is now doubtful," and nothing would enforce that the earlier statement is actually marked doubtful anywhere.

**(b) `Status` is a lifecycle, not a label.** The claim can die. `REJECTED` is a terminal state. In a RAG corpus or a conventional knowledge graph, a wrong statement is effectively immortal: it stays in the index and keeps getting retrieved. Semi-State can forget.

**(c) `Provenance` on every claim.** Who asserted it, when, and at which state revision. This is what makes "why did the system believe this?" answerable exactly rather than approximately.

### 3.3 Retrieval is by *relevance to the failure*, not by similarity

This is where the analogy breaks hardest.

RAG retrieves by cosine similarity: "find text like the query." If the query is "tests are failing in the store," RAG returns chunks about stores and testing — regardless of whether those chunks are relevant to *this* failure.

Semi-State's attention manager (`core/attention/manager.go`) retrieves differently:

```go
m.eventBus.Subscribe(events.EventTestFailed,     m.onTestFailed)
m.eventBus.Subscribe(events.EventConflictDetected, m.onConflictDetected)
m.eventBus.Subscribe(events.EventEvidenceAdded,  m.onEvidenceAdded)
```

A test failure does not retrieve "text about tests." It **boosts the debugger and code generator, and dims unrelated units.** Attention decays at `decayRate: 0.02` below `minThreshold: 0.05`, so stale concerns fade out.

That is a fundamentally different addressing scheme. RAG addresses by content similarity. Semi-State addresses by *causal relevance to the current problem state*. A unit that is semantically near the query but causally irrelevant gets zero attention. A unit that is semantically distant but causally essential — the debugger, when a test just failed — gets maximum attention.

Similarity is a proxy for relevance. Attention is relevance directly. The distinction matters because the proxy fails exactly when the problem is interesting.

### 3.4 There is a ground-truth channel, and it changes the system

RAG has no execution semantics. Semi-State has a sandbox, a build runner, and a test runner that now runs with `-race` and `-coverprofile` under cgo.

The test result is not documentation. It is **evidence with a direction**. It flows back into the state as `EvidenceTestPass` or `EvidenceTestFailure` and moves claims between statuses. The causal ledger then attributes the outcome to specific interventions:

```go
iv.NetCredit, iv.Confidence = scoreIntervention(iv, len(l.pending))
```

An intervention that turned a failing suite green earns positive credit. One that broke a passing suite earns negative credit, weighted super-linearly because a broken build is strictly worse than never having attempted the change.

**RAG cannot express "this change made things worse."** There is no mechanism in retrieval for a system to be *penalized* by an outcome. That requires a write path, an attribution step, and a signed scoring function — all three of which are outside RAG's design space.

### 3.5 The structure is executable, not just queryable

A knowledge graph is queried to *retrieve facts*. Semi-State's structure is queried to *take an action*.

The causal ledger does not tell you "intervention 7 exists." It tells you the ordering of strategies by mean credit:

```go
scores := l.TopStrategies()  // "template-generate" beats "template-test"
```

The convergence tracker does not just report a potential value. It classifies the trajectory and issues an instruction:

```go
type Phase string
const (
    PhaseProductive   Phase = "productive"   // keep going
    PhaseConverged    Phase = "converged"    // stop, no more extraction available
    PhaseOscillating  Phase = "oscillating"  // force differentiation
    PhaseRegressing   Phase = "regressing"   // roll back to best-known revision
)
```

And the Pareto frontier changes what the system *returns*. The spiral can stop at any point and hand back the best revision found rather than whatever the last iteration happened to produce:

> "A system that proves convergence, detects thrash, tracks the Pareto frontier, and stops at the right moment is operable as a real system. It is the difference between a research artifact and a tool."

In a knowledge graph, structure is *descriptive*. In Semi-State, structure is *prescriptive* — it decides what happens next.

### 3.6 Counterfactual replay is impossible in both analogies

This is the capability that no RAG system and no conventional knowledge graph can provide, and it falls directly out of the state design.

Because every artifact carries `Provenance{UnitID, Revision}` and `SemiState.Revision` increments monotonically, a prior state can be reconstructed exactly, verified by hash, and replayed under a modified condition:

```go
snap, _ := replayer.Capture(ss, "before-change-N")
result, _ := replayer.Trial(snap, "revert-N", func(dir string) (string, error) {
    return revertFile(filepath.Join(dir, "internal/store.go")), nil
})
// result.Objectives[ObjTestPass] tells you what would have happened
```

**A RAG system physically cannot do this.** It has no reversible history — it has a corpus, and the corpus is not a record of actions.

**A knowledge graph can approximate it** with full version control, but only if you bolt on bitemporal indexing, keep the workspace files themselves, and re-execute. At that point you have rebuilt a significant fraction of Semi-State, and the graph was incidental.

The honest statement: counterfactual replay is not a clever trick. It is *free* given revisioned state, and it is *impossible* without it. That asymmetry is the clearest evidence that Semi-State is a different category of thing.

---

## Part 4: The Specific Failure Mode Each Analogy Imposes

Abstract disagreement is less useful than a concrete prediction. If Semi-State were a RAG system, here is exactly what would break, in order of severity:

| # | If it were RAG | What breaks | Where Semi-State actually handles it |
|---|---|---|---|
| 1 | No write path | Generated code never influences future generations; the same bug is rediscovered every run | Proposals are recorded; the causal ledger assigns credit |
| 2 | Evidence cannot be negative | A failing test adds a chunk; nothing marks the prior design as wrong | `ContradictingEvidence` moves a Claim to `CONTRADICTED` |
| 3 | Retrieval is similarity | The debugger is invoked on "text near the failure" rather than *because* tests failed | `Boost("debugger", ..., 0.5)` on `EventTestFailed` |
| 4 | Output is prose | "We changed our mind about the store" is unrepresentable as state | `Decision.Supersedes`, `Hypothesis.Status` |
| 5 | No execution semantics | Nothing can be *disproved*, only retrieved | Sandbox + `-race` + test results as signed evidence |
| 6 | No counterfactuals | "Would it have passed without change 7?" is unanswerable | `Replayer.Trial` with hash verification |

And if Semi-State were a knowledge graph:

| # | If it were a KG | What breaks | Where Semi-State actually handles it |
|---|---|---|---|
| 1 | Static world model | No record of *what the system did* | `Intervention` with unit, target, operation, predicted effect |
| 2 | Facts are immortal | A refuted design stays retrievable forever | `FindingStatus` lifecycle with terminal `REJECTED` |
| 3 | No attribution | "Why does the graph believe X?" has no answer | `Provenance` on every artifact |
| 4 | No confidence dynamics | Confidence is static metadata or absent | `Confidence` is a named type on claims *and* evidence |
| 5 | No convergence | Nothing stops the loop | Potential function, phase classification, Pareto stop |
| 6 | Descriptive, not operative | Structure informs; it does not decide | `ShouldStop`, `ShouldRollback`, `ForceDifferentiate` |

---

## Part 5: The Cleanest Way to Say It

Every comparison framework ends up here, so let me state it directly.

**RAG is a *retrieval substrate*.** It answers "what do I know that looks like this?" It is a read path.

**A knowledge graph is a *representation substrate*.** It answers "what do I believe, and how do those beliefs relate?" It is a schema.

**Semi-State is a *correction substrate*.** It answers "what did I do, what happened when I did it, and was that a good idea — and if not, can I prove exactly where the reasoning went wrong?"

The three are not competitors. RAG and knowledge graphs are *components* — useful ones, and useful in a system like this. Where I want a non-semantic lookup, a knowledge graph is the right tool. Where I want to surface a relevant prior document, retrieval is the right tool. Semi-State needs both, and uses both.

What Semi-State adds is the part neither can provide: **a substrate for being wrong, in a way that can be detected, attributed, reversed, and not repeated.**

---

## Part 6: The Honest Disclaimers

An argument like this one is easy to make and easy to fake. Three admissions, stated plainly.

**1. Semi-State's write path is real, but shallow.** A proposal is recorded; a plan is recorded; a decision is recorded. But nothing yet *learns a policy* from that history. The bandit over intervention strategies is the next milestone, and until it exists the ledger is a recording, not a learner. Calling it a "correction substrate" describes the design and the instrumentation, not yet a demonstrated correction loop. The honest phrasing is: **the substrate for correction exists and is exercised; the correction itself is not yet automated.**

**2. Joint attribution is an upper bound, not a proof.** When *N* interventions land in one iteration and one test result follows, exact causal decomposition is underdetermined. The ledger assigns every pending intervention the full observed delta and marks `Confidence` low in proportion to cohort size (capped at 0.5). That is honest bookkeeping, not causal identification. The `Replayer` exists precisely because the cheap method has this limit — exact attribution requires actually running the counterfactual.

**3. Convergence is instrumented, not proven.** The potential function, phase classification, and oscillation detection are implemented and tested. What is *not* established is that the potential is a valid Lyapunov function in the mathematical sense — that it strictly decreases outside some neighborhood of the goal set. It is a plausible descent signal, empirically useful, and it has already caught a real bug (period-1 "cycles" being misread as oscillation rather than plateau). Treat it as a well-instrumented heuristic, not a theorem.

**None of these caveats is fatal, and all three are checkable.** The instrumentation writes `instrumentation/phase0.json` containing the full potential trajectory, every intervention with its attributed credit, the strategy ranking, and the Pareto frontier. You can audit the claims rather than taking them on faith. That property — that the system's reasoning about itself is externally inspectable — is itself something neither RAG nor a conventional knowledge graph offers, and it is the practical test of which substrate you actually have.

---

## Summary

| Dimension | RAG | Knowledge Graph | Semi-State |
|---|---|---|---|
| Primary question | What text is similar? | What is true and how is it related? | What did I do, and did it work? |
| Write path | None | Append/index | **Validated, conflict-checked, attributed** |
| Unit | Text chunk | Entity | **Typed epistemic claim with lifecycle** |
| Negative evidence | Unrepresentable | A contradicting fact (both stay live) | **`ContradictingEvidence` → status transition** |
| Addressing | Cosine similarity | Graph traversal | **Attention = causal relevance to current failure** |
| Ground truth | None | External | **Sandbox, tests, `-race`, coverage** |
| Forgetting | Impossible | Effectively impossible | **Terminal `REJECTED` status** |
| Counterfactuals | Impossible | Requires bolting on version control | **Free via revisioned state + replay** |
| Structure is | Descriptive | Descriptive | **Operative — it decides what happens next** |
| Loop | No loop possible | No loop possible | **Bounded by potential function and Pareto stop** |

**The one-sentence version:** RAG gives the system *knowledge* and a knowledge graph gives it *structure*, but both are read paths — and a read path cannot be wrong in a way the system can detect, attribute, and undo. Semi-State is the write path. That is the entire difference, and everything else in the architecture follows from taking it seriously.
