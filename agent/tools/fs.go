package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Workspace is the sandbox boundary every filesystem tool resolves paths against.
type Workspace struct {
	Root string
	// MaxReadBytes caps a single read. A model given a 40 MB file has no use for
	// it, and it would displace everything else from the context.
	MaxReadBytes int
	// WriteMode is the permission bits used for created files.
	WriteMode os.FileMode
}

// NewWorkspace builds a workspace rooted at a directory.
func NewWorkspace(root string) *Workspace {
	return &Workspace{Root: root, MaxReadBytes: 256 * 1024, WriteMode: 0o644}
}

// Resolve turns a possibly-relative path into an absolute one inside the
// workspace, refusing anything that escapes.
//
// The check is on the cleaned absolute path rather than on the literal string, so
// `a/../../etc/passwd` and an absolute path outside the root are both caught.
// Symlinks are resolved too: a link inside the workspace pointing at a file
// outside it would otherwise be a complete bypass.
func (w *Workspace) Resolve(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	p := path
	if !filepath.IsAbs(p) {
		p = filepath.Join(w.Root, p)
	}
	p = filepath.Clean(p)

	absRoot, err := filepath.Abs(w.Root)
	if err != nil {
		return "", err
	}
	absRoot = filepath.Clean(absRoot)

	rel, err := filepath.Rel(absRoot, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q resolves outside the workspace %s", path, absRoot)
	}

	// Follow symlinks when the target exists. A dangling link cannot be resolved
	// and is judged by its literal location, which is the best available answer.
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		rootResolved := absRoot
		if rr, err := filepath.EvalSymlinks(absRoot); err == nil {
			rootResolved = rr
		}
		rel, err = filepath.Rel(rootResolved, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("path %q is a link to %s, outside the workspace %s", path, resolved, absRoot)
		}
	}
	return p, nil
}

// Rel renders a path relative to the workspace.
func (w *Workspace) Rel(path string) string {
	if rel, err := filepath.Rel(w.Root, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
}

// ReadFileTool reads a file with line numbers.
type ReadFileTool struct {
	WS *Workspace
}

func (t *ReadFileTool) Name() string        { return "read_file" }
func (t *ReadFileTool) Mutates() bool       { return false }
func (t *ReadFileTool) Modes() []string     { return nil }
func (t *ReadFileTool) Description() string {
	return "Read a UTF-8 text file from the workspace, with line numbers. Use start_line and end_line for large files."
}
func (t *ReadFileTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Required: true, Description: "Workspace-relative or absolute path inside the workspace"},
		{Name: "start_line", Type: "integer", Description: "1-based first line to return"},
		{Name: "end_line", Type: "integer", Description: "1-based last line, inclusive"},
	}
}

func (t *ReadFileTool) Run(ctx context.Context, args Args) Result {
	pathArg := args.Str("path")
	if pathArg == "" {
		return FailMsg(t.Name(), "path is required")
	}
	p, err := t.WS.Resolve(pathArg)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}

	st, err := os.Stat(p)
	if err != nil {
		return FailMsg(t.Name(), fmt.Sprintf("cannot stat %s: %v", t.WS.Rel(p), err))
	}
	if st.IsDir() {
		return FailMsg(t.Name(), fmt.Sprintf("%s is a directory; use list_directory", t.WS.Rel(p)))
	}
	if st.Size() > int64(t.WS.MaxReadBytes)*4 {
		return Result{
			Tool: t.Name(), Success: false,
			Error: fmt.Sprintf("%s is %d bytes, larger than the %d byte read limit; read it in ranges or use search_code",
				t.WS.Rel(p), st.Size(), t.WS.MaxReadBytes),
		}
	}

	data, err := os.ReadFile(p)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	content := string(data)

	totalLines := strings.Count(content, "\n") + 1
	start := args.Int("start_line", 1)
	end := args.Int("end_line", 0)
	if start < 1 {
		start = 1
	}
	if end < 1 || end > totalLines {
		end = totalLines
	}

	lines := strings.Split(content, "\n")
	if start > len(lines) {
		return Result{Tool: t.Name(), Success: false,
			Error: fmt.Sprintf("start_line %d is past the end of the file (%d lines)", start, totalLines)}
	}
	if end > len(lines) {
		end = len(lines)
	}

	var b strings.Builder
	for i := start; i <= end; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i, lines[i-1])
	}
	out := b.String()
	truncated := false
	if len(out) > t.WS.MaxReadBytes {
		out = out[:t.WS.MaxReadBytes]
		truncated = true
	}

	return Result{
		Tool: t.Name(), Success: true, Stdout: out, Truncated: truncated,
		Data: map[string]any{
			"path":        t.WS.Rel(p),
			"total_lines": totalLines,
			"returned":    fmt.Sprintf("%d-%d", start, end),
			"size_bytes":  st.Size(),
		},
	}
}

// WriteFileTool creates or replaces a file.
type WriteFileTool struct {
	WS      *Workspace
	Content []byte
}

// SetContent supplies content from outside the argument map, which is how the
// patch applier and the scripted executor reuse this writer.
func (t *WriteFileTool) SetContent(c []byte) { t.Content = c }

func (t *WriteFileTool) Name() string  { return "write_file" }
func (t *WriteFileTool) Mutates() bool { return true }
func (t *WriteFileTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeRefactor}
}
func (t *WriteFileTool) Description() string {
	return "Create a new file or replace an existing file's entire contents. Prefer edit_file for changes to existing files."
}
func (t *WriteFileTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Required: true, Description: "Workspace-relative path"},
		{Name: "content", Type: "string", Required: true, Description: "Full file content to write"},
	}
}

