package selfmodify

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/core/apiscan"
	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/registry"
	"github.com/kilo/spiral-codemaker/models/routing"
)

// Proposal is a set of test files the model produced for a work list.
type Proposal struct {
	// Files maps a repository-relative path to its contents.
	Files map[string]string `json:"-"`
	// Paths lists the files in the order they were requested.
	Paths []string `json:"paths"`
	// Targets records which uncovered functions each file is meant to cover.
	Targets []string `json:"targets"`
	// Tokens is the completion cost of producing the proposal.
	Tokens int `json:"tokens"`
	// Rejected records paths the model produced that did not survive validation,
	// kept so a rejected proposal is auditable rather than silently discarded.
	Rejected []string `json:"rejected,omitempty"`
	// Trials counts how many generation attempts accepted files needed.
	Trials int `json:"trials"`
}

// selfmodTimeout and selfmodTokens are tuned for the local-model case rather
// than the interactive one. A focused test file is small; the 2048-token budget
// used for general code generation lets a small model ramble until it hits the
// limit, and on hardware running at single-digit tokens per second that is the
// difference between a proposal and a timeout.
const (
	selfmodTimeout = 150 * time.Second
	selfmodTokens  = 1200
	// apiBudget bounds the API listing in the prompt. Sized to hold a typical
	// package's exported surface whole; the alternative -- truncating signatures
	// mid-declaration -- reintroduces exactly the hallucination this prevents.
	apiBudget = 3000
)

// readFunctionSource returns just the target function, with its imports.
//
// Sending the whole file is what made the prompt too large to answer quickly,
// and it is worse for quality: a model given 400 lines has more surface to echo
// and more room to write a test for the wrong thing. The function plus its
// leading comment and the file's imports is the right unit.
func readFunctionSource(path string, fn UncoveredFunc) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	lines := strings.Split(string(data), "\n")

	start := fn.Line - 1
	if start < 0 || start >= len(lines) {
		return "", fmt.Errorf("function line %d is outside %s", fn.Line, path)
	}
	end := fn.EndLine - 1
	if end <= start || end >= len(lines) {
		end = start + 1
	}

	from := start
	for from > 0 {
		prev := strings.TrimSpace(lines[from-1])
		if strings.HasPrefix(prev, "//") {
			from--
			continue
		}
		break
	}

	to := end
	for to < len(lines) {
		if strings.TrimSpace(lines[to]) == "}" {
			break
		}
		to++
	}
	if to < len(lines) {
		to++
	}

	var b strings.Builder
	if impEnd := importBlockEnd(lines); impEnd > 0 {
		b.WriteString("package " + packageNameOf(path) + "\n\n")
		b.WriteString(strings.Join(lines[:impEnd], "\n"))
		b.WriteString("\n\n")
	}
	b.WriteString(strings.Join(lines[from:to], "\n"))
	return b.String(), nil
}

// importBlockEnd returns the line index just past the file's import block, or 0.
func importBlockEnd(lines []string) int {
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "import (") {
			start = i
			break
		}
	}
	if start < 0 {
		return 0
	}
	for i := start + 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == ")" {
			return i + 1
		}
	}
	return 0
}

// toSnake converts an exported Go identifier to snake_case for a test filename.
func toSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if r >= 'A' && r <= 'Z' {
			if i > 0 {
				b.WriteRune('_')
			}
			b.WriteRune(r - 'A' + 'a')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// extractGoFile pulls a Go file out of a model response, tolerating fences and
// preamble. Returns "" when nothing usable is present.
func extractGoFile(resp string) string {
	body := strings.TrimSpace(resp)
	if body == "" {
		return ""
	}
	if strings.Contains(body, "```") {
		if start := strings.Index(body, "```"); start >= 0 {
			rest := body[start+3:]
			rest = strings.TrimPrefix(strings.TrimPrefix(rest, "go"), "Go")
			rest = strings.TrimLeft(rest, " \t\r\n")
			if end := strings.Index(rest, "```"); end >= 0 {
				rest = rest[:end]
			}
			body = strings.TrimSpace(rest)
		}
	}
	idx := strings.Index(body, "package ")
	if idx < 0 {
		return ""
	}
	body = body[idx:]

	lines := strings.Split(body, "\n")
	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "package ") {
			return strings.TrimSpace(strings.Join(lines[i:], "\n"))
		}
	}
	return ""
}

