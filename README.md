# Spiral CodeMaker

**A local-first, self-modifying, spiral-iterating AI coding system in Go.**

Spiral CodeMaker treats a **typed epistemic state** as the artifact and
source code as one projection of it. Instead of a linear
`prompt → LLM → code` pipeline, it decomposes a coding intent into a set
of concurrent cognitive micro-units that operate at different cadences,
communicate through a typed event bus, and continuously critique, test,
and revise their own intermediate conclusions across spiral iterations.

It is **local-first**: it runs entirely against a local llama.cpp server
and GGUF models. No API keys, no cloud, no network dependency.

- **Module:** `github.com/kilo/spiral-codemaker`
- **Language:** Go 1.27.1 (single external dependency: `gopter` for property tests)
- **Version:** v0.2.0

---

## Quick start

```bash
# build
go build -o spiral.exe ./cmd/spiral

# run a spiral iteration on an intent (uses a local llama.cpp server on :8090)
./spiral.exe run --intent "Create a Go package that counts words in a string" --output my-project

# start the web dashboard
./spiral.exe dashboard --port :8080

# one-shot local model setup (llama.cpp + CUDA + GPU detection)
./spiral.exe setup
```

A run produces a complete, compiling Go project under `<output>/output`,
along with an instrumented record of every hypothesis, proposal, evidence
item, and test result under `<output>/instrumentation/`.

---

## Architecture

### The Semi-State

The `SemiState` (`core/state/semi_state.go`) is the single mutable record
a run iterates on. Every unit reads from and writes to it — units do not
talk to each other, they talk to the state (blackboard pattern). It holds
the revision counter, requirements, architecture plan, generated files,
proposals, hypotheses, evidence, contradictions, test results, attention
weights, confidence, decisions, findings, and objections.

Two properties keep it correct:

- **Concurrency** — a `sync.RWMutex` guards every field; readers return
  defensive copies so callers cannot mutate internal slices.
- **Tamper evidence** — each revision is chained
  (`core/state/chain.go`): `hash = sha256(prevHash || canonicalContent)`.
  `canonicalContent` serialises every epistemic field in a stable, sorted
  order and excludes timestamps and durations, so two runs of the same
  logic produce the same chain. `VerifyChain` walks from genesis and
  locates the exact revision where a retroactive edit surfaces. `Clone`
  copies the chain, so forking the state cannot shed its history.

See [`docs/semi-state.md`](docs/semi-state.md) for the full write-up,
including how a complete iteration runs with **no model at all**.

### Cognitive units

Each concern is a `Unit` (`core/units/`) with a `Run(ctx, semiState, bus)`
method, an attention weight, and a cadence. The orchestrator
(`core/system/orchestrator.go`) drives them through the spiral phases:

| Phase | Unit | Role |
|---|---|---|
| Requirement | `requirements_analyst` | Parse and prioritise the intent |
| Decomposition | `decomposer` | Split the intent into sub-problems |
| Architecture | `architect` | Propose a component layout |
| Synthesis | `synthesizer` | Merge the best conclusions |
| Code Proposal | `code_generator` | Render source files |
| **Build gate** | `execution/build` | `go build ./...` — ground truth |
| Test | `test_designer`, `test_runner` | Generate and run `go test ./...` |
| Critique | `debugger`, `consistency_checker`, `security_analyst`, `critic` | Repair and review |
| Documentation | `documentation_writer` | Emit README |
| Final Synthesis | `synthesizer` | Close the iteration |

Every unit has a **deterministic template fallback**. When no LLM is
configured, or the LLM response fails the validation gate, the template
path populates the identical `SemiState` fields and the run proceeds.
The LLM path is an accelerator layered on the same state, not a
prerequisite — templates propose, the compiler disposes, and the state
carries the result forward.

### Validation gates

Generated code is checked before it reaches the workspace
(`core/units/code_generator.go`): package clause first, balanced braces,
no unused imports, no placeholder text, and — critically — a
**duplicate-declaration gate** that tracks every package-level function
*and* type name per package directory, so a model that declares the same
symbol in two files (once as an interface, once as a struct) is caught
before the compiler ever sees it. A build failure is recorded and fails
the iteration honestly rather than being reported as a pass.

---

## The five pillars

The self-improvement loop is built on five mechanisms, each independently
tested:

1. **Convergence** (`core/converge`) — a Lyapunov-style potential function
   over the state (test pass rate, evidence density, confidence) plus a
   Pareto frontier that remembers the best revision found, so the spiral
   returns its best result rather than its last.
2. **Causal attribution** (`core/causal`) — a ledger that attributes each
   objective (compile, test, coverage, security, conflict reduction) to
   the proposal that caused it, with SHA-256-verified replay snapshots.
3. **Regression baseline** (`core/regression`) — a deterministic,
   field-level baseline of a known-good state; a run is gated against it
   and drift is reported per field.
4. **Canary promotion** (`core/canary`) — a candidate change is tried on a
   branch and promoted only if it passes the gate, has a valid chain, and
   does not lower the pass rate or worsen the potential beyond tolerance.
5. **Self-modification** (`core/selfmodify`) — the system proposes edits to
   its own source, validated for self-import cycles, missing imports,
   package clauses, and duplicate declarations before application.

---

## Local-first LLM

Spiral CodeMaker serves models through a local llama.cpp HTTP server
(OpenAI-compatible `/v1/chat/completions` and `/completion` endpoints).

