// Package llm is the agent's model layer: a backend interface, adapters for the
// local inference servers found on this machine, a role router, and an inventory
// of what is actually available.
//
// The layer is deliberately narrow. The agent needs exactly four things from a
// model: chat completion, embedding, listing models, and a health probe. Anything
// a specific server offers beyond that is reached through a backend-specific
// option rather than through the interface, because widening the interface to
// accommodate one server would force every future backend to implement that
// server's quirks.
package llm

import (
	"context"
	"errors"
	"time"
)

// Sentinel errors. Callers distinguish "no backend" from "backend failed",
// because the first is a configuration problem the operator must fix and the
// second is a runtime condition the agent may retry.
var (
	ErrNoBackend = errors.New("llm: no available backend")
	ErrNoModel   = errors.New("llm: no model available for request")
)

// Role is a named job for a model. Roles exist so different jobs can use
// different models: planning tolerates a small model, reviewing does not.
type Role = string

// Message is one turn of a conversation.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// System and user role constants. Kept as constants because string literals
// scattered across prompts are how role names start drifting.
const (
	RoleSystem = "system"
	RoleUser   = "user"
	RoleAssistant = "assistant"
)

// Request is a chat completion request.
type Request struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	TopP        float64   `json:"top_p,omitempty"`
	Stop        []string  `json:"stop,omitempty"`

	// JSONMode asks the backend to constrain output to a JSON object where the
	// server supports it. Servers that do not support it ignore the hint; the
	// parser in this package copes with prose-wrapped JSON either way.
	JSONMode bool `json:"json_mode,omitempty"`

	// Seed makes a request reproducible where the backend supports it. Used by
	// tests and by retry loops that need a deterministic baseline to differ from.
	Seed int `json:"seed,omitempty"`
}

// Response is a chat completion result.
type Response struct {
	Content          string        `json:"content"`
	Model            string        `json:"model"`
	Backend          string        `json:"backend"`
	FinishReason     string        `json:"finish_reason"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	Duration         time.Duration `json:"duration"`
	Attempts         int           `json:"attempts"`
}

// ModelInfo describes one model a backend can serve.
type ModelInfo struct {
	ID        string `json:"id"`
	Backend   string `json:"backend"`
	SizeBytes int64  `json:"size_bytes,omitempty"`
	Family    string `json:"family,omitempty"`
	Params    string `json:"params,omitempty"`
	Quant     string `json:"quant,omitempty"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source"`
	Notes     string `json:"notes,omitempty"`
}

// EmbedRequest asks a backend for vectors.
type EmbedRequest struct {
	Model  string
	Inputs []string
}

// EmbedResponse carries vectors aligned with EmbedRequest.Inputs.
type EmbedResponse struct {
	Model   string
	Vectors [][]float32
}

// Health is the result of probing a backend.
type Health struct {
	Backend     string        `json:"backend"`
	Reachable   bool          `json:"reachable"`
	Detail      string        `json:"detail,omitempty"`
	Latency     time.Duration `json:"latency"`
	ModelCount  int           `json:"model_count"`
	Kind        string        `json:"kind"`
	ModelLoaded string        `json:"model_loaded,omitempty"`
}

// Backend is one inference server adapter.
type Backend interface {
	// Name is the configured backend name.
	Name() string

	// Kind is the protocol family: ollama, openai, mock.
	Kind() string

	// Chat performs one completion.
	Chat(ctx context.Context, req Request) (Response, error)

	// Embed returns one vector per input. A backend that cannot embed returns
	// ErrUnsupported rather than a fake vector: a fabricated embedding is worse
	// than no embedding, because it silently corrupts retrieval ranking.
	Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error)

	// ListModels reports what this backend can serve.
	ListModels(ctx context.Context) ([]ModelInfo, error)

	// Health probes reachability.
	Health(ctx context.Context) Health
}

// ErrUnsupported is returned by an optional capability a backend does not have.
var ErrUnsupported = errors.New("llm: capability not supported by backend")

// IsUnsupported reports whether an error is the unsupported-capability sentinel.
func IsUnsupported(err error) bool { return errors.Is(err, ErrUnsupported) }