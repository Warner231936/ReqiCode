package context

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// Brief is the assembled working context for one step of a task.
type Brief struct {
	Request     string   `json:"request"`
	Project     *verify.Project `json:"-"`
	Files       []string `json:"files"`
	Code        string   `json:"code"`
	Memory      string   `json:"memory"`
	Conventions string   `json:"conventions"`
	Failures    string   `json:"failures"`
	Bytes       int      `json:"bytes"`
	// Dropped counts chunks that did not fit the budget, which is reported so a
	// silently truncated context does not look like a complete one.
	Dropped int `json:"dropped"`
}

// Options controls brief assembly.
type BriefOptions struct {
	MaxBytes       int
	IncludeMemory  bool
	IncludeTests   bool
	ExtraPaths     []string
	// MustInclude are file paths that bypass ranking, because the task or the
	// operator named them explicitly.
	MustInclude []string
}

// Render formats a brief for a prompt.
func (b *Brief) Render() string {
	var s strings.Builder
	if b.Project != nil {
		p := b.Project
		fmt.Fprintf(&s, "## Project\n")
		fmt.Fprintf(&s, "ecosystem: %s\n", p.Ecosystem)
		if len(p.Languages) > 0 {
			fmt.Fprintf(&s, "languages: %s\n", strings.Join(p.Languages, ", "))
		}
		if len(p.Build) > 0 {
			fmt.Fprintf(&s, "build: %s\n", strings.Join(commandLines(p.Build), " ; "))
		}
		if len(p.Test) > 0 {
			fmt.Fprintf(&s, "test: %s\n", strings.Join(commandLines(p.Test), " ; "))
		}
		if len(p.Manifests) > 0 {
			fmt.Fprintf(&s, "manifests: %s\n", strings.Join(p.Manifests, ", "))
		}
		if p.IsGit {
			fmt.Fprintf(&s, "git: yes\n")
		}
		s.WriteString("\n")
	}

	if b.Memory != "" {
		s.WriteString(b.Memory)
		s.WriteString("\n")
	}
	if b.Conventions != "" {
		s.WriteString("## Conventions\n" + b.Conventions + "\n\n")
	}
	if b.Failures != "" {
		s.WriteString("## Earlier failures on this task\n" + b.Failures + "\n\n")
	}
	if b.Code != "" {
		s.WriteString("## Relevant code\n")
		s.WriteString(b.Code)
		if b.Dropped > 0 {
			fmt.Fprintf(&s, "\n[%d further chunk(s) were retrieved but did not fit the context budget]\n", b.Dropped)
		}
	}
	return s.String()
}

func commandLines(cmds []verify.Command) []string {
	out := make([]string, 0, len(cmds))
	for _, c := range cmds {
		out = append(out, strings.Join(c.Args, " "))
	}
	return out
}

// Build assembles a brief for a request.
//
// Budgeting is explicit and reported. The failure this prevents is specific and
// common: a context that silently truncates looks identical to a complete one, and
// a local model will confidently act on the half of the repository it was shown.
func (m *Manager) Build(ctx context.Context, request string, opts BriefOptions) (*Brief, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = m.opts.MaxContextKB * 1024
	}
	if err := m.Index(ctx); err != nil {
		return nil, err
	}

	brief := &Brief{Request: request, Project: m.project}

	if opts.IncludeMemory && m.opts.Memory != nil {
		brief.Memory = m.opts.Memory.Digest(opts.MaxBytes/4, false)
		brief.Failures = renderFailureDigest(m.opts.Memory)
		brief.Conventions = renderConventions(m.opts.Memory)
	}

	// Explicitly named paths come first and unconditionally.
	var selected []Chunk
	seen := map[string]bool{}
	for _, p := range opts.MustInclude {
		abs := p
		if !filepath.IsAbs(abs) {
			abs = filepath.Join(m.opts.WorkspacePath, filepath.FromSlash(p))
		}
		rel := m.ws.Rel(abs)
		if ch, ok := m.files[rel]; ok {
			for _, c := range ch.Chunks {
				if !seen[c.Key()] {
					seen[c.Key()] = true
					selected = append(selected, c)
				}
			}
		}
	}

	ranked, err := m.Retrieve(ctx, request, m.opts.MaxResults)
	if err != nil {
		return nil, err
	}

	used := len([]byte(brief.Render()))
	var code strings.Builder
	var files []string
	fileSet := map[string]bool{}
	dropped := 0

	appendChunks := func(chunks []Chunk, force bool) {
		for _, c := range chunks {
			piece := renderChunk(c)
			pieceBytes := len(piece)
			if used+pieceBytes > opts.MaxBytes {
				if !force {
					dropped++
					continue
				}
				// A must-include chunk that does not fit is truncated rather than
				// dropped: the operator named it, so its absence would be a silent
				// override of an explicit instruction.
				room := opts.MaxBytes - used
				if room <= 0 {
					return
				}
				piece = piece[:room] + "\n... [truncated to fit the context budget]\n"
			}
			code.WriteString(piece)
			used += len(piece)
			if !fileSet[c.Path] {
				fileSet[c.Path] = true
				files = append(files, c.Path)
			}
			seen[c.Key()] = true
		}
	}

	appendChunks(selected, true)
	appendChunks(ranked, false)

	brief.Code = code.String()
	brief.Files = files
	brief.Dropped = dropped
	brief.Bytes = used
	return brief, nil
}