// ProposeTests asks the model for tests covering the work list, retrying each
// function up to `attempts` times.
//
// The retry is the point of the whole exercise. A local 7B asked to test a
// function with a rich struct API writes a plausible, mostly-correct test and
// then makes one small mistake -- a missing import, a wrong field name, a
// boundary case it invents. Observed on the first run: "Project("empty", nil)"
// with the correct signature and the correct field names, failing only on an
// unimported package.
//
// Its first-attempt success rate is therefore low while its near-miss rate is
// high. Because every attempt is gated by a real compile and a real test run, a
// near-miss costs compute and nothing else. Retrying is what converts an
// unreliable generator into a reliable one, and it only works because the
// verification is genuine: with an advisory gate, a retry loop would just promote
// a lucky wrong answer more often.
func ProposeTests(ctx context.Context, modelPath, root, pkg string, work []UncoveredFunc, sources []string, attempts int, prevErr error) (*Proposal, error) {
	if attempts < 1 {
		attempts = 1
	}

	reg := registry.NewModelRegistry("")
	reg.AddProvider("gguf-local", "gguf", ServerURL(), "", map[string]any{
		"model_path": modelPath,
	})
	reg.AddModel("selfmod", "gguf-local", routing.CapSpecialize, selfmodTokens, 0.2)
	reg.SetActiveModel(routing.CapSpecialize, "selfmod")

	p, modelName, ok := reg.Router().GetProvider(routing.CapSpecialize)
	if !ok || p == nil {
		return nil, fmt.Errorf("could not resolve a provider for the model at %s", modelPath)
	}

	prop := &Proposal{Files: map[string]string{}}
	testPkg := packageNameOf(pkg)
	pkgDir := filepath.Join(root, filepath.FromSlash(PackageDirRel(pkg)))

	for _, fn := range work {
		source, err := readFunctionSource(filepath.Join(root, filepath.FromSlash(fn.File)), fn)
		if err != nil {
			prop.Rejected = append(prop.Rejected,
				fmt.Sprintf("%s (cannot read source %s: %v)", fn.Name, fn.File, err))
			continue
		}

		rel := filepath.ToSlash(filepath.Join(PackageDirRel(pkg), toSnake(fn.Name)+"_test.go"))

		// The package API, extracted from the AST.
		//
		// This replaces guessing as the source of API knowledge. Across five
		// attempts a 14B coder model failed with `undefined: Project` because the
		// prompt contained a function body and nothing demonstrating how the
		// surrounding package is actually used. Feeding it signatures parsed out of
		// the source is not a prompt tweak: it is the compiler's own view of the
		// API, arrived at without any inference, so it cannot be confidently wrong
		// the way a generated API summary can.
		apiRef := ""
		if api, aerr := apiscan.Scan(pkgDir); aerr == nil {
			apiRef = api.Summary(apiBudget)
		} else {
			// Say so rather than silently falling back, so a degraded run is
			// distinguishable from one where the model simply failed.
			apiRef = "(API extraction failed: " + aerr.Error() + ")"
		}

		// Existing tests still add value: they show idioms and construction
		// patterns that a signature list cannot.
		example := existingTestExcerpt(pkgDir)

		var lastErr error = prevErr
		for attempt := 1; attempt <= attempts; attempt++ {
			body, tokens, perr := proposeOne(ctx, p, modelName, testPkg, fn, source, example, apiRef, attempt, lastErr)
			prop.Tokens += tokens
			if perr != nil {
				lastErr = perr
				prop.Rejected = append(prop.Rejected,
					fmt.Sprintf("%s attempt %d/%d: %v", fn.Name, attempt, attempts, perr))
				continue
			}
			prop.Files[rel] = body
			prop.Paths = append(prop.Paths, rel)
			prop.Targets = append(prop.Targets, fn.Name)
			prop.Trials += attempt
			break
		}
	}

	if len(prop.Files) == 0 {
		return nil, fmt.Errorf("model produced no valid test files after %d attempt(s) each (rejected: %s)",
			attempts, strings.Join(prop.Rejected, "; "))
	}
	return prop, nil
}

