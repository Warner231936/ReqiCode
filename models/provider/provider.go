package provider

import (
	"context"
)

type CompletionRequest struct {
	Model      string            `json:"model"`
	Messages   []Message         `json:"messages"`
	Temperature float64          `json:"temperature,omitempty"`
	MaxTokens   int              `json:"max_tokens,omitempty"`
	Provider    string           `json:"provider,omitempty"`
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type CompletionResponse struct {
	ID           string  `json:"id"`
	Model        string  `json:"model"`
	Content      string  `json:"content"`
	FinishReason string  `json:"finish_reason"`
	Usage        Usage   `json:"usage"`
}

type Usage struct {
	PromptTokens   int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens    int `json:"total_tokens"`
}

type AnalysisRequest struct {
	Model    string   `json:"model"`
	Subject  string   `json:"subject"`
	Questions []string `json:"questions"`
	Provider string   `json:"provider,omitempty"`
}

type AnalysisResult struct {
	Subject    string              `json:"subject"`
	Findings   []string            `json:"findings"`
	Confidence float64             `json:"confidence"`
	Usage      Usage               `json:"usage"`
}

type ReviewRequest struct {
	Model    string   `json:"model"`
	Code     string   `json:"code"`
	Filepath string   `json:"filepath"`
	Questions []string `json:"questions"`
	Provider string   `json:"provider,omitempty"`
}

type ReviewResult struct {
	Issues      []ReviewIssue `json:"issues"`
	Confidence  float64       `json:"confidence"`
	Usage       Usage         `json:"usage"`
}

type ReviewIssue struct {
	Severity string `json:"severity"`
	Line     int    `json:"line,omitempty"`
	Message  string `json:"message"`
	Rule     string `json:"rule,omitempty"`
}

type EmbedRequest struct {
	Model  string   `json:"model"`
	Inputs []string `json:"inputs"`
	Provider string `json:"provider,omitempty"`
}

type EmbedResult struct {
	Vectors [][]float32 `json:"vectors"`
	Usage    Usage      `json:"usage"`
}

type ModelProvider interface {
	Name() string
	Generate(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error)
	Review(ctx context.Context, req ReviewRequest) (ReviewResult, error)
	Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error)
	IsAvailable(ctx context.Context) bool
}
