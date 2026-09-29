package units

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type CodeGenerator struct {
	*BaseUnit
	rt       *Runtime
	ws       *workspace.Workspace
	proposer *proposals.Proposer
}

func NewCodeGenerator(rt *Runtime, ws *workspace.Workspace) *CodeGenerator {
	return &CodeGenerator{
		BaseUnit: NewBaseUnit("unit-code-generator", state.RoleCodeGenerator, "CodeGenerator", state.CadenceFast, state.ActivationOnEvent),
		rt:       rt,
		ws:       ws,
		proposer: proposals.NewProposer(),
	}
}

func (u *CodeGenerator) Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error) {
	plan := u.rt.SemiState.GetArchitecturePlan()
	if plan == nil {
		return nil, fmt.Errorf("no architecture plan available for code generation")
	}

	if semiState.HasGeneratedFiles() {
		return nil, nil
	}

	moduleName := deriveModuleName(u.rt.Config.ProjectRoot)
	if moduleName == "todo-service" && plan != nil && len(plan.Components) > 0 {
		for _, c := range plan.Components {
			if c.Name != "main" {
				moduleName = deriveModuleName(c.Name)
				break
			}
		}
	}
	if err := u.ws.InitGoModule(moduleName); err != nil {
		return nil, fmt.Errorf("init go module: %w", err)
	}

	goModContent := fmt.Sprintf("module %s\n\ngo 1.21\n", moduleName)
	semiState.AddFile(state.FileEntry{
		Path:       "go.mod",
		Content:    goModContent,
		Provenance: state.NewProvenance(u.ID()),
	})
	if err := u.ws.WriteFile("go.mod", goModContent); err != nil {
		return nil, err
	}

	existingProps := semiState.GetProposals()
	var evts []events.Event

	var files []FileSpec
	// Use template-based generation only (LLM is unreliable for code format)
	files = generateFiles(plan, moduleName)
	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("Using template-based code generation for %d files", len(files)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	for _, f := range files {
		if f.Path == "go.mod" {
			continue
		}

		prop := u.proposer.CreateProposal(
			state.OpCreate,
			f.Path,
			f.Content,
			"implement " + f.Description,
			u.ID(),
			state.ConfidenceHigh,
			"enable " + f.Description + " functionality",
			nil,
		)

		validated, conflicts, err := u.proposer.ValidateAndCheck(prop, existingProps)
		if err != nil {
			return nil, err
		}

		var hasConflict bool
		for _, c := range conflicts {
			if c.Severity == state.ConflictHigh || c.Severity == state.ConflictCritical {
				hasConflict = true
			}
		}

		if hasConflict {
			semiState.AddObjection(state.Objection{
				SourceUnit: u.ID(),
				Target:     prop.ID,
				Content:    fmt.Sprintf("proposal conflicts with existing proposal for file %s", f.Path),
				Severity:   "medium",
				Provenance: state.NewProvenance(u.ID()),
			})
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

		if err := u.ws.ApplyProposal(validated); err != nil {
			validated.Status = state.ProposalFailed
			semiState.AddEvidence(state.Evidence{
				Type:     state.EvidenceObservation,
				Content:  fmt.Sprintf("failed to apply proposal %s: %s", validated.ID, err.Error()),
				Strength: state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			})
			continue
		}

		validated.Status = state.ProposalApplied

		// Record the applied change as a causal intervention. This is the point
		// where the causal ledger learns that something changed, so it must
		// happen at application time, not proposal time: a proposal that was
		// validated but never written down cannot have caused anything.
		if u.rt.Ledger != nil {
			u.rt.Ledger.Record(validated, "template-generate", 0)
		}

		ev := events.Event{
			Type:     events.EventCodeApplied,
			SourceID: u.ID(),
			Payload:  events.PayloadForProposal(validated),
		}
		evts = append(evts, ev)

		u.rt.Attention.Boost("test_designer", fmt.Sprintf("file %s created, tests needed", f.Path), 0.15)
	}

	semiState.AddEvidence(state.Evidence{
		Type:     state.EvidenceObservation,
		Content:  fmt.Sprintf("generated %d files from architecture plan", len(files)),
		Strength: state.ConfidenceHigh,
		Provenance: state.NewProvenance(u.ID()),
	})

	return evts, nil
}

type FileSpec struct {
	Path        string
	Content     string
	Description string
}

func deriveModuleName(intent string) string {
	if intent == "" {
		return "app"
	}
	r := strings.NewReplacer(
		"Create a ", "", "Create an ", "", "Create ", "",
		"Go CLI tool", "cli",
		"CLI tool", "cli",
		"Go HTTP", "http",
		"Go web", "web",
		"Go service", "service",
		"HTTP server", "http-server",
		"HTTP service", "http-service",
		"web service", "http-service",
		"file system", "file-system",
		"file listing", "file-listing",
		"REST API", "api",
		"REST", "api",
	)
	s := r.Replace(intent)
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return "app"
	}
	// Skip common words
	skipWords := map[string]bool{"go": true, "a": true, "an": true, "the": true, "with": true, "for": true, "that": true, "in": true, "on": true}
	name := ""
	for _, f := range fields {
		if !skipWords[strings.ToLower(f)] {
			name = strings.ToLower(f)
			break
		}
	}
	if name == "" {
		name = "app"
	}
	name = strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			return r
		}
		if r == ' ' || r == '-' {
			return '-'
		}
		return -1
	}, name)
	if name == "" || name == "go" {
		return "app"
	}
	if len(name) > 30 {
		name = name[:30]
	}
	return name
}

