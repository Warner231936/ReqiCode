package integration_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/api"
	"github.com/kilo/spiral-codemaker/core/system"
)

func TestLlamaCppStatusEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "llamacpp-status-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/llamacpp", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var status map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&status); err != nil {
		t.Fatal(err)
	}

	if status["installed"] != false {
		t.Error("expected not installed in test env")
	}

	if status["path"] == "" {
		t.Error("expected non-empty path")
	}
}

func TestGPuInfoEndpoint(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "gpu-info-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/gpu", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}

	var info map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}

	if info["available"] == nil {
		t.Error("expected available field")
	}
}

func TestDownloadedModelsEmpty(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "downloaded-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/models/downloaded", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var models []map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&models); err != nil {
		t.Fatal(err)
	}

	if len(models) != 0 {
		t.Errorf("expected 0 downloaded models, got %d", len(models))
	}
}

func TestDaemonSubmitInvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-invalid-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/runs/submit", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestDaemonSubmitMissingIntent(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-no-intent-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	body := `{}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/runs/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for missing intent, got %d", rec.Code)
	}
}

func TestDaemonSubmitWithoutDaemonMode(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "daemon-nodmgr-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "output")
	orch, err := system.NewOrchestrator("", outputPath, "test", 1)
	if err != nil {
		t.Fatalf("failed to create orchestrator: %v", err)
	}
	srv := api.NewServer(orch, ":0")

	body := `{"intent":"test","iterations":1}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/runs/submit", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without daemon mode, got %d", rec.Code)
	}
}