// proposeOne makes a single generation attempt and validates the response.
//
// prevErr is the rejection from the previous attempt, fed back verbatim. This is
// what makes retrying work rather than merely repeat: observed without feedback,
// a model made the identical import-cycle mistake on all four attempts, because
// "try again" carries no information. With the specific compiler-level reason in
// the prompt, the same mistake is recoverable on the next call.
//
// Validation here is syntactic and cheap, and deliberately a subset of what the
// trial checks. It is not a substitute for the compiler: duplicating it fully
// would reject valid work on technicalities while still letting real errors
// through. Its job is to catch the cheap, frequent mistakes before spending a
// full trial on them.
func proposeOne(ctx context.Context, p provider.ModelProvider, modelName, testPkg string,
	fn UncoveredFunc, source, example, apiRef string, attempt int, prevErr error) (string, int, error) {

	// State the rule that a model reliably gets wrong, and state it every time
	// rather than only on retry. The test file lives *inside* the package, so
	// importing that package is always an import cycle; the correct move is to
	// name things directly, or to use the external _test package.
	variation := "Keep it minimal: one happy-path assertion per test function."
	if prevErr != nil {
		variation = fmt.Sprintf("Your previous attempt was REJECTED for this exact reason:\n    %v\n"+
			"Fix that specific problem. Do not repeat the mistake.", prevErr)
	}

	prompt := fmt.Sprintf(`Write a Go test for one function in this package.

Package name: %s
Function to test: %s

Function source:
%s

THE COMPLETE EXPORTED API OF THIS PACKAGE, extracted from its source:
%s

This API listing is exact. Every identifier you use must appear in it. Do not
invent a name, a field, or a signature that is not listed above.

%s
Here is how existing tests in this same package use the API:

%s

Rules:
- Output ONLY the test file contents, no fences, no explanation.
- First line must be: package %s
- You are writing INSIDE this package. Do NOT import %q or any path
  ending in %q -- an internal test file must never import its own package,
  because that is an import cycle. Use the identifiers directly.
- If you would rather use package %s_test, then you MUST import the package and
  qualify every package-level name (write %s.Foo, not Foo).
- Use ONLY types and helpers listed above. Do not invent any.
- Import every OTHER package you reference.
- Write 1-2 test functions. Keep the whole file under 40 lines.
- You MUST call %s and assert on its result.

%s

Write the test now.`, testPkg, fn.Name, truncate(source, 2000), apiRef, exampleNote(example), example,
		testPkg, testPkg, testPkg, testPkg+"_test", testPkg, fn.Name, variation)

	callCtx, cancel := context.WithTimeout(ctx, selfmodTimeout)
	defer cancel()

	resp, err := p.Generate(callCtx, provider.CompletionRequest{
		Model:     modelName,
		Messages:  []provider.Message{{Role: "user", Content: prompt}},
		MaxTokens: selfmodTokens, Temperature: 0.2,
	})
	if err != nil {
		return "", 0, err
	}

	body := extractGoFile(resp.Content)
	if body == "" {
		return "", resp.Usage.CompletionTokens, fmt.Errorf("no usable Go source in response")
	}
	if !strings.HasPrefix(body, "package "+testPkg) {
		return "", resp.Usage.CompletionTokens, fmt.Errorf("package clause is not %q", "package "+testPkg)
	}
	if strings.Count(body, "{") != strings.Count(body, "}") {
		return "", resp.Usage.CompletionTokens, fmt.Errorf("unbalanced braces, response was truncated")
	}
	if !strings.Contains(body, fn.Name) {
		return "", resp.Usage.CompletionTokens, fmt.Errorf("does not reference %s", fn.Name)
	}
	if missing := unimportedPackages(body); len(missing) > 0 {
		return "", resp.Usage.CompletionTokens,
			fmt.Errorf("references %s without importing it", strings.Join(missing, ", "))
	}
	if selfImport := detectSelfImport(body, testPkg); selfImport {
		return "", resp.Usage.CompletionTokens, fmt.Errorf(
			"an internal test (package %s) must not import its own package; "+
				"use package %s_test instead", testPkg, testPkg+"_test")
	}
	if q := externalTestNeedsImport(body, testPkg); q != "" {
		return "", resp.Usage.CompletionTokens, fmt.Errorf(
			"this file is `package %s_test`, so it must import the package and qualify every "+
				"package-level name; `%s` is called unqualified. Write `%s.%s(...)` and add the import",
			testPkg, q, testPkg, q)
	}

	return body, resp.Usage.CompletionTokens, nil
}

