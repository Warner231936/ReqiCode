package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestArgs_Str(t *testing.T) {
	a := NewArgs(map[string]any{
		"path":  "/some/file.go",
		"count": 42,
		"flag":  true,
	})
	if a.Str("path") != "/some/file.go" {
		t.Errorf(`expected "/some/file.go", got %q`, a.Str("path"))
	}
	if a.Str("count") != "42" {
		t.Errorf(`expected "42", got %q`, a.Str("count"))
	}
	if a.Str("flag") != "true" {
		t.Errorf(`expected "true", got %q`, a.Str("flag"))
	}
}

func TestArgs_StrDefault(t *testing.T) {
	a := NewArgs(map[string]any{
		"path": "/file.go",
	})
	if a.StrDefault("path", "fallback") != "/file.go" {
		t.Error("should return actual value")
	}
	if a.StrDefault("missing", "fallback") != "fallback" {
		t.Error("should return fallback for missing key")
	}
	if a.StrDefault("empty", "fallback") != "fallback" {
		t.Error("should return fallback for empty string")
	}
}

func TestArgs_Int(t *testing.T) {
	a := NewArgs(map[string]any{
		"number":     42,
		"fromString": "123",
		"float":      3.14,
		"missing":    nil,
	})
	if a.Int("number", 0) != 42 {
		t.Error("should return int")
	}
	if a.Int("fromString", 0) != 123 {
		t.Error("should parse string to int")
	}
	if a.Int("float", 0) != 3 {
		t.Error("should convert float to int")
	}
	if a.Int("missing", 99) != 99 {
		t.Error("should return fallback for missing")
	}
}

func TestArgs_Bool(t *testing.T) {
	a := NewArgs(map[string]any{
		"t":   true,
		"f":   false,
		"yes": "yes",
		"no":  "no",
		"1":   "1",
		"0":   "0",
		"y":   "y",
		"n":   "n",
		"true": "true",
		"TRUE": "TRUE",
		"2.0": 2.0,
		"0.0": 0.0,
	})
	if !a.Bool("t", false) {
		t.Error("expected true")
	}
	if a.Bool("f", true) {
		t.Error("expected false")
	}
	for _, key := range []string{"yes", "1", "y", "true", "TRUE", "2.0"} {
		if !a.Bool(key, false) {
			t.Errorf("expected %q to be true", key)
		}
	}
	for _, key := range []string{"no", "0", "n", "0.0"} {
		if a.Bool(key, true) {
			t.Errorf("expected %q to be false", key)
		}
	}
}

func TestArgs_List(t *testing.T) {
	a := NewArgs(map[string]any{
		"slice":   []string{"a", "b", "c"},
		"sliceAny": []any{"x", "y", "z"},
		"single":  "single-item",
		"empty":   "",
		"missing": nil,
	})
	if len(a.List("slice")) != 3 {
		t.Errorf("expected 3 items, got %d", len(a.List("slice")))
	}
	if len(a.List("sliceAny")) != 3 {
		t.Errorf("expected 3 items from any slice, got %d", len(a.List("sliceAny")))
	}
	if len(a.List("single")) != 1 {
		t.Error("expected single-item to be a 1-item list")
	}
	if a.List("single")[0] != "single-item" {
		t.Error("single item should be the value")
	}
	if a.List("empty") != nil {
		t.Error("empty string should return nil")
	}
	if a.List("missing") != nil {
		t.Error("missing key should return nil")
	}
}

func TestArgs_Has(t *testing.T) {
	a := NewArgs(map[string]any{
		"present": "value",
	})
	if !a.Has("present") {
		t.Error("expected Has to return true for present key")
	}
	if a.Has("absent") {
		t.Error("expected Has to return false for absent key")
	}
}

func TestArgs_Map(t *testing.T) {
	a := NewArgs(map[string]any{
		"nested": map[string]any{
			"key": "value",
		},
		"notmap": "string",
	})
	nested := a.Map("nested")
	if nested.Str("key") != "value" {
		t.Error("expected nested map access to work")
	}
	empty := a.Map("notmap")
	if empty.Str("any") != "" {
		t.Error("non-map should return empty Args")
	}
	empty2 := a.Map("missing")
	if empty2.Str("any") != "" {
		t.Error("missing key should return empty Args")
	}
}

