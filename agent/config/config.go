// Package config defines the agent's on-disk configuration and the rules for
// discovering it.
//
// Configuration is JSON rather than YAML because the Go standard library parses
// it, and a configuration format that needs a dependency before the agent can
// start is a dependency the agent can be broken by. The file is written with
// indentation and preserved field order because humans edit it.
//
// Nothing here hard-codes a machine-specific path. Every path that could differ
// between machines is either discovered or taken from the configuration, and the
// default is derived from the process working directory rather than from a
// constant.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
)

// FileName is the configuration file the agent looks for in the workspace root.
const FileName = "agent.json"

// Roles recognised by the router. A route maps a role onto a concrete
// (backend, model) pair, so planning and reviewing can use different models.
const (
	RolePlanner    = "planner"
	RoleCoder      = "coder"
	RoleDebugger   = "debugger"
	RoleReviewer   = "reviewer"
	RoleSummarizer = "summarizer"
	RoleEmbeddings = "embeddings"
)

// KnownRoles is the full role set, in the order they are reported.
var KnownRoles = []string{
	RolePlanner,
	RoleCoder,
	RoleDebugger,
	RoleReviewer,
	RoleSummarizer,
	RoleEmbeddings,
}

// Config is the whole agent configuration.
type Config struct {
	Version    int      `json:"version"`
	Workspace  string   `json:"workspace"`
	Models     Models   `json:"models"`
	Safety     Safety   `json:"safety"`
	Limits     Limits   `json:"limits"`
	Context    Context  `json:"context"`
	Memory     Memory   `json:"memory"`
	Logging    Logging  `json:"logging"`
	Verify     Verify   `json:"verification"`
	Agent      Agent    `json:"agent"`
}

// Models lists the inference backends the agent may use and how each role is
// routed onto them.
type Models struct {
	// Backends are candidate inference servers. Several may be configured at
	// once: an agent that can fall back from a busy 27B to a 3B is more useful
	// than one that can only talk to a single server.
	Backends []Backend `json:"backends"`

	// Routes assign roles to backends. A role absent from this map is resolved
	// automatically from whatever backends are healthy.
	Routes map[string]Route `json:"routes"`

	// AutoStart starts a llama.cpp server for a discovered GGUF file when no
	// reachable backend exists. Disabled by default: silently loading a
	// multi-gigabyte model is not something an agent should do without being
	// asked.
	AutoStart bool `json:"auto_start_llamacpp"`

	// LlamaCppBinary overrides discovery of the llama-server executable.
	LlamaCppBinary string `json:"llama_cpp_binary,omitempty"`

	// ModelDirs are searched for .gguf files during inventory. Discovered
	// rather than assumed; see Defaults.
	ModelDirs []string `json:"model_dirs,omitempty"`
}

// Backend is one inference server.
type Backend struct {
	Name       string            `json:"name"`
	Type       string            `json:"type"`
	BaseURL    string            `json:"base_url,omitempty"`
	Model      string            `json:"model,omitempty"`
	APIKey     string            `json:"api_key,omitempty"`
	TimeoutSec int               `json:"timeout_sec,omitempty"`
	Enabled    *bool             `json:"enabled,omitempty"`
	Options    map[string]string `json:"options,omitempty"`
}

// IsEnabled reports whether the backend should be attempted. A nil Enabled means
// enabled: an operator who adds a backend expects it to be used.
func (b Backend) IsEnabled() bool { return b.Enabled == nil || *b.Enabled }

// Route assigns one role to a backend and model.
type Route struct {
	Backend     string  `json:"backend"`
	Model       string  `json:"model,omitempty"`
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
}

// Safety is the command policy.
type Safety struct {
	// AutoApproveSafe runs SAFE commands without asking.
	AutoApproveSafe bool `json:"auto_approve_safe"`

	// AutoApproveConfirm runs REQUIRES_CONFIRMATION commands without asking.
	// Off by default: an operator who turns this on has accepted that the agent
	// can perform write and delete operations unattended.
	AutoApproveConfirm bool `json:"auto_approve_confirm"`

	// WorkspaceOnly refuses any command or file write that resolves outside the
	// workspace. This is the single most important safety default, because the
	// machine holds other projects that must not be touched.
	WorkspaceOnly bool `json:"workspace_only"`

	// TimeoutSec is the default wall-clock limit for one command.
	TimeoutSec int `json:"command_timeout_sec"`

	// AllowNetwork permits commands that reach the network.
	AllowNetwork bool `json:"allow_network"`

	// MaxOutputBytes truncates captured stdout/stderr.
	MaxOutputBytes int `json:"max_output_bytes"`
}

// Limits bound the agent's own behaviour so a run terminates.
type Limits struct {
	MaxAttempts      int `json:"max_attempts"`
	MaxSteps         int `json:"max_steps"`
	MaxToolCalls     int `json:"max_tool_calls"`
	MaxTokensPerCall int `json:"max_tokens_per_call"`
	CommandTimeout   int `json:"command_timeout_sec"`
}

