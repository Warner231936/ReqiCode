package selfmodify

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleProfile = `mode: set
core/state/chain.go:20.34,22.2 2 1
core/state/chain.go:22.2,30.1 3 0
core/state/chain.go:31.1,35.2 1 0
core/state/lifecycle.go:10.1,12.2 2 0
core/state/semi_state.go:5.1,9.2 4 4
core/converge/potential.go:1.1,5.2 1 0
`

func writeProfile(t *testing.T, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cov.out")
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestParseCoverageProfileTotals(t *testing.T) {
	p, err := ParseCoverageProfile(writeProfile(t, sampleProfile))
	if err != nil {
		t.Fatal(err)
	}

	// chain.go: 2+3+1 = 6 statements, 2 covered.
	if got := p.FileCoverage("core/state/chain.go"); got < 33 || got > 34 {
		t.Errorf("chain.go coverage = %.1f%%, want ~33%%", got)
	}
	// semi_state.go: 4 statements, all covered.
	if got := p.FileCoverage("core/state/semi_state.go"); got != 100 {
		t.Errorf("semi_state.go coverage = %.1f%%, want 100%%", got)
	}
	// potential.go: 1 statement, uncovered.
	if got := p.FileCoverage("core/converge/potential.go"); got != 0 {
		t.Errorf("potential.go coverage = %.1f%%, want 0%%", got)
	}
}

func TestParseCoverageProfileIgnoresMalformedLines(t *testing.T) {
	p, err := ParseCoverageProfile(writeProfile(t,
		"mode: atomic\ngarbage\ncore/x.go:1.1,2.2 notanumber 1\ncore/ok.go:1.1,2.2 2 2\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got := p.FileCoverage("core/ok.go"); got != 100 {
		t.Errorf("ok.go coverage = %.1f%%, want 100%%", got)
	}
	if got := p.FileCoverage("garbage"); got != 0 {
		t.Errorf("garbage should contribute nothing, got %.1f", got)
	}
}

func TestParseCoverageProfileMissingFile(t *testing.T) {
	if _, err := ParseCoverageProfile(filepath.Join(t.TempDir(), "nope.out")); err == nil {
		t.Error("a missing profile must be an error, not a silent 0%")
	}
}

func TestFindUncoveredFuncs(t *testing.T) {
	src := `package core

import "fmt"

// Uncovered finds exported functions with no coverage.
func Uncovered(x int) string {
	return fmt.Sprint(x)
}

func unexportedHelper() {}

func (t *Thing) Method() {}

func main() {}

func Generic[T any](v T) T {
	return v
}
`
	p := filepath.Join(t.TempDir(), "core.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := FindUncoveredFuncs(p, nil)
	if err != nil {
		t.Fatal(err)
	}

	names := map[string]bool{}
	for _, f := range got {
		names[f.Name] = true
	}

	if !names["Uncovered"] {
		t.Error("expected to find the exported function")
	}
	if !names["Generic"] {
		t.Error("expected to find the generic exported function, without its type params")
	}
	for _, unwanted := range []string{"unexportedHelper", "Method", "main"} {
		if names[unwanted] {
			t.Errorf("%s should not be in the work list", unwanted)
		}
	}
}

func TestFindUncoveredFuncsSkipsComments(t *testing.T) {
	src := `package core

// func CommentedOut() {}

/*
func BlockedOut() {}
*/

func Real() {}
`
	p := filepath.Join(t.TempDir(), "core.go")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := FindUncoveredFuncs(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "Real" {
		t.Errorf("expected only Real, got %+v", got)
	}
}

func TestFindUncoveredFuncsMissingFile(t *testing.T) {
	if _, err := FindUncoveredFuncs(filepath.Join(t.TempDir(), "nope.go"), nil); err == nil {
		t.Error("a missing source file must be an error")
	}
}

func TestParseTestCounts(t *testing.T) {
	out := `=== RUN   TestA
--- PASS: TestA (0.00s)
=== RUN   TestB
--- FAIL: TestB (0.01s)
=== RUN   TestC
--- SKIP: TestC (0.00s)
ok
`
	p, f, s := parseTestCounts(out)
	if p != 1 || f != 1 || s != 1 {
		t.Errorf("got pass=%d fail=%d skip=%d, want 1/1/1", p, f, s)
	}
}

func TestParseTestCountsBuildFailure(t *testing.T) {
	// A build failure produces no per-test lines. Counting it as zero failures
	// would report a broken package as clean.
	p, f, _ := parseTestCounts("FAIL\tpkg [build failed]\nsyntax error\n")
	if p != 0 {
		t.Errorf("expected no passes on a build failure, got %d", p)
	}
	if f != 1 {
		t.Errorf("a build failure must register as a failure, got %d", f)
	}
}

func TestParseTestCountsNoTests(t *testing.T) {
	p, f, _ := parseTestCounts("ok  \tpkg\t0.001s\tcoverage: 0.0% of statements\n?   \tpkg [no test files]\n")
	if p != 0 || f != 0 {
		t.Errorf("a package with no tests is neutral, got pass=%d fail=%d", p, f)
	}
}

func TestCompareRejectsBuildFailure(t *testing.T) {
	base := Measurement{BuildOK: true, CoveragePercent: 10}
	cand := Measurement{BuildOK: false, CoveragePercent: 95, Err: "syntax error"}
	v := Compare(base, cand)
	if v.Promote {
		t.Fatal("a candidate that does not build must never be promoted, however high its score")
	}
	if !strings.Contains(v.Reason, "does not build") {
		t.Errorf("reason should name the build failure, got %q", v.Reason)
	}
}

func TestCompareRejectsRegression(t *testing.T) {
	base := Measurement{BuildOK: true, CoveragePercent: 50, Failed: 0}
	cand := Measurement{BuildOK: true, CoveragePercent: 60, Failed: 2}
	if Compare(base, cand).Promote {
		t.Error("higher coverage must not excuse newly failing tests")
	}
}

func TestCompareRejectsIntroducedFailure(t *testing.T) {
	base := Measurement{BuildOK: true, CoveragePercent: 50, Failed: 1}
	cand := Measurement{BuildOK: true, CoveragePercent: 60, Failed: 1}
	v := Compare(base, cand)
	// Same failure count is tolerated; a rise is not.
	if v.Promote && cand.Failed > base.Failed {
		t.Error("a rise in failures must block promotion")
	}
}

func TestCompareRejectsTie(t *testing.T) {
	m := Measurement{BuildOK: true, CoveragePercent: 50, Failed: 0}
	if Compare(m, m).Promote {
		t.Error("an identical tree is not an improvement")
	}
}

func TestCompareRejectsNoiseGain(t *testing.T) {
	base := Measurement{BuildOK: true, CoveragePercent: 50.0}
	cand := Measurement{BuildOK: true, CoveragePercent: 50.2}
	if Compare(base, cand).Promote {
		t.Error("a gain below the threshold has verified nothing meaningful")
	}
}

func TestComparePromotesRealGain(t *testing.T) {
	base := Measurement{BuildOK: true, CoveragePercent: 40}
	cand := Measurement{BuildOK: true, CoveragePercent: 65, Passed: 12}
	v := Compare(base, cand)
	if !v.Promote {
		t.Errorf("a substantial gain should promote: %s", v.Reason)
	}
	if v.Delta < 24 || v.Delta > 26 {
		t.Errorf("unexpected delta %f", v.Delta)
	}
}

func TestCompareBrokenBaselineAcceptsBuildingCandidate(t *testing.T) {
	base := Measurement{BuildOK: false, Err: "was broken"}
	cand := Measurement{BuildOK: true, CoveragePercent: 0}
	if !Compare(base, cand).Promote {
		t.Error("any building tree is an improvement over a broken one")
	}
}

func TestCopyTreeExcludesHeavyDirs(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "go.mod"), "module x\n")
	mustWrite(t, filepath.Join(src, "keep.go"), "package x\n")
	if err := os.MkdirAll(filepath.Join(src, "models-cache"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "models-cache", "big.gguf"), "weights")
	if err := os.MkdirAll(filepath.Join(src, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, ".git", "HEAD"), "ref")
	if err := os.MkdirAll(filepath.Join(src, "spiral-output-x", "output"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(src, "spiral-output-x", "output", "stale.go"), "package stale\n")

	dst := t.TempDir()
	if err := CopyTree(src, dst); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dst, "keep.go")); err != nil {
		t.Error("source files must be copied")
	}
	for _, excluded := range []string{"models-cache", ".git", "spiral-output-x"} {
		if _, err := os.Stat(filepath.Join(dst, excluded)); err == nil {
			t.Errorf("%s must not be copied; a stale output tree would let a trial test old code", excluded)
		}
	}
}

func TestCopyTreeSkipsBinaries(t *testing.T) {
	src := t.TempDir()
	mustWrite(t, filepath.Join(src, "a.go"), "package a\n")
	mustWrite(t, filepath.Join(src, "tool.exe"), "binary")
	mustWrite(t, filepath.Join(src, "model.gguf"), "weights")
	mustWrite(t, filepath.Join(src, "coverage.out"), "profile")

	dst := t.TempDir()
	if err := CopyTree(src, dst); err != nil {
		t.Fatal(err)
	}
	for _, skipped := range []string{"tool.exe", "model.gguf", "coverage.out"} {
		if _, err := os.Stat(filepath.Join(dst, skipped)); err == nil {
			t.Errorf("%s must not be copied", skipped)
		}
	}
}

func TestPackageNameAndDir(t *testing.T) {
	if got := PackageName("github.com/kilo/spiral-codemaker/core/state"); got != "state" {
		t.Errorf("PackageName = %q, want state", got)
	}
	if got := PackageName("state"); got != "state" {
		t.Errorf("PackageName of a bare spec = %q, want state", got)
	}
	if got := CandidateDir("/tmp/trial", "./core/state"); got != filepath.FromSlash("/tmp/trial/core/state") {
		t.Errorf("CandidateDir = %q, want the package directory itself", got)
	}
	if got := CandidateDir("/tmp/trial", "core/attention/"); got != filepath.FromSlash("/tmp/trial/core/attention") {
		t.Errorf("CandidateDir with a trailing slash = %q", got)
	}
}

// TestEndToEndCoverageObjective runs the real toolchain against a real package.
//
// This is the test that decides whether self-modification is more than a
// structure. Everything else in this file tests bookkeeping; this one proves the
// measurement path actually reads coverage and pass/fail from a live build.
func TestEndToEndCoverageObjective(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end self-modification trial")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	root, err := findRepoRoot()
	if err != nil {
		t.Skipf("cannot locate repository root: %v", err)
	}

	h := NewHarness(root, NewGoRunner())
	pkg := "./core/regression"

	baseRes, err := h.Baseline(ctx, pkg)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	dir := baseRes.Dir
	base := baseRes.Measurement

	defer h.Cleanup(dir)

	if !base.BuildOK {
		t.Fatalf("baseline must build, got: %s", base.Err)
	}
	if base.CoveragePercent <= 0 {
		t.Fatalf("baseline coverage should be measurable, got %.1f (err: %s)", base.CoveragePercent, base.Err)
	}
	t.Logf("baseline: %s", FormatCounts(base))

	// An edit that does nothing must be rejected.
	_, noop, err := h.Candidate(ctx, pkg, func(string) error { return nil })
	if err != nil {
		t.Fatalf("noop candidate: %v", err)
	}
	if v := Compare(base, noop); v.Promote {
		t.Error("a no-op edit must never be promoted")
	}

	// An edit that breaks the build must be rejected regardless of anything else.
	candDir, broken, err := h.Candidate(ctx, pkg, func(d string) error {
		p := filepath.Join(d, "core", "regression", "baseline.go")
		return os.WriteFile(p, []byte("package regression\n\nthis is not valid go\n"), 0o644)
	})
	if err == nil && broken.BuildOK {
		t.Error("a candidate with a syntax error must not build")
	}
	if v := Compare(base, broken); v.Promote {
		t.Error("a broken candidate must never be promoted")
	}
	h.Cleanup(candDir)
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// findRepoRoot walks up from the test's directory looking for go.mod.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