func TestArgs_NilMap(t *testing.T) {
	a := NewArgs(nil)
	if a.Str("anything") != "" {
		t.Error("nil map should return empty string")
	}
	if a.Int("x", 0) != 0 {
		t.Error("nil map should return fallback")
	}
	if a.Bool("x", true) != true {
		t.Error("nil map should return fallback")
	}
}

func TestResult_OK(t *testing.T) {
	r := OK("test", map[string]any{"key": "value"})
	if !r.Success {
		t.Error("OK should set Success=true")
	}
	if r.Tool != "test" {
		t.Errorf("expected tool 'test', got %q", r.Tool)
	}
	if r.Data["key"] != "value" {
		t.Error("expected data to be preserved")
	}
}

func TestResult_Fail(t *testing.T) {
	r := Fail("test", context.DeadlineExceeded)
	if r.Success {
		t.Error("Fail should set Success=false")
	}
	if !strings.Contains(r.Error, "context deadline exceeded") {
		t.Errorf("expected deadline error, got %q", r.Error)
	}
}

func TestResult_FailMsg(t *testing.T) {
	r := FailMsg("test", "custom error")
	if r.Success {
		t.Error("FailMsg should set Success=false")
	}
	if r.Error != "custom error" {
		t.Errorf("expected 'custom error', got %q", r.Error)
	}
}

func TestResult_CommandResult(t *testing.T) {
	r := CommandResult("test", "go build ./...", 0, "ok", "", 0, 0)
	if !r.Success {
		t.Error("exit code 0 should be success")
	}
	if r.Stdout != "ok" {
		t.Errorf("expected stdout 'ok', got %q", r.Stdout)
	}

	r2 := CommandResult("test", "go build ./...", 1, "", "error message", 0, 0)
	if r2.Success {
		t.Error("exit code 1 should be failure")
	}
	if r2.ExitCode == nil || *r2.ExitCode != 1 {
		t.Error("expected exit code 1")
	}
}

func TestResult_Clip(t *testing.T) {
	// Short output should not be truncated
	r := CommandResult("test", "cmd", 0, "ok", "ok", 0, 100)
	if r.Truncated {
		t.Error("short output should not be truncated")
	}

	// Long output with large cap should not be truncated
	long := strings.Repeat("x", 50)
	r2 := CommandResult("test", "cmd", 0, long, long, 0, 100)
	if r2.Truncated {
		t.Error("50-byte output with 100-byte cap should not be truncated")
	}

	// Long output exceeding cap should be truncated
	longer := strings.Repeat("x", 200)
	r3 := CommandResult("test", "cmd", 0, longer, longer, 0, 100)
	if !r3.Truncated {
		t.Error("200-byte output with 100-byte cap should be truncated")
	}
}

func TestRegistry_Register(t *testing.T) {
	reg := NewRegistry()
	reg.Register(
		&mockTool{name: "tool-a"},
		&mockTool{name: "tool-b"},
		&mockTool{name: "tool-a"},
		nil,
	)
	if len(reg.tools) != 2 {
		t.Errorf("expected 2 tools, got %d", len(reg.tools))
	}
}

func TestRegistry_Get(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "tool-a"})
	tool, ok := reg.Get("tool-a")
	if !ok {
		t.Error("expected to find tool-a")
	} else if tool.Name() != "tool-a" {
		t.Errorf("expected tool-a, got %s", tool.Name())
	}
	if _, ok := reg.Get("nonexistent"); ok {
		t.Error("expected not to find nonexistent tool")
	}
}

func TestRegistry_Names(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "z-tool"}, &mockTool{name: "a-tool"})
	names := reg.Names()
	if len(names) != 2 {
		t.Fatalf("expected 2 names, got %d", len(names))
	}
	if names[0] != "a-tool" || names[1] != "z-tool" {
		t.Errorf("expected sorted names, got %v", names)
	}
}

