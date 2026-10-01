package selfmodify

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Runner executes measurement commands in a tree. Injected so the harness can be
// tested without a Go toolchain, and so a caller can substitute a sandbox.
type Runner interface {
	// TestPackage runs the test suite for a package and reports pass/fail counts
	// plus a coverage percentage.
	TestPackage(ctx context.Context, dir, pkg string) (passed, failed, skipped int, coverage float64, profilePath string, err error)
	// Build compiles a package.
	Build(ctx context.Context, dir, pkg string) error
}

// GoRunner is the real implementation, shelling out to the Go toolchain.
type GoRunner struct {
	// Timeout bounds a single test invocation.
	Timeout time.Duration
	// Env is the environment for spawned commands; nil inherits.
	Env []string
}

// NewGoRunner creates a runner with a sensible per-invocation timeout.
func NewGoRunner() *GoRunner {
	return &GoRunner{Timeout: 10 * time.Minute}
}

func (g *GoRunner) env() []string {
	if g.Env != nil {
		return g.Env
	}
	return os.Environ()
}

func (g *GoRunner) Build(ctx context.Context, dir, pkg string) error {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "build", pkg)
	cmd.Dir = dir
	cmd.Env = g.env()
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build %s: %w: %s", pkg, err, truncate(string(out), 600))
	}
	return nil
}

func (g *GoRunner) timeout() time.Duration {
	if g.Timeout <= 0 {
		return 10 * time.Minute
	}
	return g.Timeout
}

// TestPackage runs the tests for a package with coverage and parses the result.
//
// The pass/fail counts come from parsing `go test` output rather than from the
// exit code alone, because a package with zero tests exits 0 while providing no
// verification at all, and a package whose tests all fail still produces a
// coverage profile worth reporting.
func (g *GoRunner) TestPackage(ctx context.Context, dir, pkg string) (int, int, int, float64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout())
	defer cancel()

	profile := filepath.Join(dir, ".selfmod-coverage.out")
	cmd := exec.CommandContext(ctx, "go", "test", pkg, "-count=1", "-coverprofile="+profile, "-v")
	cmd.Dir = dir
	cmd.Env = g.env()
	output, err := cmd.CombinedOutput()

	passed, failed, skipped := parseTestCounts(string(output))

	coverage := 0.0
	profileWritten := false
	if _, statErr := os.Stat(profile); statErr == nil {
		profileWritten = true
		if prof, perr := ParseCoverageProfile(profile); perr == nil {
			coverage = coverageForPackage(prof, dir, pkg)
		}
	}

	if err != nil && !profileWritten {
		return passed, failed, skipped, 0, "", fmt.Errorf("go test %s: %w: %s",
			pkg, err, truncate(string(output), 600))
	}
	if err != nil {
		// A non-zero exit with a profile on disk still has a story worth telling:
		// this is the path where the model needs the actual compiler message, and
		// returning an error here would discard the output that makes the retry
		// prompt specific.
		return passed, failed, skipped, coverage, profile,
			fmt.Errorf("go test %s: %s", pkg, testFailureDigest(string(output)))
	}

	return passed, failed, skipped, coverage, profile, nil
}

// testFailureDigest extracts the diagnostic lines a model can act on: compiler
// errors and failing assertions, without the surrounding `--- RUN` noise.
func testFailureDigest(output string) string {
	var keep []string
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.Contains(trimmed, ".go:"):
			keep = append(keep, trimmed)
		case strings.HasPrefix(trimmed, "--- FAIL:"):
			keep = append(keep, trimmed)
		case strings.Contains(trimmed, "undefined:"),
			strings.Contains(trimmed, "imported and not used"),
			strings.Contains(trimmed, "declared and not used"),
			strings.Contains(trimmed, "cannot use"),
			strings.Contains(trimmed, "import cycle"),
			strings.Contains(trimmed, "does not implement"),
			strings.Contains(trimmed, "assignment mismatch"),
			strings.Contains(trimmed, "not enough arguments"),
			strings.Contains(trimmed, "too many arguments"):
			keep = append(keep, trimmed)
		}
	}
	if len(keep) == 0 {
		// Nothing recognisable; give the model the tail rather than nothing.
		lines := strings.Split(strings.TrimSpace(output), "\n")
		if len(lines) > 12 {
			lines = lines[len(lines)-12:]
		}
		return truncate(strings.Join(lines, " | "), 500)
	}
	if len(keep) > 12 {
		keep = keep[:12]
	}
	return truncate(strings.Join(keep, " | "), 500)
}

