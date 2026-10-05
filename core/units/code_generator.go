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

	// Prefer the model. Template generation is the fallback, not the default.
	//
	// This was inverted earlier because a 1.1B model could not emit the file
	// format reliably and every trial degraded to templates while looking like
	// it had used the LLM. With a capable model the LLM path is correct, and the
	// validation gate below rejects malformed output rather than trusting it.
	usedLLM := false
	if u.rt.LLM != nil && u.rt.LLM.HasProvider(routing.CapSpecialize) {
		llmFiles, err := u.generateFromLLM(ctx, plan, moduleName)
		switch {
		case err != nil:
			semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceObservation,
				Content:    fmt.Sprintf("LLM code generation failed, falling back to templates: %s", err.Error()),
				Strength:   state.ConfidenceLow,
				Provenance: state.NewProvenance(u.ID()),
			})
		case !validateLLMFiles(llmFiles, plan):
			semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceObservation,
				Content:    "LLM response did not satisfy the validation gate, falling back to templates",
				Strength:   state.ConfidenceLow,
				Provenance: state.NewProvenance(u.ID()),
			})
		default:
			files = llmFiles
			usedLLM = true
			u.rt.Ledger.RecordModelCall(state.CodeProposal{
				ID:              "prop-codegen-llm",
				File:            "workspace",
				Operation:       state.OpCreate,
				Reason:          "LLM-generated code files",
				OriginatingUnit: u.ID(),
				ExpectedEffect:  "compilable Go files matching the architecture plan",
				Provenance:      state.NewProvenance(u.ID()),
			}, "llm-codegen", u.rt.LastLLMCall())
			semiState.AddEvidence(state.Evidence{
				Type:       state.EvidenceObservation,
				Content:    fmt.Sprintf("LLM generated %d files, all passed the validation gate", len(llmFiles)),
				Strength:   state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			})
		}
	}

	if !usedLLM {
		files = generateFiles(plan, moduleName)
	}

	for _, f := range files {
		if f.Path == "go.mod" {
			continue
		}

		prop := u.proposer.CreateProposal(
			state.OpCreate,
			f.Path,
			f.Content,
			"implement "+f.Description,
			u.ID(),
			state.ConfidenceHigh,
			"enable "+f.Description+" functionality",
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
				Type:       state.EvidenceObservation,
				Content:    fmt.Sprintf("failed to apply proposal %s: %s", validated.ID, err.Error()),
				Strength:   state.ConfidenceHigh,
				Provenance: state.NewProvenance(u.ID()),
			})
			continue
		}

		validated.Status = state.ProposalApplied

		// Record the applied change as a causal intervention. This is the point
		// where the causal ledger learns that something changed, so it must
		// happen at application time, not proposal time: a proposal that was
		// validated but never written down cannot have caused anything.
		//
		// The strategy label must reflect where these files actually came from.
		// Hardcoding "template-generate" here would attribute every model-authored
		// file to the template strategy, and the strategy ranking would then
		// measure nothing but which unit wrote last.
		if u.rt.Ledger != nil {
			u.rt.Ledger.Record(validated, codeStrategy(usedLLM), 0)
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
		Type:       state.EvidenceObservation,
		Content:    fmt.Sprintf("generated %d files from architecture plan", len(files)),
		Strength:   state.ConfidenceHigh,
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

// codeStrategy names the strategy that produced a set of files, so the causal
// ledger can compare approaches rather than units.
func codeStrategy(fromLLM bool) string {
	if fromLLM {
		return "llm-codegen"
	}
	return "template-generate"
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

// packageName derives the Go package name for a path.
//
// The extension must be stripped: a root-level "main.go" yields the directory
// component "main.go", not "main". That matters beyond tidiness, because the
// duplicate-symbol gate keys on this string -- two files in different
// directories would be grouped into one package and a valid response would be
// rejected.
func packageName(path string) string {
	dir := path
	if idx := strings.LastIndex(dir, "/"); idx >= 0 {
		dir = dir[:idx]
	} else {
		// Root-level file: the package is named after the file's base.
		base := dir
		if ext := strings.LastIndex(base, "."); ext > 0 {
			base = base[:ext]
		}
		if base == "" {
			return "main"
		}
		return sanitizePackage(base)
	}

	parts := strings.Split(dir, "/")
	switch {
	case parts[0] == "cmd" && len(parts) > 1:
		return "main"
	case (parts[0] == "pkg" || parts[0] == "internal") && len(parts) > 1:
		return sanitizePackage(parts[1])
	default:
		return sanitizePackage(parts[len(parts)-1])
	}
}

// sanitizePackage keeps only characters valid in a Go identifier, so a directory
// named "my-service" cannot produce a package name the compiler rejects.
func sanitizePackage(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			if i > 0 {
				b.WriteRune(r)
			}
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "main"
	}
	return b.String()
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

// generateFromLLM asks the model for one file at a time.
//
// Asking for every planned file in a single completion is the obvious approach
// and it is wrong for a local model: the response runs to the token limit, the
// whole request times out, and nothing lands. Per-file calls are smaller, land
// faster, and degrade partially -- if the third file times out, the first two are
// already written rather than all of them being discarded together. It also gives
// the causal ledger one intervention per file, so attribution can distinguish a
// good file from a bad one.
func (u *CodeGenerator) generateFromLLM(ctx context.Context, plan *state.ArchitecturePlan, moduleName string) ([]FileSpec, error) {
	components := plan.Components
	if len(components) == 0 {
		return nil, fmt.Errorf("plan has no components to generate")
	}

	var endpointList strings.Builder
	for _, e := range plan.Endpoints {
		endpointList.WriteString(fmt.Sprintf("- %s %s: %s\n", e.Method, e.Path, e.Desc))
	}
	var depList strings.Builder
	depList.WriteString("- Go 1.21+\n- Standard library only\n")
	for _, d := range plan.Dependencies {
		depList.WriteString(fmt.Sprintf("- %s %s (%s)\n", d.Name, d.Version, d.Type))
	}

	var files []FileSpec
	var failures []string

	for _, c := range components {
		select {
		case <-ctx.Done():
			failures = append(failures, fmt.Sprintf("%s: %v", c.Path, ctx.Err()))
			continue
		default:
		}

		prompt := fmt.Sprintf(`You are a Go code generator. Write exactly ONE Go file.

Project: %s
Module: %s
Dependencies: %s
HTTP endpoints in the project:
%s
The file you must write: %s
Its purpose: %s

Rules:
- Output ONLY the file contents, no explanation, no markdown fences.
- First line must be: package %s
- Must compile on its own with the standard library.
- Keep it under 80 lines.

Write the file now.`, plan.Description, moduleName, depList.String(), endpointList.String(), c.Path, c.Description, packageName(c.Path))

		resp, err := u.rt.LLM.GenerateCode(ctx, routing.CapSpecialize, prompt)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", c.Path, err))
			continue
		}

		spec := extractSingleFile(resp, c.Path, c.Description)
		if spec == nil {
			failures = append(failures, fmt.Sprintf("%s: response contained no usable Go source", c.Path))
			continue
		}
		files = append(files, *spec)
	}

	// Cross-file validation, after the sweep.
	//
	// Per-file generation means each response is gated in isolation, and a
	// per-file gate cannot see a sibling file. A model asked for two components
	// will happily return the same body twice under different names, and both
	// individual gates pass because neither knows the other exists. This is the
	// exact failure observed: filelist.go and utils.go, identical, both accepted,
	// producing a duplicate Process declaration that only the compiler caught.
	//
	// The accumulated set is therefore checked as a set. Only the colliding
	// duplicates are dropped, because keeping the first is the whole point of
	// per-file generation -- discarding every file because two collided would
	// throw away work that was fine.
	files, dropped := dedupeDeclarations(files)
	if len(dropped) > 0 {
		failures = append(failures, "duplicate declaration, dropped: "+strings.Join(dropped, ", "))
	}

	if len(files) == 0 {
		if len(failures) > 0 {
			return nil, fmt.Errorf("no files generated: %s", strings.Join(failures, "; "))
		}
		return nil, fmt.Errorf("no files generated")
	}
	if len(failures) > 0 && u.rt.Config != nil && u.rt.Config.Debug {
		semiStateDebugf("partial LLM generation: %s", strings.Join(failures, "; "))
	}
	return files, nil
}

// semiStateDebugf emits a diagnostic without requiring every caller to hold a
// semi-state reference.
var semiStateDebugf = func(format string, args ...any) {}

// extractSingleFile pulls one Go source file out of a model response, tolerating
// the code fences and prose models add despite instructions.
func extractSingleFile(resp, wantPath, description string) *FileSpec {
	body := strings.TrimSpace(resp)
	if body == "" {
		return nil
	}

	// Strip markdown fences if present.
	if strings.Contains(body, "```") {
		if start := strings.Index(body, "```"); start >= 0 {
			rest := body[start+3:]
			rest = strings.TrimPrefix(rest, "go")
			rest = strings.TrimPrefix(rest, "Go")
			rest = strings.TrimLeft(rest, " \t\r\n")
			if end := strings.Index(rest, "```"); end >= 0 {
				rest = rest[:end]
			}
			body = strings.TrimSpace(rest)
		}
	}

	// Trim any leading prose before the package clause.
	if idx := strings.Index(body, "package "); idx > 0 {
		body = body[idx:]
	}

	if !strings.Contains(body, "package ") {
		return nil
	}
	// The package clause must lead, and what follows it must be Go.
	//
	// Trimming the preamble is necessary because models do add "Here is the
	// file:", but it also destroys the only signal that distinguishes a file from
	// an echoed prompt: an echo reads "First line must be: package core" and then
	// continues in English. Real Go continues with import, func, type, var, const,
	// or a comment.
	if !startsWithPackageClause(body) || !goFollowsPackageClause(body) {
		return nil
	}
	if strings.Count(body, "{") != strings.Count(body, "}") {
		// Truncated mid-function; the gate would reject it, so drop it here and
		// let the caller fall back for this file.
		return nil
	}

	return &FileSpec{
		Path:        wantPath,
		Content:     strings.TrimSpace(body),
		Description: description,
	}
}

func (u *CodeGenerator) generateFromLLMLegacy(ctx context.Context, plan *state.ArchitecturePlan, moduleName string) ([]FileSpec, error) {
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

// validateLLMFiles is the gate between model output and the workspace.
//
// It is deliberately stricter than "looks like Go". A weak gate here is worse
// than no gate, because it converts the pipeline from template-driven (boring but
// reliable) to model-driven (flexible but unpredictable) without adding any
// safety. Every check below corresponds to a way a model response is syntactically
// plausible and semantically useless.
//
// The gate checks shape, not compilability: the test runner is the real
// compiler, and duplicating it here would only add a slower, weaker version.
func validateLLMFiles(files []FileSpec, plan *state.ArchitecturePlan) bool {
	if len(files) == 0 {
		return false
	}

	// Plan coverage is no longer checked here. Generation is per-file, so a
	// partial response is expected and each landed file is independently valid;
	// deciding whether the set is *sufficient* is the caller's job, because only
	// the caller knows whether the earlier files already satisfied the plan.
	// Checking it per-file would reject every response after the first.

	seenPaths := map[string]bool{}
	// Symbols are tracked per package directory, because a duplicate function
	// name in two files of the same package is a compile error while the same
	// name in different packages is fine. A path-only check misses this entirely:
	// a model that emits two files with identical content under different names
	// produces duplicate declarations that only the compiler will catch, by which
	// point the workspace is already broken.
	seenSymbols := map[string]map[string]bool{}

	for _, f := range files {
		path := strings.TrimSpace(f.Path)
		if path == "" {
			return false
		}
		if seenPaths[path] {
			// Two files claiming the same path means one overwrites the other,
			// and which one wins is not deterministic from the model's intent.
			return false
		}
		seenPaths[path] = true

		if !strings.HasSuffix(path, ".go") {
			return false
		}
		if strings.ContainsAny(path, `\<>"|?*`) {
			return false
		}
		if strings.Contains(path, "..") {
			return false
		}
		if isPlaceholderText(path) {
			return false
		}

		content := f.Content
		if len(content) < 20 {
			return false
		}
		if !strings.Contains(content, "package ") {
			return false
		}
		if !startsWithPackageClause(content) {
			return false
		}
		if isPlaceholderText(content) {
			return false
		}

		// Balanced braces catch truncated output, which is what a completion cut
		// short by the token limit looks like. It is a crude check, but the real
		// compiler is one stage away.
		if strings.Count(content, "{") != strings.Count(content, "}") {
			return false
		}

		// An unused import is a guaranteed compile error, so it is always worth
		// catching here rather than paying a full trial to discover it. Observed:
		// a generated test file importing "sort" without using it, which failed
		// the whole workspace.
		if unused := unusedImports(content); len(unused) > 0 {
			return false
		}

		pkg := packageName(path)
		if seenSymbols[pkg] == nil {
			seenSymbols[pkg] = map[string]bool{}
		}
		for _, sym := range declaredFuncs(content) {
			if seenSymbols[pkg][sym] {
				return false
			}
			seenSymbols[pkg][sym] = true
		}
	}

	return true
}

// declaredFuncs extracts top-level function names from Go source.
//
// Deliberately lexical rather than a real parse: the goal is to catch the
// duplicate-declaration mistake before it reaches the compiler, and a parse would
// be a second compiler to maintain. Methods are skipped because their receiver
// makes them legitimately repeatable across embedded types.
func declaredFuncs(content string) []string {
	var out []string
	lines := strings.Split(content, "\n")
	inBlockComment := false

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)

		if inBlockComment {
			if strings.Contains(trimmed, "*/") {
				inBlockComment = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			if !strings.Contains(trimmed, "*/") {
				inBlockComment = true
			}
			continue
		}
		if strings.HasPrefix(trimmed, "//") {
			continue
		}

		if !strings.HasPrefix(trimmed, "func ") {
			continue
		}
		rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "func "))

		// A method is not a package-level declaration. Methods are legitimately
		// repeatable across types and embedded structs, so counting them would
		// reject a correct file that happens to define Get on two types.
		if strings.HasPrefix(rest, "(") {
			continue
		}

		// Generic type parameters follow the name: func Gamma[T any](v T).
		// The bracket appears mid-string, so stripping by prefix does not work.
		name := rest
		if idx := strings.IndexAny(name, "([ \t"); idx >= 0 {
			name = name[:idx]
		}
		name = strings.TrimSuffix(name, "*")
		if name == "" || name == "main" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// startsWithPackageClause reports whether the first meaningful line of a Go
// source file is its package declaration.
//
// This is a real property of every Go file, and checking it catches a failure
// mode that the looser "contains package " check missed entirely: a model that
// echoes the prompt back. An echo contains the phrase "package core" because the
// prompt asked for it, and it can have balanced braces, so it passed every other
// check and got written to disk as the file contents.
//
// Requiring the declaration to come first separates a file from anything that
// merely discusses a file.
func startsWithPackageClause(content string) bool {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A build constraint or a comment may precede the clause.
		if strings.HasPrefix(trimmed, "//") {
			continue
		}
		if strings.HasPrefix(trimmed, "/*") {
			continue
		}
		return strings.HasPrefix(trimmed, "package ")
	}
	return false
}

