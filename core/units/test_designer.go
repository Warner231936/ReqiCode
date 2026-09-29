package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type TestDesigner struct {
	*BaseUnit
	rt       *Runtime
	proposer *proposals.Proposer
}

func NewTestDesigner(rt *Runtime) *TestDesigner {
	return &TestDesigner{
		BaseUnit: NewBaseUnit("unit-test-designer", state.RoleTestDesigner, "TestDesigner", state.CadenceMedium, state.ActivationOnEvent),
		rt:       rt,
		proposer: proposals.NewProposer(),
	}
}

func (u *TestDesigner) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	if !semiState.HasGeneratedFiles() {
		return nil, nil
	}

	if semiState.HasTestFiles() {
		semiState.AddEvidence(state.Evidence{
			Type:     state.EvidenceObservation,
			Content:  "tests already exist, skipping design",
			Strength: state.ConfidenceLow,
			Provenance: state.NewProvenance(u.ID()),
		})
		return nil, nil
	}

	plan := semiState.GetArchitecturePlan()
	var testFiles []TestFileSpec
	// Use template-based test generation based on architecture plan
	testFiles = generateTestFilesForPlan(plan)
	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("Using template-based test generation for %d files", len(testFiles)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	for i, f := range testFiles {
		if !strings.HasSuffix(f.Path, "_test.go") {
			f.Path = strings.TrimSuffix(f.Path, ".go") + "_test.go"
		}
		testFiles[i] = f
	}

	for _, f := range testFiles {
		prop := u.rt.Proposals.CreateProposal(
			state.OpCreate,
			f.Path,
			f.Content,
			"test for " + f.Description,
			u.ID(),
			state.ConfidenceHigh,
			"verify " + f.Description,
			nil,
		)

		validated, _, err := u.rt.Proposals.ValidateAndCheck(prop, nil)
		if err != nil {
			continue
		}

		validated.Status = state.ProposalApproved
		semiState.AddProposal(validated)
		semiState.AddFile(state.FileEntry{
			Path:       f.Path,
			Content:    f.Content,
			ProposalID: validated.ID,
			Provenance: state.NewProvenance(u.ID()),
		})

		if err := u.rt.Workspace.ApplyProposal(validated); err != nil {
			validated.Status = state.ProposalFailed
			continue
		}
		validated.Status = state.ProposalApplied

		if u.rt.Ledger != nil {
			u.rt.Ledger.Record(validated, "template-test", 0)
		}
	}

	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("designed %d test files", len(testFiles)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	u.rt.Attention.Boost("test_runner", "tests designed, ready to run", 0.4)

	bus.Publish(events.EventCodeApplied, u.ID(), map[string]interface{}{
		"file":  "test files",
		"count": len(testFiles),
	})

	return nil, nil
}

type TestFileSpec struct {
	Path        string
	Content     string
	Description string
}

func (u *TestDesigner) generateTestsFromLLM(ctx context.Context, plan *state.ArchitecturePlan) ([]TestFileSpec, error) {
	var componentList strings.Builder
	for _, c := range plan.Components {
		componentList.WriteString(fmt.Sprintf("- %s: %s (at %s)\n", c.Name, c.Description, c.Path))
	}

	prompt := fmt.Sprintf(`You are a Go test generator. Given this architecture plan, generate Go test files for the project. Use this exact format:

Architecture: %s
Components:
%s

For each test file, output:
---FILE: <test_path>---
<go test code>
---ENDFILE---

Each file must be valid Go test code with a proper package declaration and _test.go suffix in the path. For each component in the plan, create one test file at the same directory with _test.go suffix.`, plan.Description, componentList.String())

	resp, err := u.rt.LLM.GenerateCode(ctx, routing.CapSpecialize, prompt)
	if err != nil {
		return nil, err
	}

	resp = strings.TrimSpace(resp)
	var files []TestFileSpec

	parts := strings.Split(resp, "---FILE:")
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		endIdx := strings.Index(part, "---ENDFILE---")
		if endIdx == -1 {
			continue
		}

		headerAndContent := part[:endIdx]
		content := part[endIdx+len("---ENDFILE---"):]

		path := strings.TrimSpace(headerAndContent)
		if path == "" {
			continue
		}

		code := strings.TrimSpace(content)
		if code != "" && strings.Contains(code, "package ") {
			files = append(files, TestFileSpec{
				Path:        path,
				Content:     code,
				Description: fmt.Sprintf("LLM-generated test: %s", path),
			})
		}
	}

	if len(files) == 0 {
		// Try markdown code blocks as fallback
		files = parseMarkdownTestBlocks(resp)
	}

	if len(files) == 0 {
		return nil, fmt.Errorf("no test files parsed from LLM response")
	}

	return files, nil
}

func parseMarkdownTestBlocks(resp string) []TestFileSpec {
	var files []TestFileSpec
	var lines = strings.Split(resp, "\n")
	var filePath string
	var content strings.Builder
	var inCodeBlock bool

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if !inCodeBlock {
				filePath = inferTestFilePath(lines)
				inCodeBlock = true
				content.Reset()
			} else {
				code := strings.TrimSpace(content.String())
				if code != "" && strings.Contains(code, "package ") {
					files = append(files, TestFileSpec{
						Path:        filePath,
						Content:     code,
						Description: fmt.Sprintf("LLM-generated test: %s", filePath),
					})
				}
				inCodeBlock = false
				filePath = ""
			}
			continue
		}
		if inCodeBlock {
			content.WriteString(line)
			content.WriteString("\n")
		}
	}

	return files
}