// Context controls retrieval.
type Context struct {
	MaxFileBytes     int      `json:"max_file_bytes"`
	MaxFiles         int      `json:"max_files"`
	MaxSearchResults int      `json:"max_search_results"`
	IncludeTests     bool     `json:"include_tests"`
	IgnoreGlobs      []string `json:"ignore_globs"`
	UseEmbeddings    bool     `json:"use_embeddings"`
}

// Memory controls the persistent project memory store.
type Memory struct {
	Dir           string `json:"dir"`
	MaxContextKB  int    `json:"max_context_kb"`
	WriteOnChange  bool  `json:"write_on_change"`
	StaleAfterDays int    `json:"stale_after_days"`
}

// Logging controls observability.
type Logging struct {
	Dir     string `json:"dir"`
	Verbose bool   `json:"verbose"`
	// KeepRuns is how many run directories to retain. Old runs are pruned on
	// startup so a long-lived workspace does not accumulate unbounded logs.
	KeepRuns int `json:"keep_runs"`
}

// Verify controls build/test detection and execution.
type Verify struct {
	Enabled       bool     `json:"enabled"`
	TimeoutSec    int      `json:"timeout_sec"`
	ExtraCommands []string `json:"extra_commands,omitempty"`
	SkipCommands  []string `json:"skip_commands,omitempty"`
}

// Agent controls the loop itself.
type Agent struct {
	Mode           string `json:"mode"`
	ReviewEnabled  bool   `json:"review_enabled"`
	RequireEvidence bool  `json:"require_evidence"`
	MaxRepairRounds int   `json:"max_repair_rounds"`
}

// Defaults returns a configuration with every field populated.
//
// Backend and path defaults are intentionally absent rather than guessed: an
// invented llama-server port produces a confusing connection error, whereas an
// absent backend is discovered at startup by Discover and reported honestly.
func Defaults() *Config {
	return &Config{
		Version:   1,
		Workspace: ".",
		Models: Models{
			Backends: []Backend{},
			Routes:   map[string]Route{},
		},
		Safety: Safety{
			AutoApproveSafe:   true,
			AutoApproveConfirm: false,
			WorkspaceOnly:      true,
			TimeoutSec:         600,
			AllowNetwork:       true,
			MaxOutputBytes:     256 * 1024,
		},
		Limits: Limits{
			MaxAttempts:      3,
			MaxSteps:         60,
			MaxToolCalls:     200,
			MaxTokensPerCall: 2048,
			CommandTimeout:   600,
		},
		Context: Context{
			MaxFileBytes:     256 * 1024,
			MaxFiles:         40,
			MaxSearchResults: 50,
			IncludeTests:     true,
			// The default ignore set is shared with the tools and retrieval layers
			// so that project detection, search, and context assembly all agree about
			// which files exist. Two divergent lists produced a workspace that
			// reported 460 files to detection while search saw far fewer.
			IgnoreGlobs:    fsignore.DefaultPatterns,
			UseEmbeddings: true,
		},
		Memory: Memory{
			Dir:            ".agent",
			MaxContextKB:   24,
			WriteOnChange:  true,
			StaleAfterDays: 30,
		},
		Logging: Logging{
			Dir:      ".agent/logs",
			Verbose:  false,
			KeepRuns: 20,
		},
		Verify: Verify{
			Enabled:    true,
			TimeoutSec: 900,
		},
		Agent: Agent{
			Mode:            "implement",
			ReviewEnabled:   true,
			RequireEvidence: true,
			MaxRepairRounds: 3,
		},
	}
}

// Load reads the configuration for a workspace.
//
// Search order is explicit flag path, then AGENT_CONFIG, then agent.json in the
// workspace. A missing file is not an error: Defaults plus discovery is a
// working configuration, and refusing to start without a config file would make
// first use require a manual step for no benefit.
func Load(workspace, explicitPath string) (*Config, error) {
	ws, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}

	cfg := Defaults()
	cfg.Workspace = ws

	path := explicitPath
	if path == "" {
		path = os.Getenv("AGENT_CONFIG")
	}
	if path == "" {
		candidate := filepath.Join(ws, FileName)
		if _, err := os.Stat(candidate); err == nil {
			path = candidate
		}
	}
	if path == "" {
		return cfg, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config %s: %w", path, err)
	}

	// The workspace always wins over whatever the file said: a config that can
	// redirect writes to another directory would defeat WorkspaceOnly.
	cfg.Workspace = ws
	absolutize(cfg)
	return cfg, nil
}

// absolutize turns every configured relative path into one rooted at the
// workspace, so the rest of the agent never has to ask where it is running.
func absolutize(cfg *Config) {
	rel := func(p *string) {
		if *p == "" || filepath.IsAbs(*p) {
			return
		}
		*p = filepath.Join(cfg.Workspace, filepath.FromSlash(*p))
	}
	rel(&cfg.Memory.Dir)
	rel(&cfg.Logging.Dir)
	if cfg.Models.LlamaCppBinary != "" {
		rel(&cfg.Models.LlamaCppBinary)
	}
	for i := range cfg.Models.ModelDirs {
		if cfg.Models.ModelDirs[i] != "" && !filepath.IsAbs(cfg.Models.ModelDirs[i]) {
			cfg.Models.ModelDirs[i] = filepath.Join(cfg.Workspace, filepath.FromSlash(cfg.Models.ModelDirs[i]))
		}
	}
}