func generateFiles(plan *state.ArchitecturePlan, moduleName string) []FileSpec {
	if plan != nil {
		return generateFromTemplate(plan, moduleName)
	}
	return generateDefaultFiles(moduleName)
}

func generateFromTemplate(plan *state.ArchitecturePlan, moduleName string) []FileSpec {
	var files []FileSpec

	for _, comp := range plan.Components {
		content := generateComponentCode(comp, plan, moduleName)
		files = append(files, FileSpec{
			Path:        comp.Path,
			Content:     content,
			Description: comp.Description,
		})
	}

	if len(plan.Components) == 0 {
		files = append(files, generateDefaultFiles(moduleName)...)
	}

	return files
}

func packageName(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) > 0 {
		dir := parts[0]
		if dir == "cmd" && len(parts) > 1 {
			return "main"
		}
		if dir == "pkg" || dir == "internal" {
			if len(parts) > 1 {
				return parts[1]
			}
		}
		return dir
	}
	return "main"
}

func componentFuncName(name string) string {
	if name == "main" {
		return "main"
	}
	return "New" + strings.ToUpper(name[:1]) + name[1:]
}

func generateComponentCode(comp state.ComponentSpec, plan *state.ArchitecturePlan, moduleName string) string {
	pkg := packageName(comp.Path)

	if strings.HasSuffix(comp.Path, "/main.go") || strings.HasSuffix(comp.Path, "main.go") {
		if len(plan.Endpoints) > 0 {
			return generateHTTPServerMain(pkg, plan, moduleName)
		}
		return generateCLIMain(pkg, moduleName)
	}

	if strings.Contains(comp.Path, "store") || strings.Contains(comp.Path, "storage") {
		return generateStoreFile(pkg)
	}

	if strings.Contains(comp.Path, "handler") || strings.Contains(comp.Path, "handler") {
		return generateHandlerFile(pkg)
	}

	if strings.Contains(comp.Path, "model") || strings.Contains(comp.Path, "types") ||
		strings.Contains(comp.Path, "resource") {
		return generateModelFile(pkg)
	}

	if strings.Contains(comp.Path, "core") || strings.Contains(comp.Name, "core") {
		return generateCoreFile(pkg)
	}

	return fmt.Sprintf("package %s\n\nfunc init() {\n\t// %s\n}\n", pkg, comp.Description)
}

func generateHTTPServerMain(pkg string, plan *state.ArchitecturePlan, moduleName string) string {
	var routeRegs strings.Builder
	for _, ep := range plan.Endpoints {
		var fnName string
		switch ep.Method {
		case "POST":
			fnName = "handlers.CreateResource"
		case "GET":
			if strings.Contains(ep.Path, "{id}") {
				fnName = "handlers.GetResource"
			} else {
				fnName = "handlers.ListResources"
			}
		case "PUT":
			fnName = "handlers.UpdateResource"
		case "DELETE":
			fnName = "handlers.DeleteResource"
		default:
			fnName = "handlers.HandleRequest"
		}
		routeRegs.WriteString(fmt.Sprintf("\thttp.HandleFunc(\"%s\", %s)\n", ep.Path, fnName))
	}
	return fmt.Sprintf("package %s\n\nimport (\n\t\"fmt\"\n\t\"log\"\n\t\"net/http\"\n\t\"%s/internal/handlers\"\n)\n\nfunc main() {\n%s\tport := \":8080\"\n\tfmt.Printf(\"Server starting on %%s\\n\", port)\n\tlog.Fatal(http.ListenAndServe(port, nil))\n}\n", pkg, moduleName, routeRegs.String())
}

