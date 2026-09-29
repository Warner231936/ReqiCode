package api

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/models/registry"
)

type RunStatus string

const (
	RunStatusPending  RunStatus = "pending"
	RunStatusRunning  RunStatus = "running"
	RunStatusComplete RunStatus = "completed"
	RunStatusFailed   RunStatus = "failed"
	RunStatusCanceled RunStatus = "canceled"
)

type RunConfig struct {
	Intent         string            `json:"intent"`
	Iterations     int               `json:"iterations"`
	OutputDir      string            `json:"output_dir,omitempty"`
	HFToken        string            `json:"hf_token,omitempty"`
	EnableHF       bool              `json:"enable_hf"`
	GGUFServerURL  string            `json:"gguf_server_url,omitempty"`
}

type RunResult struct {
	ID          string             `json:"id"`
	Config      RunConfig          `json:"config"`
	Status      RunStatus          `json:"status"`
	StartTime   time.Time          `json:"start_time,omitempty"`
	EndTime     time.Time          `json:"end_time,omitempty"`
	Orch         *system.Orchestrator `json:"-"`
	OutputPath  string             `json:"output_path"`
	Error       string             `json:"error,omitempty"`
}

type RunManager struct {
	mu            sync.RWMutex
	runs          map[string]*RunResult
	modelReg      *registry.ModelRegistry
	ctx           context.Context
	cancelFunc    context.CancelFunc
	eventCallback func(string, any)
}

func NewRunManager(configPath string, eventCallback func(string, any)) *RunManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &RunManager{
		runs:          make(map[string]*RunResult),
		modelReg:      registry.NewModelRegistry(configPath),
		ctx:           ctx,
		cancelFunc:    cancel,
		eventCallback: eventCallback,
	}
}

func (rm *RunManager) SubmitRun(config RunConfig) (*RunResult, error) {
	rm.mu.Lock()
	defer rm.mu.Unlock()

	if config.Intent == "" {
		return nil, fmt.Errorf("intent is required")
	}
	if config.Iterations <= 0 {
		config.Iterations = 3
	}

	runID := fmt.Sprintf("run-%d", len(rm.runs)+1)
	outputDir := config.OutputDir
	if outputDir == "" {
		outputDir = filepath.Join(os.TempDir(), "spiral-runs", runID)
	}
	absOutput, _ := filepath.Abs(outputDir)
	if err := os.MkdirAll(filepath.Join(absOutput, "output"), 0755); err != nil {
		return nil, err
	}

	result := &RunResult{
		ID:        runID,
		Config:    config,
		Status:    RunStatusPending,
		StartTime: time.Now(),
		OutputPath: absOutput,
	}

	rm.runs[runID] = result

	go rm.executeRun(result)

	return result, nil
}

func (rm *RunManager) executeRun(result *RunResult) {
	rm.mu.Lock()
	result.Status = RunStatusRunning
	rm.mu.Unlock()

	absOutput := result.OutputPath

	if result.Config.EnableHF && result.Config.HFToken != "" {
		rm.modelReg.AddProvider("huggingface", "huggingface", "https://api-inference.huggingface.co", result.Config.HFToken, map[string]any{
			"model":     "gpt2",
			"cache_dir": filepath.Join(absOutput, "hf-cache"),
		})
	}
	if result.Config.GGUFServerURL != "" {
		rm.modelReg.AddProvider("gguf-local", "gguf", result.Config.GGUFServerURL, "", map[string]any{
			"model_path": filepath.Join(absOutput, "models"),
		})
	}

	orch, err := system.NewOrchestratorWithRegistry("", absOutput, result.Config.Intent, result.Config.Iterations, rm.modelReg)
	if err != nil {
		rm.mu.Lock()
		result.Status = RunStatusFailed
		result.Error = err.Error()
		result.EndTime = time.Now()
		rm.mu.Unlock()
		if rm.eventCallback != nil {
			rm.eventCallback(result.ID, map[string]any{
				"event": "run_failed",
				"run_id": result.ID,
				"error": err.Error(),
			})
		}
		return
	}

	result.Orch = orch

	if err := orch.Run(); err != nil {
		rm.mu.Lock()
		result.Status = RunStatusFailed
		result.Error = err.Error()
		result.EndTime = time.Now()
		rm.mu.Unlock()
		if rm.eventCallback != nil {
			rm.eventCallback(result.ID, map[string]any{
				"event": "run_failed",
				"run_id": result.ID,
				"error": err.Error(),
			})
		}
		return
	}

	rm.mu.Lock()
	result.Status = RunStatusComplete
	result.EndTime = time.Now()
	rm.mu.Unlock()

	if err := orch.SaveModelConfig(); err != nil {
	}

	if rm.eventCallback != nil {
		rm.eventCallback(result.ID, map[string]any{
			"event": "run_completed",
			"run_id": result.ID,
		})
	}
}

func (rm *RunManager) GetRun(runID string) *RunResult {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	return rm.runs[runID]
}

func (rm *RunManager) ListRuns() []*RunResult {
	rm.mu.RLock()
	defer rm.mu.RUnlock()
	runs := make([]*RunResult, 0, len(rm.runs))
	for _, r := range rm.runs {
		runs = append(runs, r)
	}
	return runs
}

func (rm *RunManager) ModelRegistry() *registry.ModelRegistry {
	return rm.modelReg
}

func (rm *RunManager) Shutdown() {
	rm.cancelFunc()
}

type RunSummary struct {
	ID         string     `json:"id"`
	Intent     string     `json:"intent"`
	Status     RunStatus  `json:"status"`
	StartTime  time.Time  `json:"start_time,omitempty"`
	EndTime    time.Time  `json:"end_time,omitempty"`
	OutputPath string     `json:"output_path"`
	Error      string     `json:"error,omitempty"`
}