func (t *WriteFileTool) Run(ctx context.Context, args Args) Result {
	pathArg := args.Str("path")
	if pathArg == "" {
		return FailMsg(t.Name(), "path is required")
	}
	p, err := t.WS.Resolve(pathArg)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	content := args.Str("content")
	if content == "" && args.Has("content") {
		content = args.Str("content")
	}
	if len(t.Content) > 0 {
		content = string(t.Content)
	}

	existed := false
	if _, err := os.Stat(p); err == nil {
		existed = true
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if err := os.WriteFile(p, []byte(content), t.WS.WriteMode); err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	verb := "created"
	if existed {
		verb = "replaced"
	}
	return Result{
		Tool: t.Name(), Success: true, Changed: true,
		Stdout: fmt.Sprintf("%s %s (%d bytes)", verb, t.WS.Rel(p), len(content)),
		Data:   map[string]any{"path": t.WS.Rel(p), "created": !existed, "bytes": len(content)},
	}
}

// EditFileTool performs a search-and-replace edit, which is safer than a full
// rewrite because it cannot silently discard unrelated content.
type EditFileTool struct{ WS *Workspace }

func (t *EditFileTool) Name() string  { return "edit_file" }
func (t *EditFileTool) Mutates() bool { return true }
func (t *EditFileTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeRefactor}
}
func (t *EditFileTool) Description() string {
	return "Replace an exact snippet in a file. old_text must appear exactly; use replace_all for repeated occurrences."
}
func (t *EditFileTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Required: true, Description: "Workspace-relative path"},
		{Name: "old_text", Type: "string", Required: true, Description: "Exact text to find, including indentation"},
		{Name: "new_text", Type: "string", Required: true, Description: "Replacement text"},
		{Name: "replace_all", Type: "boolean", Description: "Replace every occurrence instead of only the first"},
	}
}

func (t *EditFileTool) Run(ctx context.Context, args Args) Result {
	pathArg := args.Str("path")
	oldText := args.Str("old_text")
	newText := args.Str("new_text")
	if pathArg == "" || oldText == "" {
		return FailMsg(t.Name(), "path and old_text are required; old_text must not be empty")
	}
	p, err := t.WS.Resolve(pathArg)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	content := string(data)

	count := strings.Count(content, oldText)
	if count == 0 {
		return Result{
			Tool: t.Name(), Success: false,
			Error: fmt.Sprintf("old_text not found in %s. The file has %d lines; re-read it and copy the exact text including indentation.",
				t.WS.Rel(p), strings.Count(content, "\n")+1),
			Hint: "read the file again and use the exact current text",
		}
	}
	if count > 1 && !args.Bool("replace_all", false) {
		return Result{
			Tool: t.Name(), Success: false,
			Error: fmt.Sprintf("old_text occurs %d times in %s. Add surrounding context to make it unique, or set replace_all.",
				count, t.WS.Rel(p)),
		}
	}

	var updated string
	if args.Bool("replace_all", false) {
		updated = strings.ReplaceAll(content, oldText, newText)
	} else {
		updated = strings.Replace(content, oldText, newText, 1)
	}
	if err := os.WriteFile(p, []byte(updated), t.WS.WriteMode); err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	replaced := count
	if !args.Bool("replace_all", false) {
		replaced = 1
	}
	return Result{
		Tool: t.Name(), Success: true, Changed: true,
		Stdout: fmt.Sprintf("edited %s (%d replacement(s), %d -> %d bytes)",
			t.WS.Rel(p), replaced, len(content), len(updated)),
		Data: map[string]any{"path": t.WS.Rel(p), "replacements": replaced},
	}
}

// ListDirectoryTool lists a directory.
type ListDirectoryTool struct {
	WS      *Workspace
	MaxShow int
}

func (t *ListDirectoryTool) Name() string  { return "list_directory" }
func (t *ListDirectoryTool) Mutates() bool { return false }
func (t *ListDirectoryTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeReview, ModeRefactor, ModeExplain}
}
func (t *ListDirectoryTool) Description() string {
	return "List the entries of a directory with size and modification time."
}
func (t *ListDirectoryTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Description: "Directory to list; defaults to the workspace root"},
		{Name: "max_entries", Type: "integer", Description: "Maximum entries to return"},
	}
}

func (t *ListDirectoryTool) Run(ctx context.Context, args Args) Result {
	pathArg := args.StrDefault("path", ".")
	p, err := t.WS.Resolve(pathArg)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	entries, err := os.ReadDir(p)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	maxShow := t.MaxShow
	if v := args.Int("max_entries", 0); v > 0 && (maxShow == 0 || v < maxShow) {
		maxShow = v
	}
	if maxShow == 0 {
		maxShow = 300
	}

	type row struct {
		name string
		dir  bool
		size int64
		mod  time.Time
	}
	var rows []row
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		rows = append(rows, row{name: e.Name(), dir: e.IsDir(), size: info.Size(), mod: info.ModTime()})
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].dir != rows[j].dir {
			return rows[i].dir
		}
		return strings.ToLower(rows[i].name) < strings.ToLower(rows[j].name)
	})

	var b strings.Builder
	dirs, files := 0, 0
	shown := 0
	for _, r := range rows {
		if shown >= maxShow {
			fmt.Fprintf(&b, "... %d more entries not shown\n", len(rows)-shown)
			break
		}
		kind := "file"
		if r.dir {
			kind = "dir "
			dirs++
		} else {
			files++
		}
		fmt.Fprintf(&b, "%s  %10d  %s  %s\n", kind, r.size, r.mod.Format("2006-01-02 15:04"), r.name)
		shown++
	}

	return Result{
		Tool: t.Name(), Success: true, Stdout: b.String(),
		Data: map[string]any{
			"path":  t.WS.Rel(p),
			"dirs":  dirs,
			"files": files,
			"total": len(rows),
		},
	}
}