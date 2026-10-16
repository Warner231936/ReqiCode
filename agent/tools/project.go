package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// InspectProjectTool reports what the project is and how to work on it.
type InspectProjectTool struct {
	WS       *Workspace
	Ignore   fsignore.Matcher
	Detected *verify.Project
}

func (t *InspectProjectTool) Name() string  { return "inspect_project" }
func (t *InspectProjectTool) Mutates() bool { return false }
func (t *InspectProjectTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeTest, ModeReview, ModeRefactor, ModeExplain}
}
func (t *InspectProjectTool) Description() string {
	return "Report the project structure: ecosystem, languages, manifests, entry points, git state, and the detected build/test/lint commands. Run this first."
}
func (t *InspectProjectTool) Params() []Param {
	return []Param{
		{Name: "detail", Type: "string", Enum: []string{"summary", "full"}, Default: "summary",
			Description: "summary lists the essentials; full includes file counts and notes"},
	}
}

func (t *InspectProjectTool) Run(ctx context.Context, args Args) Result {
	if t.Detected == nil {
		// The matcher's own patterns are passed back to detection, so a lazy
		// re-detection cannot use a different ignore set than the one this tool
		// was constructed with.
		p, err := verify.Detect(t.WS.Root, t.Ignore.Patterns())
		if err != nil {
			return FailMsg(t.Name(), err.Error())
		}
		t.Detected = p
	}
	p := t.Detected

	var b strings.Builder
	if args.Str("detail") == "full" {
		b.WriteString(p.Describe())
	} else {
		fmt.Fprintf(&b, "Project:    %s\n", p.Root)
		fmt.Fprintf(&b, "Ecosystem:  %s\n", p.Ecosystem)
		if len(p.Languages) > 0 {
			fmt.Fprintf(&b, "Languages:  %s\n", strings.Join(p.Languages, ", "))
		}
		if len(p.Manifests) > 0 {
			fmt.Fprintf(&b, "Manifests:  %s\n", strings.Join(p.Manifests, ", "))
		}
		fmt.Fprintf(&b, "Files:      %d (%d source, %d test)\n", p.FileCount, p.SourceCount, p.TestCount)
		fmt.Fprintf(&b, "Git repo:   %v\n", p.IsGit)
		if len(p.EntryPoints) > 0 {
			fmt.Fprintf(&b, "Entry:      %s\n", strings.Join(p.EntryPoints, ", "))
		}
		for _, group := range []struct {
			label string
			cmds  []verify.Command
		}{{"build", p.Build}, {"test", p.Test}, {"lint", p.Lint}} {
			if len(group.cmds) == 0 {
				continue
			}
			parts := make([]string, 0, len(group.cmds))
			for _, c := range group.cmds {
				parts = append(parts, strings.Join(c.Args, " "))
			}
			fmt.Fprintf(&b, "%-9s %s\n", group.label+":", strings.Join(parts, "; "))
		}
		for _, n := range p.Notes {
			fmt.Fprintf(&b, "note:       %s\n", n)
		}
	}

	return Result{
		Tool: t.Name(), Success: true, Stdout: b.String(),
		Hint: "use search_code or find_symbol to locate the code relevant to the task",
		Data: map[string]any{
			"ecosystem":  string(p.Ecosystem),
			"is_git":     p.IsGit,
			"files":      p.FileCount,
			"build":      commandStrings(p.Build),
			"test":       commandStrings(p.Test),
			"lint":       commandStrings(p.Lint),
			"manifests":  p.Manifests,
			"languages":  p.Languages,
			"entrypoints": p.EntryPoints,
		},
	}
}

func commandStrings(cmds []verify.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// DeleteFileTool removes a file, after recording that it existed.
//
// The agent uses this instead of a shell rm so that the deletion is a distinct,
// auditable action in the log and passes through the same safety review. Its
// content is captured first so an accidental deletion can be reversed.
type DeleteFileTool struct {
	WS     *Workspace
	Backup func(rel, content string)
}

func (t *DeleteFileTool) Name() string  { return "delete_file" }
func (t *DeleteFileTool) Mutates() bool { return true }
func (t *DeleteFileTool) Modes() []string {
	return []string{ModeImplement, ModeRefactor}
}
func (t *DeleteFileTool) Description() string {
	return "Delete a file. The prior content is preserved in the run log so the deletion can be reversed."
}
func (t *DeleteFileTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Required: true, Description: "Workspace-relative path to delete"},
	}
}

func (t *DeleteFileTool) Run(ctx context.Context, args Args) Result {
	pathArg := args.Str("path")
	if pathArg == "" {
		return FailMsg(t.Name(), "path is required")
	}
	p, err := t.WS.Resolve(pathArg)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if p == t.WS.Root {
		return FailMsg(t.Name(), "refusing to delete the workspace root")
	}
	data, err := readFileCapped(p, 1<<20)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if t.Backup != nil {
		t.Backup(t.WS.Rel(p), string(data))
	}
	if err := removeFile(p); err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	return Result{
		Tool: t.Name(), Success: true, Changed: true,
		Stdout: "deleted " + t.WS.Rel(p),
		Data:   map[string]any{"path": t.WS.Rel(p), "bytes": len(data)},
	}
}