package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type GGUFProviderConfig struct {
	ServerURL      string `json:"server_url,omitempty"`
	ModelPath      string `json:"model_path,omitempty"`
	NGPULayers     int    `json:"n_gpu_layers,omitempty"`
	MainGPU        int    `json:"main_gpu,omitempty"`
	TensorSplit    string `json:"tensor_split,omitempty"`
	UseCUDA        bool   `json:"use_cuda,omitempty"`
	UseMetal       bool   `json:"use_metal,omitempty"`
	UseVulkan      bool   `json:"use_vulkan,omitempty"`
	CacheDir       string `json:"cache_dir,omitempty"`
}

type GGUFProvider struct {
	config GGUFProviderConfig
	client *http.Client
}

type ggufLoadRequest struct {
	Model        string `json:"model"`
	NGPULayers    int    `json:"n_gpu_layers,omitempty"`
	MainGPU       int    `json:"main_gpu,omitempty"`
	TensorSplit   string `json:"tensor_split,omitempty"`
}

type ggufLoadResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

type ggufCompletionRequest struct {
	Model            string   `json:"model"`
	Messages         []Message `json:"messages"`
	Temperature      float64  `json:"temperature,omitempty"`
	MaxTokens        int      `json:"max_tokens,omitempty"`
	Stream           bool     `json:"stream,omitempty"`
	Stop             []string `json:"stop,omitempty"`
	RepeatLastN      int      `json:"repeat_last_n,omitempty"`
	RepeatPenalty    float64  `json:"repeat_penalty,omitempty"`
	TopK             int      `json:"top_k,omitempty"`
	TopP             float64  `json:"top_p,omitempty"`
}

type ggufCompletionResponse struct {
	Model       string         `json:"model"`
	Created     int64          `json:"created"`
	Choices     []ggufChoice   `json:"choices"`
	Usage       Usage          `json:"usage"`
	Object      string         `json:"object"`
}

