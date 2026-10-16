# Spiral CodeMaker: A Novel Architecture for Autonomous Software Generation

## Executive Summary

Spiral CodeMaker is a **distributed, asynchronous, multi-agent code generation system** built around a **Semi-State architecture**—a partially resolved cognitive/software state that evolves through evidence-driven refinement. Unlike monolithic LLM-codegen pipelines that follow a linear `prompt → LLM → code` flow, Spiral CodeMaker decomposes a coding task into 8-12 concurrent cognitive micro-units that operate at different cadences, communicate through a typed event bus, and continuously critique, test, and revise their own intermediate conclusions.

The novel contribution is not a new model or algorithm, but a **new architectural paradigm for AI coding systems**: one that replaces the single-loop agent with a **temporally dense ecosystem of specialized cognitive units**, coupled through an attention mechanism and bound together by a historically traceable, mutable Semi-State.

---

## What Makes This Novel

### 1. Semi-State: Beyond Static State

Most agent systems maintain either:
- A conversation history (monolithic prompt growth)
- A flat file state (git diff)
- A key-value memory

Spiral CodeMaker introduces the **Semi-State**—a typed, concurrent-safe data structure where every claim carries provenance, every hypothesis has competing alternatives, and every finding has a revision history with explicit status transitions (`UNVERIFIED → SUPPORTED → RESOLVED → REJECTED`).

The Semi-State is **not** a dictionary of text. It is a rich graph of:
- **Hypotheses** with supporting and contradicting evidence
- **Claims** that are strengthened only by supporting evidence and weakened by contradicting evidence
- **Attention maps** that dynamically redistribute computational focus
- **Revision history** that preserves every prior state and every decision

This means the system **changes its own intermediate state because of evidence generated during its operation**—the core requirement of the demonstration.

### 2. Temporal Density Over Raw Speed

The system does **not** use a single global loop. Instead:
- **Fast units** (Test Runner, Code Generator, Consistency Checker) wake every few seconds
- **Medium units** (Architect, Synthesizer, Critic, Decomposer) wake on events or at medium cadence
- **Slow units** (Security Analyst, Documentation Writer, Requirements Analyst) wake periodically or on explicit request

This creates **temporal density**—many small cognitive operations happening concurrently at different rates, rather than one large sequential operation. The attention mechanism determines which units get compute resources at any moment.

### 3. Cross-Critique as a First-Class Operation

The Critic unit is not an afterthought. It is an **explicit adversarial review stage** that asks:
- What is wrong with this implementation?
- What assumption could be false?
- What evidence contradicts it?
- What happens at boundary conditions?
- What did the previous iteration get wrong?

Other units challenge proposals through a **Conflict System** that creates structured conflict objects with competing claims, evidence, affected files, and resolution status. The Synthesizer must consider all of this—not just pick the first answer.

### 4. Model Provider Abstraction with Capability Routing

Different cognitive tasks require different model capabilities:
- **Fast model** → syntax/error detection
- **Reasoning model** → architecture
- **Specialized model** → security review
- **Small model** → classification

Spiral CodeMaker's routing layer maps units to model capabilities, not to specific providers. Adding a new provider (Ollama, OpenAI, future providers) requires only registering it with the appropriate capability tag.

### 5. Evidence-Driven Confidence Scoring

Every finding is classified:
- `UNVERIFIED` — not yet checked
- `SUPPORTED` — has supporting evidence
- `CONTRADICTED` — has contradicting evidence
- `RESOLVED` — contradictions resolved
- `REJECTED` — contradicted and discarded

A finding is **weakened** when evidence contradicts it and **strengthened only** when additional evidence supports it. This creates an explicit Bayesian-like reasoning loop at the system architecture level.

### 6. Self-Correction Through Revision History

The system is explicitly capable of saying:

```
Previous conclusion: X
New evidence: Y
Revision: X is no longer supported.
New conclusion: Z
Reason: ...
```

Every revision is recorded with provenance. The system never silently overwrites a decision. Past failures and past successful solutions are stored in persistent memory and consulted during new tasks.

---

## What It Should Be Capable Of

### Phase 1 (MVP) Capabilities
1. **Requirement parsing** — decompose natural language into structured, typed requirements
2. **Multi-plan architecture generation** — produce competing architectural designs, not just one
3. **Conflict detection and resolution** — when units disagree, create structured conflict objects
4. **Incremental code generation** — produce typed proposals (CREATE/MODIFY/DELETE/MOVE/RENAME) with validation
5. **Sandboxed build/test** — run `go build` and `go test` in an isolated workspace, capture structured results
6. **Automatic debugging** — analyze test failures and propose corrections
7. **Adversarial critique** — explicitly challenge the implementation for correctness, security, and consistency
8. **Revision-driven evolution** — change architecture/code based on test failures, not just append fixes
9. **Persistent learning** — store past failures and solutions, consult them on future tasks
10. **Live observability** — expose internal state (attention weights, active units, conflicts, test status) via API and web

### Phase 2 (Near-term Extensions)
1. **Real LLM integration** — replace mock providers with Ollama, OpenAI-compatible APIs
2. **Dynamic unit spawning** — add new units at runtime based on task complexity
3. **Cross-project knowledge transfer** — reuse solutions from past projects
4. **Human-in-the-loop approval** — require explicit approval for destructive operations
5. **Multi-file refactoring** — coordinated changes across the entire codebase

### Phase 3 (Long-term Vision)
1. **True concurrency** — units running as OS processes, not just goroutines
2. **Distributed computation** — units spread across machines
3. **Self-modifying architecture** — the system rewrites its own unit scheduling and attention policies based on performance
4. **Multi-language support** — generate, build, and test code in Go, Python, JavaScript, Rust, etc.
5. **Continuous deployment pipeline** — extend into production rollout with human approval gates

---

## Architecture Overview

```
User Intent
    ↓
RequirementsAnalyst → Decomposer → Architect (competing plans)
                                           ↓
                                   Synthesizer (selects plan)
                                           ↓
                                   CodeGenerator (produces proposals)
                                           ↓
                                   TestDesigner → TestRunner
                                           ↓
                                   Debugger (analyzes failures)
                                           ↓
                                   Critic (adversarial review)
                                   ConsistencyChecker
                                   SecurityAnalyst
                                           ↓
                                   Synthesizer (final synthesis)
                                           ↓
                                   DocumentationWriter
                                           ↓
                           Spiral Iteration (repeat until convergence)
```

Key components:
- **Event Bus** — async pub/sub for all inter-unit communication
- **Attention Manager** — dynamically redistributes computational focus
- **Scheduler** — independently schedules each unit at different cadences
- **Persistent Memory** — stores decisions, requirements, past failures, solutions
- **Sandbox** — isolated workspace for build/test execution
- **Conflict System** — structured disagreement management
- **Revision History** — every state change preserved with provenance

---

## Why This Is Not "Just Another Agent Framework"

Traditional agent frameworks (AutoGen, CrewAI, LangGraph) are fundamentally **linear workflows with parallelism bolted on**. They:
- Pass a shared message list between agents
- Append to conversation history indefinitely
- Have no concept of evidence weakening/strengthening claims
- Lack explicit cross-critique stages
- Don't preserve revision history of their own conclusions
- Don't have attention mechanisms that dynamically reallocate resources

Spiral CodeMaker is **an architecture for distributed cognitive evolution**. The units don't just talk to each other—they **challenge, revise, and evolve their shared understanding** through a structured Semi-State that remembers what was true, when it changed, why, and what evidence caused the change.

The intelligence is not in any single unit or model call. It is in the **density, diversity, and feedback rate of the cognitive ecosystem**—the spiral.