// externalTestNeedsImport reports a package-level identifier that an external test
// file called without qualifying.
//
// A test in `package foo_test` lives outside the package, so `Project` is not in
// scope -- it must be `foo.Project` with the import present. This is the mirror
// image of the import-cycle mistake, and a model reliably produces one while
// fixing the other: told not to self-import, it switches to the external package
// and then forgets to qualify. Observed exactly that with a 14B coder model.
func externalTestNeedsImport(body, pkgName string) string {
	first := strings.TrimSpace(body)
	if !strings.HasPrefix(first, "package "+pkgName+"_test") {
		return ""
	}

	imported := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(strings.TrimSpace(line))
		trimmed = strings.Trim(trimmed, `"`)
		if strings.HasSuffix(trimmed, "/"+pkgName) || trimmed == pkgName {
			imported = true
			break
		}
	}
	if imported {
		return ""
	}

	// The package is not imported at all, so any unqualified call to a
	// package-level name is unresolvable. Report the first one found.
	for _, name := range targetSymbols(body, pkgName) {
		return name
	}
	return ""
}

// targetSymbols returns identifiers mentioned in the body that look like
// references to the package under test.
//
// Heuristic by necessity: the validator has no type information, so it looks for
// bare capitalised identifiers used in call position, which is what an unqualified
// package-level reference looks like in practice.
func targetSymbols(body, pkgName string) []string {
	// Names declared locally in this file are fine.
	local := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "func ") {
			name := strings.TrimSpace(strings.TrimPrefix(trimmed, "func "))
			if idx := strings.IndexAny(name, "( \t"); idx > 0 {
				local[name[:idx]] = true
			}
		}
		if strings.HasPrefix(trimmed, "import ") {
			continue
		}
	}

	var found []string
	seen := map[string]bool{}
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "//") ||
			strings.HasPrefix(trimmed, "package ") || strings.HasPrefix(trimmed, "import ") ||
			strings.HasPrefix(trimmed, "func ") {
			continue
		}
		for _, field := range strings.Fields(trimmed) {
			name := strings.Trim(field, "(),{};:=.")
			if name == "" || local[name] || seen[name] {
				continue
			}
			if name[0] < 'A' || name[0] > 'Z' {
				continue
			}
			// Only a call or a value use is a reference; a bare word in prose is
			// not. Requiring a following "(" or "=" keeps comments out.
			if strings.Contains(trimmed, name+"(") || strings.Contains(trimmed, name+" =") ||
				strings.Contains(trimmed, name+" :=") {
				seen[name] = true
				found = append(found, name)
			}
		}
	}
	return found
}

// detectSelfImport reports whether a file declares `package X` and also imports
// a path ending in X.
//
// This is an instant compile error -- "import cycle not allowed in test" -- and a
// model produces it surprisingly often, because "import the thing under test" is
// the natural instinct when writing a test. Catching it locally is worth one
// line: otherwise every occurrence costs a full copy, build, and test run of the
// package to learn something a single pass over the imports would have shown.
func detectSelfImport(body, pkgName string) bool {
	first := strings.TrimSpace(body)
	if !strings.HasPrefix(first, "package "+pkgName) {
		// An external test package legitimately imports the package under test.
		return false
	}

	inBlock := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "import ("):
			inBlock = true
		case inBlock && trimmed == ")":
			inBlock = false
		case inBlock, strings.HasPrefix(trimmed, "import "):
			spec := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "import")), `"`)
			if idx := strings.LastIndex(spec, "/"); idx >= 0 {
				spec = spec[idx+1:]
			}
			if spec == pkgName {
				return true
			}
		}
	}
	return false
}

