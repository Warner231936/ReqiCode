package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kilo/spiral-codemaker/api"
	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/models/registry"
)

func TestDaemonServerAcceptsRunSubmission(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	body := `{"intent":"Test daemon run","iterations":1,"output_dir":"` + filepath.ToSlash(tmpDir) + `/output"}`
	req := httptest.NewRequest("POST", "/api/runs/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	if resp["status"] != "submitted" {
		t.Errorf("expected submitted, got %v", resp["status"])
	}
	if resp["run_id"] == "" {
		t.Error("expected non-empty run_id")
	}
}

func TestDaemonServerListsRuns(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/runs", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var runs []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&runs); err != nil {
		t.Fatal(err)
	}

	if len(runs) != 0 {
		t.Errorf("expected 0 runs initially, got %d", len(runs))
	}
}

func TestDaemonServerModelsEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-daemon-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/models", nil)

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var models []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}

	if len(models) != 5 {
		t.Errorf("expected 5 default models, got %d", len(models))
	}
}

func TestDaemonFullRunIntegration(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-daemon-integration-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")
	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	body := `{"intent":"Build a small HTTP service that stores TODO items.","iterations":1,"output_dir":"` + filepath.ToSlash(outputPath) + `"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/runs/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var resp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatal(err)
	}

	runID := resp["run_id"].(string)

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(500 * time.Millisecond)

		rec2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("GET", "/api/runs/"+runID, nil)
		srv.Handler().ServeHTTP(rec2, req2)

		var runDetail map[string]any
		if err := json.NewDecoder(rec2.Body).Decode(&runDetail); err != nil {
			t.Fatal(err)
		}

		status := runDetail["status"].(string)
		if status == "completed" {
			break
		}
		if status == "failed" {
			t.Fatalf("run failed: %s", runDetail["error"])
		}
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/api/runs/"+runID, nil)
	srv.Handler().ServeHTTP(rec3, req3)

	var final map[string]any
	if err := json.NewDecoder(rec3.Body).Decode(&final); err != nil {
		t.Fatal(err)
	}

	if final["status"] != "completed" {
		t.Errorf("expected completed, got %s", final["status"])
	}

	if len(final["iterations"].([]any)) == 0 {
		t.Error("expected at least 1 iteration")
	}
}

func TestDaemonModelsPersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-daemon-persist-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv1 := api.NewDaemonServer(":0", configPath)
	srv1.ModelRegistry().AddProvider("huggingface", "huggingface", "https://api-inference.huggingface.co", "secret-token", map[string]any{
		"model": "gpt2",
	})

	if err := srv1.ModelRegistry().SaveConfig(); err != nil {
		t.Fatal(err)
	}

	srv2 := api.NewDaemonServer(":0", configPath)
	providers := srv2.ModelRegistry().ListProviders()

	found := false
	for _, p := range providers {
		if p == "huggingface" {
			found = true
		}
	}
	if !found {
		t.Error("expected huggingface provider to persist")
	}
}

func TestOrchestratorWithRegistry(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "spiral-orchestrator-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")
	registryPath := filepath.Join(tmpDir, "models.json")
	rt := registry.NewModelRegistry(registryPath)

	orch, err := system.NewOrchestratorWithRegistry("", outputPath, "Build a small HTTP service that stores TODO items.", 1, rt)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}

	if orch.ModelRegistry() == nil {
		t.Error("expected model registry to be set")
	}

	if err := orch.Run(); err != nil {
		t.Fatalf("orchestrator run failed: %v", err)
	}

	if len(orch.ModelRegistry().ListModels()) == 0 {
		t.Error("expected models to be available")
	}
}
