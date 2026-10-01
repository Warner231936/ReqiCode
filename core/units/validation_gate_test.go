package units

import (
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/core/state"
)

// The validation gate is what stands between model output and a broken
// workspace. Every check here corresponds to an observed failure: a duplicate
// declaration that reached the compiler, a truncated completion, a placeholder
// echo, a duplicate path.

func planWith(paths ...string) *state.ArchitecturePlan {
	p := &state.ArchitecturePlan{AlternativeID: "p", Description: "d"}
	for _, path := range paths {
		p.Components = append(p.Components, state.ComponentSpec{
			Name: "c", Path: path, Description: "d",
		})
	}
	return p
}

const goodFile = `package core

import "fmt"

func DoThing(x int) string {
	return fmt.Sprintf("%d", x)
}
`

func TestGateRejectsPromptEcho(t *testing.T) {
	// A model that echoes the prompt back contains the phrase "package core"
	// because the prompt asked for it, and can easily have balanced braces. Only
	// requiring the package clause to *lead* distinguishes a file from something
	// that discusses a file. This exact failure reached the workspace before the
	// check existed.
	echo := "You are a Go code generator. Write exactly ONE Go file.\n\n" +
		"Project: a CLI tool\nModule: cli\n" +
		"First line must be: package core\n" +
		"Rules:\n- Output ONLY the file contents\n"
	files := []FileSpec{{Path: "pkg/core/core.go", Content: echo}}
	if validateLLMFiles(files, nil) {
		t.Error("a prompt echo must be rejected even though it mentions the package clause")
	}
}

func TestGateAcceptsFileWithLeadingComment(t *testing.T) {
	// Comments and build constraints legitimately precede the package clause.
	withComment := "// Package core provides file listing.\n// It has no side effects.\n" + goodFile
	if !validateLLMFiles([]FileSpec{{Path: "pkg/core/core.go", Content: withComment}}, nil) {
		t.Error("a file whose package clause is preceded by comments must be accepted")
	}
}

func TestStartsWithPackageClause(t *testing.T) {
	cases := []struct {
		content string
		want    bool
	}{
		{"package core\n", true},
		{"\n\npackage core\n", true},
		{"// comment\npackage core\n", true},
		{"Here is the file:\npackage core\n", false},
		{"[mock response to: write package core]", false},
		{"", false},
	}
	for _, c := range cases {
		if got := startsWithPackageClause(c.content); got != c.want {
			t.Errorf("startsWithPackageClause(%q) = %v, want %v", c.content, got, c.want)
		}
	}
}

func TestExtractSingleFileRejectsEcho(t *testing.T) {
	echo := "Sure! First line must be: package core\nHere you go."
	if extractSingleFile(echo, "pkg/core/core.go", "d") != nil {
		t.Error("a response that only mentions the package clause must not be accepted as a file")
	}
}

func TestGateRejectsEmpty(t *testing.T) {
	if validateLLMFiles(nil, nil) {
		t.Error("no files must be rejected")
	}
}

func TestGateAcceptsWellFormedFiles(t *testing.T) {
	files := []FileSpec{
		{Path: "cmd/app/main.go", Content: "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(1)\n}\n"},
		{Path: "pkg/core/core.go", Content: goodFile},
	}
	if !validateLLMFiles(files, planWith("cmd/app/main.go", "pkg/core/core.go")) {
		t.Error("well-formed files matching the plan must pass")
	}
}

func TestGateRejectsDuplicateDeclarationAcrossFiles(t *testing.T) {
	// The exact failure observed from a 7B model: two files, identical bodies,
	// same package. The path check passes; only a symbol check catches it.
	dup := goodFile
	files := []FileSpec{
		{Path: "pkg/core/file_list.go", Content: dup},
		{Path: "pkg/core/utils.go", Content: dup},
	}
	if validateLLMFiles(files, planWith("pkg/core/file_list.go", "pkg/core/utils.go")) {
		t.Error("duplicate top-level declaration in one package must be rejected")
	}
}