// unimportedPackages finds package qualifiers used but not imported.
//
// A lexical check over the import block, deliberately conservative: it reports
// a qualifier only when it is clearly a package reference -- a capitalised
// identifier immediately followed by a dot -- and no import path's final element
// matches it. A false positive costs one wasted attempt; a false negative costs
// one wasted trial, so erring toward reporting is the right direction.
func unimportedPackages(body string) []string {
	imports := map[string]bool{}
	collect := func(spec string) {
		trimmed := strings.Trim(strings.TrimSpace(spec), `"`)
		if trimmed == "" {
			return
		}
		if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
			trimmed = trimmed[idx+1:]
		}
		imports[trimmed] = true
	}

	var importLines []string
	inBlock := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "import ("):
			inBlock = true
		case inBlock && trimmed == ")":
			inBlock = false
		case inBlock:
			importLines = append(importLines, trimmed)
		case strings.HasPrefix(trimmed, "import "):
			importLines = append(importLines, strings.TrimPrefix(trimmed, "import "))
		}
	}
	for _, spec := range importLines {
		collect(spec)
	}

	inImport := func(trimmed string) bool {
		if inBlock {
			return true
		}
		return strings.HasPrefix(trimmed, "import")
	}

	seen := map[string]bool{}
	var missing []string
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if inImport(trimmed) {
			continue
		}
		for i := 0; i+1 < len(trimmed); i++ {
			if trimmed[i] < 'A' || trimmed[i] > 'Z' {
				continue
			}
			if i > 0 && isIdentByte(trimmed[i-1]) {
				continue
			}
			if trimmed[i+1] != '.' {
				continue
			}
			if i+2 < len(trimmed) && isIdentByte(trimmed[i+2]) {
				continue
			}
			q := string(trimmed[i])
			if imports[q] || seen[q] {
				continue
			}
			seen[q] = true
			missing = append(missing, q)
		}
	}
	sort.Strings(missing)
	return missing
}

func isIdentByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// Write applies the proposal inside a trial tree.
func (p *Proposal) Write(treeRoot string) error {
	for rel, content := range p.Files {
		path := filepath.Join(treeRoot, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return fmt.Errorf("mkdir for %s: %w", rel, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
	}
	return nil
}

// ApplyTo writes the proposal into the live source tree.
//
// This is the only function in the package that touches the real repository, and
// it is deliberately the smallest. Everything that could reject a proposal has
// already run -- generation, syntax validation, import validation, the compiler,
// and the full test suite on a trial tree -- so promotion is the only gate left.
// Keeping the destructive step to one obvious call makes the one place that can
// damage the repository auditable by reading this file.
func (p *Proposal) ApplyTo(root string) (string, error) {
	for rel := range p.Files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if _, err := os.Stat(path); err == nil {
			return "", fmt.Errorf("refusing to overwrite existing file %s", rel)
		}
	}
	if err := p.Write(root); err != nil {
		return "", err
	}
	return strings.Join(p.Paths, ", "), nil
}

// PackageDirRel returns a package spec as a tree-relative directory, with the
// leading "./" removed.
func PackageDirRel(pkg string) string {
	trimmed := filepath.ToSlash(pkg)
	trimmed = strings.TrimSuffix(trimmed, "/")
	trimmed = strings.TrimPrefix(trimmed, "./")
	return filepath.FromSlash(trimmed)
}

// existingTestExcerpt returns a bounded slice of an existing test file from the
// package, preferring the setup helpers at the top.
//
// The top of a Go test file is where the construction helpers live, which is
// exactly the part a model needs and cannot infer. Later test bodies are mostly
// assertions that do not generalise.
func existingTestExcerpt(pkgDir string) string {
	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		return ""
	}

	var files []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), "_test.go") {
			files = append(files, e.Name())
		}
	}
	if len(files) == 0 {
		return ""
	}
	sort.Strings(files)

	for _, name := range files {
		data, rerr := os.ReadFile(filepath.Join(pkgDir, name))
		if rerr != nil {
			continue
		}
		return truncate(string(data), 2200)
	}
	return ""
}

func exampleNote(example string) string {
	if example == "" {
		return "(no existing tests in this package; infer the API from the source above)"
	}
	return "Existing tests from this package:"
}

func packageNameOf(pkg string) string {
	trimmed := strings.TrimSuffix(strings.TrimPrefix(filepath.ToSlash(pkg), "./"), "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

// DefaultServerURL is where the local llama.cpp server is expected.
//
// 8090 rather than 8080 because the Agent Manager occupies 8080 on this machine.
// Keeping it in one exported constant means a port conflict is fixed in one place
// rather than three, which is exactly the bug that a hardcoded literal invites.
const DefaultServerURL = "http://localhost:8090"

// ServerURL resolves the inference endpoint, preferring an explicit override.
func ServerURL() string {
	if v := os.Getenv("SPIRAL_GGUF_SERVER"); v != "" {
		return v
	}
	return DefaultServerURL
}
