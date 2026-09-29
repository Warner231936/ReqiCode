package provider

import (
	"context"
	"fmt"
	"time"
)

type OllamaProviderConfig struct {
	ServerURL string `json:"server_url,omitempty"`
}

type OllamaProvider struct {
	config OllamaProviderConfig
}

func NewOllamaProvider(config OllamaProviderConfig) *OllamaProvider {
	if config.ServerURL == "" {
		config.ServerURL = "http://localhost:11434"
	}
	return &OllamaProvider{config: config}
}

func (p *OllamaProvider) Name() string {
	return "ollama"
}

func (p *OllamaProvider) Generate(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if len(req.Messages) == 0 {
		return CompletionResponse{}, fmt.Errorf("no messages provided")
	}

	return CompletionResponse{
		ID:           fmt.Sprintf("ollama-%d", time.Now().Unix()),
		Model:        req.Model,
		Content:      fmt.Sprintf("[ollama] %s", req.Messages[len(req.Messages)-1].Content),
		FinishReason: "stop",
		Usage: Usage{
			PromptTokens:   100,
			CompletionTokens: 150,
			TotalTokens:    250,
		},
	}, nil
}

func (p *OllamaProvider) Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error) {
	resp, err := p.Generate(ctx, CompletionRequest{
		Model:      req.Model,
		Messages:   []Message{{Role: "user", Content: fmt.Sprintf("Analyze: %s. Questions: %v", req.Subject, req.Questions)}},
		Temperature: 0.5,
		MaxTokens:   2048,
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

func (p *OllamaProvider) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	resp, err := p.Generate(ctx, CompletionRequest{
		Model:      req.Model,
		Messages:   []Message{{Role: "user", Content: fmt.Sprintf("Review code in %s:\n%s\nQuestions: %v", req.Filepath, req.Code, req.Questions)}},
		Temperature: 0.3,
		MaxTokens:   2048,
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

func (p *OllamaProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error) {
	return EmbedResult{}, fmt.Errorf("ollama: embed not yet implemented")
}

func (p *OllamaProvider) IsAvailable(ctx context.Context) bool {
	return true
}