func generateCLIMain(pkg string, moduleName string) string {
	return fmt.Sprintf("package %s\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"%s/pkg/core\"\n)\n\nfunc main() {\n\tif len(os.Args) < 2 {\n\t\tfmt.Println(\"Usage: %s <input>\")\n\t\tos.Exit(1)\n\t}\n\tinput := os.Args[1]\n\tresult := core.Process(input)\n\tfmt.Println(result)\n}\n", pkg, moduleName, moduleName)
}

func generateStoreFile(pkg string) string {
	return fmt.Sprintf("package %s\n\nimport (\n\t\"sync\"\n)\n\ntype StoreItem struct {\n\tID   string\n\tData string\n}\n\ntype Store struct {\n\tmu    sync.RWMutex\n\titems map[string]StoreItem\n}\n\nfunc NewStore() *Store {\n\treturn &Store{items: make(map[string]StoreItem)}\n}\n\nfunc (s *Store) Create(item StoreItem) error {\n\ts.mu.Lock()\n\tdefer s.mu.Unlock()\n\ts.items[item.ID] = item\n\treturn nil\n}\n\nfunc (s *Store) Get(id string) (StoreItem, bool) {\n\ts.mu.RLock()\n\tdefer s.mu.RUnlock()\n\titem, ok := s.items[id]\n\treturn item, ok\n}\n\nfunc (s *Store) List() []StoreItem {\n\ts.mu.RLock()\n\tdefer s.mu.RUnlock()\n\tresult := make([]StoreItem, 0, len(s.items))\n\tfor _, v := range s.items {\n\t\tresult = append(result, v)\n\t}\n\treturn result\n}\n", pkg)
}

func generateHandlerFile(pkg string) string {
	return fmt.Sprintf("package %s\n\nimport (\n\t\"encoding/json\"\n\t\"net/http\"\n)\n\nfunc CreateResource(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(http.StatusCreated)\n\tjson.NewEncoder(w).Encode(map[string]string{\"status\": \"created\"})\n}\n\nfunc GetResource(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(http.StatusOK)\n\tjson.NewEncoder(w).Encode(map[string]string{\"status\": \"ok\"})\n}\n\nfunc ListResources(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(http.StatusOK)\n\tjson.NewEncoder(w).Encode([]interface{}{})\n}\n\nfunc UpdateResource(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(http.StatusOK)\n\tjson.NewEncoder(w).Encode(map[string]string{\"status\": \"updated\"})\n}\n\nfunc DeleteResource(w http.ResponseWriter, r *http.Request) {\n\tw.WriteHeader(http.StatusNoContent)\n}\n", pkg)
}

func generateModelFile(pkg string) string {
	return `package ` + pkg + `

import "time"

type Resource struct {
	ID        string    ` + "`json:\"id\"`" + `
	Name      string    ` + "`json:\"name\"`" + `
	CreatedAt time.Time ` + "`json:\"created_at\"`" + `
	UpdatedAt time.Time ` + "`json:\"updated_at\"`" + `
}
`
}

func generateCoreFile(pkg string) string {
	return fmt.Sprintf("package %s\n\nimport (\n\t\"fmt\"\n\t\"os\"\n\t\"path/filepath\"\n)\n\nfunc Process(input string) string {\n\t// Check if path exists\n\tif _, err := os.Stat(input); os.IsNotExist(err) {\n\t\treturn fmt.Sprintf(\"Error: path does not exist: %%s\", input)\n\t}\n\tfiles, err := filepath.Glob(input + \"/*\")\n\tif err != nil {\n\t\treturn fmt.Sprintf(\"Error: %%v\", err)\n\t}\n\tcount := 0\n\tfor _, f := range files {\n\t\tinfo, err := os.Stat(f)\n\t\tif err == nil && !info.IsDir() {\n\t\t\tcount++\n\t\t}\n\t}\n\treturn fmt.Sprintf(\"Found %%d items\", count)\n}\n", pkg)
}

func generateDefaultFiles(moduleName string) []FileSpec {
	return []FileSpec{
		{
			Path:        "main.go",
			Content:     fmt.Sprintf("package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"Hello from %s\")\n}\n", moduleName),
			Description: "Application entry point",
		},
	}
}

