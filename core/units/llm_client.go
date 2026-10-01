package units

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/routing"
)

// DefaultTimeout bounds every LLM call.
//
// Without this a single unresponsive provider hangs the entire spiral. The
// orchestrator's context has no deadline of its own, so an LLM call inherits
// unbounded patience -- observed as a ten-minute hang against a dead server.
//
// The value is deliberately short. Planning is a single structured response; if
// the model cannot produce one within this window something is wrong with the
// server or the model, and waiting longer converts a diagnostic failure into an
// unusable run. Callers expecting genuinely long work override it.
const DefaultTimeout = 90 * time.Second

// Token budgets per call shape.
//
// Code generation used to request 4096 tokens for every call. On a local model
// that is roughly eighty seconds of generation per file, and a model asked for
// more than it needs tends to fill the budget rather than stop. Planning needs
// far less than code, so the budgets are separated instead of sharing one
// generous number.
const (
	planningTokens = 1200
	codeTokens     = 2048
)

type LLMClient struct {
	router  *routing.Router
	timeout time.Duration
	usage   *UsageTracker

	// breaker short-circuits the provider after repeated failures.
	//
	// Without it a dead server costs one timeout per call, and code generation
	// asks once per planned component. Eleven components at a 90-second timeout is
	// sixteen minutes of a process that is provably going to fail every time. The
	// failure is knowable after the first or second attempt; the remaining cost is
	// pure waste.
	breaker *breaker
}

// breakerFailureThreshold is how many consecutive failures open the circuit.
const breakerFailureThreshold = 3

// breakerOpenCooldown is how long the circuit stays open before a probe is
// allowed through.
const breakerOpenCooldown = 30 * time.Second

type breaker struct {
	mu       sync.Mutex
	failures int
	openedAt time.Time
}

// allow reports whether a call may proceed, and records the outcome.
func (b *breaker) allow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures < breakerFailureThreshold {
		return true
	}
	return time.Since(b.openedAt) > breakerOpenCooldown
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures = 0
}

func (b *breaker) failure() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if b.failures == breakerFailureThreshold {
		b.openedAt = time.Now()
	}
}

// Open reports whether the circuit is currently open, for the usage report.
func (b *breaker) Open() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.failures >= breakerFailureThreshold && time.Since(b.openedAt) <= breakerOpenCooldown
}

// NewLLMClient creates a client with usage tracking enabled.
func NewLLMClient(router *routing.Router) *LLMClient {
	return &LLMClient{
		router:  router,
		timeout: DefaultTimeout,
		usage:   NewUsageTracker(),
		breaker: &breaker{},
	}
}

// WithTimeout overrides the per-call timeout.
func (c *LLMClient) WithTimeout(d time.Duration) *LLMClient {
	if d > 0 {
		c.timeout = d
	}
	return c
}

// Usage exposes the call record so a run can report whether the LLM was actually
// consulted or whether the pipeline silently degraded to templates.
func (c *LLMClient) Usage() *UsageTracker {
	return c.usage
}

// LastLLMCall reports the completion tokens of the most recent successful LLM
// call, or 0 if there was none. Used to attribute cost to the intervention that
// prompted the call, so a model's contribution appears in the ledger like any
// other change rather than costing nothing.
func (c *LLMClient) LastLLMCall() int {
	calls := c.usage.Calls()
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Err == nil {
			return calls[i].CompletionTokens
		}
	}
	return 0
}

