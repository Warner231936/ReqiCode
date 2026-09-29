package units_test

import (
	"context"
	"testing"

	"github.com/kilo/spiral-codemaker/core/units"
	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/routing"
)

func TestLLMClientValidation(t *testing.T) {
	router := routing.NewRouter()
	router.AddProvider("mock", provider.NewMockProvider("mock"))
	router.ConfigureDefaults()

	client := units.NewLLMClient(router)

	if !client.HasProvider(routing.CapFast) {
		t.Error("expected fast capability provider")
	}

	if !client.HasProvider(routing.CapReasoning) {
		t.Error("expected reasoning capability provider")
	}

	if client.HasProvider(routing.CapEmbed) {
	}
}

func TestLLMClientNoProvider(t *testing.T) {
	router := routing.NewRouter()

	client := units.NewLLMClient(router)

	if client.HasProvider(routing.CapFast) {
		t.Error("expected no provider for empty router")
	}

	if client.IsProviderAvailable(context.Background(), routing.CapFast) {
		t.Error("expected false for empty router")
	}
}

func TestLLMClientGenerate(t *testing.T) {
	router := routing.NewRouter()
	router.AddProvider("mock", provider.NewMockProvider("mock"))
	router.ConfigureDefaults()
	router.AddProvider("mock", provider.NewMockProvider("mock"))

	client := units.NewLLMClient(router)

	ctx := context.Background()
	resp, err := client.Generate(ctx, routing.CapFast, "write a Go function", 512, 0.7)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	if resp == "" {
		t.Error("expected non-empty response")
	}
}

func TestLLMClientGenerateNoProvider(t *testing.T) {
	router := routing.NewRouter()
	client := units.NewLLMClient(router)

	_, err := client.Generate(context.Background(), routing.CapFast, "test", 512, 0.7)
	if err == nil {
		t.Error("expected error when no provider available")
	}
}
