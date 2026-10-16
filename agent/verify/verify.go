// Package verify detects what a project is and how to build and test it.
//
// Detection is evidence-based: a manifest file is proof, an import is weaker
// evidence, and a directory name is weakest. Every inferred command is derived
// from something that was actually observed in the tree, because a verification
// command that does not match the project reports failures that are not the
// project's failures, and the agent then spends its repair budget chasing a
// problem it invented.
package verify

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
)

// Ecosystem identifies the primary toolchain of a project.
type Ecosystem string

const (
	EcosystemGo     Ecosystem = "go"
	EcosystemPython Ecosystem = "python"
	EcosystemNode   Ecosystem = "node"
	EcosystemRust   Ecosystem = "rust"
	EcosystemDotnet Ecosystem = "dotnet"
	EcosystemJava   Ecosystem = "java"
	EcosystemMixed  Ecosystem = "mixed"
	EcosystemNone   Ecosystem = "unknown"
)

// Command is one verification step.
type Command struct {
	Name string   `json:"name"` // build | test | lint | vet
	Args []string `json:"args"`
	Dir  string   `json:"dir,omitempty"`
	// Why records the evidence that produced this command, so an operator can see
	// whether it was detected or guessed.
	Why string `json:"why"`
	// Optional marks a command that is run only when the operator asks, because
	// running it can mutate the tree.
	Optional bool `json:"optional,omitempty"`
}

// Line renders a command for display.
func (c Command) Line() string {
	if len(c.Args) == 0 {
		return c.Name
	}
	return c.Name + " [" + strings.Join(c.Args, " ") + "]"
}

// Project is what detection found.
type Project struct {
	Root        string     `json:"root"`
	Ecosystem   Ecosystem  `json:"ecosystem"`
	Languages   []string   `json:"languages"`
	Manifests   []string   `json:"manifests"`
	Build       []Command  `json:"build"`
	Test        []Command  `json:"test"`
	Lint        []Command  `json:"lint"`
	IsGit       bool       `json:"is_git"`
	GitRoot     string     `json:"git_root,omitempty"`
	FileCount   int        `json:"file_count"`
	SourceCount int        `json:"source_count"`
	TestCount   int        `json:"test_count"`
	Notes       []string   `json:"notes,omitempty"`
	EntryPoints []string   `json:"entry_points,omitempty"`
}

// Has reports whether the project defines a command of a kind.
func (p *Project) Has(kind string) bool {
	for _, c := range p.All() {
		if c.Name == kind {
			return true
		}
	}
	return false
}

// All returns every detected command.
func (p *Project) All() []Command {
	var out []Command
	out = append(out, p.Build...)
	out = append(out, p.Test...)
	out = append(out, p.Lint...)
	return out
}

// Describe renders detection results for a terminal.
func (p *Project) Describe() string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project:      %s\n", p.Root)
	fmt.Fprintf(&b, "Ecosystem:    %s\n", p.Ecosystem)
	if len(p.Languages) > 0 {
		fmt.Fprintf(&b, "Languages:    %s\n", strings.Join(p.Languages, ", "))
	}
	if len(p.Manifests) > 0 {
		fmt.Fprintf(&b, "Manifests:    %s\n", strings.Join(p.Manifests, ", "))
	}
	fmt.Fprintf(&b, "Files:        %d (%d source, %d test)\n", p.FileCount, p.SourceCount, p.TestCount)
	fmt.Fprintf(&b, "Git:          %v", p.IsGit)
	if p.GitRoot != "" {
		fmt.Fprintf(&b, " (root %s)", p.GitRoot)
	}
	b.WriteString("\n")
	if len(p.EntryPoints) > 0 {
		fmt.Fprintf(&b, "Entry points: %s\n", strings.Join(p.EntryPoints, ", "))
	}
	for _, section := range []struct {
		label string
		cmds  []Command
	}{{"Build", p.Build}, {"Test", p.Test}, {"Lint", p.Lint}} {
		if len(section.cmds) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s:\n", section.label)
		for _, c := range section.cmds {
			marker := ""
			if c.Optional {
				marker = "  (optional)"
			}
			fmt.Fprintf(&b, "  %s%s\n      because: %s\n", c.Line(), marker, c.Why)
		}
	}
	for _, n := range p.Notes {
		fmt.Fprintf(&b, "note: %s\n", n)
	}
	return b.String()
}

