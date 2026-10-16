// Package tools is the agent's action surface.
//
// Every capability the agent has is a Tool with a name, a JSON-schema
// description, an explicit mode allow-list, and a structured Result. Three
// properties are non-negotiable and enforced by the types:
//
//  1. A tool never reports success ambiguously. Result carries Success, an exit
//     code where one applies, stdout, stderr, and a duration, so the model never
//     has to infer whether something worked from prose.
//  2. A tool declares whether it mutates the workspace, so modes that must not
//     write cannot call something that writes.
//  3. A tool declares which modes may use it, so an EXPLORE run physically cannot
//     apply a patch.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Mode names are the agent's operating modes.
const (
	ModeExplore   = "explore"
	ModePlan      = "plan"
	ModeImplement = "implement"
	ModeDebug     = "debug"
	ModeTest      = "test"
	ModeReview    = "review"
	ModeRefactor  = "refactor"
	ModeExplain   = "explain"
)

// Modes is the full mode list.
var Modes = []string{
	ModeExplore, ModePlan, ModeImplement, ModeDebug,
	ModeTest, ModeReview, ModeRefactor, ModeExplain,
}

// ValidMode reports whether a mode name is recognised.
func ValidMode(mode string) bool {
	for _, m := range Modes {
		if m == mode {
			return true
		}
	}
	return false
}

