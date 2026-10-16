package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OllamaBackend speaks Ollama's native API.
//
// A separate adapter from the OpenAI-compatible one, rather than configuring the
// OpenAI adapter against Ollama's /v1 shim, because the native API exposes things
// the shim does not: /api/tags carries real model metadata including size and
// quantisation, /api/ps reports which models are currently resident, and
// /api/embed works for models the shim refuses. Inventory is most of what this
// agent needs from a model backend, so the richer endpoint is worth the extra
// code.
type OllamaBackend struct {
	name       string
	baseURL    string
	model      string
	client     *http.Client
	keepAlive  string
}

// OllamaOptions configures an Ollama adapter.
type OllamaOptions struct {
	Name       string
	BaseURL    string
	Model      string
	TimeoutSec int
	KeepAlive  string
}

// NewOllamaBackend builds an adapter for a local Ollama server.
func NewOllamaBackend(opts OllamaOptions) *OllamaBackend {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:11434"
	}
	timeout := opts.TimeoutSec
	if timeout <= 0 {
		timeout = 900
	}
	name := opts.Name
	if name == "" {
		name = "ollama"
	}
	keepAlive := opts.KeepAlive
	if keepAlive == "" {
		// Ollama unloads a model after five idle minutes by default. For an agent
		// loop with pauses for compilation, a longer residency avoids paying a
		// multi-minute load twice in one task.
		keepAlive = "30m"
	}
	return &OllamaBackend{
		name:      name,
		baseURL:   base,
		model:     opts.Model,
		client:    &http.Client{Timeout: time.Duration(timeout) * time.Second},
		keepAlive: keepAlive,
	}
}

func (b *OllamaBackend) Name() string { return b.name }
func (b *OllamaBackend) Kind() string { return "ollama" }

// DefaultModel reports the configured model.
func (b *OllamaBackend) DefaultModel() string { return b.model }

// SetModel overrides the model used for requests.
func (b *OllamaBackend) SetModel(m string) { b.model = m }

type ollamaChatRequest struct {
	Model    string         `json:"model"`
	Messages []oaiMessage   `json:"messages"`
	Stream   bool           `json:"stream"`
	Format   string         `json:"format,omitempty"`
	Options  map[string]any `json:"options,omitempty"`
	KeepAlive string        `json:"keep_alive,omitempty"`
}

type ollamaChatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"message"`
	Done            bool   `json:"done"`
	DoneReason      string `json:"done_reason"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	Error           string `json:"error"`
}

// Chat performs a completion against /api/chat.
func (b *OllamaBackend) Chat(ctx context.Context, req Request) (Response, error) {
	if len(req.Messages) == 0 {
		return Response{}, fmt.Errorf("llm/ollama: empty message list")
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	if model == "" {
		return Response{}, fmt.Errorf("%w: ollama backend has no model configured", ErrNoModel)
	}

	body := ollamaChatRequest{
		Model:     model,
		Stream:    false,
		KeepAlive: b.keepAlive,
	}
	if req.JSONMode {
		body.Format = "json"
	}
	opts := map[string]any{}
	if req.Temperature > 0 {
		opts["temperature"] = req.Temperature
	}
	if req.TopP > 0 {
		opts["top_p"] = req.TopP
	}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if req.Seed > 0 {
		opts["seed"] = req.Seed
	}
	if len(req.Stop) > 0 {
		opts["stop"] = req.Stop
	}
	if len(opts) > 0 {
		body.Options = opts
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, oaiMessage{Role: m.Role, Content: m.Content})
	}

	data, err := json.Marshal(body)
	if err != nil {
		return Response{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/api/chat", bytes.NewReader(data))
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("llm/ollama: %s: %w", b.baseURL, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return Response{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, fmt.Errorf("llm/ollama: status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	var out ollamaChatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return Response{}, fmt.Errorf("llm/ollama: decode: %w", err)
	}
	if out.Error != "" {
		return Response{}, fmt.Errorf("llm/ollama: %s", out.Error)
	}
	return Response{
		Content:          out.Message.Content,
		Model:            firstNonEmpty(out.Model, model),
		Backend:          b.name,
		FinishReason:     out.DoneReason,
		PromptTokens:     out.PromptEvalCount,
		CompletionTokens: out.EvalCount,
		Duration:         time.Since(start),
	}, nil
}

// Embed calls /api/embed.
func (b *OllamaBackend) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return EmbedResponse{}, nil
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	if model == "" {
		return EmbedResponse{}, fmt.Errorf("%w: ollama backend has no embedding model configured", ErrUnsupported)
	}
	payload := map[string]any{
		"model":      model,
		"input":      req.Inputs,
		"keep_alive": b.keepAlive,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/api/embed", bytes.NewReader(data))
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("llm/ollama: embed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return EmbedResponse{}, fmt.Errorf("%w: ollama /api/embed unavailable", ErrUnsupported)
	}
	var out struct {
		Model      string      `json:"model"`
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EmbedResponse{}, fmt.Errorf("llm/ollama: decode embed: %w", err)
	}
	if len(out.Embeddings) == 0 {
		return EmbedResponse{}, ErrUnsupported
	}
	return EmbedResponse{Model: out.Model, Vectors: out.Embeddings}, nil
}

// ListModels reads /api/tags, which reports installed models with sizes.
func (b *OllamaBackend) ListModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm/ollama: /api/tags status %d", resp.StatusCode)
	}
	var out struct {
		Models []struct {
			Name       string `json:"name"`
			Model      string `json:"model"`
			Size       int64  `json:"size"`
			ModifiedAt string `json:"modified_at"`
			Details    struct {
				Family            string `json:"family"`
				ParameterSize     string `json:"parameter_size"`
				QuantizationLevel string `json:"quantization_level"`
			} `json:"details"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm/ollama: decode tags: %w", err)
	}
	models := make([]ModelInfo, 0, len(out.Models))
	for _, m := range out.Models {
		id := firstNonEmpty(m.Name, m.Model)
		models = append(models, ModelInfo{
			ID:        id,
			Backend:   b.name,
			SizeBytes: m.Size,
			Family:    m.Details.Family,
			Params:    m.Details.ParameterSize,
			Quant:     m.Details.QuantizationLevel,
			Source:    "ollama",
		})
	}
	return models, nil
}

// Health probes /api/tags and /api/ps. /api/ps is asked because it distinguishes
// "installed" from "loaded", which decides whether the first request will be fast
// or will block for minutes while weights are paged in.
func (b *OllamaBackend) Health(ctx context.Context) Health {
	start := time.Now()
	h := Health{Backend: b.name, Kind: b.Kind()}
	models, err := b.ListModels(ctx)
	if err != nil {
		h.Detail = err.Error()
		h.Latency = time.Since(start)
		return h
	}
	h.Reachable = true
	h.ModelCount = len(models)
	h.Latency = time.Since(start)
	if len(models) > 0 {
		h.ModelLoaded = models[0].ID
	}
	if ps, perr := b.loadedModels(ctx); perr == nil && len(ps) > 0 {
		h.Detail = "loaded: " + strings.Join(ps, ", ")
		h.ModelLoaded = ps[0]
	}
	return h
}

func (b *OllamaBackend) loadedModels(ctx context.Context) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(out.Models))
	for _, m := range out.Models {
		names = append(names, m.Name)
	}
	return names, nil
}