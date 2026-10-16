// Package safety decides whether the agent may run something.
//
// The classification is deliberately conservative but not obstructive: read-only
// inspection, builds, and tests run unattended because they are what development
// is made of, while writes outside the workspace, network publishing, and
// destructive operations require an explicit human decision. Blocking is reserved
// for operations that cannot be meaningfully recovered from, because a block that
// covers ordinary development gets worked around, and a safety layer that gets
// worked around provides no safety at all.
package safety

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Classification is the policy verdict for one command.
type Classification string

const (
	// Safe commands may run unattended.
	Safe Classification = "SAFE"
	// Confirm commands require an explicit human approval before running.
	Confirm Classification = "REQUIRES_CONFIRMATION"
	// Blocked commands may not run at all unless the operator has explicitly
	// unlocked them.
	Blocked Classification = "BLOCKED"
)

// Verdict is the outcome of classifying one command.
type Verdict struct {
	Command     string         `json:"command"`
	Class       Classification `json:"class"`
	Rule        string         `json:"rule"`
	Reason      string         `json:"reason"`
	Segment     string         `json:"segment,omitempty"`
	PathEscape  bool           `json:"path_escape,omitempty"`
	Network     bool           `json:"network,omitempty"`
	Executed    bool           `json:"executed"`
	ApprovalBy  string         `json:"approval_by,omitempty"`
}

// Allowed reports whether the verdict permits running with no further approval.
func (v Verdict) Allowed() bool { return v.Class == Safe }

// NeedsApproval reports whether a human decision is required.
func (v Verdict) NeedsApproval() bool { return v.Class == Confirm }

// rule maps a pattern onto a class.
type rule struct {
	name    string
	class   Classification
	reason  string
	re      *regexp.Regexp
}

// Policy classifies commands.
type Policy struct {
	Workspace     string
	WorkspaceOnly bool
	AllowNetwork  bool
	// AllowBlocked must be set explicitly by an operator who understands what it
	// unlocks. It is never enabled by configuration discovery.
	AllowBlocked bool

	rules []rule
}

// Options configures a Policy.
type Options struct {
	Workspace     string
	WorkspaceOnly bool
	AllowNetwork  bool
	AllowBlocked  bool
}

// NewPolicy builds a policy with the built-in rule set.
func NewPolicy(opts Options) *Policy {
	p := &Policy{
		Workspace:     opts.Workspace,
		WorkspaceOnly: opts.WorkspaceOnly,
		AllowNetwork:  opts.AllowNetwork,
		AllowBlocked:  opts.AllowBlocked,
	}
	p.rules = builtinRules()
	return p
}

// Classify decides the verdict for a whole command line.
//
// The line is split on shell operators first and each segment classified
// separately. Without that split, `git status && shutdown` classifies as safe on
// its prefix, and the safety layer becomes advisory in the only place it matters.
// Pipelines are handled the same way, so `cat x | sh` is caught.
func (p *Policy) Classify(command string) Verdict {
	trimmed := strings.TrimSpace(command)
	if trimmed == "" {
		return Verdict{Command: command, Class: Blocked, Rule: "empty", Reason: "empty command"}
	}

	segments := SplitSegments(trimmed)
	if len(segments) == 0 {
		return Verdict{Command: command, Class: Blocked, Rule: "unparsable",
			Reason: "command could not be parsed into recognisable segments"}
	}

	// Every segment must pass. A single blocked segment blocks the line, which
	// is what prevents a safe prefix from laundering a dangerous tail.
	worst := Verdict{Command: command, Class: Safe, Rule: "allow-list", Reason: "matched an allow-listed read-only or development command"}
	for _, seg := range segments {
		v := p.classifySegment(seg)
		v.Command = command
		v.Segment = seg
		if severity(v.Class) > severity(worst.Class) {
			worst = v
		}
		if worst.Network {
			v.Network = true
		}
	}
	worst.Network = worst.Network || p.usesNetwork(trimmed)
	return worst
}

func severity(c Classification) int {
	switch c {
	case Safe:
		return 0
	case Confirm:
		return 1
	default:
		return 2
	}
}

func (p *Policy) classifySegment(seg string) Verdict {
	lowered := strings.ToLower(strings.TrimSpace(seg))

	// An interpreter reading a program from a pipe or command substitution is
	// arbitrary code execution wearing a costume. Classifying the executable is
	// not enough; the payload has to be caught too.
	if interpreterPayload(lowered) {
		return Verdict{Class: Blocked, Rule: "interpreter-payload",
			Reason: "pipes a downloaded or generated script into an interpreter; arbitrary code execution cannot be reviewed"}
	}

	if p.WorkspaceOnly && p.escapesWorkspace(seg) {
		return Verdict{Class: Blocked, Rule: "workspace-escape",
			Reason: fmt.Sprintf("path leaves the workspace %s", p.Workspace),
			PathEscape: true}
	}

	for _, r := range p.rules {
		if r.re.MatchString(lowered) {
			if r.class == Blocked && !p.AllowBlocked {
				return Verdict{Class: Blocked, Rule: r.name, Reason: r.reason}
			}
			if r.class == Confirm && !p.AllowNetwork && r.name == "network-publish" {
				return Verdict{Class: Confirm, Rule: r.name, Reason: r.reason, Network: true}
			}
			return Verdict{Class: r.class, Rule: r.name, Reason: r.reason}
		}
	}

	// Unknown commands require confirmation rather than being assumed safe. The
	// cost is one prompt; the benefit is that a newly installed tool cannot be
	// run by the agent without the operator seeing it.
	return Verdict{Class: Confirm, Rule: "unknown-command",
		Reason: "command is not in the allow list; approval required"}
}