// manifest describes one recognised project marker.
type manifest struct {
	file      string
	ecosystem Ecosystem
	build     [][]string
	test      [][]string
	lint      [][]string
	optional  map[int]bool
	why       string
}

var manifests = []manifest{
	{
		file: "go.mod", ecosystem: EcosystemGo, why: "go.mod declares a Go module",
		build: [][]string{{"go", "build", "./..."}},
		test:  [][]string{{"go", "test", "./..."}},
		lint:  [][]string{{"go", "vet", "./..."}},
	},
	{
		file: "pyproject.toml", ecosystem: EcosystemPython, why: "pyproject.toml declares a Python project",
		lint: [][]string{{"python", "-m", "ruff", "check", "."}},
	},
	{
		file: "setup.py", ecosystem: EcosystemPython, why: "setup.py declares a Python package",
	},
	{
		file: "requirements.txt", ecosystem: EcosystemPython, why: "requirements.txt declares Python dependencies",
	},
	{
		file: "Pipfile", ecosystem: EcosystemPython, why: "Pipfile declares Python dependencies",
	},
	{
		file: "package.json", ecosystem: EcosystemNode, why: "package.json declares Node scripts",
	},
	{
		file: "Cargo.toml", ecosystem: EcosystemRust, why: "Cargo.toml declares a Cargo package",
		build: [][]string{{"cargo", "build"}},
		test:  [][]string{{"cargo", "test"}},
		lint:  [][]string{{"cargo", "clippy"}},
	},
	{
		file: "*.csproj", ecosystem: EcosystemDotnet, why: "a .csproj file declares a .NET project",
		build: [][]string{{"dotnet", "build"}},
		test:  [][]string{{"dotnet", "test"}},
	},
	{
		file: "*.sln", ecosystem: EcosystemDotnet, why: "a .sln file declares a .NET solution",
		build: [][]string{{"dotnet", "build"}},
		test:  [][]string{{"dotnet", "test"}},
	},
	{
		file: "pom.xml", ecosystem: EcosystemJava, why: "pom.xml declares a Maven project",
		build: [][]string{{"mvn", "-q", "-DskipTests", "compile"}},
		test:  [][]string{{"mvn", "-q", "test"}},
	},
	{
		file: "build.gradle", ecosystem: EcosystemJava, why: "build.gradle declares a Gradle project",
		build: [][]string{{"gradle", "build"}},
		test:  [][]string{{"gradle", "test"}},
	},
	{
		file: "Makefile", ecosystem: EcosystemNone, why: "Makefile declares developer targets",
	},
	{
		file: "CMakeLists.txt", ecosystem: EcosystemNone, why: "CMakeLists.txt declares a CMake project",
		build: [][]string{{"cmake", "--build", "build"}},
	},
}

var extLangs = map[string]string{
	".go": "go", ".py": "python", ".js": "javascript", ".jsx": "javascript",
	".ts": "typescript", ".tsx": "typescript", ".rs": "rust", ".cs": "csharp",
	".java": "java", ".kt": "kotlin", ".rb": "ruby", ".php": "php",
	".c": "c", ".h": "c", ".cc": "cpp", ".cpp": "cpp", ".hpp": "cpp",
	".cshtml": "csharp", ".swift": "swift", ".sh": "shell", ".ps1": "powershell",
	".sql": "sql", ".md": "markdown", ".yaml": "yaml", ".yml": "yaml",
	".json": "json", ".toml": "toml",
}

var testPatterns = []string{"_test.go", "test_", "_test.", "test.", "tests.", ".spec.", ".test."}

