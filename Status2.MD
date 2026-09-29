# Status 2 - Local CUDA/GPU Focus

## Current State
- **Build**: Clean (`go vet` passes, `go build ./...` succeeds)
- **Tests**: All passing (9 packages)
- **Version**: v0.2.1

## Local Model Infrastructure (DONE)

### GGUF Provider Enhancements
- **CUDA support**: `NGPULayers` field controls GPU offloading (0 = CPU only, 999 = full offload)
- **Backend detection**: `UseCUDA`, `UseMetal`, `UseVulkan` flags
- **GPU info**: `GPUInfo()` method returns vendor, device, VRAM, backend, gpu-layers
- **Dynamic model loading**: `LoadModel()` with GPU layer config
- **Tensor splitting**: `TensorSplit` for multi-GPU setups
- **Multi-GPU**: `MainGPU` field for selecting primary GPU

### Local Model Downloader
- Downloads full model weights from HuggingFace (not API inference)
- Configurable cache directory
- Download progress tracking (bytes, percent, speed)
- Selective file download (gguf, bin, safetensors, json, txt)
- Lists downloaded models
- Resumes partially downloaded files

### llama.cpp Binary Manager
- GPU detection (NVIDIA via `nvidia-smi`, AMD/Intel via `lspci`)
- CUDA-enabled llama.cpp binary management
- Server lifecycle management (start/stop with n-gpu-layers)
- Health checking

### Ollama Provider
- Local model serving via Ollama (localhost:11434)
- Full ModelProvider interface implementation

## API Endpoints (New)
- `/api/gpu` — GPU info (vendor, device, VRAM, backend, layers)
- `/api/llamacpp` — llama.cpp status (installed, version, path, server running)
- `/api/models/download` — Download model from HF (POST, returns accepted)
- `/api/models/download-progress` — Get download progress
- `/api/models/downloaded` — List locally downloaded models

## CLI Commands (New)
- `spiral setup` — Detect GPU, show llama.cpp status, provide install instructions
- `spiral daemon --port :8080 --output <dir>` — Start daemon with web UI

## Web Dashboard (Updated)
- **GPU / Local tab**: GPU info, llama.cpp status, downloaded models
- **llama.cpp server management**: Load model with GPU layers
- **Daemon mode badge**: Shows DAEMON vs SINGLE RUN mode
- Model cards show "Use in Spiral" button for downloaded models
- All model management tabs updated with Ollama support

## Workflow for Local CUDA Usage
1. `spiral setup` — Check GPU, see llama.cpp status
2. Install llama.cpp with CUDA: `LLAMA_CUDA=1 make server`
3. Start llama.cpp server: `./server --model model.gguf --n-gpu-layers 999 --port 8080`
4. `spiral daemon --port :8080` — Start spiral daemon
5. Open `http://localhost:8080/dashboard/`
6. Go to Models → add GGUF provider pointing to `http://localhost:8080`
7. Submit runs via the web UI

## What's Next
- HF model GGUF conversion via llama.cpp
- Auto-start llama.cpp server from daemon
- GPU utilization monitoring in dashboard
- VRAM usage tracking during inference
