package tools

import (
	"os"
	"time"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
	"github.com/kilo/spiral-codemaker/agent/safety"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// Options assembles the tool registry.
type Options struct {
	WorkspacePath  string
	IgnoreGlobs    []string
	Policy         *safety.Policy
	Approver       safety.Approver
	Timeout        time.Duration
	MaxOutputBytes int
	MaxReadBytes   int
	MaxResults     int
	History        func() []string
	// RipgrepPath, when set, is reported in search results as an available
	// accelerator. Search does not depend on it.
	RipgrepPath string
}

// Deps is the resolved dependency set handed to the agent loop.
type Deps struct {
	WS       *Workspace
	Registry *Registry
	Exec     ExecConfig
	Project  *verify.Project
	Policy   *safety.Policy
}

// New assembles the full tool registry and detects the project.
//
// Detection happens here rather than lazily so that `inspect_project` returns
// instantly and so that a detection failure is reported at startup, where the
// operator can see it, instead of on the first tool call mid-task.
func New(opts Options) (*Deps, error) {
	ws := NewWorkspace(opts.WorkspacePath)
	if opts.MaxReadBytes > 0 {
		ws.MaxReadBytes = opts.MaxReadBytes
	}

	policy := opts.Policy
	if policy == nil {
		policy = safety.NewPolicy(safety.Options{
			Workspace:     ws.Root,
			WorkspaceOnly: true,
			AllowNetwork:  true,
		})
	}
	approver := opts.Approver
	if approver == nil {
		approver = safety.NewAutoApprover(false)
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	maxOut := opts.MaxOutputBytes
	if maxOut <= 0 {
		maxOut = 256 * 1024
	}
	maxResults := opts.MaxResults
	if maxResults <= 0 {
		maxResults = 60
	}

	execCfg := ExecConfig{
		Workspace:      ws,
		Policy:         policy,
		Approver:       approver,
		Timeout:        timeout,
		MaxOutputBytes: maxOut,
		History:        opts.History,
	}

	project, err := verify.Detect(ws.Root, opts.IgnoreGlobs)
	if err != nil {
		// Detection failure is not fatal. The agent can still read, search, edit
		// and run explicit commands; it just has no detected verification step,
		// which is reported rather than hidden.
		project = &verify.Project{Root: ws.Root, Ecosystem: verify.EcosystemNone,
			Notes: []string{"project detection failed: " + err.Error()}}
	}

	// An empty ignore list must not mean "ignore nothing". Search, detection, and
	// retrieval all share this matcher, and a workspace scanned with no exclusions
	// would count .git objects and build output as source files.
	ignorePatterns := opts.IgnoreGlobs
	if len(ignorePatterns) == 0 {
		ignorePatterns = fsignore.DefaultPatterns
	}
	ignore := fsignore.New(ignorePatterns)

	reg := NewRegistry()
	reg.SetMaxOutputBytes(maxOut)
	reg.Register(
		&InspectProjectTool{WS: ws, Ignore: ignore, Detected: project},
		&ReadFileTool{WS: ws},
		&ListDirectoryTool{WS: ws},
		&SearchCodeTool{WS: ws, Ignore: ignore, MaxResults: maxResults,
			Binary: opts.RipgrepPath, UseRipgrep: opts.RipgrepPath != ""},
		&SearchFilesTool{WS: ws, Ignore: ignore, MaxResults: maxResults * 2},
		&FindSymbolTool{WS: ws, Ignore: ignore},
		&WriteFileTool{WS: ws},
		&EditFileTool{WS: ws},
		&PatchTool{WS: ws},
		&DeleteFileTool{WS: ws},
		&RunCommandTool{Cfg: execCfg},
		&BuildProjectTool{Cfg: execCfg, Detected: project},
		&RunTestsTool{Cfg: execCfg, Detected: project},
		&GitStatusTool{Cfg: execCfg},
		&GitDiffTool{Cfg: execCfg},
		&GitLogTool{Cfg: execCfg},
		&GitTool{Cfg: execCfg},
	)

	return &Deps{
		WS:       ws,
		Registry: reg,
		Exec:     execCfg,
		Project:  project,
		Policy:   policy,
	}, nil
}

// readFileCapped reads at most n bytes from the start of a file.
func readFileCapped(path string, n int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	buf := make([]byte, n)
	read, err := f.Read(buf)
	if err != nil && read == 0 {
		return nil, err
	}
	return buf[:read], nil
}

// removeFile deletes a file, tolerating a read-only attribute on Windows.
//
// A generated file that was checked out read-only is common enough that failing
// here would turn a routine cleanup into an unexplained error.
func removeFile(path string) error {
	err := os.Remove(path)
	if err == nil {
		return nil
	}
	if _, statErr := os.Stat(path); statErr == nil {
		_ = os.Chmod(path, 0o644)
		if retryErr := os.Remove(path); retryErr == nil {
			return nil
		}
	}
	return err
}