func TestGateAllowsSameSymbolInDifferentPackages(t *testing.T) {
	body := goodFile
	files := []FileSpec{
		{Path: "pkg/core/core.go", Content: body},
		{Path: "pkg/store/store.go", Content: body},
	}
	if !validateLLMFiles(files, planWith("pkg/core/core.go", "pkg/store/store.go")) {
		t.Error("the same symbol name in different packages is legal and must be allowed")
	}
}

func TestGateAllowsMethodsToRepeat(t *testing.T) {
	// Methods are legitimately repeatable across types and embedded structs.
	m1 := "package core\n\ntype A struct{}\n\nfunc (a *A) Get() string {\n\treturn \"\"\n}\n"
	m2 := "package core\n\ntype B struct{}\n\nfunc (b *B) Get() string {\n\treturn \"\"\n}\n"
	files := []FileSpec{
		{Path: "pkg/core/a.go", Content: m1},
		{Path: "pkg/core/b.go", Content: m2},
	}
	if !validateLLMFiles(files, planWith("pkg/core/a.go", "pkg/core/b.go")) {
		t.Error("methods on different types must not be treated as duplicates")
	}
}

func TestGateRejectsDuplicatePath(t *testing.T) {
	files := []FileSpec{
		{Path: "pkg/core/core.go", Content: goodFile},
		{Path: "pkg/core/core.go", Content: goodFile},
	}
	if validateLLMFiles(files, planWith("pkg/core/core.go")) {
		t.Error("duplicate paths must be rejected")
	}
}

func TestGateRejectsTruncatedOutput(t *testing.T) {
	truncated := "package core\n\nimport \"fmt\"\n\nfunc DoThing() {\n\tfmt.Println(\"unterminated\"\n"
	files := []FileSpec{{Path: "pkg/core/core.go", Content: truncated}}
	if validateLLMFiles(files, planWith("pkg/core/core.go")) {
		t.Error("unbalanced braces indicate a truncated completion and must be rejected")
	}
}

func TestGateRejectsPlaceholders(t *testing.T) {
	files := []FileSpec{{Path: "pkg/core/core.go", Content: goodFile + "\n// path/to/example.go\n"}}
	if validateLLMFiles(files, planWith("pkg/core/core.go")) {
		t.Error("placeholder echo must be rejected")
	}
}

func TestGateRejectsPathEscape(t *testing.T) {
	files := []FileSpec{{Path: "../../etc/passwd.go", Content: goodFile}}
	if validateLLMFiles(files, planWith("x.go")) {
		t.Error("a path escaping the workspace must be rejected")
	}
}

func TestGateRejectsNonGoPath(t *testing.T) {
	files := []FileSpec{{Path: "pkg/core/core.txt", Content: goodFile}}
	if validateLLMFiles(files, planWith("pkg/core/core.txt")) {
		t.Error("a non-.go path must be rejected")
	}
}

func TestGateRejectsMissingPackageClause(t *testing.T) {
	files := []FileSpec{{Path: "pkg/core/core.go", Content: "func DoThing() string {\n\treturn \"\"\n}\n"}}
	if validateLLMFiles(files, planWith("pkg/core/core.go")) {
		t.Error("content without a package clause must be rejected")
	}
}

func TestGateAcceptsPartialGeneration(t *testing.T) {
	// Generation is per-file, so a partial set is expected and each landed file
	// is judged on its own. The gate must not reject a valid single file just
	// because the plan named three.
	files := []FileSpec{{Path: "pkg/core/core.go", Content: goodFile}}
	if !validateLLMFiles(files, planWith("cmd/app/main.go", "pkg/core/core.go", "pkg/types/t.go")) {
		t.Error("a single valid file must pass even when the plan named more")
	}
}

