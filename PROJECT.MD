# SPIRAL CODEMAKER

## Build Specification — Recursive Multi-Unit Code Generation System

You are building **Spiral CodeMaker**, a custom autonomous coding system designed around a concept of **distributed, asynchronous cognitive micro-units** rather than a single linear agent loop.

Do not treat this as a chatbot wrapper.

The objective is to create an actual software architecture in which many small reasoning units operate independently, maintain state, exchange observations, challenge one another, and converge on a tested software artifact.

---

## 1. CORE PRINCIPLE

Do NOT implement:

```text
prompt → LLM → code
```

Implement:

```text
INTENT
  ↓
DECOMPOSE
  ↓
SPAWN MICRO-UNITS
  ↓
ASYNC PROCESSING
  ↓
SHARED SEMI-STATE
  ↓
CROSS-CRITIQUE
  ↓
SYNTHESIS
  ↓
GENERATE
  ↓
TEST
  ↓
OBSERVE
  ↓
REVISE
  ↓
SPIRAL
```

The system should improve its result through repeated interaction between small units.

The system is not required to claim consciousness.

Do not anthropomorphize the architecture in the implementation.

---

# 2. SEMI-STATE

The central data structure is the **Semi-State**.

A Semi-State represents a partially resolved cognitive/software state.

It must be capable of containing:

* current hypotheses
* competing hypotheses
* evidence
* contradictions
* unresolved questions
* proposed implementations
* confidence values
* dependencies
* test results
* objections
* previous revisions
* active attention
* inactive/background processes
* provenance

Example:

```text
SemiState
├── hypotheses[]
├── evidence[]
├── contradictions[]
├── unresolved[]
├── proposals[]
├── test_results[]
├── attention_map
├── active_units[]
├── background_units[]
├── confidence
├── revision
└── history[]
```

A Semi-State is NOT simply a dictionary of text.

Use typed structures.

Every important claim should have provenance.

Example:

```text
Claim
├── content
├── source
├── confidence
├── supporting_evidence[]
├── contradicting_evidence[]
├── created_by
├── created_at
└── revision
```

---

# 3. MICRO-UNITS

Create a generic `Unit` abstraction.

Every unit has:

```text
Unit
├── ID
├── Role
├── State
├── Input
├── LocalMemory
├── Cadence
├── Activation
├── AttentionWeight
├── Confidence
├── Dependencies
├── Outputs
├── Objections
└── History
```

Units must be independently schedulable.

Examples:

```text
Architect
Decomposer
RequirementsAnalyst
CodeGenerator
TypeAnalyst
APIAnalyst
SecurityAnalyst
DependencyAnalyst
RuntimeAnalyst
TestDesigner
TestRunner
Debugger
Critic
ConsistencyChecker
DocumentationWriter
Synthesizer
```

Do not hard-code the architecture so tightly that new units cannot be added dynamically.

---

# 4. CADENCE / FREQUENCY

The word "frequency" here means **update cadence**, not literal physical frequency.

Different units should be allowed to operate at different rates.

For example:

```text
fast:
    syntax monitoring
    test monitoring
    error detection

medium:
    architecture
    implementation analysis
    dependency analysis

slow:
    long-term consistency
    documentation
    architectural drift
```

Do NOT implement a single global loop that waits for every unit.

Use asynchronous scheduling.

A unit may:

* wake periodically
* wake on an event
* wake when its attention score changes
* wake when a dependency changes
* wake when another unit requests review
* remain dormant

The system should therefore have **temporal density rather than merely raw speed**.

---

# 5. ATTENTION

Implement an explicit attention mechanism.

Attention determines which units receive additional computational resources.

Represent it numerically.

Example:

```text
Attention
├── unit_id
├── weight
├── reason
├── timestamp
└── decay
```

Attention should be influenced by:

* unresolved contradictions
* failed tests
* high-impact changes
* dependency changes
* uncertainty
* explicit user requests
* repeated unit disagreement
* security-sensitive findings
* architectural instability

Attention must be redistributable.

A unit can become important without the entire system restarting.

---

# 6. ASYNCHRONOUS EVENT BUS