func inferTestFilePath(lines []string) string {
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.Contains(trimmed, "_test.go") && !strings.HasPrefix(trimmed, "```") {
			words := strings.Fields(trimmed)
			for _, w := range words {
				if strings.HasSuffix(w, "_test.go") {
					return w
				}
			}
		}
		if strings.Contains(trimmed, ".go") && !strings.HasPrefix(trimmed, "```") {
			words := strings.Fields(trimmed)
			for _, w := range words {
				if strings.HasSuffix(w, ".go") {
					return strings.Replace(w, ".go", "_test.go", 1)
				}
			}
		}
	}
	return "test.go"
}

func generateTestFilesForPlan(plan *state.ArchitecturePlan) []TestFileSpec {
	if plan == nil {
		return generateDefaultTestFiles()
	}

	hasHTTP := len(plan.Endpoints) > 0
	hasStore := false
	for _, c := range plan.Components {
		if strings.Contains(strings.ToLower(c.Path), "store") {
			hasStore = true
			break
		}
	}

	var files []TestFileSpec

	if hasHTTP && hasStore {
		// HTTP service with storage - generate store and handler tests
		files = append(files, TestFileSpec{
			Path:        "internal/store/store_test.go",
			Description: "Store CRUD operations",
			Content:     testStoreFile,
		})
		files = append(files, TestFileSpec{
			Path:        "internal/handlers/handlers_test.go",
			Description: "HTTP handler tests",
			Content:     testHandlerFile,
		})
	} else if hasStore {
		// Store-only project
		files = append(files, TestFileSpec{
			Path:        "internal/store/store_test.go",
			Description: "Store CRUD operations",
			Content:     testStoreFile,
		})
	} else {
		// CLI or general project - test the core logic
		files = append(files, TestFileSpec{
			Path:        "pkg/core/core_test.go",
			Description: "Core logic tests",
			Content:     testCoreFile,
		})
	}

	return files
}

func generateDefaultTestFiles() []TestFileSpec {
	return []TestFileSpec{
		{
			Path:        "pkg/core/core_test.go",
			Description: "Core logic tests",
			Content:     testCoreFile,
		},
	}
}

const testCoreFile = `package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProcess(t *testing.T) {
	// Create a temporary directory with some files
	tmpDir, err := os.MkdirTemp("", "test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a few test files
	for i := 0; i < 3; i++ {
		filePath := filepath.Join(tmpDir, "file"+string(rune(i+'0'))+".txt")
		if err := os.WriteFile(filePath, []byte("content"), 0644); err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}
	}

	// Create a subdirectory
	subDir := filepath.Join(tmpDir, "subdir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatalf("failed to create subdir: %v", err)
	}

	result := Process(tmpDir)

	// Should find 3 files in the root (not counting subdirectory)
	if result != "Found 3 items" {
		t.Errorf("expected 'Found 3 items', got '%s'", result)
	}
}

func TestProcessWithError(t *testing.T) {
	result := Process("/nonexistent/path/that/does/not/exist")
	if result == "" || !strings.Contains(result, "Error") {
		t.Errorf("expected error message, got '%s'", result)
	}
}
`

const testStoreFile = `package store

import (
	"fmt"
	"sync"
	"testing"
)

func TestStoreCreate(t *testing.T) {
	s := NewStore()
	item := StoreItem{ID: "1", Data: "test"}
	if err := s.Create(item); err != nil {
		t.Fatalf("create failed: %v", err)
	}
}

func TestStoreGet(t *testing.T) {
	s := NewStore()
	item := StoreItem{ID: "1", Data: "test"}
	s.Create(item)
	got, ok := s.Get("1")
	if !ok {
		t.Fatal("expected to find item")
	}
	if got.Data != "test" {
		t.Errorf("expected 'test', got '%s'", got.Data)
	}
}

func TestStoreList(t *testing.T) {
	s := NewStore()
	s.Create(StoreItem{ID: "1", Data: "a"})
	s.Create(StoreItem{ID: "2", Data: "b"})
	items := s.List()
	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore()
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			s.Create(StoreItem{ID: fmt.Sprintf("%d", id), Data: "val"})
		}(i)
	}
	wg.Wait()
	items := s.List()
	if len(items) != 100 {
		t.Errorf("expected 100 items, got %d", len(items))
	}
}`

const testHandlerFile = `package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreateResource(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/resources", nil)
	w := httptest.NewRecorder()
	CreateResource(w, req)
	if w.Code != http.StatusCreated {
		t.Errorf("expected 201, got %d", w.Code)
	}
	var resp map[string]string
	json.NewDecoder(w.Body).Decode(&resp)
	if resp["status"] != "created" {
		t.Errorf("expected status=created, got %v", resp)
	}
}

func TestGetResource(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/resources/1", nil)
	w := httptest.NewRecorder()
	GetResource(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestListResources(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/resources", nil)
	w := httptest.NewRecorder()
	ListResources(w, req)
	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}
}

func TestDeleteResource(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/resources/1", nil)
	w := httptest.NewRecorder()
	DeleteResource(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", w.Code)
	}
}`
