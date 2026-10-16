package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// MockBackend is a deterministic, scriptable backend.
//
// It exists for three real reasons, not as a test stub: it lets the agent's own
// tests run without a 3GB model and a GPU; it lets the loop be exercised with
// deliberately hostile responses (empty output, malformed JSON, wrong tool name)
// to prove the failure paths work; and it keeps the agent runnable when no model
// backend is reachable, in which case scripted plans are executed and every
// action is still verifiable.
//
// Responses are matched by substring. The first rule whose keyword appears in the
// system prompt wins, so a caller controls which reply a given prompt receives.
type MockBackend struct {
	name   string
	mu     sync.Mutex
	rules  []mockRule
	calls  int
	failN  int
	delay  time.Duration
}

type mockRule struct {
	match    string
	response string
	repeat   int
}

// NewMockBackend builds a deterministic backend with no scripted rules.
func NewMockBackend(name string) *MockBackend {
	if name == "" {
		name = "mock"
	}
	return &MockBackend{name: name}
}

// Rule adds a scripted reply triggered when the prompt contains match.
func (m *MockBackend) Rule(match, response string) *MockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append(m.rules, mockRule{match: match, response: response, repeat: -1})
	return m
}

// RuleOnce adds a scripted reply that is consumed by the first matching prompt.
func (m *MockBackend) RuleOnce(match, response string) *MockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rules = append(m.rules, mockRule{match: match, response: response, repeat: 1})
	return m
}

// FailFirst makes the next n calls fail, which is how retry behaviour is tested.
func (m *MockBackend) FailFirst(n int) *MockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failN = n
	return m
}

// WithDelay makes each call take d, for timeout testing.
func (m *MockBackend) WithDelay(d time.Duration) *MockBackend {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.delay = d
	return m
}

// Calls reports how many Chat calls have been made.
func (m *MockBackend) Calls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func (m *MockBackend) Name() string { return m.name }
func (m *MockBackend) Kind() string { return "mock" }

func (m *MockBackend) Chat(ctx context.Context, req Request) (Response, error) {
	m.mu.Lock()
	m.calls++
	fail := m.failN
	if fail > 0 {
		m.failN--
	}
	delay := m.delay
	prompt := flattenPrompt(req.Messages)
	var chosen string
	for i := range m.rules {
		r := &m.rules[i]
		if r.match != "" && !strings.Contains(prompt, r.match) {
			continue
		}
		if r.repeat == 0 {
			continue
		}
		chosen = r.response
		if r.repeat > 0 {
			r.repeat--
		}
		break
	}
	m.mu.Unlock()

	if delay > 0 {
		select {
		case <-ctx.Done():
			return Response{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if fail > 0 {
		return Response{}, fmt.Errorf("llm/mock: injected failure")
	}
	if chosen == "" {
		chosen = `{"thought":"no scripted response","action":"final","summary":"nothing to do"}`
	}
	model := req.Model
	if model == "" {
		model = "mock-model"
	}
	return Response{
		Content:          chosen,
		Model:            model,
		Backend:          m.name,
		FinishReason:     "stop",
		PromptTokens:     estimateTokens(prompt),
		CompletionTokens: estimateTokens(chosen),
	}, nil
}

func (m *MockBackend) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	vectors := make([][]float32, len(req.Inputs))
	for i, in := range req.Inputs {
		vectors[i] = hashEmbedding(in, 64)
	}
	return EmbedResponse{Model: "mock-embed", Vectors: vectors}, nil
}

func (m *MockBackend) ListModels(ctx context.Context) ([]ModelInfo, error) {
	return []ModelInfo{
		{ID: "mock-planner", Backend: m.name, Family: "scripted", Source: "mock", Notes: "deterministic"},
		{ID: "mock-coder", Backend: m.name, Family: "scripted", Source: "mock", Notes: "deterministic"},
	}, nil
}

func (m *MockBackend) Health(ctx context.Context) Health {
	return Health{
		Backend:    m.name,
		Kind:       m.Kind(),
		Reachable:  true,
		ModelCount: 2,
		Detail:     "scripted responses",
	}
}

func flattenPrompt(msgs []Message) string {
	var b strings.Builder
	for _, m := range msgs {
		b.WriteString(m.Role)
		b.WriteString(": ")
		b.WriteString(m.Content)
		b.WriteString("\n")
	}
	return b.String()
}

// estimateTokens is a rough token count. Exact counts need a tokenizer per model;
// an approximation is enough for budgeting and reporting, and it is honest about
// being an approximation because that is what it is.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	// ~4 characters per token is the usual English-text rule of thumb.
	n := len(s) / 4
	if n == 0 {
		n = 1
	}
	return n
}

// hashEmbedding maps text to a deterministic unit-length vector.
//
// It exists so retrieval can be tested end to end without an embedding model.
// Cosine similarity between two such vectors is a weak but real signal: text
// sharing a character bag scores higher than text that does not. It is not a
// semantic embedding and is never presented as one -- when a real embedding
// backend is available the context manager prefers it, and this is the fallback
// for when it is not.
func hashEmbedding(text string, dim int) []float32 {
	vec := make([]float32, dim)
	tokens := strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !(r == '_' || r == '.' || r == '/' || r == '\\' || r == '-' ||
			(r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'))
	})
	for _, tok := range tokens {
		h := fnv32(tok)
		vec[h%uint32(dim)] += 1
		// A second, differently-seeded bucket gives some discrimination between
		// tokens that collide in the first.
		vec[(h>>16)%uint32(dim)] += 0.5
	}
	normalize(vec)
	return vec
}

func fnv32(s string) uint32 {
	const (
		offset = 2166136261
		prime  = 16777619
	)
	h := uint32(offset)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime
	}
	return h
}

func normalize(v []float32) {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return
	}
	inv := float32(1 / sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
}

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	// Newton's method converges in a handful of iterations for these magnitudes
	// and avoids pulling in math for one call.
	for i := 0; i < 12; i++ {
		x = 0.5 * (x + f/x)
	}
	return x
}

// sortedIDs returns model IDs in a stable order, which keeps inventory output
// and test assertions deterministic.
func sortedIDs(models []ModelInfo) []string {
	out := make([]string, 0, len(models))
	for _, m := range models {
		out = append(out, m.ID)
	}
	sort.Strings(out)
	return out
}