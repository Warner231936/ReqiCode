package provider

import (
	"context"
	"fmt"
)

type MockProvider struct {
	name    string
	latency float64
}

func NewMockProvider(name string) *MockProvider {
	return &MockProvider{name: name, latency: 0}
}

func NewMockProviderWithLatency(name string, latency float64) *MockProvider {
	return &MockProvider{name: name, latency: latency}
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) Generate(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	return CompletionResponse{
		ID:           fmt.Sprintf("mock-comp-%d", len(req.Messages)),
		Model:        req.Model,
		Content:      generateMockResponse(req),
		FinishReason: "stop",
		Usage: Usage{
			PromptTokens:   100,
			CompletionTokens: 150,
			TotalTokens:    250,
		},
	}, nil
}

func (m *MockProvider) Analyze(ctx context.Context, req AnalysisRequest) (AnalysisResult, error) {
	return AnalysisResult{
		Subject: req.Subject,
		Findings: []string{
			"Mock analysis finding",
			"Structure appears sound",
		},
		Confidence: 0.5,
		Usage: Usage{
			PromptTokens:   50,
			CompletionTokens: 30,
			TotalTokens:    80,
		},
	}, nil
}

func (m *MockProvider) Review(ctx context.Context, req ReviewRequest) (ReviewResult, error) {
	return ReviewResult{
		Issues: []ReviewIssue{
			{
				Severity: "low",
				Message:  "Mock review: no critical issues found",
			},
		},
		Confidence: 0.3,
		Usage: Usage{
			PromptTokens:   80,
			CompletionTokens: 60,
			TotalTokens:    140,
		},
	}, nil
}

func (m *MockProvider) Embed(ctx context.Context, req EmbedRequest) (EmbedResult, error) {
	result := EmbedResult{
		Vectors: make([][]float32, len(req.Inputs)),
		Usage: Usage{
			PromptTokens:   0,
			CompletionTokens: 0,
			TotalTokens:    0,
		},
	}
	for i := range req.Inputs {
		result.Vectors[i] = []float32{0.1, 0.2, 0.3, 0.4, 0.5}
	}
	return result, nil
}

func (m *MockProvider) IsAvailable(ctx context.Context) bool {
	return true
}

func generateMockResponse(req CompletionRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	lastMsg := req.Messages[len(req.Messages)-1]
	return fmt.Sprintf("[mock response to: %s]", lastMsg.Content)
}