// Discover fills in anything the configuration left unset by probing the
// machine. It never overwrites an operator's explicit choice.
func (c *Config) Discover() {
	if len(c.Models.Backends) == 0 {
		c.Models.Backends = probeBackends()
	}
	if len(c.Models.ModelDirs) == 0 {
		c.Models.ModelDirs = c.modelDirs()
	}
	if c.Models.LlamaCppBinary == "" {
		if bin := DiscoverLlamaServer(); bin != "" {
			c.Models.LlamaCppBinary = bin
		}
	}
	// Map entries are structs, so the value has to be written back; assigning
	// into the indexed field directly is not legal.
	for role, rt := range c.Models.Routes {
		if rt.Backend == "" && len(c.Models.Backends) > 0 {
			rt.Backend = c.Models.Backends[0].Name
			c.Models.Routes[role] = rt
		}
	}
}

// probeBackends returns the backends reachable right now, best first.
//
// Discovery order reflects cost: a small Ollama model answers immediately, while
// a GGUF served by llama.cpp may have to be paged into memory first. Listing an
// unreachable backend is worse than listing none, so reachability is tested.
func probeBackends() []Backend {
	var found []Backend

	if portOpen("127.0.0.1", 11434) {
		found = append(found, Backend{
			Name:       "ollama",
			Type:       "ollama",
			BaseURL:    "http://127.0.0.1:11434",
			Model:      "llama3.2:3b",
			TimeoutSec: 900,
		})
	}
	for _, port := range []int{8090, 8080, 8000, 1234, 5000, 11435} {
		if portOpen("127.0.0.1", port) {
			found = append(found, Backend{
				Name:       fmt.Sprintf("openai-%d", port),
				Type:       "openai",
				BaseURL:    fmt.Sprintf("http://127.0.0.1:%d/v1", port),
				TimeoutSec: 900,
			})
			break
		}
	}
	return found
}

// modelDirs finds GGUF collections relative to the workspace.
//
// The parent directory is included because the local model layout on this
// machine keeps weights outside the project (sibling directories), which is a
// common arrangement for large models that should not live in version control.
func (c *Config) modelDirs() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(p string) {
		if p == "" {
			return
		}
		if abs, err := filepath.Abs(p); err == nil {
			p = abs
		}
		if !seen[p] {
			seen[p] = true
			dirs = append(dirs, p)
		}
	}

	add(filepath.Join(c.Workspace, "models-cache"))
	add(filepath.Join(c.Workspace, "Models"))
	add(filepath.Join(c.Workspace, "models"))

	// One level up, but only sibling directories that actually contain weights.
	// Walking further would risk scanning unrelated projects.
	parent := filepath.Dir(c.Workspace)
	if entries, err := os.ReadDir(parent); err == nil {
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.Join(parent, e.Name())
			if containsGGUF(p) {
				add(p)
			}
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".ollama", "models")
		if _, err := os.Stat(p); err == nil {
			add(p)
		}
	}
	return dirs
}

func containsGGUF(dir string) bool {
	found := false
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		if info.IsDir() {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(p), ".gguf") && info.Size() > 64*1024*1024 {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// DiscoverLlamaServer locates a llama.cpp server binary.
//
// Candidate locations are the vendored directory, then anything on PATH. The
// vendored copy is preferred because it is known to have been built against this
// project's needs, but the PATH copy is a genuine fallback rather than an error.
func DiscoverLlamaServer() string {
	candidates := []string{}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates,
			filepath.Join(wd, "third_party", "llama-cpp", "llama-server.exe"),
			filepath.Join(wd, "third_party", "llama-cpp", "server.exe"),
		)
	}
	name := "llama-server"
	if runtime.GOOS != "windows" {
		name = "llama-server"
	}
	if path, err := exeLookPath(name); err == nil {
		candidates = append(candidates, path)
	}
	if path, err := exeLookPath("server"); err == nil {
		candidates = append(candidates, path)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c
		}
	}
	return ""
}

// Save writes the configuration to the workspace as indented JSON.
func (c *Config) Save() error {
	path := filepath.Join(c.Workspace, FileName)
	if err := os.MkdirAll(c.Workspace, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	// Rename through a temporary file so an interrupted write cannot leave a
	// truncated config that fails to parse on the next run.
	return os.Rename(tmp, path)
}

// CommandTimeout returns the effective command timeout.
func (c *Config) CommandTimeout() time.Duration {
	secs := c.Limits.CommandTimeout
	if secs <= 0 {
		secs = c.Safety.TimeoutSec
	}
	if secs <= 0 {
		secs = 600
	}
	return time.Duration(secs) * time.Second
}