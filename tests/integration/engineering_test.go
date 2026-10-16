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
)

func TestEngineeringTaskSubmission(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	task := `{
		"task_id": "eng-001",
		"objective": "Add a health check endpoint to the API",
		"rationale": "Clients need to verify service readiness before sending traffic",
		"acceptance_criteria": ["GET /health returns 200", "Response includes status field"]
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader(task))
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
	if resp["task_id"] != "eng-001" {
		t.Errorf("expected task_id eng-001, got %v", resp["task_id"])
	}
	if resp["run_id"] == "" {
		t.Error("expected non-empty run_id")
	}
}

func TestEngineeringTaskMissingObjective(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-missing-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	task := `{"task_id":"eng-002"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader(task))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestEngineeringTaskInvalidJSON(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-badjson-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader("{invalid}"))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestEngineeringRunDetailsIncludeTaskResult(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-result-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	task := `{"task_id":"eng-res-001","objective":"Create a package main that prints Hello World","acceptance_criteria":["Program prints exactly Hello World"]}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader(task))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", rec.Code, rec.Body.String())
	}

	var submitResp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&submitResp); err != nil {
		t.Fatal(err)
	}
	runID := submitResp["run_id"].(string)

	deadline := time.Now().Add(60 * time.Second)
	var finalStatus string
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)

		rec2 := httptest.NewRecorder()
		req2 := httptest.NewRequest("GET", "/api/runs/"+runID, nil)
		srv.Handler().ServeHTTP(rec2, req2)

		var detail map[string]any
		if err := json.NewDecoder(rec2.Body).Decode(&detail); err != nil {
			t.Fatal(err)
		}

		status, _ := detail["status"].(string)
		if status == "completed" || status == "failed" || status == "canceled" {
			finalStatus = status
			break
		}
	}

	if finalStatus == "" {
		t.Fatal("timed out waiting for run to complete")
	}

	rec3 := httptest.NewRecorder()
	req3 := httptest.NewRequest("GET", "/api/runs/"+runID, nil)
	srv.Handler().ServeHTTP(rec3, req3)

	var final map[string]any
	if err := json.NewDecoder(rec3.Body).Decode(&final); err != nil {
		t.Fatal(err)
	}

	if final["status"] != finalStatus {
		t.Errorf("expected status %s, got %s", finalStatus, final["status"])
	}

	result, ok := final["result"].(map[string]any)
	if !ok {
		t.Fatal("expected result field in response")
	}

	if result["task_id"] != "eng-res-001" {
		t.Errorf("expected task_id eng-res-001, got %v", result["task_id"])
	}

	expectedStatus := "VERIFIED"
	if finalStatus == "failed" {
		expectedStatus = "FAILED"
	}
	if result["status"] != expectedStatus {
		t.Errorf("expected result status %s, got %v", expectedStatus, result["status"])
	}

	if result["verification_status"] == "" {
		t.Error("expected non-empty verification_status")
	}

	if result["duration_ms"] == nil {
		t.Error("expected duration_ms to be set")
	}
}

func TestEngineeringRunCancel(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-cancel-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	task := `{"task_id":"eng-cancel-001","objective":"Create a long-running service that sleeps indefinitely"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader(task))
	req.Header.Set("Content-Type", "application/json")

	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d", rec.Code)
	}

	var submitResp map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&submitResp); err != nil {
		t.Fatal(err)
	}
	runID := submitResp["run_id"].(string)

	time.Sleep(500 * time.Millisecond)

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("DELETE", "/api/runs/"+runID, nil)
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec2.Code, rec2.Body.String())
	}

	var cancelResp map[string]any
	if err := json.NewDecoder(rec2.Body).Decode(&cancelResp); err != nil {
		t.Fatal(err)
	}

	if cancelResp["status"] != "canceled" {
		t.Errorf("expected canceled, got %v", cancelResp["status"])
	}

	if cancelResp["run_id"] != runID {
		t.Errorf("expected run_id %s, got %v", runID, cancelResp["run_id"])
	}
}

func TestEngineeringRunCancelNotFound(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-cancel-nf-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/runs/nonexistent", nil)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestEngineeringRunCancelViaQueryAction(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "eng-test-cancel-query-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	configPath := filepath.Join(tmpDir, "models.json")
	srv := api.NewDaemonServer(":0", configPath)

	task := `{"task_id":"eng-cancel-q-001","objective":"long task"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/engineering", strings.NewReader(task))
	req.Header.Set("Content-Type", "application/json")
	srv.Handler().ServeHTTP(rec, req)

	var submitResp map[string]any
	json.NewDecoder(rec.Body).Decode(&submitResp)
	runID := submitResp["run_id"].(string)

	time.Sleep(200 * time.Millisecond)

	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/api/runs/"+runID+"?action=cancel", nil)
	srv.Handler().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec2.Code)
	}

	var cancelResp map[string]any
	json.NewDecoder(rec2.Body).Decode(&cancelResp)
	if cancelResp["status"] != "canceled" {
		t.Errorf("expected canceled, got %v", cancelResp["status"])
	}
}
