package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// OpenAIBackend speaks the OpenAI-compatible HTTP API.
//
// This one adapter covers llama.cpp's llama-server, LM Studio, vLLM, Ollama's
// /v1 compatibility layer, text-generation-webui, and anything else that
// implements /v1/chat/completions. That coverage is the reason it exists: on this
// machine there are at least three servers that speak it, and three adapters for
// the same wire format would be three places for the same bug.
type OpenAIBackend struct {
	name     string
	baseURL  string
	apiKey   string
	model    string
	client   *http.Client
	chatPath string
}

// OpenAIOptions configures an OpenAI-compatible backend.
type OpenAIOptions struct {
	Name       string
	BaseURL    string
	APIKey     string
	Model      string
	TimeoutSec int
}

// NewOpenAIBackend builds an adapter. BaseURL may or may not already include the
// /v1 suffix; both forms are accepted because servers and their documentation
// disagree about which half the path belongs to.
func NewOpenAIBackend(opts OpenAIOptions) *OpenAIBackend {
	base := strings.TrimRight(opts.BaseURL, "/")
	if base == "" {
		base = "http://127.0.0.1:8090"
	}
	if !strings.HasSuffix(base, "/v1") && !strings.Contains(base, "/v1/") {
		base += "/v1"
	}
	timeout := opts.TimeoutSec
	if timeout <= 0 {
		timeout = 900
	}
	name := opts.Name
	if name == "" {
		name = "openai"
	}
	return &OpenAIBackend{
		name:     name,
		baseURL:  base,
		apiKey:   opts.APIKey,
		model:    opts.Model,
		client:   &http.Client{Timeout: time.Duration(timeout) * time.Second},
		chatPath: "/chat/completions",
	}
}

func (b *OpenAIBackend) Name() string { return b.name }
func (b *OpenAIBackend) Kind() string { return "openai" }

// DefaultModel reports the configured model, which for a llama.cpp server is
// whatever weights it was launched with and is often empty.
func (b *OpenAIBackend) DefaultModel() string { return b.model }

// SetModel overrides the model sent in requests.
func (b *OpenAIBackend) SetModel(m string) { b.model = m }

type oaiMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type oaiRequest struct {
	Model       string       `json:"model"`
	Messages    []oaiMessage `json:"messages"`
	Temperature float64      `json:"temperature,omitempty"`
	MaxTokens   int          `json:"max_tokens,omitempty"`
	TopP        float64      `json:"top_p,omitempty"`
	Stop        []string     `json:"stop,omitempty"`
	Stream      bool         `json:"stream"`
	Seed        int          `json:"seed,omitempty"`
	// response_format is only sent when JSONMode is set, because several
	// llama.cpp builds reject the field outright rather than ignoring it.
	ResponseFormat *oaiResponseFormat `json:"response_format,omitempty"`
}

type oaiResponseFormat struct {
	Type string `json:"type"`
}

type oaiResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Index        int `json:"index"`
		Message      struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		Text      string `json:"text"`
		Delta     struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// Chat performs a completion, falling back to /v1/completions.
//
// Some minimal OpenAI-compatible servers expose only the legacy completions
// endpoint. Requesting /chat/completions and retrying against /completions turns
// an integration failure into a working one, and the retry costs one round trip
// on a server that already rejected the first request.
func (b *OpenAIBackend) Chat(ctx context.Context, req Request) (Response, error) {
	if len(req.Messages) == 0 {
		return Response{}, fmt.Errorf("llm/openai: empty message list")
	}

	model := req.Model
	if model == "" {
		model = b.model
	}

	body := oaiRequest{
		Model:       model,
		Temperature: req.Temperature,
		MaxTokens:   req.MaxTokens,
		TopP:        req.TopP,
		Stop:        req.Stop,
		Seed:        req.Seed,
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, oaiMessage{Role: m.Role, Content: m.Content})
	}
	if req.JSONMode {
		body.ResponseFormat = &oaiResponseFormat{Type: "json_object"}
	}

	resp, err := b.post(ctx, b.chatPath, body)
	if err != nil {
		if errors_IsUnsupportedEndpoint(err) {
			return b.completionsFallback(ctx, req, model)
		}
		return Response{}, err
	}

	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("llm/openai: response contained no choices (model=%q)", model)
	}
	choice := resp.Choices[0]
	content := choice.Message.Content
	if content == "" {
		content = choice.Text
	}
	if content == "" {
		content = choice.Delta.Content
	}
	return Response{
		Content:          content,
		Model:            firstNonEmpty(resp.Model, model),
		Backend:          b.name,
		FinishReason:     choice.FinishReason,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}, nil
}

// completionsFallback flattens the conversation into a single prompt for servers
// that only implement /v1/completions.
func (b *OpenAIBackend) completionsFallback(ctx context.Context, req Request, model string) (Response, error) {
	var prompt strings.Builder
	for _, m := range req.Messages {
		switch m.Role {
		case RoleSystem:
			prompt.WriteString("System: ")
		case RoleAssistant:
			prompt.WriteString("Assistant: ")
		default:
			prompt.WriteString("User: ")
		}
		prompt.WriteString(m.Content)
		prompt.WriteString("\n\n")
	}
	prompt.WriteString("Assistant:")

	body := map[string]any{
		"model":       model,
		"prompt":      prompt.String(),
		"temperature": req.Temperature,
		"max_tokens":  req.MaxTokens,
		"stop":        req.Stop,
		"stream":      false,
	}

	resp, err := b.postAny(ctx, "/completions", body)
	if err != nil {
		return Response{}, err
	}
	if len(resp.Choices) == 0 {
		return Response{}, fmt.Errorf("llm/openai: /completions returned no choices")
	}
	return Response{
		Content:          resp.Choices[0].Text,
		Model:            firstNonEmpty(resp.Model, model),
		Backend:          b.name,
		FinishReason:     resp.Choices[0].FinishReason,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
	}, nil
}

