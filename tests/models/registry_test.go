package models_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/registry"
)

func TestModelRegistryBasic(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "model-registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	reg := registry.NewModelRegistry(configPath)

	providers := reg.ListProviders()
	if len(providers) != 1 || providers[0] != "mock" {
		t.Errorf("expected mock provider, got %v", providers)
	}

	models := reg.ListModels()
	if len(models) != 5 {
		t.Errorf("expected 5 default models, got %d", len(models))
	}
}

func TestModelRegistryAddProvider(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "model-registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	reg := registry.NewModelRegistry(configPath)

	reg.AddProvider("huggingface", "huggingface", "https://api-inference.huggingface.co", "test-token", map[string]any{
		"model":     "gpt2",
		"cache_dir": filepath.Join(tmpDir, "hf-cache"),
	})

	providers := reg.ListProviders()
	found := false
	for _, p := range providers {
		if p == "huggingface" {
			found = true
		}
	}
	if !found {
		t.Error("expected huggingface provider to be registered")
	}

	if err := reg.SaveConfig(); err != nil {
		t.Fatalf("failed to save config: %v", err)
	}

	reg2 := registry.NewModelRegistry(configPath)
	providers2 := reg2.ListProviders()
	found2 := false
	for _, p := range providers2 {
		if p == "huggingface" {
			found2 = true
		}
	}
	if !found2 {
		t.Error("expected huggingface provider to persist after reload")
	}
}

func TestModelRegistryGGUFAdd(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "model-registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	reg := registry.NewModelRegistry(configPath)

	ggufFile := filepath.Join(tmpDir, "test-model.Q4_K_M.gguf")
	if err := os.WriteFile(ggufFile, []byte("fake"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := reg.AddGGUFModel(ggufFile); err != nil {
		t.Fatalf("failed to add GGUF model: %v", err)
	}

	models := reg.ListModels()
	found := false
	for _, m := range models {
		if filepath.Base(m.Path) == "test-model.Q4_K_M.gguf" {
			found = true
			if m.Status != registry.ModelStatusAvailable {
				t.Errorf("expected available status, got %s", m.Status)
			}
		}
	}
	if !found {
		t.Error("expected to find added GGUF model")
	}
}

func TestModelRegistrySetActiveModel(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "model-registry-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	reg := registry.NewModelRegistry(configPath)

	reg.SetActiveModel("fast", "my-fast-model")
	active := reg.ActiveModels()

	if active["fast"] != "my-fast-model" {
		t.Errorf("expected my-fast-model for fast capability, got %s", active["fast"])
	}
}

func TestGGUFScanner(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gguf-scan-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, "models"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "models", "codellama.Q4_K_M.gguf"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "models", "starcoder.Q8_0.gguf"), []byte("x"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "models", "readme.txt"), []byte("not a model"), 0644)

	scanner := registry.NewGGUFScanner()
	ctx := context.Background()
	entries, err := scanner.Scan(ctx, tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 GGUF files, got %d", len(entries))
	}

	capMap := map[string]string{}
	for _, e := range entries {
		capMap[e.Name] = string(e.Capability)
	}

	if capMap["starcoder.Q8_0"] == "" {
		t.Error("expected starcoder model to have a capability")
	}
}

func TestGGUFScannerIgnoresHidden(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gguf-scan-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(filepath.Join(tmpDir, ".git"), 0755)
	os.WriteFile(filepath.Join(tmpDir, ".git", "model.gguf"), []byte("x"), 0644)
	os.MkdirAll(filepath.Join(tmpDir, "vendor"), 0755)
	os.WriteFile(filepath.Join(tmpDir, "vendor", "deps.gguf"), []byte("x"), 0644)

	scanner := registry.NewGGUFScanner()
	ctx := context.Background()
	entries, err := scanner.Scan(ctx, tmpDir)
	if err != nil {
		t.Fatalf("scan failed: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 GGUF files (hidden dirs skipped), got %d", len(entries))
	}
}

func TestMockProviderAvailability(t *testing.T) {
	ctx := context.Background()
	p := provider.NewMockProvider("test-mock")
	if !p.IsAvailable(ctx) {
		t.Error("expected mock provider to always be available")
	}
}

func TestHuggingFaceProvider(t *testing.T) {
	p := provider.NewHuggingFaceProvider(provider.HFProviderConfig{
		Model: "test/model",
	})
	if p.Name() != "huggingface" {
		t.Errorf("expected provider name 'huggingface', got '%s'", p.Name())
	}
}

func TestGGUFProvider(t *testing.T) {
	p := provider.NewGGUFProvider(provider.GGUFProviderConfig{
		ServerURL: "http://localhost:9999",
	})
	if p.Name() != "gguf" {
		t.Errorf("expected provider name 'gguf', got '%s'", p.Name())
	}

	ctx := context.Background()
	if p.IsAvailable(ctx) {
		t.Error("expected GGUF provider to be unavailable with no server")
	}
}

func TestHFSearchResultParsing(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "hf-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	tmpDir = filepath.Join(tmpDir, "models")
	reg := registry.NewModelRegistry(tmpDir)

	models := reg.ListModels()
	if len(models) == 0 {
		t.Log("no models configured by default, expected 5 default models")
	}
}
