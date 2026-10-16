package tools

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// GitTool runs read-only git inspection commands.
//
// Only inspection verbs are reachable through this tool. Mutating verbs are not
// implemented here on purpose: they belong to run_command, where they pass
// through the safety policy and can therefore be approved or declined on the
// record. A second path to the same operations with different rules is how a
// safety layer gets bypassed.
type GitTool struct {
	Cfg ExecConfig
}

func (t *GitTool) Name() string        { return "git" }
func (t *GitTool) Mutates() bool       { return false }
func (t *GitTool) Modes() []string     { return nil }
func (t *GitTool) Description() string {
	return "Run a read-only git inspection command: status, diff, log, show, branch, blame, ls-files, rev-parse."
}
func (t *GitTool) Params() []Param {
	return []Param{
		{Name: "subcommand", Type: "string", Required: true,
			Enum:        []string{"status", "diff", "log", "show", "branch", "blame", "ls-files", "rev-parse"},
			Description: "Which git verb to run"},
		{Name: "args", Type: "array", Description: "Extra arguments for the verb"},
		{Name: "path", Type: "string", Description: "Limit a path-scoped verb to this path"},
		{Name: "max_lines", Type: "integer", Description: "Cap the output length"},
	}
}

func (t *GitTool) Run(ctx context.Context, args Args) Result {
	sub := strings.ToLower(strings.TrimSpace(args.Str("subcommand")))
	allowed := map[string]bool{
		"status": true, "diff": true, "log": true, "show": true,
		"branch": true, "blame": true, "ls-files": true, "rev-parse": true,
	}
	if !allowed[sub] {
		return Result{
			Tool: t.Name(), Success: false, Class: "not_permitted",
			Error: fmt.Sprintf("git %s is not available through this tool. Permitted: %s. "+
				"Use run_command for any other git operation, which passes through the safety policy.",
				sub, strings.Join(sortedKeys(allowed), ", ")),
		}
	}

	argv := []string{"git", sub}
	argv = append(argv, args.List("args")...)
	if p := args.Str("path"); p != "" && sub == "diff" {
		argv = append(argv, "--", p)
	}
	maxLines := args.Int("max_lines", 0)

	cmdLine := joinCommand(argv)
	verdict := t.Cfg.Policy.Classify(cmdLine)
	if verdict.Class != "safe" {
		return Result{
			Tool: t.Name(), Success: false, Class: "policy",
			Error: fmt.Sprintf("git %s was not classified SAFE (rule: %s, %s). Use run_command instead.",
				sub, verdict.Rule, verdict.Class),
		}
	}

	runner := &RunCommandTool{Cfg: t.Cfg}
	res, err := runner.execute(ctx, cmdLine, 0)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if maxLines > 0 {
		res.Stdout = capLines(res.Stdout, maxLines)
		res.Truncated = true
	}
	res.Tool = t.Name()
	return res
}

// GitStatusTool returns a compact change summary.
type GitStatusTool struct{ Cfg ExecConfig }

func (t *GitStatusTool) Name() string  { return "git_status" }
func (t *GitStatusTool) Mutates() bool { return false }
func (t *GitStatusTool) Modes() []string {
	return nil
}
func (t *GitStatusTool) Description() string {
	return "Summarise the working tree: branch, staged and unstaged changes, and untracked files. This is the cheapest way to see what has already been modified."
}
func (t *GitStatusTool) Params() []Param {
	return []Param{
		{Name: "porcelain", Type: "boolean", Default: true, Description: "Machine-readable porcelain format"},
	}
}

func (t *GitStatusTool) Run(ctx context.Context, args Args) Result {
	runner := &RunCommandTool{Cfg: t.Cfg}
	if !gitAvailable(t.Cfg.Workspace.Root) {
		return Result{Tool: t.Name(), Success: true,
			Stdout: "not a git repository; git inspection is unavailable",
			Hint:   "the agent can still read, search, and edit files; it cannot diff against a baseline",
			Data:   map[string]any{"is_repo": false}}
	}

	branch, _ := runner.execute(ctx, "git rev-parse --abbrev-ref HEAD", 0)
	status, err := runner.execute(ctx, "git status --porcelain=v1 --untracked-files=all", 0)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if !status.Success {
		return status
	}

	staged, unstaged, untracked := 0, 0, 0
	var lines []string
	for _, line := range strings.Split(status.Stdout, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		code := line[:2]
		switch {
		case code == "??":
			untracked++
		case code[0] != ' ':
			staged++
		default:
			unstaged++
		}
		lines = append(lines, line)
	}

	summary := strings.Join([]string{
		fmt.Sprintf("branch:  %s", strings.TrimSpace(branch.Stdout)),
		fmt.Sprintf("staged:  %d", staged),
		fmt.Sprintf("modified:%d", unstaged),
		fmt.Sprintf("untracked: %d", untracked),
	}, "  ")

	body := strings.Join(lines, "\n")
	if body == "" {
		body = "(working tree clean)"
	}
	return Result{
		Tool: t.Name(), Success: true, Stdout: summary + "\n" + body,
		Data: map[string]any{"is_repo": true, "branch": strings.TrimSpace(branch.Stdout),
			"staged": staged, "modified": unstaged, "untracked": untracked, "clean": len(lines) == 0},
	}
}