// coverageForPackage aggregates coverage across the files belonging to a package
// directory.
//
// Path matching is done by suffix rather than by stripping components. Go's
// coverage profile uses full import paths for cross-package entries
// ("github.com/kilo/spiral-codemaker/core/state/chain.go") but module-relative
// paths for the package under test ("core/state/chain.go"). Stripping a fixed
// number of leading components matches one and silently misses the other, which
// is how a package with genuinely 94% coverage reported 0%.
func coverageForPackage(prof *CoverageProfile, dir, pkg string) float64 {
	needle := filepath.ToSlash(pkg)
	needle = strings.TrimPrefix(needle, "./")
	needle = strings.TrimSuffix(needle, "/")
	if idx := strings.LastIndex(needle, "/"); idx >= 0 {
		needle = needle[idx+1:]
	}

	var total, covered float64
	for _, f := range prof.Files() {
		rel := filepath.ToSlash(f)
		base := rel
		if idx := strings.LastIndex(rel, "/"); idx >= 0 {
			base = rel[idx+1:]
		}
		// Both layouts are accepted: "<pkg>/<file>.go" and
		// ".../<pkg>/<file>.go".
		dirPart := strings.TrimSuffix(rel, base)
		dirPart = strings.TrimSuffix(strings.TrimSuffix(dirPart, "/"), ".go")

		dirName := dirPart
		if idx := strings.LastIndex(dirPart, "/"); idx >= 0 {
			dirName = dirPart[idx+1:]
		}
		if dirName != needle {
			continue
		}
		total += float64(prof.fileTotals[f])
		covered += float64(prof.fileCovered[f])
	}
	if total == 0 {
		return 0
	}
	return 100 * covered / total
}

// parseTestCounts extracts per-test outcomes from `go test -v` output.
func parseTestCounts(output string) (passed, failed, skipped int) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "--- PASS:"):
			passed++
		case strings.HasPrefix(trimmed, "--- FAIL:"):
			failed++
		case strings.HasPrefix(trimmed, "--- SKIP:"):
			skipped++
		}
	}
	// A build failure produces no per-test lines but is not a pass either.
	if failed == 0 && passed == 0 {
		if strings.Contains(output, "build failed") || strings.Contains(output, "[setup failed]") {
			failed = 1
		}
	}
	return
}

// ---------------------------------------------------------------------------
// Tree management
// ---------------------------------------------------------------------------

// CopyTree copies a source tree, excluding directories that would bloat the copy
// or make the copy build differently from the original.
//
// Excluding these is not an optimisation. Copying a stale `output/` directory
// would let a self-edit appear to pass by testing yesterday's code, and copying
// `.kilo/worktrees` would copy a nested checkout of the whole repository into
// every trial tree.
var excludedDirs = map[string]bool{
	".git": true, "models-cache": true, "third_party": true,
	"instrumentation": true, "node_modules": true, "vendor": true,
	".kilo": true, "tmp": true,
}

