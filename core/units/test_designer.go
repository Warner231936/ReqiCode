package units

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/core/apiscan"
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
			Type:       state.EvidenceObservation,
			Content:    "tests already exist, skipping design",
			Strength:   state.ConfidenceLow,
			Provenance: state.NewProvenance(u.ID()),
		})
		return nil, nil
	}

	plan := semiState.GetArchitecturePlan()
	var testFiles []TestFileSpec
	// Use template-based test generation based on architecture plan
	// Tests must be derived from the files the code generator actually produced.
	//
	// It used to be derived from the plan, which assumed the template layout.
	// That worked while both sides used templates, and broke the moment LLM
	// generation was enabled: the model produced its own file set, the template
	// tests still called Process, and the workspace failed to compile with
	// `undefined: Process`. A test that assumes a layout nobody promised is the
	// same circular-verification bug in a different place.
	//
	// When the plan and the generated files disagree, the generated files win:
	// they are what the compiler will see.
	if len(semiState.GetGeneratedFiles()) > 0 {
		testFiles = u.deriveTestsFromGenerated(semiState, plan)
	} else {
		testFiles = generateTestFilesForPlan(plan)
	}
	semiState.AddEvidence(state.Evidence{
		Type:       state.EvidenceObservation,
		Content:    fmt.Sprintf("Using template-based test generation for %d files", len(testFiles)),
		Strength:   state.ConfidenceHigh,
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
			"test for "+f.Description,
			u.ID(),
			state.ConfidenceHigh,
			"verify "+f.Description,
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
		Type:       state.EvidenceObservation,
		Content:    fmt.Sprintf("designed %d test files", len(testFiles)),
		Strength:   state.ConfidenceHigh,
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

// deriveTestsFromGenerated builds tests against the code generator's actual
// output rather than the plan's assumed layout.
//
// It worked while both sides used templates, and broke the moment LLM generation
// was enabled: the model produced its own file set, the template tests still
// called Process, and the workspace failed to compile with `undefined: Process`.
// A test that assumes a layout nobody promised is the circular-verification bug
// in a different place.
//
// When the plan and the generated code disagree, the generated code wins: it is
// what the compiler will see.
func (u *TestDesigner) deriveTestsFromGenerated(ss *state.SemiState, plan *state.ArchitecturePlan) []TestFileSpec {
	generated := ss.GetGeneratedFiles()
	if len(generated) == 0 {
		return generateTestFilesForPlan(plan)
	}

	root := u.rt.Workspace.RootPath()

	// Scan every generated package, not just the first one alphabetically.
	//
	// The first file is typically cmd/cli/main.go, whose package contains main and
	// none of the symbols the template tests call. Deciding the layout from one
	// arbitrary file is how a fix for the original bug introduces a worse one.
	dirs := map[string]bool{}
	for p := range generated {
		if strings.HasSuffix(p, "_test.go") {
			continue // a generated test is not a source of API truth
		}
		dirs[filepath.ToSlash(filepath.Dir(p))] = true
	}

	names := make([]string, 0, len(dirs))
	for d := range dirs {
		names = append(names, d)
	}
	sort.Strings(names)

	// A package carrying the symbols the template tests expect means the generated
	// layout matches the plan's assumption and those tests are safe.
	for _, dir := range names {
		api, err := apiscan.Scan(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		if _, ok := api.Lookup("Process"); ok {
			return generateTestFilesForPlan(plan)
		}
		if _, ok := api.Lookup("NewInMemoryStore"); ok {
			return generateTestFilesForPlan(plan)
		}
		if _, ok := api.Lookup("NewStore"); ok {
			return generateTestFilesForPlan(plan)
		}
	}

	// No generated package matches the assumed layout. Synthesise smoke tests
	// against the package that exposes the most callable functions, which is the
	// most substantial thing the generator produced.
	var best *apiscan.API
	var bestDir string
	var bestCount int
	for _, dir := range names {
		api, err := apiscan.Scan(filepath.Join(root, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		if n := len(api.Functions); n > bestCount {
			best, bestDir, bestCount = api, dir, n
		}
	}
	if best == nil {
		ss.AddEvidence(state.Evidence{
			Type:       state.EvidenceObservation,
			Content:    "generated code could not be scanned on disk; using plan layout for tests",
			Strength:   state.ConfidenceLow,
			Provenance: state.NewProvenance(u.ID()),
		})
		return generateTestFilesForPlan(plan)
	}

	return testsFromAPI(best, bestDir)
}

// testsFromAPI emits smoke tests for a model-shaped API.
//
// The assertions are deliberately minimal: a call with zero arguments that must
// not panic. Inventing an expected value would be guessing, and guessing is the
// failure mode this path exists to eliminate.
func testsFromAPI(api *apiscan.API, pkgDir string) []TestFileSpec {
	pkgName := filepath.Base(pkgDir)

	var out []TestFileSpec
	for _, fn := range api.Functions {
		if fn.Receiver != "" || len(fn.Params) > 3 {
			continue
		}

		args := make([]string, 0, len(fn.Params))
		synthesisable := true
		for _, p := range fn.Params {
			lit, ok := apiscan.ZeroValue(p.Type)
			if !ok {
				synthesisable = false
				break
			}
			args = append(args, lit)
		}
		if !synthesisable {
			continue
		}

		call := fn.Name + "(" + strings.Join(args, ", ") + ")"
		content := fmt.Sprintf(`package %s

import "testing"

// TestGenerated_%s is synthesised from the generated code's own signature.
//
// It asserts only that the call is well-typed and does not panic on zero
// arguments. Inventing an expected value would be guessing, and guessing is
// exactly what a synthesised test must not do.
func TestGenerated_%s(t *testing.T) {
	_ = %s
}
`, pkgName, sanitiseTestName(fn.Name), sanitiseTestName(fn.Name), call)

		out = append(out, TestFileSpec{
			Path:        filepath.ToSlash(filepath.Join(pkgDir, "zz_generated_test.go")),
			Content:     content,
			Description: "smoke test for " + fn.Name,
		})
		// One file per function: a single file would redeclare the package and
		// declare the same function name more than once.
		out[len(out)-1].Path = filepath.ToSlash(filepath.Join(pkgDir, "zz_"+sanitiseTestName(fn.Name)+"_test.go"))
	}
	return out
}

func sanitiseTestName(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i == 0 {
				b.WriteRune('N')
			}
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
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
	"strings"
	"path/filepath"
	"sort"
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
