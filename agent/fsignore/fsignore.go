// Package fsignore is the shared path filter for workspace traversal.
//
// It exists as its own package because two consumers need identical behaviour and
// neither can import the other: the tool layer needs it to bound searches, and the
// verification layer needs it to decide which files exist. Two copies of this logic
// would drift, and they demonstrably had: the tool layer ignored what the detection
// layer counted, so a workspace scan reported 460 files that a search would never
// look at.
package fsignore

import (
	"path/filepath"
	"strings"
)

// Matcher reports whether a path should be skipped.
type Matcher interface {
	Match(rel string, isDir bool) bool
	// Patterns returns the configured patterns, for passing to another component.
	Patterns() []string
}

// MatcherFunc adapts a function to Matcher.
type MatcherFunc func(rel string, isDir bool) bool

// Match implements Matcher.
func (f MatcherFunc) Match(rel string, isDir bool) bool { return f(rel, isDir) }

// Patterns implements Matcher; a function matcher has no patterns to report.
func (f MatcherFunc) Patterns() []string { return nil }

// Rules is a pattern-based matcher.
type Rules struct {
	exact    map[string]bool
	prefixes []string
	suffixes []string
	exts     []string
	patterns []string
}

// DefaultPatterns is the set ignored unless the configuration overrides it.
//
// The list is built from what a real workspace accumulates: version-control
// metadata, dependency directories, build output, model weights, and the previous
// run's own artefacts. Ignoring the agent's own log directory matters most — an
// agent that searches its own logs will find its previous attempts and act on
// them.
var DefaultPatterns = []string{
	".git",
	".hg",
	".svn",
	".kilo",
	".agent/logs",
	"node_modules",
	"vendor",
	"target",
	"dist",
	"build",
	"out",
	"__pycache__",
	".venv",
	"venv",
	".mypy_cache",
	".pytest_cache",
	".ruff_cache",
	".tox",
	".gradle",
	".idea",
	".vscode",
	"coverage",
	"testdata",
	"instrumentation",
	"spiral-output*",
	"models-cache",
	"third_party",
	"*.gguf",
	"*.safetensors",
	"*.bin",
	"*.exe",
	"*.dll",
	"*.so",
	"*.dylib",
	"*.zip",
	"*.tar",
	"*.gz",
	"*.7z",
	"*.png",
	"*.jpg",
	"*.jpeg",
	"*.gif",
	"*.ico",
	"*.pdf",
	"*.mp4",
	"*.mp3",
	"*.wav",
	"*.db",
	"*.sqlite",
	"*.pyc",
	"*.pyo",
}

// New builds a matcher from glob patterns.
//
// Three forms are recognised: a bare name or path (exact match, also matching as a
// directory prefix), a leading-dot-star extension pattern such as *.go, and a
// trailing-star prefix pattern such as spiral-output*. The trailing form exists
// because generated output directories are conventionally named with a suffix
// (spiral-output-test, spiral-output-http) and listing each one does not scale.
func New(patterns []string) *Rules {
	r := &Rules{exact: map[string]bool{}}
	for _, raw := range patterns {
		g := strings.TrimSpace(filepath.ToSlash(raw))
		if g == "" {
			continue
		}
		r.patterns = append(r.patterns, g)
		switch {
		case strings.HasPrefix(g, "*.") && !strings.Contains(g, "/"):
			r.exts = append(r.exts, strings.ToLower(g[1:]))
		case strings.HasSuffix(g, "*") && !strings.HasPrefix(g, "*"):
			r.prefixes = append(r.prefixes, strings.ToLower(strings.TrimSuffix(g, "*")))
		case strings.Contains(g, "*") || strings.Contains(g, "?"):
			r.exact[g] = true
		default:
			r.exact[g] = true
		}
	}
	return r
}

// Patterns returns the configured patterns.
func (r *Rules) Patterns() []string { return append([]string{}, r.patterns...) }

// Match reports whether a workspace-relative path is ignored.
func (r *Rules) Match(rel string, isDir bool) bool {
	if r == nil || rel == "" || rel == "." {
		return false
	}
	rel = filepath.ToSlash(rel)
	lower := strings.ToLower(rel)
	base := lower
	if i := strings.LastIndex(lower, "/"); i >= 0 {
		base = lower[i+1:]
	}

	// Version-control metadata is always skipped, whatever the configuration says.
	// A search that descends into .git finds commit objects as if they were source.
	if base == ".git" {
		return true
	}

	for e := range r.exact {
		if lower == e || base == e || strings.HasPrefix(lower, strings.TrimSuffix(e, "/")+"/") {
			return true
		}
	}
	for _, p := range r.prefixes {
		if strings.HasPrefix(base, p) {
			return true
		}
		// A prefix pattern may also name a path segment at any depth.
		if strings.Contains(lower, "/"+p) {
			return true
		}
	}
	if !isDir {
		ext := strings.ToLower(filepath.Ext(lower))
		for _, e := range r.exts {
			if ext == e {
				return true
			}
		}
	}
	return false
}

// Default returns a matcher for the default pattern set.
func Default() *Rules { return New(DefaultPatterns) }