// CopyTree copies src to dst.
func CopyTree(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(src, path)
		if rerr != nil {
			return rerr
		}
		if rel == "." {
			return os.MkdirAll(dst, 0o755)
		}

		base := filepath.Base(path)
		if info.IsDir() {
			if excludedDirs[base] || strings.HasPrefix(base, "spiral-output") {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}

		// Never copy model weights or built binaries.
		switch strings.ToLower(filepath.Ext(path)) {
		case ".gguf", ".exe", ".bin", ".safetensors", ".err", ".pid":
			return nil
		}
		// Go build and test artefacts.
		if strings.HasPrefix(base, "coverage.out") || strings.HasSuffix(base, ".test") {
			return nil
		}

		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		return os.WriteFile(filepath.Join(dst, rel), data, info.Mode())
	})
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

// Harness measures a tree and trialled edits against it.
type Harness struct {
	// SystemRoot is the live source tree that proposals are measured against.
	SystemRoot string
	// Runner executes the toolchain.
	Runner Runner
	// ScratchParent holds trial copies.
	ScratchParent string
}

// NewHarness creates a harness.
func NewHarness(systemRoot string, runner Runner) *Harness {
	return &Harness{
		SystemRoot:    systemRoot,
		Runner:        runner,
		ScratchParent: os.TempDir(),
	}
}

// Measure reports the current state of a package in a tree.
func (h *Harness) Measure(ctx context.Context, dir, pkg string) Measurement {
	start := time.Now()
	m := Measurement{Objective: ObjectiveCoverage, Target: pkg}

	if err := h.Runner.Build(ctx, dir, pkg); err != nil {
		m.Err = err.Error()
		m.ElapsedMS = time.Since(start).Milliseconds()
		return m
	}
	m.BuildOK = true

	passed, failed, skipped, coverage, _, err := h.Runner.TestPackage(ctx, dir, pkg)
	m.Passed, m.Failed, m.Skipped, m.CoveragePercent = passed, failed, skipped, coverage
	if err != nil {
		m.Err = err.Error()
	}
	m.ElapsedMS = time.Since(start).Milliseconds()
	return m
}

// BaselineResult carries the trial directory and measurement together.
type BaselineResult struct {
	// Dir is the trial tree; the caller owns its lifetime.
	Dir string
	// Measurement is the observed state of the copied tree.
	Measurement Measurement
	// ProfilePath is the coverage profile inside Dir, needed to build a work
	// list. Without it every exported function looks uncovered.
	ProfilePath string
}

// Baseline copies the system into a trial tree and measures it.
//
// Copying rather than measuring in place is essential: the baseline must be
// observed on the same tree the candidate will occupy, or differences in
// build cache, timestamps, or leftover artefacts will be attributed to the edit.
func (h *Harness) Baseline(ctx context.Context, pkg string) (BaselineResult, error) {
	dir, err := os.MkdirTemp(h.ScratchParent, "selfmod-base-*")
	if err != nil {
		return BaselineResult{}, fmt.Errorf("create trial dir: %w", err)
	}
	if err := CopyTree(h.SystemRoot, dir); err != nil {
		os.RemoveAll(dir)
		return BaselineResult{}, fmt.Errorf("copy system tree: %w", err)
	}

	// Measure against the trial tree so the profile path is valid there.
	m := Measurement{Objective: ObjectiveCoverage, Target: pkg}
	start := time.Now()
	if err := h.Runner.Build(ctx, dir, pkg); err != nil {
		m.Err = err.Error()
		m.ElapsedMS = time.Since(start).Milliseconds()
		return BaselineResult{Dir: dir, Measurement: m}, nil
	}
	m.BuildOK = true

	passed, failed, skipped, coverage, profile, err := h.Runner.TestPackage(ctx, dir, pkg)
	m.Passed, m.Failed, m.Skipped, m.CoveragePercent = passed, failed, skipped, coverage
	if err != nil {
		m.Err = err.Error()
	}
	m.ElapsedMS = time.Since(start).Milliseconds()

	return BaselineResult{Dir: dir, Measurement: m, ProfilePath: profile}, nil
}