// GitDiffTool returns a diff of the working tree or of a commit range.
type GitDiffTool struct{ Cfg ExecConfig }

func (t *GitDiffTool) Name() string  { return "git_diff" }
func (t *GitDiffTool) Mutates() bool { return false }
func (t *GitDiffTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeReview, ModeRefactor, ModeExplain}
}
func (t *GitDiffTool) Description() string {
	return "Show the current diff, optionally limited to a path or a commit range. Use this to review exactly what a change did."
}
func (t *GitDiffTool) Params() []Param {
	return []Param{
		{Name: "path", Type: "string", Description: "Limit the diff to this path"},
		{Name: "ref", Type: "string", Description: "Diff against this ref, e.g. HEAD~1 or a branch"},
		{Name: "staged", Type: "boolean", Description: "Show the staged diff instead of the working tree"},
	}
}

func (t *GitDiffTool) Run(ctx context.Context, args Args) Result {
	if !gitAvailable(t.Cfg.Workspace.Root) {
		return FailMsg(t.Name(), "not a git repository")
	}
	var cmdLine strings.Builder
	cmdLine.WriteString("git diff")
	if args.Bool("staged", false) {
		cmdLine.WriteString(" --cached")
	}
	if ref := args.Str("ref"); ref != "" {
		fmt.Fprintf(&cmdLine, " %s", ref)
	}
	if p := args.Str("path"); p != "" {
		fmt.Fprintf(&cmdLine, " -- %s", p)
	}
	runner := &RunCommandTool{Cfg: t.Cfg}
	res, err := runner.execute(ctx, cmdLine.String(), 0)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if res.Success && strings.TrimSpace(res.Stdout) == "" {
		res.Stdout = "(no differences)"
	}
	res.Tool = t.Name()
	return res
}

// GitLogTool returns recent commit history.
type GitLogTool struct{ Cfg ExecConfig }

func (t *GitLogTool) Name() string  { return "git_log" }
func (t *GitLogTool) Mutates() bool { return false }
func (t *GitLogTool) Modes() []string {
	return []string{ModeExplore, ModePlan, ModeReview, ModeExplain, ModeRefactor}
}
func (t *GitLogTool) Description() string {
	return "Show recent commit history, optionally for one path. Use it to learn the project's conventions and to see how similar problems were solved before."
}
func (t *GitLogTool) Params() []Param {
	return []Param{
		{Name: "limit", Type: "integer", Default: 20, Description: "How many commits to show"},
		{Name: "path", Type: "string", Description: "Only commits touching this path"},
		{Name: "oneline", Type: "boolean", Default: true, Description: "One line per commit"},
	}
}

func (t *GitLogTool) Run(ctx context.Context, args Args) Result {
	if !gitAvailable(t.Cfg.Workspace.Root) {
		return FailMsg(t.Name(), "not a git repository")
	}
	limit := args.Int("limit", 20)
	cmdLine := fmt.Sprintf("git log -n %d", limit)
	if args.Bool("oneline", true) {
		cmdLine += " --pretty=format:%h %ad %an %s --date=short"
	}
	if p := args.Str("path"); p != "" {
		cmdLine += " -- " + p
	}
	runner := &RunCommandTool{Cfg: t.Cfg}
	res, err := runner.execute(ctx, cmdLine, 0)
	if err != nil {
		return FailMsg(t.Name(), err.Error())
	}
	if res.Success && strings.TrimSpace(res.Stdout) == "" {
		res.Stdout = "(no commits yet)"
	}
	res.Tool = t.Name()
	return res
}

func gitAvailable(root string) bool {
	cmd := exec.Command("git", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = root
	out, err := cmd.Output()
	return err == nil && strings.TrimSpace(string(out)) == "true"
}

func joinCommand(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		if strings.ContainsAny(a, " \t\"") {
			b.WriteString(`"` + strings.ReplaceAll(a, `"`, `\"`) + `"`)
		} else {
			b.WriteString(a)
		}
	}
	return b.String()
}

func capLines(s string, max int) string {
	lines := strings.Split(s, "\n")
	if len(lines) <= max {
		return s
	}
	out := strings.Join(lines[:max], "\n")
	return out + fmt.Sprintf("\n... [%d more lines]", len(lines)-max)
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small fixed set; sorting keeps the error message stable across runs.
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}