// bounded returns a context with a deadline derived from the caller's context.
//
// Deriving from the parent rather than replacing it is deliberate: a caller that
// sets a shorter deadline must still be honoured.
func (c *LLMClient) bounded(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, hasDeadline := ctx.Deadline(); hasDeadline {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func (c *LLMClient) Generate(ctx context.Context, capability routing.ModelCapability, prompt string, maxTokens int, temperature float64) (string, error) {
	// Consult the breaker before resolving anything. A call that cannot possibly
	// succeed should cost microseconds, not a timeout.
	if !c.breaker.allow() {
		return "", fmt.Errorf("LLM circuit open: %d consecutive failures, provider is not being retried", breakerFailureThreshold)
	}

	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return "", fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		c.breaker.failure()
		return "", fmt.Errorf("provider %s is not available", p.Name())
	}

	callCtx, cancel := c.bounded(ctx)
	defer cancel()

	start := time.Now()
	resp, err := p.Generate(callCtx, provider.CompletionRequest{
		Model:       modelName,
		Messages:    []provider.Message{{Role: "user", Content: prompt}},
		Temperature: temperature,
		MaxTokens:   maxTokens,
	})
	elapsed := time.Since(start)

	record := Call{
		Capability:       capability,
		Provider:         p.Name(),
		Model:            modelName,
		Elapsed:          elapsed,
		Err:              err,
		PromptTokens:     resp.Usage.PromptTokens,
		CompletionTokens: resp.Usage.CompletionTokens,
		TotalTokens:      resp.Usage.TotalTokens,
		// A completion cut short by the token limit yields a plausible but
		// invalid plan, so it must be distinguishable from a complete one.
		Truncated: resp.FinishReason == "length",
	}
	c.usage.Record(record)

	if err != nil {
		c.breaker.failure()
		return "", fmt.Errorf("LLM generation failed after %s: %w", elapsed.Round(time.Millisecond), err)
	}

	c.breaker.success()
	return resp.Content, nil
}

func (c *LLMClient) Analyze(ctx context.Context, capability routing.ModelCapability, subject string, questions []string) (provider.AnalysisResult, error) {
	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return provider.AnalysisResult{}, fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		return provider.AnalysisResult{}, fmt.Errorf("provider %s is not available", p.Name())
	}

	callCtx, cancel := c.bounded(ctx)
	defer cancel()

	start := time.Now()
	res, err := p.Analyze(callCtx, provider.AnalysisRequest{
		Model:     modelName,
		Subject:   subject,
		Questions: questions,
	})
	c.usage.Record(Call{
		Capability: capability,
		Provider:   p.Name(),
		Model:      modelName,
		Elapsed:    time.Since(start),
		Err:        err,
	})
	return res, err
}

func (c *LLMClient) Review(ctx context.Context, capability routing.ModelCapability, code, filepath string, questions []string) (provider.ReviewResult, error) {
	p, modelName, ok := c.router.GetProvider(capability)
	if !ok {
		return provider.ReviewResult{}, fmt.Errorf("no provider available for capability %s", capability)
	}

	if !p.IsAvailable(ctx) {
		return provider.ReviewResult{}, fmt.Errorf("provider %s is not available", p.Name())
	}

	callCtx, cancel := c.bounded(ctx)
	defer cancel()

	start := time.Now()
	res, err := p.Review(callCtx, provider.ReviewRequest{
		Model:     modelName,
		Code:      code,
		Filepath:  filepath,
		Questions: questions,
	})
	c.usage.Record(Call{
		Capability: capability,
		Provider:   p.Name(),
		Model:      modelName,
		Elapsed:    time.Since(start),
		Err:        err,
	})
	return res, err
}

func (c *LLMClient) HasProvider(capability routing.ModelCapability) bool {
	_, _, ok := c.router.GetProvider(capability)
	return ok
}

func (c *LLMClient) IsProviderAvailable(ctx context.Context, capability routing.ModelCapability) bool {
	p, _, ok := c.router.GetProvider(capability)
	if !ok {
		return false
	}
	return p.IsAvailable(ctx)
}

// GeneratePlan asks for a structured planning response, which needs far fewer
// tokens than code and should finish quickly.
func (c *LLMClient) GeneratePlan(ctx context.Context, capability routing.ModelCapability, prompt string) (string, error) {
	return c.Generate(ctx, capability, prompt, planningTokens, 0.2)
}

// GenerateCode asks for source files.
//
// A separate entry point from Generate so the caller cannot accidentally request
// a token budget sized for a different kind of work. Planning and code
// generation failing for the same reason at wildly different costs was exactly
// the confusion the original shared budget produced.
func (c *LLMClient) GenerateCode(ctx context.Context, capability routing.ModelCapability, prompt string) (string, error) {
	return c.Generate(ctx, capability, prompt, codeTokens, 0.3)
}

func (c *LLMClient) GenerateReview(ctx context.Context, capability routing.ModelCapability, code, filepath string) (provider.ReviewResult, error) {
	return c.Review(ctx, capability, code, filepath, []string{
		"What are the bugs?",
		"Are there security issues?",
		"Is the code idiomatic Go?",
	})
}

// ---------------------------------------------------------------------------
// Usage tracking
// ---------------------------------------------------------------------------