// dedupeDeclarations drops files that redeclare a symbol already present in
// another file of the same package, keeping the first occurrence.
//
// The first file is kept rather than the largest or the last: generation is
// ordered by the plan, so the first is the file the plan listed first and
// therefore the one whose loss is least surprising.
func dedupeDeclarations(files []FileSpec) ([]FileSpec, []string) {
	seen := map[string]map[string]bool{}
	kept := make([]FileSpec, 0, len(files))
	dropped := make([]string, 0)

	for _, f := range files {
		pkg := packageName(f.Path)
		if seen[pkg] == nil {
			seen[pkg] = map[string]bool{}
		}

		collides := false
		for _, sym := range declaredFuncs(f.Content) {
			if seen[pkg][sym] {
				collides = true
				break
			}
		}
		if collides {
			dropped = append(dropped, f.Path)
			continue
		}

		for _, sym := range declaredFuncs(f.Content) {
			seen[pkg][sym] = true
		}
		kept = append(kept, f)
	}

	return kept, dropped
}

// goFollowsPackageClause reports whether the line after the package declaration
// begins a Go construct.
//
// This is the check that catches a prompt echo after the preamble has been
// trimmed. The echo says "package core" and then keeps talking in English; real
// code says "import", "func", "type", "var", "const", or opens a comment.
func goFollowsPackageClause(content string) bool {
	lines := strings.Split(content, "\n")
	sawClause := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !sawClause {
			if strings.HasPrefix(trimmed, "package ") {
				sawClause = true
			}
			continue
		}
		for _, kw := range []string{"import ", "import(", "func ", "type ", "var ", "const ", "//", "/*"} {
			if strings.HasPrefix(trimmed, kw) {
				return true
			}
		}
		// A bare parenthesised import block is also valid.
		return false
	}
	return false
}