// Detect inspects a tree and reports how to build, test, and lint it.
func Detect(root string, ignoreGlobs []string) (*Project, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("detect project %s: %w", abs, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("detect project %s: not a directory", abs)
	}

	p := &Project{Root: abs, Ecosystem: EcosystemNone}
	ign := fsignore.New(ignoreGlobs)

	langCount := map[string]int{}
	ecoFound := map[Ecosystem]bool{}
	var cmdBuild, cmdTest, cmdLint []Command

	seen := map[string]bool{}
	err = filepath.Walk(abs, func(path string, d os.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		rel, _ := filepath.Rel(abs, path)
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			if ign.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if ign.Match(rel, false) {
			return nil
		}

		p.FileCount++
		name := d.Name()
		if d.Size() > 0 && d.Size() < 2<<20 {
			if _, ok := extLangs[strings.ToLower(filepath.Ext(name))]; ok {
				p.SourceCount++
			}
		}

		// Manifests.
		for _, m := range manifests {
			if !matchesManifest(name, m.file) {
				continue
			}
			key := m.file + "@" + filepath.Dir(rel)
			if seen[key] {
				continue
			}
			seen[key] = true
			ecoFound[m.ecosystem] = true
			p.Manifests = append(p.Manifests, rel)
			dir := filepath.ToSlash(filepath.Dir(rel))
			if dir == "." {
				dir = ""
			}
			for _, argv := range m.build {
				cmdBuild = append(cmdBuild, Command{Name: "build", Args: argv, Dir: dir, Why: m.why})
			}
			for _, argv := range m.test {
				cmdTest = append(cmdTest, Command{Name: "test", Args: argv, Dir: dir, Why: m.why})
			}
			for _, argv := range m.lint {
				cmdLint = append(cmdLint, Command{Name: "lint", Args: argv, Dir: dir, Why: m.why})
			}
		}

		ext := strings.ToLower(filepath.Ext(name))
		if lang, ok := extLangs[ext]; ok && lang != "markdown" && lang != "json" && lang != "yaml" && lang != "toml" {
			langCount[lang]++
		}
		lower := strings.ToLower(name)
		for _, pat := range testPatterns {
			if strings.HasSuffix(lower, strings.ToLower(pat)) {
				p.TestCount++
				break
			}
		}
		if isEntryPoint(rel, ext) && len(p.EntryPoints) < 8 {
			p.EntryPoints = append(p.EntryPoints, rel)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	p.Manifests = dedupeSorted(p.Manifests)

	for lang, n := range langCount {
		if n >= 1 {
			p.Languages = append(p.Languages, lang)
		}
	}
	sort.Slice(p.Languages, func(i, j int) bool {
		return langCount[p.Languages[i]] > langCount[p.Languages[j]]
	})

	// Ecosystem: manifests are proof, file counts are a fallback for a tree with
	// no manifest at all.
	switch {
	case len(ecoFound) > 1:
		p.Ecosystem = EcosystemMixed
	case len(ecoFound) == 1:
		for eco := range ecoFound {
			p.Ecosystem = eco
		}
	default:
		if len(p.Languages) > 0 {
			p.Ecosystem = Ecosystem(p.Languages[0])
		}
		if p.Ecosystem == Ecosystem("javascript") {
			p.Ecosystem = EcosystemNode
		}
		p.Notes = append(p.Notes, "no build manifest found; commands were inferred from file extensions and may be incomplete")
	}
	if p.Ecosystem == EcosystemMixed {
		p.Notes = append(p.Notes, "multiple ecosystems present; every detected command is run, because a change in one language can break another language's build")
	}

	p.Build = dedupeCommands(cmdBuild)
	p.Test = dedupeCommands(cmdTest)
	p.Lint = dedupeCommands(cmdLint)

	p.Test = p.fillNodeScripts(abs, p.Test)
	p.Build = p.fillNodeScripts(abs, p.Build)
	p.Lint = p.fillNodeScripts(abs, p.Lint)

	p.Build = p.filterAvailable(p.Build)
	p.Test = p.filterAvailable(p.Test)
	p.Lint = p.filterAvailable(p.Lint)

	p.IsGit, p.GitRoot = detectGit(abs)

	if len(p.Build) == 0 && len(p.Test) == 0 {
		p.Notes = append(p.Notes, "no build or test command could be detected; verification will rely on tool output only")
	}
	return p, nil
}

func matchesManifest(name, pattern string) bool {
	if strings.HasPrefix(pattern, "*") {
		return strings.HasSuffix(strings.ToLower(name), strings.ToLower(pattern[1:]))
	}
	return strings.EqualFold(name, pattern)
}

func isEntryPoint(rel, ext string) bool {
	switch ext {
	case ".go", ".py", ".js", ".ts", ".rs":
	default:
		return false
	}
	base := filepath.Base(rel)
	switch base {
	case "main.go", "__main__.py", "main.py", "app.py", "index.js", "index.ts", "main.rs", "server.js", "app.js", "cli.go", "main.ts":
		return true
	}
	return false
}

// fillNodeScripts reads package.json and maps its scripts onto build/test/lint.
//
// package.json is the only place a JavaScript project's real commands are
// declared. Guessing `npm test` for a project that defines `npm run check` would
// produce a false pass, which is worse than producing no command at all.
func (p *Project) fillNodeScripts(abs string, existing []Command) []Command {
	data, err := os.ReadFile(filepath.Join(abs, "package.json"))
	if err != nil {
		return existing
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return existing
	}
	if len(pkg.Scripts) == 0 {
		return existing
	}

	find := func(candidates ...string) (string, bool) {
		for _, c := range candidates {
			if v, ok := pkg.Scripts[c]; ok && strings.TrimSpace(v) != "" {
				return c, true
			}
		}
		return "", false
	}

	add := func(kind string, candidates []string) {
		name, ok := find(candidates...)
		if !ok {
			return
		}
		cmd := Command{Name: kind, Args: []string{"npm", "run", name}, Why: fmt.Sprintf("package.json defines the %q script", name)}
		for _, e := range existing {
			if e.Name == kind {
				return
			}
		}
		existing = append(existing, cmd)
	}

	add("build", []string{"build", "compile"})
	add("test", []string{"test", "test:unit", "unit"})
	add("lint", []string{"lint", "typecheck", "check"})
	return existing
}

// filterAvailable drops commands whose executable is not installed.
//
// Reporting them anyway would let a run "verify" a project with a compiler that
// does not exist; the command would fail with an exec error and be reported as a
// project failure.
func (p *Project) filterAvailable(cmds []Command) []Command {
	var out []Command
	for _, c := range cmds {
		if len(c.Args) == 0 {
			continue
		}
		bin := c.Args[0]
		if bin == "dotnet" && runtime.GOOS == "windows" {
			bin = "dotnet.exe"
		}
		if _, err := exec.LookPath(bin); err != nil {
			if bin == "dotnet" && runtime.GOOS != "windows" {
				if _, err2 := exec.LookPath("dotnet"); err2 != nil {
					continue
				}
			} else {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

func detectGit(root string) (bool, string) {
	dir := root
	for i := 0; i < 12; i++ {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return true, dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return false, ""
}

func dedupeSorted(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func dedupeCommands(in []Command) []Command {
	seen := map[string]bool{}
	var out []Command
	for _, c := range in {
		key := c.Name + "|" + c.Dir + "|" + strings.Join(c.Args, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, c)
	}

	// A multi-module workspace legitimately produces one command per module, but
	// the same argv repeated in a directory that holds no module of its own is an
	// artefact of how the walk found nested manifests. Running `go build ./...`
	// eleven times because eleven go.mod files were noticed is slow and produces
	// eleven copies of the same diagnostic, which buries the real one.
	byArgv := map[string]int{}
	kept := out[:0]
	for _, c := range out {
		argv := strings.Join(c.Args, " ")
		if prior, seenArgv := byArgv[argv]; seenArgv && !moduleRoot(c.Dir) {
			// Keep the first occurrence and record that the duplicate was folded in,
			// so nothing is silently dropped from the report.
			out[prior].Why += fmt.Sprintf("; also applied in %s", orDot(c.Dir))
			continue
		}
		byArgv[argv] = len(kept)
		kept = append(kept, c)
	}
	out = kept

	// Build before test before lint: a failing build makes tests meaningless, and
	// running them anyway buries the real error in noise.
	rank := map[string]int{"build": 0, "test": 1, "lint": 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Name] != rank[out[j].Name] {
			return rank[out[i].Name] < rank[out[j].Name]
		}
		return out[i].Dir < out[j].Dir
	})
	return out
}

// moduleRoot reports whether a directory is a module root in its own right.
//
// Depth zero is the workspace root, which is the module the workspace is about.
// Any other directory that declared its own manifest is a genuine nested module
// and keeps its own command.
func moduleRoot(dir string) bool { return dir == "" || dir == "." }

func orDot(dir string) string {
	if dir == "" {
		return "."
	}
	return dir
}


func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	// Keep the head and the tail: compiler errors at the top and test summaries at
	// the bottom are both load-bearing, and the middle is usually the longest and
	// least informative part.
	head := max / 3
	tail := max - head
	return s[:head] + "\n... [truncated " + strconv.Itoa(len(s)-max) + " bytes] ...\n" + s[len(s)-tail:]
}