// Call is one recorded LLM invocation.
type Call struct {
	Capability       routing.ModelCapability `json:"capability"`
	Provider         string                  `json:"provider"`
	Model            string                  `json:"model"`
	PromptTokens     int                     `json:"prompt_tokens"`
	CompletionTokens int                     `json:"completion_tokens"`
	TotalTokens      int                     `json:"total_tokens"`
	Elapsed          time.Duration           `json:"-"`
	ElapsedMS        int64                   `json:"elapsed_ms"`
	Err              error                   `json:"-"`
	Error            string                  `json:"error,omitempty"`
	// Truncated records whether the provider reported the completion was cut
	// short by the token limit. A truncated architecture plan is a plausible but
	// invalid plan, and it must be distinguishable from a complete one.
	Truncated bool `json:"truncated"`
}

// UsageReport summarises a run's LLM engagement.
type UsageReport struct {
	Calls            int            `json:"calls"`
	Successes        int            `json:"successes"`
	Failures         int            `json:"failures"`
	PromptTokens     int            `json:"prompt_tokens"`
	CompletionTokens int            `json:"completion_tokens"`
	TotalTokens      int            `json:"total_tokens"`
	TotalElapsedMS   int64          `json:"total_elapsed_ms"`
	ByCapability     map[string]int `json:"by_capability"`
	Models           []string       `json:"models"`
	Errors           []string       `json:"errors,omitempty"`
	// Degraded is true when the LLM was configured but never produced a usable
	// result, meaning the run fell back to templates. This is the single number
	// that answers "did the LLM actually do anything".
	Degraded bool `json:"degraded"`
	// NeverCalled is true when no LLM call was attempted at all.
	NeverCalled bool `json:"never_called"`
	// CircuitOpen records that repeated failures short-circuited the provider.
	// A run that reports all-green while the circuit was open has hidden a
	// wholesale fallback behind per-call successes.
	CircuitOpen bool `json:"circuit_open"`
	// ShortCircuited counts calls that were refused without being attempted.
	ShortCircuited int `json:"short_circuited"`
}

// UsageTracker records LLM calls across a run.
type UsageTracker struct {
	mu    sync.Mutex
	calls []Call
}

// NewUsageTracker creates an empty tracker.
func NewUsageTracker() *UsageTracker {
	return &UsageTracker{}
}

// Record appends a call.
func (t *UsageTracker) Record(c Call) {
	c.ElapsedMS = c.Elapsed.Milliseconds()
	if c.Err != nil {
		c.Error = c.Err.Error()
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.calls = append(t.calls, c)
}

// Calls returns a copy of the recorded calls.
func (t *UsageTracker) Calls() []Call {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Call(nil), t.calls...)
}

// TotalTokens sums completion cost across all calls. This is the figure the
// convergence tracker uses as a cost axis.
func (t *UsageTracker) TotalTokens() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	total := 0
	for _, c := range t.calls {
		total += c.CompletionTokens
	}
	return total
}

// UsageReportWith summarises engagement and folds in circuit state, which lives
// on the client rather than the tracker.
//
// A report that omits the circuit would show every attempted call as accounted
// for while the ones that mattered were never made.
func (c *LLMClient) Report() UsageReport {
	r := c.usage.Report()
	r.CircuitOpen = c.breaker.Open()
	if r.CircuitOpen {
		r.Degraded = true
	}
	return r
}

// Report summarises engagement on the tracker alone.
//
// Degraded is deliberately pessimistic: any failure marks the run degraded even
// if other calls succeeded, because a run that silently fell back for part of its
// work must not report itself as a clean LLM run.
func (t *UsageTracker) Report() UsageReport {
	t.mu.Lock()
	defer t.mu.Unlock()

	r := UsageReport{
		ByCapability: map[string]int{},
		Models:       []string{},
	}
	seenModel := map[string]bool{}

	for _, c := range t.calls {
		r.Calls++
		r.PromptTokens += c.PromptTokens
		r.CompletionTokens += c.CompletionTokens
		r.TotalTokens += c.CompletionTokens
		r.TotalElapsedMS += c.ElapsedMS
		r.ByCapability[string(c.Capability)]++

		if c.Err != nil {
			r.Failures++
			if len(r.Errors) < 10 {
				r.Errors = append(r.Errors, fmt.Sprintf("%s: %s", c.Capability, c.Error))
			}
			continue
		}
		r.Successes++

		if c.Model != "" && !seenModel[c.Model] {
			seenModel[c.Model] = true
			r.Models = append(r.Models, c.Model)
		}
	}

	r.NeverCalled = r.Calls == 0
	r.Degraded = r.Failures > 0 || (r.Calls > 0 && r.Successes == 0)
	return r
}
