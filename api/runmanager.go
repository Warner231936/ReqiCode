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
	TaskID      string             `json:"task_id,omitempty"`
	Result      *TaskResult        `json:"result,omitempty"`
}

type RunManager struct {
	mu            sync.RWMutex
	runs          map[string]*RunResult
	cancelFuncs   map[string]context.CancelFunc
	modelReg      *registry.ModelRegistry
	ctx           context.Context
	cancelFunc    context.CancelFunc
	eventCallback func(string, any)
}

func NewRunManager(configPath string, eventCallback func(string, any)) *RunManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &RunManager{
		runs:          make(map[string]*RunResult),
		cancelFuncs:   make(map[string]context.CancelFunc),
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
	runCtx, runCancel := context.WithCancel(rm.ctx)
	rm.cancelFuncs[result.ID] = runCancel
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

	orchCtx, orchCancel := context.WithCancel(runCtx)
	defer orchCancel()
	orch, err := system.NewOrchestratorWithRegistry("", absOutput, result.Config.Intent, result.Config.Iterations, rm.modelReg)
	if err != nil {
		rm.mu.Lock()
		result.Status = RunStatusFailed
		result.Error = err.Error()
		result.EndTime = time.Now()
		result.Result = rm.extractTaskResult(result, nil, err.Error())
		delete(rm.cancelFuncs, result.ID)
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
	orch.WithContext(orchCtx)
	result.Orch = orch

	if err := orch.Run(); err != nil {
		rm.mu.Lock()
		result.Status = RunStatusFailed
		result.Error = err.Error()
		result.EndTime = time.Now()
		result.Result = rm.extractTaskResult(result, orch, err.Error())
		delete(rm.cancelFuncs, result.ID)
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
	result.Result = rm.extractTaskResult(result, orch, "")
	_ = orch.SaveModelConfig()
	delete(rm.cancelFuncs, result.ID)
	rm.mu.Unlock()

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

func (rm *RunManager) CancelRun(runID string) bool {
	rm.mu.Lock()
	cancel, ok := rm.cancelFuncs[runID]
	rm.mu.Unlock()
	if !ok {
		return false
	}
	cancel()
	rm.mu.Lock()
	result := rm.runs[runID]
	if result != nil {
		result.Status = RunStatusCanceled
		result.EndTime = time.Now()
	}
	delete(rm.cancelFuncs, runID)
	rm.mu.Unlock()
	return true
}

func (rm *RunManager) SubmitEngineeringTask(task EngineeringTask) (*RunResult, error) {
	intent := task.Objective
	if task.Rationale != "" {
		intent = task.Rationale + "\n\n" + intent
	}
	if len(task.AcceptanceCriteria) > 0 {
		intent += "\n\nAcceptance Criteria:\n"
		for _, ac := range task.AcceptanceCriteria {
			intent += "- " + ac + "\n"
		}
	}

	config := RunConfig{
		Intent:     intent,
		Iterations: 3,
	}
	if task.WorkspacePath != "" {
		config.OutputDir = task.WorkspacePath
	}

	result, err := rm.SubmitRun(config)
	if err != nil {
		return nil, err
	}

	rm.mu.Lock()
	result.TaskID = task.TaskID
	if task.TaskID == "" {
		result.TaskID = result.ID
	}
	rm.mu.Unlock()

	return result, nil
}

func (rm *RunManager) extractTaskResult(result *RunResult, orch *system.Orchestrator, runErr string) *TaskResult {
	taskResult := &TaskResult{
		TaskID:    result.TaskID,
		Objective: result.Config.Intent,
		StartedAt: result.StartTime,
	}

	if result.EndTime.After(result.StartTime) {
		taskResult.DurationMs = result.EndTime.Sub(result.StartTime).Milliseconds()
	}
	taskResult.CompletedAt = result.EndTime

	if runErr != "" {
		taskResult.Status = TaskStatusFailed
		taskResult.ErrorSummary = runErr
		taskResult.VerificationStatus = "failed"
		return taskResult
	}

	taskResult.Status = TaskStatusVerified
	taskResult.VerificationStatus = "passed"

	if orch != nil {
		ss := orch.SemiState()
		if ss != nil {
			taskResult.TestResults = ss.GetTestResults()
			chainValid := ss.VerifyChain()
			if chainValid.Valid {
				taskResult.VerificationStatus = "passed"
			} else {
				taskResult.VerificationStatus = "chain_invalid"
			}
		}

		if orch.Workspace() != nil {
			files, _ := orch.Workspace().ListFiles()
			taskResult.ArtifactLocations = files
			plan := ss.GetArchitecturePlan()
			if plan != nil {
				taskResult.FilesChanged = make([]FileChange, 0, len(plan.Components))
				for _, c := range plan.Components {
					taskResult.FilesChanged = append(taskResult.FilesChanged, FileChange{
						Path:      c.Path,
						Operation: "create",
					})
				}
			}
		}
	}

	return taskResult
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
