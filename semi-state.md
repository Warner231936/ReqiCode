# Semi-State: A State-Centered Approach to AI Software Construction

## Overview

The Semi-State system represents a paradigm shift in AI-assisted software development. Instead of treating code generation as a one-shot LLM prompt, Semi-State provides a structured, iterative framework where AI development proceeds through competing hypotheses, evidence accumulation, conflict resolution, and gradual refinement—all tracked in a unified state model.

## The Problem with Traditional AI Coding

Traditional AI coding tools follow a linear pipeline:
1. User describes what they want
2. LLM generates a code blob
3. Code is written to disk
4. User tests and discovers it doesn't work
5. User refines prompt and tries again

This approach fails because:
- **No state persistence**: Each interaction is disconnected; the LLM "forgets" previous attempts
- **No hypothesis testing**: Alternative designs aren't systematically evaluated
- **No conflict resolution**: Contradictory code decisions pile up silently
- **No evidence tracking**: There's no record of what worked vs. what failed
- **No iterative refinement**: The "spiral" of improvement is manual and error-prone

## The Semi-State Architecture

### Core Concept

Semi-State is a **persistent, structured state object** that accumulates knowledge throughout the development cycle. It tracks:

- **Requirements**: Structured user intent and constraints
- **Architecture Plan**: Competing design proposals with confidence scores
- **Hypotheses**: Assumptions and their supporting evidence
- **Decisions**: Chosen approaches and their rationale
- **Proposals**: Concrete code change proposals with conflict detection
- **Evidence**: Observations, analysis results, and test outcomes
- **Files**: Generated source files with provenance tracking
- **Conflicts**: Detected contradictions between proposals or requirements
- **Findings**: Insights discovered during development
- **Objections**: Raised concerns about proposals or designs

### The Spiral Process

Development proceeds through iterative cycles:

```
Requirements → Architecture → Code Generation → Test Design → Test Execution → Critique → Refinement
```

Each cycle:
1. **Analyzes** the current state
2. **Proposes** changes based on the plan
3. **Tests** those changes
4. **Reviews** results and raises objections
5. **Resolves** conflicts and updates decisions
6. **Accumulates** evidence for the next cycle

### Key Innovations

#### 1. Hypothesis-Driven Development
Instead of committing to a single design, Semi-State maintains multiple competing hypotheses:
- "In-memory store is simplest correct design for an HTTP service" (Confidence: Medium)
- "File-based JSON storage provides persistence without external deps" (Confidence: Low)

The system selects the best hypothesis based on evidence, and can revisit decisions when new evidence emerges.

#### 2. Evidence-Based Decision Making
Every claim is backed by evidence with a confidence score:
- `EvidenceObservation`: Direct observations from code execution
- `EvidenceAnalysis`: Architectural analysis results
- `EvidenceTestFailure`: Test results that contradict assumptions

This creates an auditable trail of *why* each decision was made.

#### 3. Conflict Detection
Before applying any code change, the system validates it against existing proposals and detects conflicts:
- File path conflicts (two proposals writing to the same file)
- Type conflicts (incompatible interfaces)
- Requirement conflicts (code that contradicts a requirement)

#### 4. Attention Management
Units communicate through an attention system rather than direct coupling:
- When requirements are parsed, the architect and decomposer are "boosted"
- When files are generated, the test designer is alerted
- When tests fail, the critic and debugger are prioritized

This creates emergent behavior without hard-coded orchestration.

#### 5. Local LLM Integration
Semi-State can operate with:
- **Remote LLM APIs** (HuggingFace, OpenAI, etc.)
- **Local LLM servers** (llama.cpp, Ollama)
- **Mock providers** for testing

The router abstraction means the same development pipeline works regardless of the LLM backend, enabling fully local, private, offline development.

## Comparison: Traditional vs. Semi-State

