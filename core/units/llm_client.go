package units

import (
	"context"
	"fmt"

	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type LLMClient struct {
	router *routing.Router
}

func NewLLMClient(router *routing.Router) *LLMClient {
	return &LLMClient{router: router}
}

func (c *LLMClient) Generate(ctx context.Context, capability routing.ModelCapability, prompt string, maxTokens int, temperature float64) (string, error) {
	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return "", fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		return "", fmt.Errorf("provider %s is not available", p.Name())
	}

	resp, err := p.Generate(ctx, provider.CompletionRequest{
		Model:       modelName,
		Messages:    []provider.Message{{Role: "user", Content: prompt}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("LLM generation failed: %w", err)
	}

	return resp.Content, nil
}

func (c *LLMClient) Analyze(ctx context.Context, capability routing.ModelCapability, subject string, questions []string) (provider.AnalysisResult, error) {
	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return provider.AnalysisResult{}, fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		return provider.AnalysisResult{}, fmt.Errorf("provider %s is not available", p.Name())
	}

	return p.Analyze(ctx, provider.AnalysisRequest{
		Model:    modelName,
		Subject:  subject,
		Questions: questions,
	})
}

func (c *LLMClient) Review(ctx context.Context, capability routing.ModelCapability, code, filepath string, questions []string) (provider.ReviewResult, error) {
	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return provider.ReviewResult{}, fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		return provider.ReviewResult{}, fmt.Errorf("provider %s is not available", p.Name())
	}

	return p.Review(ctx, provider.ReviewRequest{
		Model:    modelName,
		Code:     code,
		Filepath: filepath,
		Questions: questions,
	})
}

func (c *LLMClient) HasProvider(capability routing.ModelCapability) bool {
	_, _, ok := c.router.GetProvider(capability)
	return ok
}

func (c *LLMClient) IsProviderAvailable(ctx context.Context, capability routing.ModelCapability) bool {
	p, _, ok := c.router.GetProvider(capability)
	if !ok {
		return false
	}
	return p.IsAvailable(ctx)
}

func (c *LLMClient) GenerateCode(ctx context.Context, capability routing.ModelCapability, prompt string) (string, error) {
	return c.Generate(ctx, capability, prompt, 4096, 0.3)
}

func (c *LLMClient) GenerateReview(ctx context.Context, capability routing.ModelCapability, code, filepath string) (provider.ReviewResult, error) {
	return c.Review(ctx, capability, code, filepath, []string{
		"What are the bugs?",
		"Are there security issues?",
		"Is the code idiomatic Go?",
	})
}