func interpreterPayload(lowered string) bool {
	patterns := []string{
		`\b(curl|wget|iwr|invoke-webrequest)\b[^|]*\|\s*(sh|bash|zsh|cmd|powershell|pwsh|python|python3|perl|node|ruby|iex)\b`,
		`\b(sh|bash|zsh)\s+-c\b`,
		`\bpowershell\b[^|]*\b(iex|invoke-expression)\b`,
		`\b(cmd\.exe\s*/c|powershell\.exe\s+-enc|pwsh\s+-enc)\b`,
	}
	for _, pat := range patterns {
		if regexp.MustCompile(pat).MatchString(lowered) {
			return true
		}
	}
	return false
}

// escapesWorkspace reports whether a segment references a path outside the
// workspace.
//
// Only clearly absolute or parent-escaping paths are treated as escapes. A bare
// relative path is assumed to be resolved against the working directory, which
// the executor sets to the workspace, so this check is about intent rather than
// about being a complete path analyser.
func (p *Policy) escapesWorkspace(seg string) bool {
	if p.Workspace == "" {
		return false
	}
	lower := strings.ToLower(seg)
	abs, _ := filepath.Abs(p.Workspace)

	for _, raw := range quotedPaths(seg) {
		norm := filepath.Clean(raw)
		candidate := norm
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(abs, candidate)
		}
		rel, err := filepath.Rel(abs, candidate)
		if err != nil {
			return true
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
		if isProtectedRoot(norm) {
			return true
		}
	}
	_ = lower
	return false
}

func quotedPaths(seg string) []string {
	var out []string
	// Quoted paths, which is how a path with spaces arrives.
	for _, m := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(seg, -1) {
		if looksLikePath(m[1]) {
			out = append(out, m[1])
		}
	}
	// Unquoted absolute paths and drive letters.
	for _, m := range regexp.MustCompile(`([A-Za-z]:[\\/][^\s"'|;&]+)`).FindAllStringSubmatch(seg, -1) {
		out = append(out, m[1])
	}
	// Unix absolute paths.
	for _, m := range regexp.MustCompile(`(^|\s)(/[^\s"'|;&]+)`).FindAllStringSubmatch(seg, -1) {
		out = append(out, m[2])
	}
	// Explicit parent traversal.
	for _, m := range regexp.MustCompile(`(\.\.[\\/]|\.\.(?:\s|$))`).FindAllStringSubmatch(seg, -1) {
		out = append(out, filepath.Join("..", "x"))
		_ = m
	}
	return out
}

func looksLikePath(s string) bool {
	if s == "" {
		return false
	}
	if filepath.IsAbs(s) {
		return true
	}
	if strings.HasPrefix(s, "../") || strings.HasPrefix(s, "..\\") {
		return true
	}
	// A flag value or a URL is not a path.
	if strings.HasPrefix(s, "-") || strings.Contains(s, "://") {
		return false
	}
	if strings.ContainsAny(s, `*?`) {
		return false
	}
	return strings.ContainsAny(s, `/.\:`)
}

// isProtectedRoot recognises locations that are never a legitimate target for
// agent-driven work.
func isProtectedRoot(p string) bool {
	norm := filepath.Clean(p)
	if norm == "" {
		return false
	}
	lower := strings.ToLower(norm)
	if lower == "/" || lower == `c:\` || lower == `c:/` {
		return true
	}
	// Windows system directories.
	for _, dir := range []string{`c:\windows`, `c:\program files`, `c:\program files (x86)`, "/etc", "/usr", "/bin", "/boot", "/sys", "/proc", "/var/lib"} {
		if strings.HasPrefix(lower, strings.ToLower(dir)) {
			return true
		}
	}
	// The user's home root, which recursive deletion of would take everything.
	if home, err := os.UserHomeDir(); err == nil {
		if norm == filepath.Clean(home) {
			return true
		}
	}
	return false
}

// usesNetwork reports whether a command line appears to reach the network.
func (p *Policy) usesNetwork(cmd string) bool {
	lower := strings.ToLower(cmd)
	for _, marker := range []string{"curl ", "wget ", "git clone", "git fetch", "git pull", "git push",
		"npm publish", "pip install", "npm install", "cargo install", "go get", "go install",
		"dotnet restore", "nuget push", "docker pull", "ssh ", "scp ", "rsync "} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// SplitSegments breaks a command line into individually-classifiable parts,
// respecting quotes so that a separator inside a quoted argument is not a split.
func SplitSegments(command string) []string {
	var (
		segments []string
		current  strings.Builder
		inSingle bool
		inDouble bool
	)
	runes := []rune(command)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case c == '\'' && !inDouble:
			inSingle = !inSingle
			current.WriteRune(c)
		case c == '"' && !inSingle:
			inDouble = !inDouble
			current.WriteRune(c)
		case !inSingle && !inDouble && (c == ';' || c == '\n'):
			segments = append(segments, strings.TrimSpace(current.String()))
			current.Reset()
		case !inSingle && !inDouble && (c == '&' || c == '|'):
			// Two-character operators. A single & or | followed by whitespace is
			// still a separator; inside a Windows path a lone | is not valid, so
			// treating it as a separator costs nothing.
			if i+1 < len(runes) && (runes[i+1] == '&' || runes[i+1] == '|') {
				i++
			}
			segments = append(segments, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteRune(c)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		segments = append(segments, strings.TrimSpace(current.String()))
	}

	var out []string
	for _, s := range segments {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// Rules returns the rule set in evaluation order, for `agent doctor` output.
func (p *Policy) Rules() []Verdict {
	var out []Verdict
	for _, r := range p.rules {
		out = append(out, Verdict{Class: r.class, Rule: r.name, Reason: r.reason})
	}
	sort.SliceStable(out, func(i, j int) bool {
		return severity(out[i].Class) > severity(out[j].Class)
	})
	return out
}