- **Models** live in `models-cache/` (gitignored, multi-GB). Bundled
  choices: Qwen2.5-Coder-14B-Instruct, Qwen2.5-7B-Instruct,
  TinyLlama-1.1B (all Q4_K_M GGUF).
- **llama.cpp prebuilts** live in `third_party/` (gitignored), including
  the CUDA build. The CUDA runtime DLLs (`cudart64_12.dll`,
  `cublas64_12.dll`, `cublasLt64_12.dll`) must sit next to
  `llama-server.exe` or `ggml-cuda.dll` silently fails to load and the
  model falls back to CPU.
- **GPU** offload is automatic (`-ngl all`); `spiral setup` detects the
  GPU and configures the server.
- **Provider routing** (`models/`) selects a model per capability
  (fast / reasoning / specialize / classification / embed) from a
  registry. A mock provider exists for tests but is never selected when a
  real model is active.

LLM call deadlines are derived from the token budget, not a fixed
constant: a 2048-token code response gets the time its shape needs,
while structured-response calls (analysis, review) use a shorter bound.

---

## CLI reference

```
spiral <command> [flags]

Commands:
  run         Run spiral iteration on an intent
  dashboard   Start web dashboard
  daemon      Start daemon mode (web UI for submitting runs)
  setup       Setup local tools (llama.cpp with CUDA, GPU detection)
  models      List/manage models and providers
  scan <path> Scan a path for GGUF files
  hf-search   Search HuggingFace Hub
  baseline    Capture or check the regression baseline
  canary      Trial and promote a candidate change
  improve     Run the self-improvement loop
  version     Print version
  help        Show help
```

`spiral run` key flags: `--intent` (required), `--output`, `--iterations`,
`--serve`, `--local-model <gguf>` (auto-start a server), `--gguf-server <url>`,
`--enable-hf`, `--hf-token`.

---

## API reference

The dashboard/daemon server (`api/server.go`) exposes a REST API:

- **State:** `/api/snapshot`, `/api/iterations`, `/api/units`, `/api/events`,
  `/api/attention`, `/api/report`, `/api/health`, `/api/stream` (SSE)
- **Files:** `/api/file/<path>`
- **Models:** `/api/models`, `/api/models/scan`, `/api/models/hf-search`,
  `/api/models/add-provider`, `/api/models/activate`, `/api/models/gguf-add`,
  `/api/models/providers`, `/api/models/download`, `/api/models/convert`,
  `/api/models/download-progress`, `/api/models/downloaded`
- **Infra:** `/api/gpu`, `/api/llamacpp`, `/api/llamacpp/load`
- **Runs:** `/api/runs`, `/api/runs/submit`, `/api/runs/<id>`

---

## Directory structure

```
api/            REST API server + daemon
cmd/spiral/     CLI entrypoint
cmd/agent/      parallel agent subsystem (see docs/statusreport.md)
code/           workspace, proposals, validation, patches
core/           the engine:
  units/        cognitive micro-units
  state/        SemiState + tamper-evident chain
  system/       orchestrator
  spiral/       iteration manager
  converge/     potential function + Pareto frontier
  causal/       attribution ledger + replay
  regression/   baseline + drift gate
  canary/       branch trial + gated promotion
  selfmodify/   self-edit proposal validation + harness
  modelcheck/   model adequacy assessment
  apiscan/      Go API extraction (go/ast)
  verify/       mechanical test synthesis
  attention/    attention manager
  conflicts/    contradiction manager
  events/       typed event bus
  scheduler/    unit scheduler
execution/      build runner, test runner, sandbox
models/         provider, registry, capability routing
persistence/    persistent memory
tests/          integration + e2e suites
testdata/       test fixtures (regression baselines)
web/            dashboard assets
docs/           documentation (see below)
third_party/    llama.cpp prebuilts (gitignored)
models-cache/   GGUF model weights (gitignored)
instrumentation/ per-run artifacts (gitignored, regenerated)
```

---

## Testing

```bash
go build ./...                          # whole module
go test ./core/... ./tests/...          # 18 packages, ~600s
go test ./core/units/... -run TestDeclared -v   # validation-gate tests
```

Property-based tests use `gopter` (`core/causal/properties_test.go`,
`tests/state/properties_test.go`). The state chain, lifecycle, and
causal ledger are covered by dedicated property suites.

---

## Documentation

All design and status documents live in [`docs/`](docs/):

| Document | Contents |
|---|---|
| [`report.md`](docs/report.md) | Architecture paper — executive summary |
| [`PROJECT.MD`](docs/PROJECT.MD) | Original build specification |
| [`final.md`](docs/final.md) | What the system is |
| [`semi-state.md`](docs/semi-state.md) | The SemiState and the no-model path |
| [`difference.md`](docs/difference.md) | Why Semi-State is not RAG or a knowledge graph |
| [`mindbreak.md`](docs/mindbreak.md) | How the architecture beats a frontier model |
| [`state of the art.md`](docs/state%20of%20the%20art.md) | Measured status, pillar by pillar |
| [`ready.md`](docs/ready.md) | Readiness assessment (blockers) |
| [`statusreport.md`](docs/statusreport.md) | Agent subsystem status |
| `Status1.MD` … `status4.md` | Historical status snapshots |

## License

MIT — see [`LICENSE`](LICENSE).