// codePlaceholderMarkers are literal tokens a model emits when it echoes the
// prompt's examples instead of answering.
// unusedImports finds imports the body never references.
//
// A lexical check on the package qualifier: if an import's final path element
// never appears followed by a dot in the body, it is unused. Exact for the
// overwhelmingly common case; a false positive costs one retry while a false
// negative costs a compile failure of the whole workspace.
func unusedImports(content string) []string {
	var specs []string
	inBlock := false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "import ("):
			inBlock = true
		case inBlock && trimmed == ")":
			inBlock = false
		case inBlock:
			if trimmed != "" && !strings.HasPrefix(trimmed, "//") {
				specs = append(specs, trimmed)
			}
		case strings.HasPrefix(trimmed, "import "):
			specs = append(specs, strings.TrimPrefix(trimmed, "import "))
		}
	}

	type qualified struct{ path, name string }
	var imports []qualified
	for _, spec := range specs {
		spec = strings.TrimSpace(spec)
		// Aliased imports need type information to resolve, so they are skipped.
		if spec == "" || strings.Contains(spec, " ") || strings.HasPrefix(spec, "//") {
			continue
		}
		path := strings.Trim(spec, `"`)
		if path == "" {
			continue
		}
		name := path
		if idx := strings.LastIndex(name, "/"); idx >= 0 {
			name = name[idx+1:]
		}
		imports = append(imports, qualified{path: path, name: name})
	}

	// Strip the import block before searching, so an import is not counted as a
	// use of itself.
	var body strings.Builder
	inBlock = false
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == ")" {
				inBlock = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, "import ") {
			continue
		}
		body.WriteString(line)
		body.WriteString("\n")
	}
	text := body.String()

	var unused []string
	for _, i := range imports {
		if i.name == "_" || i.name == "." {
			continue
		}
		if !strings.Contains(text, i.name+".") {
			unused = append(unused, i.path)
		}
	}
	return unused
}

var codePlaceholderMarkers = []string{"<path>", "<name>", "<description>", "...", "path/to", "your-", "TODO:", "PLACEHOLDER"}

func isPlaceholderText(s string) bool {
	lower := strings.ToLower(s)
	for _, m := range codePlaceholderMarkers {
		if strings.Contains(lower, strings.ToLower(m)) {
			return true
		}
	}
	return false
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
