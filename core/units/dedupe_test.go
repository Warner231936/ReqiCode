package units

import "testing"

// Regression: a 7B model asked for two components returned the same body under
// two names. Per-file gating passed both in isolation, and the workspace ended
// up with a duplicate Process declaration that only the compiler caught.
//
// These tests pin the cross-file check that fixes it.

const observedDupBody = `package core

import (
	"fmt"
	"os"
	"path/filepath"
)

func Process(input string) string {
	if _, err := os.Stat(input); os.IsNotExist(err) {
		return fmt.Sprintf("Error: path does not exist: %s", input)
	}
	files, err := filepath.Glob(input + "/*")
	if err != nil {
		return fmt.Sprintf("Error: %v", err)
	}
	count := 0
	for _, f := range files {
		info, err := os.Stat(f)
		if err == nil && !info.IsDir() {
			count++
		}
	}
	return fmt.Sprintf("Found %d items", count)
}
`

func TestDedupeDropsIdenticalBodiesInOnePackage(t *testing.T) {
	files := []FileSpec{
		{Path: "pkg/core/filelist.go", Content: observedDupBody},
		{Path: "pkg/core/utils.go", Content: observedDupBody},
	}
	kept, dropped := dedupeDeclarations(files)

	if len(kept) != 1 {
		t.Errorf("expected one file kept, got %d", len(kept))
	}
	if len(dropped) != 1 || dropped[0] != "pkg/core/utils.go" {
		t.Errorf("expected utils.go dropped, got %v", dropped)
	}
	if kept[0].Path != "pkg/core/filelist.go" {
		t.Errorf("the first file in plan order should be kept, got %s", kept[0].Path)
	}
}

func TestDedupeKeepsDistinctFilesInOnePackage(t *testing.T) {
	a := "package core\n\nfunc Alpha() string {\n\treturn \"a\"\n}\n"
	b := "package core\n\nfunc Beta() string {\n\treturn \"b\"\n}\n"
	files := []FileSpec{
		{Path: "pkg/core/alpha.go", Content: a},
		{Path: "pkg/core/beta.go", Content: b},
	}
	kept, dropped := dedupeDeclarations(files)
	if len(kept) != 2 {
		t.Errorf("distinct files must both survive, got %d", len(kept))
	}
	if len(dropped) != 0 {
		t.Errorf("nothing should have been dropped, got %v", dropped)
	}
}

func TestDedupeAllowsSameSymbolAcrossPackages(t *testing.T) {
	body := "package core\n\nfunc Shared() {}\n"
	files := []FileSpec{
		{Path: "pkg/core/core.go", Content: body},
		{Path: "pkg/store/store.go", Content: body},
	}
	kept, dropped := dedupeDeclarations(files)
	if len(kept) != 2 {
		t.Errorf("same symbol in different packages is legal, got %d kept", len(kept))
	}
	if len(dropped) != 0 {
		t.Errorf("nothing should be dropped across packages, got %v", dropped)
	}
}

func TestDedupePreservesOrderAndDropsAllCollisions(t *testing.T) {
	body := "package core\n\nfunc Same() {}\n"
	files := []FileSpec{
		{Path: "pkg/core/a.go", Content: body},
		{Path: "pkg/core/b.go", Content: body},
		{Path: "pkg/core/c.go", Content: body},
	}
	kept, dropped := dedupeDeclarations(files)
	if len(kept) != 1 || kept[0].Path != "pkg/core/a.go" {
		t.Errorf("expected only a.go kept, got %v", kept)
	}
	if len(dropped) != 2 {
		t.Errorf("expected two dropped, got %v", dropped)
	}
}

func TestGateStillRejectsDuplicateWithinOneResponse(t *testing.T) {
	// The single-response path remains guarded too; cross-file dedupe is a second
	// line of defence, not a replacement.
	files := []FileSpec{
		{Path: "pkg/core/filelist.go", Content: observedDupBody},
		{Path: "pkg/core/utils.go", Content: observedDupBody},
	}
	if validateLLMFiles(files, nil) {
		t.Error("the gate must reject a duplicate pair in a single response")
	}
}
