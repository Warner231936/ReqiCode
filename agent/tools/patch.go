package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Patch is a unified diff applied to a workspace.
type Patch struct {
	WS *Workspace
}

// PatchResult reports what a patch changed.
type PatchResult struct {
	Applied  []string         `json:"applied"`
	Rejected []PatchRejection `json:"rejected"`
	DryRun   bool             `json:"dry_run"`
}

// PatchRejection explains why a hunk was not applied.
type PatchRejection struct {
	File   string `json:"file"`
	Hunk   int    `json:"hunk"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
	Expected string `json:"expected,omitempty"`
	Actual   string `json:"actual,omitempty"`
}

// Error renders the first rejection as a message for the model.
func (r PatchRejection) Error() string {
	msg := fmt.Sprintf("%s hunk %d (line %d) rejected: %s", r.File, r.Hunk, r.Line, r.Reason)
	if r.Expected != "" {
		msg += fmt.Sprintf("\n  expected: %s\n  actual:   %s", r.Expected, r.Actual)
	}
	return msg
}

func (t *PatchTool) Name() string  { return "apply_patch" }
func (t *PatchTool) Mutates() bool { return true }
func (t *PatchTool) Modes() []string {
	return []string{ModeImplement, ModeDebug, ModeRefactor}
}
func (t *PatchTool) Description() string {
	return "Apply a unified diff to one or more files. Hunks that do not match the current content are rejected with the exact mismatch, and nothing is written for a rejected file."
}
func (t *PatchTool) Params() []Param {
	return []Param{
		{Name: "diff", Type: "string", Required: true, Description: "Unified diff with ---/+++ headers and @@ hunks"},
		{Name: "dry_run", Type: "boolean", Description: "Validate without writing"},
	}
}

func (t *PatchTool) Run(ctx context.Context, args Args) Result {
	diff := args.Str("diff")
	if diff == "" {
		return FailMsg(t.Name(), "diff is required")
	}
	dry := args.Bool("dry_run", false)
	res, err := t.WS2Apply(diff, dry)
	if err != nil {
		return Result{Tool: t.Name(), Success: false, Error: err.Error(), Class: "patch_conflict",
			Data: map[string]any{"rejected": res.Rejected, "applied": res.Applied}}
	}
	return Result{
		Tool: t.Name(), Success: true, Changed: !dry && len(res.Applied) > 0,
		Stdout: fmt.Sprintf("%s %d file(s): %s", verbFor(dry), len(res.Applied), strings.Join(res.Applied, ", ")),
		Hint:   "re-read the changed files and run verification before reporting success",
		Data:   map[string]any{"applied": res.Applied, "dry_run": dry},
	}
}

func verbFor(dry bool) string {
	if dry {
		return "validated"
	}
	return "applied patch to"
}

// WS2Apply applies a unified diff.
//
// Validation is all-or-nothing per file: a file is rewritten only once every one
// of its hunks has matched. A partially applied file is the worst outcome of all,
// because the file no longer compiles and no hunk explains why.
func (t *PatchTool) WS2Apply(diff string, dry bool) (PatchResult, error) {
	var res PatchResult
	res.DryRun = dry

	files, err := parseUnifiedDiff(diff)
	if err != nil {
		return res, err
	}
	if len(files) == 0 {
		return res, fmt.Errorf("the diff contains no file headers; expected lines beginning with --- and +++")
	}

	// Validate everything first, so a rejection in the last file cannot leave the
	// first file already written.
	type staged struct {
		path string
		body string
	}
	var toWrite []staged

	for _, f := range files {
		rel := f.Path
		abs, err := t.WS.Resolve(rel)
		if err != nil {
			return res, fmt.Errorf("patch target rejected: %v", err)
		}

		original := ""
		if data, err := os.ReadFile(abs); err == nil {
			original = string(data)
		} else if !os.IsNotExist(err) {
			return res, fmt.Errorf("read %s: %w", rel, err)
		}

		if f.IsDelete {
			if original == "" {
				res.Rejected = append(res.Rejected, PatchRejection{File: rel,
					Reason: "cannot delete a file that does not exist"})
				continue
			}
			res.Rejected = append(res.Rejected, PatchRejection{File: rel,
				Reason: "deletion requires explicit approval; use the delete_file tool so the action is auditable"})
			continue
		}

		lines := splitLines(original)
		var rejections []PatchRejection

		// Apply from the bottom up so earlier hunk line numbers stay valid after
		// later hunks change the line count.
		for i := len(f.Hunks) - 1; i >= 0; i-- {
			h := f.Hunks[i]
			offset := h.StartLine - 1
			if offset < 0 {
				offset = 0
			}
			// Allow a small search window for the hunk start. A model computing line
			// numbers from a context window it saw a moment ago will be off by a few
			// lines often enough that refusing to search would reject valid patches.
			window := findHunk(lines, h, offset, 40)
			if window < 0 {
				rej := PatchRejection{
					File: rel, Hunk: h.Index, Line: h.StartLine,
					Reason: "hunk context does not match the current file content",
				}
				if len(h.Context) > 0 && h.StartLine-1 < len(lines) {
					rej.Expected = strings.Join(append(append([]string{}, h.Context...), h.Removed...), " ")
					rej.Actual = strings.Join(lines[h.StartLine-1:minInt(len(lines), h.StartLine-1+len(h.Context))], " ")
				}
				rejections = append(rejections, rej)
				continue
			}
			lines = spliceLines(lines, window, len(h.Removed), h.Added)
		}

		if len(rejections) > 0 {
			res.Rejected = append(res.Rejected, rejections...)
			continue
		}
		toWrite = append(toWrite, staged{path: abs, body: strings.Join(lines, "\n")})
		res.Applied = append(res.Applied, rel)
	}

	if len(res.Rejected) > 0 && len(res.Applied) == 0 {
		return res, fmt.Errorf("no hunk applied:\n%s", joinRejections(res.Rejected))
	}
	if len(res.Rejected) > 0 {
		return res, fmt.Errorf("patch partially rejected; nothing was written:\n%s", joinRejections(res.Rejected))
	}

	if !dry {
		for _, s := range toWrite {
			if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
				return res, err
			}
			if err := os.WriteFile(s.path, []byte(s.body), 0o644); err != nil {
				return res, fmt.Errorf("write %s: %w", t.WS.Rel(s.path), err)
			}
		}
	}
	return res, nil
}

// PatchTool applies unified diffs.
type PatchTool struct{ WS *Workspace }

func joinRejections(rs []PatchRejection) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		parts = append(parts, r.Error())
	}
	return strings.Join(parts, "\n")
}

// diffFile is one file's worth of a unified diff.
type diffFile struct {
	OldPath string
	Path    string
	IsNew   bool
	IsDelete bool
	Hunks   []diffHunk
}

// diffHunk is one @@ block.
type diffHunk struct {
	Index     int
	StartLine int
	OldCount  int
	NewCount  int
	Context   []string
	Removed   []string
	Added     []string
}

// parseUnifiedDiff decodes a unified diff.
//
// It accepts the reduced form git produces with --no-prefix and with a/ b/
// prefixes, because a model produces all three and rejecting two of them wastes
// repair rounds on formatting rather than on the actual defect.
func parseUnifiedDiff(diff string) ([]diffFile, error) {
	lines := strings.Split(strings.ReplaceAll(diff, "\r\n", "\n"), "\n")
	var (
		files []diffFile
		cur   *diffFile
		hunk  *diffHunk
	)

	flushHunk := func() {
		if cur != nil && hunk != nil {
			cur.Hunks = append(cur.Hunks, *hunk)
			hunk = nil
		}
	}
	flushFile := func() {
		flushHunk()
		if cur != nil {
			files = append(files, *cur)
			cur = nil
		}
	}

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		switch {
		case strings.HasPrefix(line, "--- "):
			flushFile()
			cur = &diffFile{OldPath: strings.TrimSpace(line[4:])}

		case strings.HasPrefix(line, "+++ "):
			if cur == nil {
				cur = &diffFile{}
			}
			p := strings.TrimSpace(line[4:])
			// Strip a trailing timestamp column if present.
			if idx := strings.IndexAny(p, "\t"); idx >= 0 {
				p = strings.TrimSpace(p[:idx])
			}
			if p == "/dev/null" {
				cur.IsNew = true
			} else {
				cur.Path = stripDiffPrefix(p)
				if cur.OldPath != "" && cur.OldPath != "/dev/null" {
					o := stripDiffPrefix(strings.TrimSpace(cur.OldPath))
					if cur.Path == o {
						cur.IsDelete = true
					}
				}
			}

		case strings.HasPrefix(line, "@@"):
			flushHunk()
			if cur == nil {
				return nil, fmt.Errorf("hunk header on line %d has no preceding ---/+++ file header", i+1)
			}
			start, oldCount, newCount, err := parseHunkHeader(line)
			if err != nil {
				return nil, fmt.Errorf("line %d: %v", i+1, err)
			}
			idx := len(cur.Hunks) + 1
			hunk = &diffHunk{Index: idx, StartLine: start, OldCount: oldCount, NewCount: newCount}

		case hunk != nil:
			switch {
			case strings.HasPrefix(line, "+"):
				hunk.Added = append(hunk.Added, line[1:])
			case strings.HasPrefix(line, "-"):
				hunk.Removed = append(hunk.Removed, line[1:])
			case strings.HasPrefix(line, " "):
				hunk.Context = append(hunk.Context, line[1:])
			case line == "":
				// A trailing empty line in the diff body is ambiguous: some
				// producers emit it, some do not. Treating it as context is the
				// choice that matches how diff(1) output is normally consumed.
				hunk.Context = append(hunk.Context, "")
			case strings.HasPrefix(line, "\\"):
				// "\ No newline at end of file"
			default:
				hunk = nil
			}
		}
	}
	flushFile()

	if len(files) == 0 {
		return nil, fmt.Errorf("no file headers found; a unified diff needs --- and +++ lines")
	}
	for _, f := range files {
		if f.Path == "" && !f.IsDelete {
			return nil, fmt.Errorf("a file header has no usable target path")
		}
	}
	return files, nil
}

func stripDiffPrefix(p string) string {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "a/")
	p = strings.TrimPrefix(p, "b/")
	p = strings.ReplaceAll(p, "\\", "/")
	return p
}

func parseHunkHeader(line string) (start, oldCount, newCount int, err error) {
	// @@ -oldStart,oldCount +newStart,newCount @@ optional section heading
	rest := strings.TrimPrefix(line, "@@")
	rest = strings.TrimSpace(strings.SplitN(rest, "@@", 2)[0])

	var oldPart, newPart string
	for _, tok := range strings.Fields(rest) {
		if strings.HasPrefix(tok, "-") {
			oldPart = tok[1:]
		} else if strings.HasPrefix(tok, "+") {
			newPart = tok[1:]
		}
	}
	if oldPart == "" && newPart == "" {
		return 0, 0, 0, fmt.Errorf("malformed hunk header %q", line)
	}

	parseSide := func(s string) (int, int) {
		if s == "" {
			return 1, 1
		}
		parts := strings.SplitN(s, ",", 2)
		n, cerr := strconv.Atoi(parts[0])
		if cerr != nil {
			return 1, 1
		}
		c := 1
		if len(parts) == 2 {
			if v, cerr := strconv.Atoi(parts[1]); cerr == nil {
				c = v
			}
		}
		return n, c
	}

	oldStart, oldCount := parseSide(oldPart)
	newStart, newCount := parseSide(newPart)
	_ = oldCount
	// The new start is what the hunk applies at in the current file, which is what
	// the applier needs. Falling back to the old start keeps a malformed header
	// usable instead of failing the whole patch.
	start = newStart
	if start <= 0 {
		start = oldStart
	}
	return start, oldCount, newCount, nil
}

// findHunk locates a hunk's start line, allowing a small offset window.
func findHunk(lines []string, h diffHunk, preferred, window int) int {
	// The expected old content is context followed by removed lines.
	expected := append(append([]string{}, h.Context...), h.Removed...)
	if len(expected) == 0 {
		// A pure insertion: anchor on context alone.
		expected = h.Context
	}
	if len(expected) == 0 {
		if preferred <= len(lines) {
			return preferred
		}
		return len(lines)
	}

	tryAt := func(start int) bool {
		if start < 0 || start+len(expected) > len(lines) {
			return false
		}
		for i, want := range expected {
			if lines[start+i] != want {
				return false
			}
		}
		return true
	}

	if tryAt(preferred) {
		return preferred
	}
	for d := 1; d <= window; d++ {
		if tryAt(preferred - d) {
			return preferred - d
		}
		if tryAt(preferred + d) {
			return preferred + d
		}
	}
	return -1
}

// spliceLines replaces removed lines at start with added lines.
func spliceLines(lines []string, start, removeCount int, added []string) []string {
	out := make([]string, 0, len(lines)-removeCount+len(added))
	out = append(out, lines[:start]...)
	out = append(out, added...)
	if start+removeCount < len(lines) {
		out = append(out, lines[start+removeCount:]...)
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return []string{}
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	lines := strings.Split(s, "\n")
	// A file ending in a newline yields a trailing empty element. Dropping it
	// keeps line numbers aligned with what an editor shows, which is what a model
	// computes a @@ header from.
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}