| Aspect | Traditional AI Coding | Semi-State |
|--------|----------------------|------------|
| State persistence | None | Full audit trail |
| Design exploration | One design per prompt | Multiple competing hypotheses |
| Conflict detection | Undefined behavior | Explicit conflict objects |
| Test integration | After-the-fact | Built into every cycle |
| Evidence tracking | None | Structured evidence with confidence |
| Iterative improvement | Manual retry | Automated spiral cycles |
| Local LLM support | Limited/bolted on | First-class abstraction |
| Reproducibility | No | Full semi-state replay |
| Auditability | No | Complete decision log |

## Technical Implementation

### State Model

The Semi-State is implemented as a Go struct with thread-safe access patterns:

```go
type SemiState struct {
    requirements     []Requirement
    architecturePlan *ArchitecturePlan
    hypotheses       []Hypothesis
    decisions        []Decision
    proposals        []Proposal
    files            map[string]FileEntry
    evidence         []Evidence
    conflicts        []Conflict
    objections       []Objection
    findings         []Finding
    revision         int
    mu               sync.RWMutex
}
```

### Provider Abstraction

The model provider abstraction allows interchangeable LLM backends:

```
LLMClient → Router → ModelProvider
  ↓          ↓         ↓
Generate   Match    GGUFProvider     (llama.cpp server)
           capability HuggingFaceProvider (HF API)
                       MockProvider       (testing)
                       OllamaProvider     (local)
```

Each provider registers capabilities (reasoning, specialize, fast, etc.) and the router routes requests based on capability requirements.

### Capability System

Units request LLM capabilities rather than specific models:
- `CapReasoning`: Used for architecture planning and analysis
- `CapSpecialize`: Used for code generation and test creation
- `CapFast`: Used for quick classification and categorization
- `CapClassification`: Used for categorizing and routing code elements
- `CapEmbed`: Used for semantic search and retrieval

This ensures the right tool is used for the right job, regardless of backend.

## What This Makes Possible

### 1. Self-Healing Code Generation
When tests fail, the system doesn't just report failure—it:
1. Analyzes the failure
2. Identifies the root cause
3. Generates a corrected proposal
4. Validates against existing proposals
5. Applies the fix and retests

### 2. Design Space Exploration
The system can maintain and evaluate multiple architectural approaches simultaneously:
- Generate code for Plan A (in-memory store)
- Generate code for Plan B (file-based store)
- Run tests for both
- Compare results and select the best

### 3. Local/Privacy-First AI Development
With full local LLM support:
- No data leaves the developer's machine
- No API costs or rate limits
- Works in air-gapped environments
- Full control over the model used

### 4. Audit-Ready Development
Every decision is recorded with:
- Rationale
- Supporting evidence
- Confidence level
- Who/what made the decision (provenance)

### 5. Reproduducible Development
The entire semi-state can be saved, loaded, and replayed:
- Reproduce bugs deterministically
- A/B test different architectures
- Resume interrupted development sessions
- Share development progress with team members

## Future Directions

### 1. Multi-Agent Orchestration
Semi-State can be extended to coordinate multiple AI agents, each specializing in different aspects (security, performance, UX, testing).

### 2. Continuous Architecture Evolution
The semi-state can persist across multiple user sessions, allowing the architecture to evolve over time as requirements change.

### 3. Learning from History
Past semi-states can be analyzed to improve future development cycles—learning which architectures, patterns, and decision sequences lead to successful outcomes.

### 4. Collaborative Development
Multiple developers can contribute evidence, hypotheses, and decisions to the same semi-state, creating a shared understanding of the project's evolution.

## Conclusion

Semi-State transforms AI-assisted development from a stateless, one-shot generation tool into a structured, iterative, evidence-driven process. By maintaining a rich, persistent state model that tracks hypotheses, decisions, evidence, and conflicts, it enables a level of sophistication and reliability that traditional approaches cannot match.

The result is software that is not only generated correctly the first time, but continuously refined, tested, and validated through a systematic process that mirrors scientific methodology—the same approach that has served engineering disciplines well for centuries.
