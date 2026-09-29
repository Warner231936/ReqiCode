# Status 1

## Current State
- **Project**: ReqiCode — self-modifying, spiral-iterating AI coding system in Go
- **Build**: Clean (`go vet` passes, `go build ./...` succeeds)
- **Tests**: All passing (9 packages: attention, conflicts, events, integration, models, proposals, spiral, state)
- **Version**: v0.2.0

## Architecture
- **Core**: `core/state` (SemiState with lock-protected fields), `core/events` (async pub/sub bus), `core/attention` (focus manager), `core/scheduler` (unit registry/runtime), `core/spiral` (iteration loop), `core/units` (10+ concrete units), `core/system` (orchestrator)
- **Code**: `code/proposals` (proposer + patcher + validator + conflict detector), `code/workspace` (workspace manager), `code/validation` (security + Go syntax checks)
- **Execution**: `execution/build` (compiler), `execution/tests` (test runner), `execution/sandbox` (sandboxed exec)
- **Models**: `models/provider` (mock, huggingface, gguf), `models/registry` (model discovery, config persistence, GGUF scanner, HF lister), `models/routing` (router with capability-based routing)
- **Persistence**: `persistence/` (memory store)
- **API**: `api/server.go` (HTTP endpoints), `web/` (dashboard)
- **CLI**: `cmd/spiral/main.go` (commands: run, dashboard, models, scan, hf-search)

## Spiral Iteration Model
- Each iteration records: starting state, ending state, changes, evidence
- Units vote on claims, evidence is weighted by attention
- Confidence decreases on test failures (→Low), increases on passes (→High)
- Competing hypotheses are proposed, then accepted/rejected by synthesis
- Self-correction: failing tests trigger evidence → critic objections → synthesis lowers confidence + records findings → next iteration debugs

## Model Management Features (NEW)
- **HuggingFace provider**: Fetches/inference via HF inference API, downloads models to local cache
- **GGUF provider**: Communicates with llama.cpp server (HTTP), loads models dynamically
- **Model registry**: Config persistence (`models.json`), provider management, active model assignment per capability
- **GGUF scanner**: Recursively searches directories/drives for `.gguf` files, skips `.git`/`vendor`/`node_modules`, auto-detects capability from filename
- **HF model lister**: Searches public HuggingFace Hub for models, parses tags for capability detection
- **CLI commands**:
  - `spiral run --intent "..." --enable-hf --hf-token <token>` — run with HF backend
  - `spiral scan <path>` — scan for GGUF files
  - `spiral hf-search <query> --token <token>` — search HF Hub
  - `spiral models --list` / `--providers` / `--add-provider` / `--activate`
  - `spiral dashboard` — start dashboard only

## Web Dashboard (`/dashboard/`)
- Full SPA-style dashboard with tabs: System State, Attention, Units, Tests, Proposals, Conflicts, Files, Revision History
- **Model Management section** with 4 tabs:
  - Configured Models (activate per capability)
  - Scan for GGUF (scan any path/drive)
  - HuggingFace Search
  - Providers (add/edit/remove)
- Real-time event streaming via SSE (`/api/stream`)
- Auto-refresh every 5s

## API Endpoints
- `/api/snapshot` — full system state snapshot
- `/api/iterations` — spiral iteration records
- `/api/units` — registered units
- `/api/attention` — attention weights
- `/api/report` — final text report
- `/api/stream` — SSE event stream
- `/api/file/<path>` — read generated file
- `/api/models` — list configured models
- `/api/models/providers` — list providers
- `/api/models/scan?path=<path>` — scan for GGUF
- `/api/models/hf-search?q=<query>` — search HF Hub
- `/api/models/add-provider` — add provider (POST)
- `/api/models/activate` — activate model for capability (POST)
- `/api/models/gguf-add?path=<path>` — add GGUF model (POST)

## Key Data Structures
- `SemiState`: central claim store with lock-protected fields
- `ArchitecturePlan`, `CodeProposal`, `TestResult`, `Evidence`, `Finding`, `Hypothesis`, `Decision`, `Objection`, `Conflict`
- `ModelEntry`, `ModelRegistry`, `ModelConfig`, `ProviderConfigEntry`
- 3 confidence levels: Low (0.25), Medium (0.50), High (0.75)
- Revision tracking via incrementing `Revision` field