// Candidate copies the system fresh, applies the edit, and measures.
//
// A fresh copy rather than a reuse of the baseline tree keeps the two branches
// symmetric: reusing would let the baseline's build cache make the candidate look
// faster and the comparison meaningless.
func (h *Harness) Candidate(ctx context.Context, pkg string, edit func(dir string) error) (string, Measurement, error) {
	dir, err := os.MkdirTemp(h.ScratchParent, "selfmod-cand-*")
	if err != nil {
		return "", Measurement{}, fmt.Errorf("create trial dir: %w", err)
	}
	if err := CopyTree(h.SystemRoot, dir); err != nil {
		os.RemoveAll(dir)
		return "", Measurement{}, fmt.Errorf("copy system tree: %w", err)
	}
	if err := edit(dir); err != nil {
		m := h.Measure(ctx, dir, pkg)
		m.Err = "edit failed to apply: " + err.Error()
		return dir, m, err
	}
	return dir, h.Measure(ctx, dir, pkg), nil
}

// Cleanup removes a trial tree.
func (h *Harness) Cleanup(dir string) {
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
}

// CandidateDir resolves a package spec to a directory inside a trial tree.
//
// A package spec names a directory, not a file, so the whole spec is the
// directory. An earlier version stripped the last path component on the
// assumption it was a file, which resolved "./core/attention" to "core/" --
// silently reading the wrong directory and reporting an empty work list for a
// package that has exported functions.
func CandidateDir(treeRoot, pkg string) string {
	spec := filepath.ToSlash(pkg)
	spec = strings.TrimSuffix(spec, "/")

	full := filepath.Join(treeRoot, filepath.FromSlash(spec))
	return filepath.Clean(full)
}

// PackageName extracts the import-path element of a package spec.
func PackageName(pkg string) string {
	trimmed := strings.TrimSuffix(filepath.ToSlash(pkg), "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return trimmed[idx+1:]
	}
	return trimmed
}

// SourceFiles lists the Go files belonging to a package in a tree, relative to
// the tree root and using forward slashes.
func (h *Harness) SourceFiles(treeRoot, pkg string) ([]string, error) {
	dir := CandidateDir(treeRoot, pkg)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read package dir %s: %w", dir, err)
	}

	rel, err := filepath.Rel(treeRoot, dir)
	if err != nil {
		rel = "."
	}
	rel = filepath.ToSlash(rel)

	var out []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") {
			continue
		}
		// Never propose tests for a file that is already a test.
		if strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		out = append(out, rel+"/"+e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// WorkList returns the exported functions in a package that no test exercises.
//
// The coverage profile from the baseline run is required: without it every
// exported function looks uncovered and the work list is the entire API.
func (h *Harness) WorkList(treeRoot, pkg, profilePath string) ([]UncoveredFunc, error) {
	var profile *CoverageProfile
	if profilePath != "" {
		p, err := ParseCoverageProfile(profilePath)
		if err == nil {
			profile = p
		}
	}

	files, err := h.SourceFiles(treeRoot, pkg)
	if err != nil {
		return nil, err
	}

	var work []UncoveredFunc
	for _, rel := range files {
		abs := filepath.Join(treeRoot, filepath.FromSlash(rel))
		funcs, ferr := FindUncoveredFuncs(abs, profile)
		if ferr != nil {
			continue
		}
		// Re-anchor to the tree-relative path. FindUncoveredFuncs records the path
		// it was given, which is absolute here, and an absolute path pointing into
		// a trial tree cannot be re-read against the live repository -- the
		// proposal step would silently find no source for every function.
		for i := range funcs {
			funcs[i].File = rel
		}
		work = append(work, funcs...)
	}
	return work, nil
}

// FormatCounts renders a measurement compactly.
func FormatCounts(m Measurement) string {
	return fmt.Sprintf("build=%v pass=%d fail=%d skip=%d cov=%.1f%%",
		m.BuildOK, m.Passed, m.Failed, m.Skipped, m.CoveragePercent)
}

// ParseUint is a small helper for callers reading counts from output.
func ParseUint(s string) (int, error) {
	return strconv.Atoi(strings.TrimSpace(s))
}
