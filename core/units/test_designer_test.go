package units

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kilo/spiral-codemaker/core/apiscan"
)

func TestTestsFromAPIUsesRealPackageName(t *testing.T) {
	// A cmd/... entry point lives in a directory whose name differs
	// from its package: cmd/palindrome holds `package main`. The
	// generated test must declare the package the compiler will see,
	// or the workspace fails to build with `found packages main
	// (main.go) and palindrome (...)` -- the exact failure that made
	// a run report test failures.
	dir := t.TempDir()
	pkgDir := filepath.Join(dir, "cmd", "palindrome")
	if err := os.MkdirAll(pkgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	src := `package main

// IsPalindrome reports whether s reads the same both ways.
func IsPalindrome(s string) bool {
	return len(s) > 0
}
`
	if err := os.WriteFile(filepath.Join(pkgDir, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	api, err := apiscan.Scan(pkgDir)
	if err != nil {
		t.Fatal(err)
	}
	if api.Package != "main" {
		t.Fatalf("apiscan reported package %q, want main", api.Package)
	}

	files := testsFromAPI(api, "cmd/palindrome")
	if len(files) == 0 {
		t.Fatal("expected a test file for the exported IsPalindrome")
	}
	for _, f := range files {
		if !strings.Contains(f.Content, "package main") {
			t.Errorf("test for %s must declare package main, got:\n%s", f.Path, f.Content)
		}
		if strings.Contains(f.Content, "package palindrome") {
			t.Errorf("test for %s must not declare the directory name as the package:\n%s", f.Path, f.Content)
		}
	}
}

func TestTestsFromAPIOneFilePerFunction(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "api.go"), []byte(`package widgets

func Alpha(s string) bool { return true }
func Beta(s string) bool { return true }
`), 0o644); err != nil {
		t.Fatal(err)
	}

	api, err := apiscan.Scan(dir)
	if err != nil {
		t.Fatal(err)
	}

	files := testsFromAPI(api, "widgets")
	if len(files) != 2 {
		t.Fatalf("expected one test file per function, got %d: %v", len(files), files)
	}
	paths := map[string]bool{}
	for _, f := range files {
		paths[filepath.Base(f.Path)] = true
	}
	if !paths["zz_Alpha_test.go"] || !paths["zz_Beta_test.go"] {
		t.Errorf("expected zz_Alpha_test.go and zz_Beta_test.go, got %v", paths)
	}
}