// renderChunk formats one chunk with a source header.
func renderChunk(c Chunk) string {
	var b strings.Builder
	kind := c.Kind
	if c.Name != "" {
		kind += " " + c.Name
	}
	fmt.Fprintf(&b, "\n--- %s (%s lines %d-%d) ---\n", c.Path, kind, c.Start, c.End)
	b.WriteString(c.Content)
	b.WriteString("\n")
	return b.String()
}

// renderFailureDigest lists failures relevant to the request.
func renderFailureDigest(store *memory.Store) string {
	failures := store.Failures(false)
	if len(failures) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range failures {
		if i >= 8 {
			break
		}
		fmt.Fprintf(&b, "- [%s x%d] %s\n", f.Kind, f.Occurrences, firstLine(f.Summary))
	}
	return b.String()
}

// renderConventions reads the conventions document.
func renderConventions(store *memory.Store) string {
	doc, err := store.ReadDoc("conventions.md")
	if err != nil || doc.Content == "" {
		return ""
	}
	return condenseMarkdown(doc.Content, 1200)
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// condenseMarkdown strips the boilerplate placeholder and heading markers so a
// memory document costs fewer tokens in a prompt.
func condenseMarkdown(s string, maxBytes int) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "#") {
			continue
		}
		if strings.HasPrefix(t, "_Not yet recorded") {
			continue
		}
		if t == "" {
			continue
		}
		out = append(out, t)
		if len(strings.Join(out, "\n")) > maxBytes {
			break
		}
	}
	joined := strings.Join(out, "\n")
	if len(joined) > maxBytes {
		joined = joined[:maxBytes] + "\n... [truncated]"
	}
	return joined
}

// Readme extracts a short orientation from the project README, when present.
//
// The README is the one document a maintainer wrote for an arriving reader, which
// makes it far better use of a context budget than any generated summary.
func (m *Manager) Readme() string {
	for _, name := range []string{"README.md", "readme.md", "README.MD", "PROJECT.MD", "AGENTS.md"} {
		p := filepath.Join(m.opts.WorkspacePath, name)
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		return condenseMarkdown(string(data), 2000)
	}
	return ""
}

// Map renders a compact file tree of the workspace.
//
// This is included in every brief because knowing the shape of a repository is a
// large part of knowing where to look, and it costs a few hundred tokens to say.
func (m *Manager) Map(maxEntries int) string {
	if maxEntries <= 0 {
		maxEntries = 200
	}
	var entries []string
	_ = filepath.Walk(m.opts.WorkspacePath, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel := m.ws.Rel(path)
		if info.IsDir() {
			if m.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			if strings.Count(rel, "/") > 3 {
				return filepath.SkipDir
			}
			entries = append(entries, rel+"/")
			return nil
		}
		if m.ignore.Match(rel, false) {
			return nil
		}
		if !isRetrievable(rel) {
			return nil
		}
		entries = append(entries, rel)
		return nil
	})
	sort.Strings(entries)
	if len(entries) > maxEntries {
		entries = append(entries[:maxEntries],
			fmt.Sprintf("... %d more entries", len(entries)-maxEntries))
	}
	return strings.Join(entries, "\n")
}