func TestExtractSingleFileStripsFences(t *testing.T) {
	resp := "Here is the file:\n\n```go\n" + goodFile + "```\n\nThat should work."
	spec := extractSingleFile(resp, "pkg/core/core.go", "d")
	if spec == nil {
		t.Fatal("expected to extract a file from a fenced response")
	}
	if strings.Contains(spec.Content, "```") {
		t.Errorf("fences must be stripped, got:\n%s", spec.Content)
	}
	if !strings.HasPrefix(spec.Content, "package core") {
		t.Errorf("expected the package clause first, got:\n%s", spec.Content)
	}
}

func TestExtractSingleFileTrimsLeadingProse(t *testing.T) {
	resp := "Sure! Here is the implementation you asked for.\n\n" + goodFile
	spec := extractSingleFile(resp, "pkg/core/core.go", "d")
	if spec == nil {
		t.Fatal("expected extraction despite leading prose")
	}
	if !strings.HasPrefix(spec.Content, "package core") {
		t.Errorf("prose must be trimmed, got:\n%s", spec.Content)
	}
}

func TestExtractSingleFileRejectsTruncated(t *testing.T) {
	truncated := "package core\n\nfunc DoThing() {\n\tfmt.Println(\"unterminated\"\n"
	if extractSingleFile(truncated, "pkg/core/core.go", "d") != nil {
		t.Error("a truncated response must not be accepted as a file")
	}
}

func TestExtractSingleFileRejectsProseOnly(t *testing.T) {
	if extractSingleFile("I cannot help with that request.", "pkg/core/core.go", "d") != nil {
		t.Error("a prose-only response must be rejected")
	}
}

func TestExtractSingleFileRejectsEmpty(t *testing.T) {
	if extractSingleFile("   \n  ", "pkg/core/core.go", "d") != nil {
		t.Error("an empty response must be rejected")
	}
}
func TestDeclaredFuncsExtractsNames(t *testing.T) {
	src := "package core\n\nfunc Alpha() {}\nfunc Beta(x int) string {\n\treturn \"\"\n}\nfunc Gamma[T any](v T) {}\n"
	got := declaredFuncs(src)
	set := map[string]bool{}
	for _, g := range got {
		set[g] = true
	}
	for _, want := range []string{"Alpha", "Beta", "Gamma"} {
		if !set[want] {
			t.Errorf("expected to extract %q, got %v", want, got)
		}
	}
}

func TestDeclaredFuncsSkipsMethodsAndComments(t *testing.T) {
	src := `package core

// func Commented() {}
/* func Blocked() {} */
type T struct{}

func (t *T) Method() {}

func Real() {}
`
	got := declaredFuncs(src)
	for _, g := range got {
		if g == "Commented" || g == "Blocked" {
			t.Errorf("commented-out declarations must not be extracted, got %v", got)
		}
		if g == "Method" {
			t.Errorf("methods must be skipped for the duplicate check, got %v", got)
		}
	}
	found := false
	for _, g := range got {
		if g == "Real" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected Real to be extracted, got %v", got)
	}
}

func TestDeclaredFuncsIgnoresMain(t *testing.T) {
	// main legitimately exists in exactly one package per command directory, and
	// the check keys on directory, so counting it would create false positives.
	src := "package main\n\nfunc main() {\n}\n"
	for _, g := range declaredFuncs(src) {
		if strings.TrimSpace(g) == "main" {
			t.Error("main must not be tracked as a duplicate-sensitive symbol")
		}
	}
}

func TestPackageNameDerivationForGate(t *testing.T) {
	cases := map[string]string{
		"pkg/core/core.go":    "core",
		"internal/store/s.go": "store",
		"cmd/app/main.go":     "main",
		"main.go":             "main",
	}
	for path, want := range cases {
		if got := packageName(path); got != want {
			t.Errorf("packageName(%q) = %q, want %q", path, got, want)
		}
	}
}