Create an internal event bus.

Example events:

```text
RequirementAdded
HypothesisCreated
HypothesisChanged
EvidenceAdded
ConflictDetected
AttentionChanged
CodeProposed
CodeChanged
TestStarted
TestPassed
TestFailed
DependencyChanged
ReviewRequested
ReviewCompleted
SynthesisRequested
SpiralIterationCompleted
```

Units communicate through events and typed state rather than directly manipulating arbitrary global variables.

---

# 7. MEMORY

Implement three levels of memory.

### Local Memory

Owned by an individual unit.

Used for temporary reasoning/context.

### Shared Working Memory

Contains the current Semi-State.

### Persistent Project Memory

Contains durable information about the project:

```text
architecture
decisions
requirements
known constraints
past failures
past successful solutions
dependency information
test history
revision history
```

Every persistent memory item should have provenance.

Do not silently overwrite important decisions.

Record revisions.

---

# 8. CODE GENERATION

The generator must not blindly rewrite the entire project.

Use incremental changes.

Represent changes as typed proposals:

```text
CodeProposal
├── file
├── operation
├── location
├── before
├── after
├── reason
├── originating_unit
├── confidence
└── expected_effect
```

Possible operations:

```text
CREATE
MODIFY
DELETE
MOVE
RENAME
```

Before applying a proposal:

1. validate it
2. detect conflicts
3. determine dependencies
4. record provenance
5. apply it in an isolated workspace

---

# 9. SANDBOXED BUILD / TEST LOOP

Never allow generated code to blindly execute with unrestricted host privileges.

Create a project workspace.

The system should:

```text
generate
↓
build
↓
test
↓
capture output
↓
analyze failure
↓
create evidence
↓
wake relevant units
↓
propose correction
↓
rebuild
```

Tests should produce structured results.

Example:

```text
TestResult
├── test_id
├── command
├── status
├── stdout
├── stderr
├── duration
├── failure_class
└── evidence
```

---

# 10. CONFLICT SYSTEM

Multiple units will disagree.

Do NOT immediately select the first answer.

Create a conflict object:

```text
Conflict
├── subject
├── competing_claims[]
├── evidence[]
├── affected_files[]
├── severity
├── participants[]
└── resolution
```

Allow units to challenge proposals.

The synthesizer must consider:

```text
supporting evidence
contradicting evidence
test results
confidence
dependency impact
historical outcomes
```

A claim should be weakened when evidence contradicts it.

A claim should strengthen only when additional evidence supports it.

---

# 11. SPIRAL ITERATION

The system operates in iterations.

Each iteration must preserve what happened previously.

Example:

```text
Iteration 0
    ↓
Initial model
    ↓
Implementation
    ↓
Tests
    ↓
Failures
    ↓
Iteration 1
    ↓
Revised model
    ↓
New implementation
    ↓
New evidence
    ↓
Iteration 2
```

Do not simply append increasingly large prompts.

The state itself must evolve.

Every iteration records:

```text
iteration_id
starting_state
changes
new_evidence
contradictions
decisions
tests
ending_state
```

---

# 12. SELF-CRITIQUE

Before accepting a significant implementation, create an explicit adversarial review stage.

Ask independent units:

```text
What is wrong with this implementation?

What assumption could be false?

What evidence contradicts it?

What happens at the boundary conditions?

What happens if the dependency changes?

What happens under failure?

What did the generator overlook?

What did the previous iteration get wrong?
```

The critic is not allowed to simply approve everything.

Its job is to find reasons the current state may be wrong.

---

# 13. SELF-CORRECTION

A finding is not automatically true because a unit generated it.

Every significant finding should be classified:

```text
UNVERIFIED
SUPPORTED
CONTRADICTED
RESOLVED
REJECTED
```

The system must be capable of saying:

```text
Previous conclusion:
X

New evidence:
Y

Revision:
X is no longer supported.

New conclusion:
Z

Reason:
...
```

This revision history is mandatory.

---

# 14. MODEL PROVIDER ABSTRACTION

Do not hard-code the system around one AI provider.