func (b *OpenAIBackend) post(ctx context.Context, path string, body any) (*oaiResponse, error) {
	return b.postAny(ctx, path, body)
}

func (b *OpenAIBackend) postAny(ctx context.Context, path string, body any) (*oaiResponse, error) {
	data, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("llm/openai: encode request: %w", err)
	}
	url := b.baseURL + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("llm/openai: build request: %w", err)
	}
	b.setHeaders(req)

	resp, err := b.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("llm/openai: %s (%s): %w", b.name, url, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, fmt.Errorf("llm/openai: read response: %w", err)
	}
	var out oaiResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("llm/openai: status %d: %s", resp.StatusCode, truncate(string(raw), 400))
		}
		return nil, fmt.Errorf("llm/openai: decode response: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, fmt.Errorf("llm/openai: server error: %s", out.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm/openai: status %d: %s", resp.StatusCode, truncate(string(raw), 400))
	}
	return &out, nil
}

func (b *OpenAIBackend) setHeaders(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	if b.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+b.apiKey)
	}
}

// Embed requests vectors. llama.cpp only serves this from a dedicated embedding
// model, so an empty model or a server rejection both map to ErrUnsupported.
func (b *OpenAIBackend) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if len(req.Inputs) == 0 {
		return EmbedResponse{}, nil
	}
	model := req.Model
	if model == "" {
		model = b.model
	}
	if model == "" {
		return EmbedResponse{}, fmt.Errorf("%w: openai backend has no embedding model configured", ErrUnsupported)
	}
	body := map[string]any{"model": model, "input": req.Inputs}
	data, err := json.Marshal(body)
	if err != nil {
		return EmbedResponse{}, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+"/embeddings", bytes.NewReader(data))
	if err != nil {
		return EmbedResponse{}, err
	}
	b.setHeaders(httpReq)
	resp, err := b.client.Do(httpReq)
	if err != nil {
		return EmbedResponse{}, fmt.Errorf("llm/openai: embeddings: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadRequest {
		return EmbedResponse{}, fmt.Errorf("%w: server does not expose /v1/embeddings", ErrUnsupported)
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return EmbedResponse{}, fmt.Errorf("llm/openai: decode embeddings: %w", err)
	}
	if len(out.Data) == 0 {
		return EmbedResponse{}, ErrUnsupported
	}
	// Order by index: servers are permitted to return rows out of order, and
	// silently misaligning vectors would corrupt retrieval without erroring.
	ordered := make([][]float32, len(req.Inputs))
	seen := make([]bool, len(req.Inputs))
	for _, d := range out.Data {
		if d.Index < 0 || d.Index >= len(ordered) || seen[d.Index] {
			continue
		}
		ordered[d.Index] = d.Embedding
		seen[d.Index] = true
	}
	for i, ok := range seen {
		if !ok {
			return EmbedResponse{}, fmt.Errorf("llm/openai: embedding %d missing from response", i)
		}
	}
	return EmbedResponse{Model: model, Vectors: ordered}, nil
}

// ListModels calls /v1/models.
func (b *OpenAIBackend) ListModels(ctx context.Context) ([]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.baseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	b.setHeaders(req)
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("llm/openai: /models status %d", resp.StatusCode)
	}
	var out struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("llm/openai: decode /models: %w", err)
	}
	models := make([]ModelInfo, 0, len(out.Data))
	for _, m := range out.Data {
		models = append(models, ModelInfo{ID: m.ID, Backend: b.name, Family: m.OwnedBy, Source: "openai-api"})
	}
	return models, nil
}

// Health probes /v1/models, which every conforming server implements.
func (b *OpenAIBackend) Health(ctx context.Context) Health {
	start := time.Now()
	h := Health{Backend: b.name, Kind: b.Kind()}
	models, err := b.ListModels(ctx)
	h.Latency = time.Since(start)
	if err != nil {
		h.Detail = err.Error()
		return h
	}
	h.Reachable = true
	h.ModelCount = len(models)
	if len(models) == 1 {
		h.ModelLoaded = models[0].ID
	}
	if len(models) > 0 {
		h.ModelLoaded = models[0].ID
	}
	return h
}

// errors_IsUnsupportedEndpoint recognises the "this endpoint does not exist"
// family of HTTP failures, which is what signals a retry against /completions.
func errors_IsUnsupportedEndpoint(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range []string{"status 404", "status 405", "no endpoint", "not found", "unsupported"} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// ScanNDJSONResponse reads a server-sent-event or newline-delimited JSON stream
// and concatenates delta content.
//
// Streaming is not the default because a streamed response cannot be retried
// safely and cannot be validated before it is parsed, but some servers refuse
// non-streaming requests. This reader exists so such a server can still be used
// rather than reported as unsupported.
func ScanNDJSONResponse(body io.Reader) (string, error) {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 0, 64*1024), 8<<20)
	var out strings.Builder
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || line == "[DONE]" {
			continue
		}
		line = strings.TrimPrefix(line, "data:")
		line = strings.TrimSpace(line)
		var chunk oaiResponse
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		for _, c := range chunk.Choices {
			if c.Delta.Content != "" {
				out.WriteString(c.Delta.Content)
			} else if c.Message.Content != "" {
				out.WriteString(c.Message.Content)
			} else if c.Text != "" {
				out.WriteString(c.Text)
			}
		}
	}
	return out.String(), scanner.Err()
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "... (truncated)"
}