func deriveHandlerName(desc string) string {
	s := strings.ToLower(desc)
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "-", "")
	if s == "" {
		return "HandleRequest"
	}
	return "Handle" + strings.ToUpper(s[:1]) + s[1:]
}

func (u *CodeGenerator) generateFromLLM(ctx context.Context, plan *state.ArchitecturePlan, moduleName string) ([]FileSpec, error) {
	var componentList strings.Builder
	for _, c := range plan.Components {
		componentList.WriteString(fmt.Sprintf("- %s: %s (at %s)\n", c.Name, c.Description, c.Path))
	}
	var endpointList strings.Builder
	for _, e := range plan.Endpoints {
		endpointList.WriteString(fmt.Sprintf("- %s %s: %s\n", e.Method, e.Path, e.Desc))
	}

	prompt := fmt.Sprintf(`You are a Go code generator. Generate complete, compilable Go code files for a Go project based on this architecture plan:

Architecture: %s
Module: %s
Components:
%s
Endpoints:
%s
Dependencies:
- Go 1.21+
- Standard library only

Generate code for these files based on the components listed above. Use this exact format for each file:
---FILE: <path>---
<go code>
---ENDFILE---

Each file must be valid Go code with a proper package declaration. Do not add explanations outside the file blocks.`, plan.Description, moduleName, componentList.String(), endpointList.String())

		resp, err := u.rt.LLM.GenerateCode(ctx, routing.CapSpecialize, prompt)
		if u.rt.Config.Debug {
			fmt.Printf("[DEBUG] LLM code generation response:\n%s\n", resp)
		}
		if err != nil {
			return nil, err
		}

	return parseLLMResponse(resp, moduleName), nil
}

func validateLLMFiles(files []FileSpec) bool {
	if len(files) == 0 {
		return false
	}
	for _, f := range files {
		if len(f.Content) < 20 {
			return false
		}
		if !strings.Contains(f.Content, "package ") {
			return false
		}
	}
	return true
}

func parseLLMResponse(resp string, moduleName string) []FileSpec {
	resp = strings.TrimSpace(resp)

	var files []FileSpec

	// First, try the ---FILE:--- format
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

		files = append(files, FileSpec{
			Path:        path,
			Content:     strings.TrimSpace(content),
			Description: fmt.Sprintf("LLM-generated: %s", path),
		})
	}

	// If no files found, try markdown code blocks
	if len(files) == 0 {
		files = parseMarkdownCodeBlocks(resp, moduleName)
	}

	return files
}

func parseMarkdownCodeBlocks(resp string, moduleName string) []FileSpec {
	var files []FileSpec
	var lines = strings.Split(resp, "\n")
	var filePath string
	var content strings.Builder
	var inCodeBlock bool
	var codeLang string

	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if !inCodeBlock {
				// Start of code block; check if there's a language tag
				codeLang = strings.TrimPrefix(strings.TrimSpace(line), "```")
				codeLang = strings.TrimSpace(codeLang)
				// Look for path in preceding text
				filePath = inferFilePath(resp, lines, &files)
				if filePath == "" {
					if codeLang == "go" || codeLang == "" {
						filePath = "main.go"
					} else {
						filePath = codeLang + ".go"
					}
				}
				inCodeBlock = true
				content.Reset()
			} else {
				// End of code block
				code := strings.TrimSpace(content.String())
				if code != "" && strings.Contains(code, "package ") {
					files = append(files, FileSpec{
						Path:        filePath,
						Content:     code,
						Description: fmt.Sprintf("LLM-generated: %s", filePath),
					})
				}
				inCodeBlock = false
				filePath = ""
				codeLang = ""
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

func inferFilePath(resp string, lines []string, existingFiles *[]FileSpec) string {
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			continue
		}

		if strings.HasSuffix(trimmed, ".go") {
			if isLikelyPath(trimmed) {
				if hasCodeBlockAfter(lines, i) {
					return trimmed
				}
			}
		}

		words := strings.Fields(trimmed)
		for _, w := range words {
			if strings.HasSuffix(w, ".go") && isLikelyPath(w) {
				if hasCodeBlockAfter(lines, i) {
					return w
				}
			}
		}
	}
	return ""
}

func isLikelyPath(s string) bool {
	if !strings.HasSuffix(s, ".go") {
		return false
	}
	if strings.Count(s, "..") > 0 {
		return false
	}
	parts := strings.Split(s, "/")
	if len(parts) > 4 {
		return false
	}
	for _, p := range parts {
		if p == "path" && len(parts) > 2 {
			return false
		}
	}
	return true
}

