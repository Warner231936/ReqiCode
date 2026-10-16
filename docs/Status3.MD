# Status 3 - Local CUDA/GPU Support + LLM Integration

## Since Status 2

### LLM Integration (`core/units/llm_client.go`)
- **LLMClient struct** — wraps Router, provides capability-based provider access
- **Generate()** — sends prompts to LLM via capability routing (fast/reasoning/specialize)
- **Analyze()** — structured analysis with findings + confidence
- **Review()** — code review with severity-tagged issues
- **GenerateCode()** — specialized code generation (temperature 0.3, 4096 tokens)
- **GenerateReview()** — pre-configured code review questions (bugs, security, idiomatic Go)
- **IsProviderAvailable()** / **HasProvider()** — availability checks
- **Validation**: `validateLLMFiles()` checks LLM output contains valid Go (`package` keyword) before use, falls back to templates if invalid

### Code Generator LLM Integration (`core/units/code_generator.go`)
- When an LLM provider is available (CapSpecialize), generates code via LLM
- Fallback to template-based generation if LLM fails or returns invalid code
- `generateFromLLM()` — constructs architecture prompt, calls LLM, parses response
- `parseLLMResponse()` — parses `---FILE: path--- ... ---ENDFILE---` format from LLM responses

### Critic LLM Review (`core/units/critic.go`)
- When LLM available, reviews each generated file for high/critical issues
- Issues added as CritiqueIssue objects and feed into objection system
- Falls back to rule-based critique when no LLM available

### Local Model Downloader (`models/registry/downloader.go`)
- `LocalModelDownloader` — downloads full model weights from HuggingFace
- Progress tracking: bytes downloaded, percent, speed (B/s)
- Selective file download: filters to .gguf, .bin, .safetensors, .json, .txt
- `ListDownloadedModels()` — lists cached models
- `ConvertToGGUF()` — GGUF conversion stub (requires llama.cpp)

### llama.cpp Manager (`models/registry/llamacpp.go`)
- `LlamaCppManager` — manages llama.cpp binary lifecycle
- **GPU detection**: NVIDIA via `nvidia-smi`, AMD/Intel via `lspci`
- **Server management**: Start/stop llama.cpp server with model path + n-gpu-layers
- **Status endpoint**: Reports installed, version, path, GPU backend, running state
- **Binary installation guidance**: Tells user to build with `LLAMA_CUDA=1 make server`

### CUDA-Aware GGUF Provider (`models/provider/gguf.go`)
- `GGUFProviderConfig` fields: `UseCUDA`, `UseMetal`, `UseVulkan`, `NGPULayers`, `MainGPU`, `TensorSplit`
- `GPUInfo()` method + `GPUInfo` struct: vendor, device, VRAM, backend, gpu-layers
- `StartServer()` — launches llama.cpp with `--n-gpu-layers` flag
- Automatic GPU layer setting (999 = offload all to GPU)

### API Endpoints (New)
- `/api/gpu` — GPU info (vendor, backend, VRAM, layers)
- `/api/llamacpp` — llama.cpp status (installed, version, running)
- `/api/llamacpp/load` — POST to start llama.cpp server with model + GPU layers
- `/api/models/download` — POST to download full HF model weights
- `/api/models/download-progress` — Get download progress
- `/api/models/downloaded` — List locally cached models

### Dashboard GPU/Local Tab (New)
- Real-time GPU info (backend, vendor, device, VRAM, layers)
- llama.cpp server status panel
- Model loading form (model path + GPU layers)
- Downloaded models browser with "use in spiral" button
- Provider status shows GPU ready / llama.cpp running / CPU fallback

### CLI Updates
- `spiral setup` — detects GPU, shows llama.cpp status, prints install instructions
- `spiral daemon` — daemon mode for web UI
- All model management commands

### Tests
- `tests/units/llm_client_test.go` — 4 tests: validation, no provider, generate, generate no provider
- `tests/integration/llamacpp_test.go` — 6 tests: status, GPU, downloaded models, invalid JSON, missing intent, daemon-less mode

### Build Status
- `go vet ./...` — clean
- `go build ./...` — clean
- **10 test packages**: attention, conflicts, events, integration, models, proposals, spiral, state, units — all passing
