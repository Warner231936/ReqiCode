package models_test

import (
	"context"
	"testing"

	"github.com/kilo/spiral-codemaker/models/provider"
)

func TestGGUFProviderIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test (requires running llama.cpp server)")
	}

	p := provider.NewGGUFProvider(provider.GGUFProviderConfig{
		ServerURL: "http://127.0.0.1:8080",
	})

	ctx := context.Background()
	if !p.IsAvailable(ctx) {
		t.Skip("llama.cpp server not running on localhost:8080")
	}

	t.Run("Generate", func(t *testing.T) {
		resp, err := p.Generate(ctx, provider.CompletionRequest{
			Model:       "tinyllama",
			Messages:    []provider.Message{{Role: "user", Content: "What is 2+2?"}},
			Temperature: 0.1,
			MaxTokens:   64,
		})
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty content")
		}
		t.Logf("Response: %s", resp.Content)
	})

	t.Run("GenerateCode", func(t *testing.T) {
		resp, err := p.Generate(ctx, provider.CompletionRequest{
			Model:       "tinyllama",
			Messages:    []provider.Message{{Role: "user", Content: "Write a Go function that returns hello world"}},
			Temperature: 0.7,
			MaxTokens:   200,
		})
		if err != nil {
			t.Fatalf("Generate failed: %v", err)
		}
		if resp.Content == "" {
			t.Fatal("expected non-empty content")
		}
		t.Logf("Code response: %s", resp.Content)
	})
}