func TestRegistry_ForMode(t *testing.T) {
	reg := NewRegistry()
	reg.Register(
		&mockTool{name: "all-modes", modes: nil},
		&mockTool{name: "explore-only", modes: []string{ModeExplore}},
		&mockTool{name: "multi", modes: []string{ModeExplore, ModePlan}},
	)
	tools := reg.ForMode(ModeExplore)
	if len(tools) != 3 {
		t.Errorf("Explore should see all 3 tools, got %d", len(tools))
	}
	tools = reg.ForMode(ModeImplement)
	if len(tools) != 1 {
		t.Errorf("Implement should see 1 universal tool, got %d", len(tools))
	}
}

func TestRegistry_Mutating(t *testing.T) {
	reg := NewRegistry()
	reg.Register(
		&mockTool{name: "read", mutates: false},
		&mockTool{name: "write", mutates: true},
		&mockTool{name: "execute", mutates: true},
	)
	mut := reg.Mutating(ModeImplement)
	if len(mut) != 2 {
		t.Errorf("expected 2 mutating tools, got %d", len(mut))
	}
}

func TestRegistry_Invoke(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "echo", response: Result{Tool: "echo", Success: true, Data: map[string]any{"echoed": true}}})

	r := reg.Invoke(context.Background(), "echo", NewArgs(nil))
	if !r.Success {
		t.Error("expected success")
	}
	if r.Tool != "echo" {
		t.Errorf("expected tool name 'echo', got %q", r.Tool)
	}

	r2 := reg.Invoke(context.Background(), "nonexistent", NewArgs(nil))
	if r2.Success {
		t.Error("unknown tool should fail")
	}
	if !strings.Contains(r2.Error, "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got %q", r2.Error)
	}
}

func TestRegistry_CatalogueFor(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{
		name:        "list",
		description: "list files",
		mutates:     false,
		params: []Param{
			{Name: "path", Type: "string", Required: true},
			{Name: "recursive", Type: "boolean", Required: false},
		},
	})

	cat := reg.CatalogueFor(ModeExplore)
	if cat.Mode != ModeExplore {
		t.Errorf("expected mode 'explore', got %q", cat.Mode)
	}
	if len(cat.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(cat.Tools))
	}
	e := cat.Tools[0]
	if e.Name != "list" {
		t.Errorf("expected 'list', got %q", e.Name)
	}
	if e.Mutates {
		t.Error("expected Mutates=false")
	}
	if len(e.Required) != 1 || e.Required[0] != "path" {
		t.Errorf("expected required=[path], got %v", e.Required)
	}
}

func TestRegistry_RenderCatalogue(t *testing.T) {
	reg := NewRegistry()
	reg.Register(&mockTool{name: "test-tool", params: []Param{{Name: "x", Type: "string"}}})

	rendered := reg.RenderCatalogue(ModePlan)
	if !strings.Contains(rendered, "test-tool") {
		t.Error("expected catalogue to contain tool name")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(rendered), &parsed); err != nil {
		t.Fatalf("catalogue should be valid JSON: %v", err)
	}
}

func TestValidMode(t *testing.T) {
	modes := []string{ModeExplore, ModePlan, ModeImplement, ModeDebug, ModeTest, ModeReview, ModeRefactor, ModeExplain}
	for _, m := range modes {
		if !ValidMode(m) {
			t.Errorf("expected %q to be valid", m)
		}
	}
	if ValidMode("invalid") {
		t.Error("expected 'invalid' to be invalid")
	}
}

type mockTool struct {
	name        string
	description string
	params      []Param
	modes       []string
	mutates     bool
	response    Result
}

func (m *mockTool) Name() string        { return m.name }
func (m *mockTool) Description() string { return m.description }
func (m *mockTool) Params() []Param     { return m.params }
func (m *mockTool) Modes() []string     { return m.modes }
func (m *mockTool) Mutates() bool       { return m.mutates }
func (m *mockTool) Run(ctx context.Context, args Args) Result {
	if m.response.Tool == "" {
		m.response.Tool = m.name
	}
	return m.response
}