Create an interface such as:

```text
ModelProvider
├── Generate()
├── Analyze()
├── Review()
└── Embed()
```

Allow providers to include:

```text
local Ollama
OpenAI-compatible API
other local inference servers
future providers
```

Different units should potentially use different models.

For example:

```text
fast model → syntax/error detection
reasoning model → architecture
specialized model → security review
small model → classification
```

---

# 15. HUMAN BOUNDARY

The user remains the authority.

The system may:

* propose
* analyze
* generate
* test
* criticize
* revise

But it should not silently perform irreversible external actions.

Require explicit approval for things such as:

```text
destructive filesystem operations
deployment
publishing
credential changes
external account actions
network-facing infrastructure changes
```

---

# 16. OBSERVABILITY

Build a live internal visualization/API.

Expose:

```text
active units
dormant units
attention weights
current Semi-State
current conflicts
current proposals
test status
spiral iteration
model calls
latency
token usage
confidence
revision history
```

The UI should make it possible to see the system's internal activity without pretending that the visualization represents literal consciousness.

---

# 17. INITIAL IMPLEMENTATION

Build an MVP first.

The MVP should contain:

```text
1. Project loader
2. Requirement parser
3. Unit scheduler
4. Semi-State
5. Event bus
6. Attention manager
7. Code proposal system
8. Workspace manager
9. Build/test runner
10. Critic
11. Synthesizer
12. Revision history
13. CLI
14. Basic web dashboard
```

Start with approximately:

```text
8–12 units
```

Do not build 100 units immediately.

The architecture must support scaling later.

---

# 18. RECOMMENDED PROJECT STRUCTURE

Use a clean separation similar to:

```text
spiral-codemaker/
│
├── cmd/
│   └── spiral/
│
├── core/
│   ├── state/
│   ├── units/
│   ├── attention/
│   ├── scheduler/
│   ├── events/
│   ├── memory/
│   ├── conflicts/
│   └── spiral/
│
├── models/
│   ├── provider/
│   └── routing/
│
├── code/
│   ├── proposals/
│   ├── workspace/
│   ├── patches/
│   └── validation/
│
├── execution/
│   ├── build/
│   ├── tests/
│   └── sandbox/
│
├── persistence/
│
├── api/
│
├── web/
│
└── tests/
```

Prefer Go for the orchestration/runtime layer if practical.

Use Python only where it provides a clear advantage.

---

# 19. FIRST DEMONSTRATION

The first successful demonstration should be:

```text
User:
"Build a small HTTP service that stores TODO items."

Spiral CodeMaker:

1. decomposes requirement
2. creates units
3. units analyze architecture
4. units produce competing designs
5. synthesizer selects a provisional design
6. generator creates code
7. tests run
8. failures become evidence
9. relevant units wake
10. code is revised
11. tests run again
12. critic attacks the final implementation
13. final synthesis occurs
14. system reports:
       - files created
       - tests
       - revisions
       - unresolved issues
       - confidence
       - architectural decisions
```

The important demonstration is NOT merely that it generates working code.

The demonstration is that **the system changes its own intermediate state because of evidence generated during its operation.**

---

# 20. DEVELOPMENT RULE

Do not spend the first implementation cycle trying to make the system philosophically perfect.

Build the machinery.

First make:

```text
units
+
state
+
events
+
attention
+
asynchronous scheduling
+
tests
+
revision
```

actually work.

Then experiment with increasingly sophisticated cognition.

The architecture should be capable of becoming more sophisticated without requiring a rewrite.

---

# FINAL OBJECTIVE

Build a code-making system in which:

**many small processes operate concurrently, at different cadences, sharing a mutable but historically traceable Semi-State, while an attention mechanism dynamically couples them and a recursive test/critique loop continually alters the system's trajectory.**

The goal is not:

> make the AI think faster.

The goal is:

> **increase the density, diversity, and feedback rate of useful cognitive operations without collapsing them into one monolithic loop.**

Build the MVP now.

Do not merely describe the architecture.

Create the actual project, implement the core abstractions, write tests, and demonstrate the first complete spiral iteration.
a