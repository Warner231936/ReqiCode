# SemiState — the spiral's shared working memory

`SemiState` (`core/state/semi_state.go`) is the single mutable record a
spiral run iterates on. Every unit — requirements analyst, architect,
code generator, test designer, test runner, debugger, critic — reads
from and writes to the same `SemiState`. It is the blackboard pattern:
units do not talk to each other, they talk to the state.

## What it holds

| Field | Meaning |
|---|---|
| `Revision` | Iteration counter, incremented each spiral pass |
| `Requirements` | Parsed, prioritised requirements from the intent |
| `ArchitecturePlan` | Chosen component layout (and its rival alternative) |
| `GeneratedFiles` | Path → content of every file written to the workspace |
| `Proposals` | Code proposals awaiting approval/application |
| `Hypotheses` | "architecture X satisfies the requirements" claims, with status + confidence |
| `Evidence` | Observations, analyses, test passes/failures — the audit trail |
| `Contradictions` | Conflicts between evidence, with severity |
| `TestResults` | `go test` outcomes: status, failure class, command, stdout |
| `AttentionMap` | Per-unit attention weight (drives what runs next) |
| `Confidence` | Aggregate confidence in the current state |
| `Decisions`, `Findings`, `Objections` | Decision log, claim-status changes, formal objections |

## How it stays correct

**Concurrency.** A `sync.RWMutex` guards every field. Writers take the
write lock; readers take the read lock and return defensive copies, so a
caller cannot mutate internal slices through a returned reference.

**Tamper evidence.** Each revision is chained (`core/state/chain.go`):
`hash = sha256(prevHash || canonicalContent)`. `canonicalContent`
serialises every epistemic field in a stable, sorted order and
deliberately excludes timestamps and durations, so two runs of the same
logic produce the same chain — which is what makes a golden regression
baseline possible. `VerifyChain` walks from genesis and locates the exact
revision where a retroactive edit surfaces: altering revision N
invalidates every hash from N+1 onward. `Clone` copies the chain too, so
forking the state cannot shed its history.

## How it achieves a full iteration with no model

The spiral does not require an LLM. Every unit has a deterministic
template fallback that populates the same fields:

- **Architect** (`generatePlanFromTemplate`) classifies the intent by
  keywords (`http`/`service`/`api` → HTTP server, `cli`/`command` →
  CLI tool, else a default layout) and emits a component list.
- **CodeGenerator** (`generateFromTemplate`) renders one template per
  planned component (entry point, store, handler, model, core).
- **TestDesigner** produces tests for the planned components, deriving
  them from the generated code's own signatures.
- **TestRunner** executes `go test ./...` and records the real outcome.

The orchestrator (`core/system/orchestrator.go`) runs these in phases —
architecture → synthesis → code proposal → **build gate** → test →
critique — and the build gate (`go build ./...`) plus the test runner
supply ground truth from the actual compiler. A unit's template output
is written into the `SemiState`, the compiler verdict is recorded as
evidence and a test result, and the next iteration reacts to that.

So the loop closes on itself without a model: templates propose, the
compiler disposes, and the `SemiState` carries the result forward. The
LLM path (`generateFromLLM`, `generatePlanFromLLM`) is an accelerator
layered on the same state, not a prerequisite — when it is absent or
fails the validation gate, the template path fills the identical fields
and the run proceeds.
