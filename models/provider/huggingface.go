package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

type HFProviderConfig struct {
	APIToken  string `json:"api_token,omitempty"`
	Endpoint  string `json:"endpoint,omitempty"`
	Model     string `json:"model,omitempty"`
	CacheDir  string `json:"cache_dir,omitempty"`
}

type HuggingFaceProvider struct {
	config HFProviderConfig
	client *http.Client
}

type hfRequest struct {
	Model       string   `json:"model"`
	Messages    []Message `json:"messages"`
	Temperature float64  `json:"temperature,omitempty"`
	MaxTokens   int      `json:"max_tokens,omitempty"`
}

type hfChoice struct {
	Index        int            `json:"index"`
	Message      map[string]any `json:"message"`
	FinishReason string         `json:"finish_reason"`
}

type hfResponse struct {
	ID      string       `json:"id"`
	Object  string       `json:"object"`
	Created int64        `json:"created"`
	Model   string       `json:"model"`
	Choices []hfChoice   `json:"choices"`
	Usage   Usage        `json:"usage"`
	Error   *hfError     `json:"error,omitempty"`
}

type hfError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

func NewHuggingFaceProvider(config HFProviderConfig) *HuggingFaceProvider {
	if config.Endpoint == "" {
		config.Endpoint = "https://api-inference.huggingface.co"
	}
	if config.CacheDir == "" {
		config.CacheDir = filepath.Join(os.TempDir(), "hf-models")
	}
	return &HuggingFaceProvider{
		config: config,
		client: &http.Client{Timeout: 60 * time.Second},
	}
}

func (p *HuggingFaceProvider) Name() string {
	return "huggingface"
}

func (p *HuggingFaceProvider) Generate(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	model := req.Model
	if model == "" {
		model = p.config.Model
	}
	if model == "" {
		return CompletionResponse{}, fmt.Errorf("huggingface: no model specified")
	}

	hfReq := hfRequest{
		Model:       model,
		Messages:    req.Messages,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
	}

	apiURL := fmt.Sprintf("%s/models/%s/v1/chat/completions", p.config.Endpoint, model)

	body, err := json.Marshal(hfReq)
	if err != nil {
		return CompletionResponse{}, err
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(body))
	if err != nil {
		return CompletionResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if p.config.APIToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIToken))
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("huggingface: request failed: %w", err)
	}
	defer resp.Body.Close()

	return p.parseResponse(resp, model)
}

func (p *HuggingFaceProvider) Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error) {
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

func (p *HuggingFaceProvider) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
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

func (p *HuggingFaceProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error) {
	return EmbedResult{}, fmt.Errorf("huggingface: embed not yet implemented")
}

func (p *HuggingFaceProvider) IsAvailable(ctx context.Context) bool {
	apiURL := fmt.Sprintf("%s/models/%s", p.config.Endpoint, p.config.Model)
	httpReq, _ := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if p.config.APIToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIToken))
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == 200
}

func (p *HuggingFaceProvider) DownloadModel(ctx context.Context, modelID, destPath string) error {
	cacheDir := p.config.CacheDir
	localPath := filepath.Join(cacheDir, modelID)

	if _, err := os.Stat(localPath); err == nil {
		return nil
	}

	if err := os.MkdirAll(filepath.Dir(localPath), 0755); err != nil {
		return err
	}

	snapshotURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/config.json", modelID)

	httpReq, err := http.NewRequestWithContext(ctx, "GET", snapshotURL, nil)
	if err != nil {
		return err
	}
	if p.config.APIToken != "" {
		httpReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", p.config.APIToken))
	}

	resp, err := p.client.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to check model %s: %w", modelID, err)
	}
	resp.Body.Close()

	return os.MkdirAll(localPath, 0755)
}

func (p *HuggingFaceProvider) parseResponse(resp *http.Response, model string) (CompletionResponse, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to read response: %w", err)
	}

	var hfResp hfResponse
	if err := json.Unmarshal(body, &hfResp); err != nil {
		return CompletionResponse{}, fmt.Errorf("failed to parse response: %w (body: %s)", err, string(body))
	}

	if hfResp.Error != nil {
		return CompletionResponse{}, fmt.Errorf("huggingface api error: %s (%s)", hfResp.Error.Message, hfResp.Error.Type)
	}

	var content string
	if len(hfResp.Choices) > 0 {
		msg, ok := hfResp.Choices[0].Message["content"].(string)
		if ok {
			content = msg
		}
	}

	return CompletionResponse{
		ID:           hfResp.ID,
		Model:        model,
		Content:      content,
		FinishReason: hfResp.Choices[0].FinishReason,
		Usage:        hfResp.Usage,
	}, nil
}