type ggufChoice struct {
	Index        int            `json:"index"`
	Message      map[string]any `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

func NewGGUFProvider(config GGUFProviderConfig) *GGUFProvider {
	if config.ServerURL == "" {
		config.ServerURL = "http://localhost:8080"
	}
	return &GGUFProvider{
		config: config,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

func (p *GGUFProvider) Name() string {
	return "gguf"
}

func (p *GGUFProvider) Generate(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	apiURL := fmt.Sprintf("%s/v1/chat/completions", p.config.ServerURL)

	ggufReq := ggufCompletionRequest{
		Model:       p.config.ModelPath,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		Stream:      false,
	}

	body, err := json.Marshal(ggufReq)
	if err != nil {
		return CompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("gguf: request failed (is llama.cpp server running?): %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return CompletionResponse{}, fmt.Errorf("gguf: server returned %d", resp.StatusCode)
	}

	var ggufResp ggufCompletionResponse
	if err := json.NewDecoder(resp.Body).Decode(&ggufResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to parse response: %w", err)
	}

	var content string
	if len(ggufResp.Choices) > 0 {
		if msg, ok := ggufResp.Choices[0].Message["content"].(string); ok {
			content = msg
		}
	}

	return CompletionResponse{
		ID:           fmt.Sprintf("gguf-%d", ggufResp.Created),
		Model:        ggufResp.Model,
		Content:      content,
		FinishReason: ggufResp.Choices[0].FinishReason,
		Usage:        ggufResp.Usage,
	}, nil
}

func (p *GGUFProvider) Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error) {
	prompt := fmt.Sprintf("Analyze the following subject and provide findings.\nSubject: %s\nQuestions: %v\nResponse format: findings as bullet points, then a confidence score (0-1).", req.Subject, req.Questions)

	resp, err := p.Generate(ctx, CompletionRequest{
		Model:      req.Model,
		Messages:   []Message{{Role: "user", Content: prompt}},
		Temperature: 0.5,
		MaxTokens:   2048,
		Provider:    req.Provider,
	})
	if err != nil {
		return AnalysisResult{}, err
	}

	return AnalysisResult{
		Subject:    req.Subject,
		Findings:   []string{resp.Content},
		Confidence: 0.7,
		Usage:      resp.Usage,
	}, nil
}

func (p *GGUFProvider) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	prompt := fmt.Sprintf("Review the following Go code for issues:\n\nFile: %s\nCode:\n%s\nQuestions: %v\n\nResponse format: list issues with severity (low/medium/high/critical), line number, and message.", req.Filepath, req.Code, req.Questions)

	resp, err := p.Generate(ctx, CompletionRequest{
		Model:      req.Model,
		Messages:   []Message{{Role: "user", Content: prompt}},
		Temperature: 0.3,
		MaxTokens:   2048,
		Provider:    req.Provider,
	})
	if err != nil {
		return ReviewResult{}, err
	}

	return ReviewResult{
		Issues: []ReviewIssue{
			{
				Severity: "medium",
				Message:  resp.Content,
			},
		},
		Confidence: 0.6,
		Usage:      resp.Usage,
	}, nil
}

func (p *GGUFProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error) {
	return EmbedResult{}, fmt.Errorf("gguf: embed not yet implemented")
}

func (p *GGUFProvider) IsAvailable(ctx context.Context) bool {
	healthURL := fmt.Sprintf("%s/health", p.config.ServerURL)
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", healthURL, nil)

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (p *GGUFProvider) LoadModel(ctx context.Context, modelPath string) error {
	gpuLayers := p.config.NGPULayers
	if gpuLayers == 0 && (p.config.UseCUDA || p.config.UseMetal || p.config.UseVulkan) {
		gpuLayers = 999
	}

	if gpuLayers == 0 {
		gpuLayers = 0
	}

	unloadURL := fmt.Sprintf("%s/v1/unload", p.config.ServerURL)
	httpReq, _ := http.NewRequestWithContext(ctx, "POST", unloadURL, bytes.NewReader([]byte("{}")))
	httpReq.Header.Set("Content-Type", "application/json")
	p.client.Do(httpReq)

	loadURL := fmt.Sprintf("%s/v1/load_model", p.config.ServerURL)
	loadReq := ggufLoadRequest{
		Model:        modelPath,
		NGPULayers:   gpuLayers,
		MainGPU:      p.config.MainGPU,
		TensorSplit:  p.config.TensorSplit,
	}
	body, _ := json.Marshal(loadReq)
	httpReq2, _ := http.NewRequestWithContext(ctx, "POST", loadURL, bytes.NewReader(body))
	httpReq2.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(httpReq2)
	if err != nil {
		return fmt.Errorf("gguf: failed to load model: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		var loadResp ggufLoadResponse
		json.NewDecoder(resp.Body).Decode(&loadResp)
		return fmt.Errorf("gguf: model load returned %d: %s", resp.StatusCode, loadResp.Message)
	}
	return nil
}

func (p *GGUFProvider) GPUInfo() GPUInfo {
	return detectGPUInfo(p.config)
}

type GPUInfo struct {
	Available    bool     `json:"available"`
	Vendor       string   `json:"vendor"`
	Device       string   `json:"device"`
	VRAM         int64    `json:"vram_mb"`
	NGPULayers    int      `json:"n_gpu_layers"`
	Backend      string   `json:"backend"`
}

func detectGPUInfo(config GGUFProviderConfig) GPUInfo {
	info := GPUInfo{
		Available: config.UseCUDA || config.UseMetal || config.UseVulkan,
		Backend:   "cpu",
	}
	if config.UseCUDA {
		info.Vendor = "nvidia"
		info.Device = "cuda"
		info.Backend = "cuda"
		if info.NGPULayers == 0 {
			info.NGPULayers = 999
		}
	} else if config.UseMetal {
		info.Vendor = "apple"
		info.Device = "metal"
		info.Backend = "metal"
		if info.NGPULayers == 0 {
			info.NGPULayers = 999
		}
	} else if config.UseVulkan {
		info.Vendor = "vulkan"
		info.Device = "vulkan"
		info.Backend = "vulkan"
		if info.NGPULayers == 0 {
			info.NGPULayers = 999
		}
	}
	return info
}