// Result is the structured outcome of a tool call.
//
// Success is never inferred from the presence of output. A tool that produced
// text and failed still reports Success false with the output attached, because
// "here is the compiler error" is exactly what the agent needs to see next.
type Result struct {
	Tool       string         `json:"tool"`
	Success    bool           `json:"success"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Stdout     string         `json:"stdout,omitempty"`
	Stderr     string         `json:"stderr,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Error      string         `json:"error,omitempty"`
	Truncated  bool           `json:"truncated,omitempty"`
	Hint       string         `json:"hint,omitempty"`
	Class      string         `json:"class,omitempty"`
	Changed    bool           `json:"changed,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

// OK builds a successful result.
func OK(tool string, data map[string]any) Result {
	return Result{Tool: tool, Success: true, Data: data}
}

// Fail builds a failed result.
func Fail(tool string, err error) Result {
	return Result{Tool: tool, Success: false, Error: err.Error()}
}

// FailMsg builds a failed result from a message.
func FailMsg(tool, msg string) Result {
	return Result{Tool: tool, Success: false, Error: msg}
}

// CommandResult builds the standard result shape for an executed command, which
// is the shape the specification requires: success, exit code, stdout, stderr,
// duration.
func CommandResult(tool, command string, exitCode int, stdout, stderr string, d time.Duration, maxBytes int) Result {
	stdout, t1 := clip(stdout, maxBytes)
	stderr, t2 := clip(stderr, maxBytes)
	code := exitCode
	return Result{
		Tool:       tool,
		Success:    exitCode == 0,
		ExitCode:   &code,
		Stdout:     stdout,
		Stderr:     stderr,
		DurationMS: d.Milliseconds(),
		Truncated:  t1 || t2,
		Hint:       command,
	}
}

func clip(s string, max int) (string, bool) {
	if max <= 0 || len(s) <= max {
		return s, false
	}
	head := max / 2
	tail := max - head
	return s[:head] + "\n... [truncated " + fmt.Sprint(len(s)-max) + " bytes] ...\n" + s[len(s)-tail:], true
}

// Args is a decoded tool argument map.
//
// Accessors never panic on a missing or wrongly-typed key. A model that omits an
// argument should produce a clear tool error, not a Go panic that takes the run
// down with no record of what the model actually said.
type Args struct {
	raw map[string]any
}

// NewArgs wraps a raw map.
func NewArgs(raw map[string]any) Args {
	if raw == nil {
		raw = map[string]any{}
	}
	return Args{raw: raw}
}

// Raw exposes the underlying map.
func (a Args) Raw() map[string]any { return a.raw }

// Str returns a string argument, or "".
func (a Args) Str(key string) string {
	v, ok := a.raw[key]
	if !ok {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%g", t)
	case bool:
		return fmt.Sprintf("%v", t)
	default:
		return fmt.Sprintf("%v", t)
	}
}

// StrDefault returns a string argument or a fallback.
func (a Args) StrDefault(key, fallback string) string {
	if s := a.Str(key); s != "" {
		return s
	}
	return fallback
}

// Int returns an integer argument.
func (a Args) Int(key string, fallback int) int {
	switch t := a.raw[key].(type) {
	case float64:
		return int(t)
	case int:
		return t
	case string:
		var n int
		if _, err := fmt.Sscanf(t, "%d", &n); err == nil {
			return n
		}
	}
	return fallback
}

// Bool returns a boolean argument, accepting the string forms a model often emits.
func (a Args) Bool(key string, fallback bool) bool {
	switch t := a.raw[key].(type) {
	case bool:
		return t
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true", "yes", "1", "y":
			return true
		case "false", "no", "0", "n":
			return false
		}
	case float64:
		return t != 0
	}
	return fallback
}

// List returns a string-slice argument, accepting a bare string as a single item.
func (a Args) List(key string) []string {
	switch t := a.raw[key].(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, v := range t {
			out = append(out, fmt.Sprint(v))
		}
		return out
	case string:
		if strings.TrimSpace(t) == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

// Has reports whether a key was supplied.
func (a Args) Has(key string) bool {
	_, ok := a.raw[key]
	return ok
}

// Map returns a nested object argument.
func (a Args) Map(key string) Args {
	if m, ok := a.raw[key].(map[string]any); ok {
		return NewArgs(m)
	}
	return NewArgs(nil)
}

// Param is one tool parameter's schema.
type Param struct {
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Required    bool     `json:"required"`
	Description string   `json:"description"`
	Enum        []string `json:"enum,omitempty"`
	Default     any      `json:"default,omitempty"`
}

// Tool is one capability.
type Tool interface {
	// Name is the identifier the model uses.
	Name() string
	// Description is one sentence explaining what the tool does.
	Description() string
	// Params describes the accepted arguments.
	Params() []Param
	// Modes lists the modes permitted to call this tool. Empty means all modes.
	Modes() []string
	// Mutates reports whether calling the tool can change the workspace.
	Mutates() bool
	// Run executes the tool.
	Run(ctx context.Context, args Args) Result
}

// Registry holds the available tools.
type Registry struct {
	tools []Tool
	// maxOutputBytes clips tool output before it reaches a model.
	maxOutputBytes int
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{maxOutputBytes: 256 * 1024} }

// SetMaxOutputBytes sets the output clip size.
func (r *Registry) SetMaxOutputBytes(n int) {
	if n > 0 {
		r.maxOutputBytes = n
	}
}

// Register adds tools, skipping duplicates by name.
func (r *Registry) Register(tools ...Tool) {
	for _, t := range tools {
		if t == nil {
			continue
		}
		replaced := false
		for i := range r.tools {
			if r.tools[i].Name() == t.Name() {
				r.tools[i] = t
				replaced = true
				break
			}
		}
		if !replaced {
			r.tools = append(r.tools, t)
		}
	}
}

// Get returns a tool by name.
func (r *Registry) Get(name string) (Tool, bool) {
	for _, t := range r.tools {
		if t.Name() == name {
			return t, true
		}
	}
	return nil, false
}

// Names returns every registered tool name, sorted.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.tools))
	for _, t := range r.tools {
		out = append(out, t.Name())
	}
	sort.Strings(out)
	return out
}

// All returns every tool.
func (r *Registry) All() []Tool { return append([]Tool{}, r.tools...) }

// ForMode returns the tools a mode may call, sorted by name.
func (r *Registry) ForMode(mode string) []Tool {
	var out []Tool
	for _, t := range r.tools {
		if allowsMode(t, mode) {
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

func allowsMode(t Tool, mode string) bool {
	modes := t.Modes()
	if len(modes) == 0 {
		return true
	}
	for _, m := range modes {
		if m == mode || m == "*" {
			return true
		}
	}
	return false
}

// Mutating returns the tools a mode may call that can change the workspace.
func (r *Registry) Mutating(mode string) []Tool {
	var out []Tool
	for _, t := range r.ForMode(mode) {
		if t.Mutates() {
			out = append(out, t)
		}
	}
	return out
}

// Invoke runs a tool by name and returns a structured result.
//
// Every failure path produces a Result rather than an error, including an unknown
// tool name. The model's next decision depends on receiving a usable result; an
// error return would have to be turned into one anyway, and doing it here means
// the loop has exactly one shape to handle.
func (r *Registry) Invoke(ctx context.Context, name string, args Args) Result {
	t, ok := r.Get(name)
	if !ok {
		return Result{
			Tool:    name,
			Success: false,
			Error:   fmt.Sprintf("unknown tool %q; available tools: %s", name, strings.Join(r.Names(), ", ")),
		}
	}
	start := time.Now()
	res := t.Run(ctx, args)
	if res.Tool == "" {
		res.Tool = name
	}
	if res.DurationMS == 0 {
		res.DurationMS = time.Since(start).Milliseconds()
	}
	r.clipResult(&res)
	return res
}

func (r *Registry) clipResult(res *Result) {
	if res.Stdout != "" {
		res.Stdout, res.Truncated = clip(res.Stdout, r.maxOutputBytes)
	}
	if res.Stderr != "" {
		var t2 bool
		res.Stderr, t2 = clip(res.Stderr, r.maxOutputBytes)
		res.Truncated = res.Truncated || t2
	}
}

// Catalogue is the machine-readable description of a mode's tools, which is what
// the prompt embeds.
type Catalogue struct {
	Mode  string           `json:"mode"`
	Tools []CatalogueEntry `json:"tools"`
}

// CatalogueEntry is one tool's prompt-facing description.
type CatalogueEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Mutates     bool     `json:"mutates"`
	Params      []Param  `json:"params,omitempty"`
	Required    []string `json:"required,omitempty"`
}

// CatalogueFor builds the prompt-facing description for a mode.
func (r *Registry) CatalogueFor(mode string) Catalogue {
	cat := Catalogue{Mode: mode}
	for _, t := range r.ForMode(mode) {
		e := CatalogueEntry{
			Name:        t.Name(),
			Description: t.Description(),
			Mutates:     t.Mutates(),
			Params:      t.Params(),
		}
		for _, p := range e.Params {
			if p.Required {
				e.Required = append(e.Required, p.Name)
			}
		}
		cat.Tools = append(cat.Tools, e)
	}
	return cat
}

// RenderCatalogue formats the catalogue for a prompt.
//
// JSON is used rather than prose because the model has to emit a matching JSON
// object, and a single consistent representation removes one whole class of
// format failure.
func (r *Registry) RenderCatalogue(mode string) string {
	cat := r.CatalogueFor(mode)
	data, err := json.MarshalIndent(cat, "", "  ")
	if err != nil {
		return "{}"
	}
	return string(data)
}