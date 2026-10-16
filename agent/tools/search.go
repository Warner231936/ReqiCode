package tools

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
)

// SearchCodeTool performs lexical content search.
//
// A native walker is used rather than shelling out to ripgrep, for three reasons:
// ripgrep is not on PATH on this machine, so depending on it would make search
// silently unavailable; a subprocess per query costs more than a direct walk for
// a single workspace; and the native path lets search share the same ignore rules
// and workspace boundary as every other tool. When ripgrep *is* on PATH, UseRipgrep
// reports that it is available so an operator knows a faster path exists.
type SearchCodeTool struct {
	WS         *Workspace
	Ignore     fsignore.Matcher
	MaxResults int
	MaxFileBytes int64
	// UseRipgrep reports whether ripgrep was detected.
	UseRipgrep bool
	Binary    string
}

func (t *SearchCodeTool) Name() string  { return "search_code" }
func (t *SearchCodeTool) Mutates() bool { return false }
func (t *SearchCodeTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeTest, ModeReview, ModeRefactor, ModeExplain}
}
func (t *SearchCodeTool) Description() string {
	return "Search file contents for a regular expression. Returns file:line matches with surrounding context. This is the primary way to locate relevant code."
}
func (t *SearchCodeTool) Params() []Param {
	return []Param{
		{Name: "query", Type: "string", Required: true, Description: "Regular expression to search for"},
		{Name: "path", Type: "string", Description: "File or directory to search; defaults to the workspace root"},
		{Name: "glob", Type: "string", Description: "Only search files whose name matches this glob, e.g. *.go"},
		{Name: "max_results", Type: "integer", Description: "Maximum matches to return"},
		{Name: "case_insensitive", Type: "boolean", Description: "Case-insensitive search"},
		{Name: "context_lines", Type: "integer", Description: "Lines of context around each match"},
	}
}

