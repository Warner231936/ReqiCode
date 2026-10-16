package llm

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestMockBackend_BasicChat(t *testing.T) {
	mb := NewMockBackend("test-mock")
	mb.Rule("hello", `{"thought":"greeting received","action":"final","summary":"replied"}`)

	resp, err := mb.Chat(context.Background(), Request{
		Messages: []Message{
			{Role: RoleUser, Content: "hello"},
		},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != `{"thought":"greeting received","action":"final","summary":"replied"}` {
		t.Errorf("unexpected content: %q", resp.Content)
	}
	if resp.Model != "" && resp.Model != "mock-model" {
		t.Errorf("expected model to be set, got %q", resp.Model)
	}
	if resp.Backend != "test-mock" {
		t.Errorf("expected backend 'test-mock', got %q", resp.Backend)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish reason 'stop', got %q", resp.FinishReason)
	}
	if mb.Calls() != 1 {
		t.Errorf("expected 1 call, got %d", mb.Calls())
	}
}

func TestMockBackend_NoRuleDefault(t *testing.T) {
	mb := NewMockBackend("default-test")

	resp, err := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "anything"}},
	})
	if err != nil {
		t.Fatalf("Chat returned error: %v", err)
	}
	if resp.Content != `{"thought":"no scripted response","action":"final","summary":"nothing to do"}` {
		t.Errorf("unexpected default content: %q", resp.Content)
	}
}

func TestMockBackend_RuleMatching(t *testing.T) {
	mb := NewMockBackend("matcher")
	mb.Rule("create", `{"action":"create"}`)
	mb.Rule("fix", `{"action":"fix"}`)

	resp1, _ := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "please create a function"}},
	})
	if !strings.Contains(resp1.Content, `"create"`) {
		t.Errorf("expected 'create' response, got %q", resp1.Content)
	}

	resp2, _ := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "fix the bug"}},
	})
	if !strings.Contains(resp2.Content, `"fix"`) {
		t.Errorf("expected 'fix' response, got %q", resp2.Content)
	}
}

func TestMockBackend_RuleOnceConsumed(t *testing.T) {
	mb := NewMockBackend("once-test")
	mb.RuleOnce("alpha", `{"action":"alpha"}`)
	mb.Rule("", `{"action":"fallback"}`)

	resp1, _ := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "alpha request"}},
	})
	if !strings.Contains(resp1.Content, `"alpha"`) {
		t.Errorf("first call should match RuleOnce, got %q", resp1.Content)
	}

	resp2, _ := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "beta request"}},
	})
	if !strings.Contains(resp2.Content, `"fallback"`) {
		t.Errorf("second call should fall through to empty-match rule, got %q", resp2.Content)
	}
}

func TestMockBackend_FailFirst(t *testing.T) {
	mb := NewMockBackend("fail-test")
	mb.FailFirst(2)
	mb.Rule("all", `{"action":"all"}`)

	_, err1 := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "all"}},
	})
	if err1 == nil {
		t.Error("expected error on first call")
	}

	_, err2 := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "all"}},
	})
	if err2 == nil {
		t.Error("expected error on second call")
	}

	resp3, err3 := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "all"}},
	})
	if err3 != nil {
		t.Fatalf("third call should succeed: %v", err3)
	}
	if !strings.Contains(resp3.Content, `"all"`) {
		t.Errorf("expected 'all' response, got %q", resp3.Content)
	}
}

func TestMockBackend_WithDelay(t *testing.T) {
	mb := NewMockBackend("delay-test")
	mb.WithDelay(50 * time.Millisecond)
	mb.Rule("x", `{"ok":true}`)

	start := time.Now()
	_, err := mb.Chat(context.Background(), Request{
		Messages: []Message{{Role: RoleUser, Content: "x"}},
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed < 40*time.Millisecond {
		t.Errorf("expected at least 40ms delay, got %v", elapsed)
	}
}

func TestMockBackend_DelayContextCancel(t *testing.T) {
	mb := NewMockBackend("cancel-test")
	mb.WithDelay(10 * time.Second)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	_, err := mb.Chat(ctx, Request{
		Messages: []Message{{Role: RoleUser, Content: "anything"}},
	})
	if err == nil {
		t.Error("expected context cancellation error")
	}
}

func TestMockBackend_Embed(t *testing.T) {
	mb := NewMockBackend("embed-test")
	resp, err := mb.Embed(context.Background(), EmbedRequest{
		Inputs: []string{"hello", "world"},
	})
	if err != nil {
		t.Fatalf("Embed returned error: %v", err)
	}
	if len(resp.Vectors) != 2 {
		t.Errorf("expected 2 vectors, got %d", len(resp.Vectors))
	}
	for i, v := range resp.Vectors {
		if len(v) != 64 {
			t.Errorf("vector %d: expected dim 64, got %d", i, len(v))
		}
	}
}

func TestMockBackend_ListModels(t *testing.T) {
	mb := NewMockBackend("list-test")
	models, err := mb.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels returned error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	if models[0].Backend != "list-test" {
		t.Errorf("expected backend 'list-test', got %q", models[0].Backend)
	}
}

func TestMockBackend_Health(t *testing.T) {
	mb := NewMockBackend("health-test")
	h := mb.Health(context.Background())
	if !h.Reachable {
		t.Error("expected reachable to be true")
	}
	if h.Kind != "mock" {
		t.Errorf("expected kind 'mock', got %q", h.Kind)
	}
	if h.Backend != "health-test" {
		t.Errorf("expected backend 'health-test', got %q", h.Backend)
	}
	if h.ModelCount != 2 {
		t.Errorf("expected 2 models, got %d", h.ModelCount)
	}
}

func TestMockBackend_NameAndKind(t *testing.T) {
	mb := NewMockBackend("")
	if mb.Name() != "mock" {
		t.Errorf("expected name 'mock' for empty input, got %q", mb.Name())
	}
	if mb.Kind() != "mock" {
		t.Errorf("expected kind 'mock', got %q", mb.Kind())
	}

	mb2 := NewMockBackend("custom")
	if mb2.Name() != "custom" {
		t.Errorf("expected name 'custom', got %q", mb2.Name())
	}
}
