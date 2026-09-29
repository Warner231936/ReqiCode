package build

import (
	"context"
	"os/exec"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

type BuildRunner struct {
	workspacePath string
}

func NewBuildRunner(workspacePath string) *BuildRunner {
	return &BuildRunner{workspacePath: workspacePath}
}

type BuildResult struct {
	Success   bool   `json:"success"`
	Output    string `json:"output"`
	Error     string `json:"error,omitempty"`
	Duration  int64  `json:"duration_ms"`
}

func (b *BuildRunner) Build(ctx context.Context) BuildResult {
	start := time.Now()

	cmd := exec.CommandContext(ctx, "go", "build", "./...")
	cmd.Dir = b.workspacePath
	output, err := cmd.CombinedOutput()

	result := BuildResult{
		Output:   string(output),
		Duration: time.Since(start).Milliseconds(),
	}

	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
	}

	return result
}

func (b *BuildRunner) BuildResult(ctx context.Context) BuildResult {
	return b.Build(ctx)
}

func (b *BuildRunner) Vet(ctx context.Context) BuildResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, "go", "vet", "./...")
	cmd.Dir = b.workspacePath
	output, err := cmd.CombinedOutput()

	result := BuildResult{
		Output:   string(output),
		Duration: time.Since(start).Milliseconds(),
	}
	if err != nil {
		result.Success = false
		result.Error = err.Error()
	} else {
		result.Success = true
	}
	return result
}

func (b *BuildRunner) WorkspacePath() string {
	return b.workspacePath
}

func BuildResultToEvidence(br BuildResult, unitID string) state.Evidence {
	content := "build completed"
	if !br.Success {
		content = "build failed: " + br.Error
	}
	return state.Evidence{
		ID:       "",
		Type:     state.EvidenceAnalysis,
		Content:  content,
		Strength: state.ConfidenceMedium,
		Provenance: state.NewProvenance(unitID),
	}
}