func (t *SearchCodeTool) Run(ctx context.Context, args Args) Result {
	query := args.Str("query")
	if query == "" {
		return FailMsg(t.Name(), "query is required")
	}

	pattern := query
	flags := ""
	if args.Bool("case_insensitive", false) {
		flags = "(?i)"
	}
	re, err := regexp.Compile(flags + pattern)
	if err != nil {
		return FailMsg(t.Name(), fmt.Sprintf("invalid regular expression %q: %v", query, err))
	}

	root := t.WS.Root
	if p := args.Str("path"); p != "" {
		abs, err := t.WS.Resolve(p)
		if err != nil {
			return FailMsg(t.Name(), err.Error())
		}
		root = abs
	}
	glob := args.Str("glob")
	maxResults := args.Int("max_results", t.MaxResults)
	if maxResults <= 0 {
		maxResults = 60
	}
	contextLines := args.Int("context_lines", 1)
	maxFileBytes := t.MaxFileBytes
	if maxFileBytes <= 0 {
		maxFileBytes = 2 << 20
	}

	var matches []codeMatch
	truncated := false

	walkErr := filepath.Walk(root, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := t.WS.Rel(path)
		if info.IsDir() {
			if t.Ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.Ignore.Match(rel, false) {
			return nil
		}
		if glob != "" && !matchGlob(info.Name(), glob) {
			return nil
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		if isBinaryName(info.Name()) {
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer f.Close()

		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 0, 64*1024), int(maxFileBytes))
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			line := scanner.Text()
			if !re.MatchString(line) {
				continue
			}
			matches = append(matches, codeMatch{path: rel, line: lineNo, text: strings.TrimRight(line, "\r")})
			if len(matches) > maxResults*4 {
				truncated = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	if walkErr != nil && walkErr != filepath.SkipAll {
		return FailMsg(t.Name(), walkErr.Error())
	}

	// Group by file so a single hot file cannot consume the entire result budget,
	// which is how a search for a common token degenerates into one file's contents.
	rank, files := rankByFile(matches, maxResults)
	truncated = truncated || len(matches) > len(rank)

	var b strings.Builder
	for _, m := range rank {
		fmt.Fprintf(&b, "%s:%d:%s\n", m.path, m.line, m.text)
		if contextLines > 0 {
			fmt.Fprintf(&b, "    (context: use read_file %s start_line=%d end_line=%d)\n",
				m.path, max(1, m.line-contextLines), m.line+contextLines)
		}
	}
	if len(rank) == 0 {
		b.WriteString("no matches\n")
	}

	hint := fmt.Sprintf("%d matches across %d file(s)", len(rank), files)
	if t.UseRipgrep {
		hint += " (ripgrep is available at " + t.Binary + " and would be faster for very large trees)"
	}
	return Result{
		Tool: t.Name(), Success: true, Stdout: b.String(),
		Truncated: truncated, Hint: hint,
		Data: map[string]any{"matches": len(rank), "files": files, "total_found": len(matches)},
	}
}

// codeMatch is one lexical search hit.
type codeMatch struct {
	path  string
	line  int
	text  string
	score float64
}

// rankByFile limits the number of matches returned from any one file.
//
// The per-file cap matters more than the total cap: a search for a word that
// appears hundreds of times in one file would otherwise fill the entire result
// set with that file and hide every other candidate.
func rankByFile(matches []codeMatch, maxResults int) ([]codeMatch, int) {
	perFile := map[string]int{}
	const maxPerFile = 8

	out := make([]codeMatch, 0, len(matches))
	files := map[string]bool{}

	for _, m := range matches {
		if perFile[m.path] >= maxPerFile {
			continue
		}
		perFile[m.path]++
		files[m.path] = true
		out = append(out, m)
		if len(out) >= maxResults {
			break
		}
	}
	return out, len(files)
}

// SearchFilesTool finds files by name.
type SearchFilesTool struct {
	WS         *Workspace
	Ignore     fsignore.Matcher
	MaxResults int
}

func (t *SearchFilesTool) Name() string  { return "search_files" }
func (t *SearchFilesTool) Mutates() bool { return false }
func (t *SearchFilesTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeTest, ModeReview, ModeRefactor, ModeExplain}
}
func (t *SearchFilesTool) Description() string {
	return "Find files and directories by name pattern. Use this to locate a file when you know roughly what it is called."
}
func (t *SearchFilesTool) Params() []Param {
	return []Param{
		{Name: "query", Type: "string", Required: true, Description: "Name pattern; supports * and ? wildcards"},
		{Name: "path", Type: "string", Description: "Directory to search; defaults to the workspace root"},
		{Name: "kind", Type: "string", Enum: []string{"any", "file", "dir"}, Default: "any", Description: "Restrict to files or directories"},
	}
}

func (t *SearchFilesTool) Run(ctx context.Context, args Args) Result {
	query := args.Str("query")
	if query == "" {
		return FailMsg(t.Name(), "query is required")
	}
	root := t.WS.Root
	if p := args.Str("path"); p != "" {
		abs, err := t.WS.Resolve(p)
		if err != nil {
			return FailMsg(t.Name(), err.Error())
		}
		root = abs
	}
	kind := args.StrDefault("kind", "any")
	maxResults := t.MaxResults
	if maxResults <= 0 {
		maxResults = 100
	}

	var found []string
	_ = filepath.Walk(root, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		rel := t.WS.Rel(path)
		if info.IsDir() {
			if t.Ignore.Match(rel, true) {
				return filepath.SkipDir
			}
		} else if t.Ignore.Match(rel, false) {
			return nil
		}
		if !matchGlob(info.Name(), query) && !matchGlob(rel, query) {
			return nil
		}
		switch kind {
		case "file":
			if info.IsDir() {
				return nil
			}
		case "dir":
			if !info.IsDir() {
				return nil
			}
		}
		size := int64(0)
		if !info.IsDir() {
			size = info.Size()
		}
		found = append(found, fmt.Sprintf("%-10s %8d  %s", dirKind(info), size, rel))
		if len(found) >= maxResults {
			return filepath.SkipAll
		}
		return nil
	})

	sort.Strings(found)
	out := strings.Join(found, "\n")
	if out == "" {
		out = "no matching files"
	}
	return Result{
		Tool: t.Name(), Success: true, Stdout: out,
		Hint: fmt.Sprintf("%d matches", len(found)),
		Data:  map[string]any{"matches": len(found)},
	}
}

func dirKind(info fs.FileInfo) string {
	if info.IsDir() {
		return "dir"
	}
	return "file"
}

// FindSymbolTool locates a symbol definition or reference.
type FindSymbolTool struct {
	WS     *Workspace
	Ignore fsignore.Matcher
}

func (t *FindSymbolTool) Name() string  { return "find_symbol" }
func (t *FindSymbolTool) Mutates() bool { return false }
func (t *FindSymbolTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeReview, ModeRefactor, ModeExplain}
}
func (t *FindSymbolTool) Description() string {
	return "Locate where a symbol is defined or referenced. Tries language-aware definition patterns first, then falls back to a plain text search."
}
func (t *FindSymbolTool) Params() []Param {
	return []Param{
		{Name: "symbol", Type: "string", Required: true, Description: "Identifier to find"},
		{Name: "path", Type: "string", Description: "Directory to search; defaults to the workspace root"},
	}
}

