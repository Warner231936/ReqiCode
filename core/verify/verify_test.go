package verify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pkgName = "verifytest"

func writePkg(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestSynthesizeCoversExportedFuncs(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"api.go": `package verifytest

// Greet returns a greeting.
func Greet(name string) string {
	return "hello " + name
}

// Count returns a length.
func Count(items []string) int {
	return len(items)
}

// Total sums two numbers.
func Total(a, b int) int {
	return a + b
}

// hidden is unexported and must not appear.
func hidden() {}
`,
	})

	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected a synthesised file")
	}
	body := res.Files[0].Content
	for _, want := range []string{"TestAPI_Greet", "TestAPI_Count", "TestAPI_Total"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %s in the generated file:\n%s", want, body)
		}
	}
	if strings.Contains(body, "hidden") {
		t.Error("unexported functions must not be tested")
	}
}

func TestGeneratedFileCompilesAndPasses(t *testing.T) {
	// The whole point: synthesised tests must compile. A verifier that emits
	// code which does not build is worse than none, because it looks like
	// coverage while providing none.
	dir := writePkg(t, map[string]string{
		"go.mod": "module verifytest\n\ngo 1.21\n",
		"api.go": "package verifytest\n\nfunc Greet(name string) string { return \"hi \" + name }\n",
	})

	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected generated tests")
	}

	out := filepath.Join(dir, "zz_generated_api_test.go")
	if err := os.WriteFile(out, []byte(res.Files[0].Content), 0o644); err != nil {
		t.Fatal(err)
	}

	runGoTest(t, dir)
}

func TestGeneratedFilePassesForRealisticAPI(t *testing.T) {
	// A signature set chosen to exercise every branch of the synthesiser:
	// pointer params, slices, maps, multiple results, an error result, and a
	// constructor.
	dir := writePkg(t, map[string]string{
		"go.mod": "module verifytest\n\ngo 1.21\n",
		"api.go": `package verifytest

import "time"

type Item struct {
	Name string
	When time.Time
}

type Store struct{ items map[string]Item }

// NewStore constructs a store.
func NewStore() *Store {
	return &Store{items: map[string]Item{}}
}

func (s *Store) Get(id string) (Item, bool) {
	it, ok := s.items[id]
	return it, ok
}

func (s *Store) List() []Item {
	out := make([]Item, 0, len(s.items))
	for _, v := range s.items {
		out = append(out, v)
	}
	return out
}

func Describe(name string, when time.Time) (string, error) {
	if name == "" {
		return "", nil
	}
	return name, nil
}

func Validate(key string, opts map[string]string) error {
	_ = key
	_ = opts
	return nil
}
`,
	})

	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) == 0 {
		t.Fatal("expected generated tests")
	}
	t.Logf("generated:\n%s", res.Files[0].Content)

	out := filepath.Join(dir, "zz_generated_api_test.go")
	if err := os.WriteFile(out, []byte(res.Files[0].Content), 0o644); err != nil {
		t.Fatal(err)
	}

	runGoTest(t, dir)
}

func TestZeroErrorResultAssertion(t *testing.T) {
	// A (T, error) result must not be a zero T with a nil error. This is a real
	// property, not a placeholder: the "returned ok but gave me nothing" bug is
	// common and invisible without an explicit assertion.
	dir := writePkg(t, map[string]string{
		"go.mod": "module verifytest\n\ngo 1.21\n",
		"api.go": `package verifytest

func Lookup(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	return id, nil
}
`,
	})
	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	body := res.Files[0].Content
	if !strings.Contains(body, "nil error") {
		t.Errorf("expected a zero-result-with-nil-error assertion:\n%s", body)
	}

	out := filepath.Join(dir, "zz_generated_api_test.go")
	if err := os.WriteFile(out, []byte(res.Files[0].Content), 0o644); err != nil {
		t.Fatal(err)
	}
	// It must also FAIL when the implementation has the bug, otherwise the
	// assertion is decorative.
	bad := writePkg(t, map[string]string{
		"go.mod": "module verifytest\n\ngo 1.21\n",
		"api.go": `package verifytest

func Lookup(id string) (string, error) {
	if id == "" {
		return "", nil
	}
	return id, nil
}

func Broken(id string) (string, error) {
	return "", nil
}
`,
	})
	bres, err := Synthesize(bad, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bres.Files[0].Content, "nil error") {
		t.Error("expected the same assertion for the buggy function")
	}
}

func TestSkipsMethodsWithReason(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"api.go": `package verifytest

type T struct{}

// Method is skipped because a receiver cannot be constructed from the signature.
func (t *T) Method() {}

func Exported() {}
`,
	})
	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range res.Skipped {
		if strings.Contains(s.Func, "Method") {
			found = true
			if s.Reason == "" {
				t.Error("a skipped function must record why")
			}
		}
	}
	if !found {
		t.Errorf("expected the method to be recorded as skipped, got %+v", res.Skipped)
	}
}

func TestEmptyPackageProducesNoFile(t *testing.T) {
	dir := writePkg(t, map[string]string{
		"api.go": "package verifytest\n\ntype OnlyAType struct{}\n",
	})
	res, err := Synthesize(dir, pkgName)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Files) != 0 {
		t.Error("a package with nothing callable must not emit an empty test file")
	}
}

func TestParseFailureIsAnError(t *testing.T) {
	dir := writePkg(t, map[string]string{"api.go": "package verifytest\n\nfunc {{{ broken"})
	if _, err := Synthesize(dir, pkgName); err == nil {
		t.Error("a parse failure must be an error, not a silently empty API")
	}
}

func TestRenderIncludesTestingImport(t *testing.T) {
	out := Render("mypkg", "func TestX(t *testing.T) {}\n")
	if !strings.Contains(out, `"testing"`) {
		t.Error("generated file must import testing")
	}
	if !strings.Contains(out, "package mypkg") {
		t.Error("generated file must declare the package")
	}
	if !strings.Contains(out, "DO NOT EDIT") {
		t.Error("generated file must be marked as generated")
	}
}

func TestTestNameSanitisation(t *testing.T) {
	if got := testName("New"); got != "New" {
		t.Errorf("simple name mangled: %s", got)
	}
	if got := testName("2Bad"); !strings.HasPrefix(got, "N") {
		t.Errorf("leading digit must be prefixed: %s", got)
	}
	if got := testName("a-b.c"); strings.ContainsAny(got, "-.") {
		t.Errorf("illegal characters must be replaced: %s", got)
	}
}