func hasCodeBlockAfter(lines []string, idx int) bool {
	for j := idx + 1; j < len(lines); j++ {
		if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
			return true
		}
		if strings.TrimSpace(lines[j]) != "" {
			break
		}
	}
	return false
}

const bt = "`"

const todoModelFile = `package model

import "time"

type Todo struct {
	ID        string    ` + bt + `json:"id"` + bt + `
	Title     string    ` + bt + `json:"title"` + bt + `
	Completed bool      ` + bt + `json:"completed"` + bt + `
	CreatedAt time.Time ` + bt + `json:"created_at"` + bt + `
	UpdatedAt time.Time ` + bt + `json:"updated_at"` + bt + `
}
`

const todoStoreFile = `package store

import (
	"sync"
	"strconv"
	"time"

	"todo-service/internal/model"
)

type TodoStore struct {
	mu    sync.RWMutex
	items map[string]model.Todo
	nextID int
}

func NewTodoStore() *TodoStore {
	return &TodoStore{
		items: make(map[string]model.Todo),
	}
}

func (s *TodoStore) Create(todo model.Todo) model.Todo {
	s.mu.Lock()
	defer s.mu.Unlock()

	id := strconv.Itoa(s.nextID)
	s.nextID++

	todo.ID = id
	todo.CreatedAt = time.Now()
	todo.UpdatedAt = time.Now()
	todo.Completed = false

	s.items[id] = todo
	return todo
}

func (s *TodoStore) GetAll() []model.Todo {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := make([]model.Todo, 0, len(s.items))
	for _, t := range s.items {
		result = append(result, t)
	}
	return result
}

func (s *TodoStore) Get(id string) (model.Todo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	t, ok := s.items[id]
	return t, ok
}

func (s *TodoStore) Update(id string, todo model.Todo) (model.Todo, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[id]; !ok {
		return model.Todo{}, false
	}

	todo.ID = id
	todo.CreatedAt = s.items[id].CreatedAt
	todo.UpdatedAt = time.Now()
	todo.Completed = s.items[id].Completed

	if todo.Title != "" {
		s.items[id] = todo
	}

	return s.items[id], true
}

func (s *TodoStore) Delete(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.items[id]; !ok {
		return false
	}

	delete(s.items, id)
	return true
}
`

const todoHandlersFile = `package handlers

import (
	"encoding/json"
	"net/http"

	"todo-service/internal/model"
	"todo-service/internal/store"
)

type TodoHandler struct {
	store *store.TodoStore
}

func NewTodoHandler(s *store.TodoStore) *TodoHandler {
	return &TodoHandler{store: s}
}

func (h *TodoHandler) CreateTodo(w http.ResponseWriter, r *http.Request) {
	var todo model.Todo
	if err := json.NewDecoder(r.Body).Decode(&todo); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	created := h.store.Create(todo)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(created)
}

func (h *TodoHandler) ListTodos(w http.ResponseWriter, r *http.Request) {
	todos := h.store.GetAll()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(todos)
}

func (h *TodoHandler) GetTodo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/todos/"):]
	todo, ok := h.store.Get(id)
	if !ok {
		http.Error(w, "todo not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(todo)
}

func (h *TodoHandler) UpdateTodo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/todos/"):]
	var todo model.Todo
	if err := json.NewDecoder(r.Body).Decode(&todo); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	updated, ok := h.store.Update(id, todo)
	if !ok {
		http.Error(w, "todo not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(updated)
}

func (h *TodoHandler) DeleteTodo(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Path[len("/todos/"):]
	if ok := h.store.Delete(id); !ok {
		http.Error(w, "todo not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
`

const mainFile = `package main

import (
	"fmt"
	"log"
	"net/http"

	"todo-service/internal/handlers"
	"todo-service/internal/store"
)

func main() {
	store := store.NewTodoStore()
	handler := handlers.NewTodoHandler(store)

	http.HandleFunc("/todos", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			handler.CreateTodo(w, r)
		case http.MethodGet:
			handler.ListTodos(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	http.HandleFunc("/todos/", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			handler.GetTodo(w, r)
		case http.MethodPut:
			handler.UpdateTodo(w, r)
		case http.MethodDelete:
			handler.DeleteTodo(w, r)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
	})

	port := ":8080"
	fmt.Printf("TODO service starting on %s\n", port)
	log.Fatal(http.ListenAndServe(port, nil))
}
`