func (t *FindSymbolTool) Run(ctx context.Context, args Args) Result {
	symbol := args.Str("symbol")
	if symbol == "" {
		return FailMsg(t.Name(), "symbol is required")
	}
	root := t.WS.Root
	if p := args.Str("path"); p != "" {
		abs, err := t.WS.Resolve(p)
		if err != nil {
			return FailMsg(t.Name(), err.Error())
		}
		root = abs
	}

	// A word boundary keeps a search for Add from matching ReadAddress, which is
	// the single largest source of noise in symbol lookup.
	word := regexp.MustCompile(`\b` + regexp.QuoteMeta(symbol) + `\b`)
	defPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?m)^func\s+(\([^)]*\)\s*)?` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*(pub\s+)?(async\s+)?fn\s+` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*(def|class|async def)\s+` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*(export\s+)?(async\s+)?function\s+` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*(public|private|protected|static|final|abstract).*\b` + regexp.QuoteMeta(symbol) + `\s*\(`),
		regexp.MustCompile(`(?m)^\s*(public|private|protected)\s+(class|interface|struct|enum)\s+` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*(struct|type|interface|const|var)\s+` + regexp.QuoteMeta(symbol) + `\b`),
		regexp.MustCompile(`(?m)^\s*impl(?:<[^>]*>)?\s+` + regexp.QuoteMeta(symbol) + `\b`),
	}

	type hit struct {
		path string
		line int
		text string
		kind string
	}
	var defs, refs []hit

	_ = filepath.Walk(root, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		rel := t.WS.Rel(path)
		if info.IsDir() {
			if t.Ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if t.Ignore.Match(rel, false) || isBinaryName(info.Name()) || info.Size() > 2<<20 {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			text := strings.TrimRight(line, "\r")
			isDef := false
			for _, p := range defPatterns {
				if p.MatchString(text) {
					isDef = true
					break
				}
			}
			if isDef {
				defs = append(defs, hit{rel, i + 1, text, "definition"})
			} else if word.MatchString(text) && len(refs) < 200 {
				refs = append(refs, hit{rel, i + 1, text, "reference"})
			}
		}
		return nil
	})

	var b strings.Builder
	if len(defs) == 0 {
		b.WriteString("no definition matched language-aware patterns; showing references\n")
	} else {
		b.WriteString("definitions:\n")
		for _, h := range defs {
			fmt.Fprintf(&b, "  %s:%d: %s\n", h.path, h.line, strings.TrimSpace(h.text))
		}
	}
	shown := refs
	if len(shown) > 30 {
		shown = shown[:30]
	}
	b.WriteString("references:\n")
	for _, h := range shown {
		fmt.Fprintf(&b, "  %s:%d: %s\n", h.path, h.line, strings.TrimSpace(h.text))
	}
	if len(refs) > len(shown) {
		fmt.Fprintf(&b, "  ... %d more references\n", len(refs)-len(shown))
	}

	return Result{
		Tool: t.Name(), Success: true, Stdout: b.String(),
		Hint: fmt.Sprintf("%d definition(s), %d reference(s)", len(defs), len(refs)),
		Data: map[string]any{"definitions": len(defs), "references": len(refs)},
	}
}

// matchGlob matches a shell-style glob against a name.
func matchGlob(name, pattern string) bool {
	if pattern == "" {
		return true
	}
	if !strings.ContainsAny(pattern, "*?") {
		return strings.EqualFold(name, pattern)
	}
	ok, err := filepath.Match(strings.ToLower(pattern), strings.ToLower(name))
	if err != nil {
		return false
	}
	return ok
}

func isBinaryName(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".gguf", ".safetensors", ".bin", ".dll", ".exe", ".zip", ".tar", ".gz", ".7z",
		".png", ".jpg", ".jpeg", ".gif", ".ico", ".pdf", ".mp4", ".mp3", ".wav",
		".db", ".sqlite", ".so", ".dylib", ".class", ".jar", ".pdb", ".woff", ".woff2":
		return true
	}
	return false
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}