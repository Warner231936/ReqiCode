package llm

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Route binds a role to a backend and model.
type Route struct {
	Role        string  `json:"role"`
	Backend     string  `json:"backend"`
	Model       string  `json:"model"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	Source      string  `json:"source"` // config | auto | fallback
}

// Router dispatches a chat request to the model assigned to a role.
//
// Roles exist because one model should not do every job. Planning and reviewing
// need a model that follows instructions reliably; error classification is
// better served by a small fast model that answers instantly. Making that
// assignment explicit is the difference between a configurable agent and one
// that has been hard-coded around whichever model happened to be installed.
type Router struct {
	mu       sync.RWMutex
	backends map[string]Backend
	routes   map[string]Route
	order    []string
	// chatAttempts is how many backends to try before declaring failure.
	chatAttempts int
	// timeout bounds one role's total attempt budget.
	timeout time.Duration
	stats   map[string]*RoleStats
	inv     Inventory
}

// RoleStats records usage per role, which is how an operator learns that a
// 27B model is being used for punctuation.
type RoleStats struct {
	Role        string        `json:"role"`
	Calls       int           `json:"calls"`
	Failures    int           `json:"failures"`
	Retries     int           `json:"retries"`
	TotalTime   time.Duration `json:"total_time"`
	Tokens      int           `json:"tokens"`
	Backend     string        `json:"backend"`
	Model       string        `json:"model"`
	LastError   string        `json:"last_error,omitempty"`
}

// RouterOptions configures a Router.
type RouterOptions struct {
	Timeout time.Duration
}

// NewRouter builds an empty router.
func NewRouter(opts RouterOptions) *Router {
	if opts.Timeout <= 0 {
		opts.Timeout = 15 * time.Minute
	}
	return &Router{
		backends:     map[string]Backend{},
		routes:       map[string]Route{},
		chatAttempts: 3,
		timeout:      opts.Timeout,
		stats:        map[string]*RoleStats{},
	}
}

// AddBackend registers a backend adapter.
func (r *Router) AddBackend(b Backend) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.backends[b.Name()] = b
	for _, existing := range r.order {
		if existing == b.Name() {
			return
		}
	}
	r.order = append(r.order, b.Name())
}

// Backends returns the registered backends in registration order.
func (r *Router) Backends() []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Backend, 0, len(r.order))
	for _, n := range r.order {
		if b, ok := r.backends[n]; ok {
			out = append(out, b)
		}
	}
	return out
}

// SetRoute pins a role to a backend and model.
func (r *Router) SetRoute(route Route) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes[route.Role] = route
}

// SetRoutes installs several routes at once.
func (r *Router) SetRoutes(routes []Route) {
	for _, rt := range routes {
		r.SetRoute(rt)
	}
}

// Routes returns the resolved routes, sorted by role.
func (r *Router) Routes() []Route {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Route, 0, len(r.routes))
	for _, rt := range r.routes {
		out = append(out, rt)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// RouteFor returns the route for a role.
func (r *Router) RouteFor(role string) (Route, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	rt, ok := r.routes[role]
	return rt, ok
}

// SetInventory records the scan result, which AutoRoute consults.
func (r *Router) SetInventory(inv Inventory) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.inv = inv
}

// Inventory returns the recorded scan result.
func (r *Router) Inventory() Inventory {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.inv
}

// Stats returns a copy of per-role statistics.
func (r *Router) Stats() []RoleStats {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]RoleStats, 0, len(r.stats))
	for _, s := range r.stats {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out
}

// Resolve returns the backend and model a role would use, without calling it.
func (r *Router) Resolve(role string) (Backend, Route, error) {
	r.mu.RLock()
	route, hasRoute := r.routes[role]
	backends := make([]Backend, 0, len(r.order))
	for _, n := range r.order {
		backends = append(backends, r.backends[n])
	}
	inv := r.inv
	r.mu.RUnlock()

	if len(backends) == 0 {
		return nil, Route{}, ErrNoBackend
	}

	if hasRoute && route.Backend != "" {
		for _, b := range backends {
			if b.Name() == route.Backend {
				// A route naming a model the backend cannot serve is a
				// configuration error worth reporting precisely rather than
				// silently using whatever is loaded.
				return b, route, nil
			}
		}
		return nil, Route{}, fmt.Errorf("llm: route %q names unknown backend %q", role, route.Backend)
	}

	b, model := pickFromInventory(inv, backends, role)
	if b == nil {
		// No inventory yet: fall back to the first registered backend and let
		// the backend's own default model apply.
		b = backends[0]
		route = Route{Role: role, Backend: b.Name(), Model: model, Source: "fallback"}
		return b, route, nil
	}
	return b, Route{Role: role, Backend: b.Name(), Model: model, Source: "auto"}, nil
}

// pickFromInventory chooses a backend and model for a role from a scan.
func pickFromInventory(inv Inventory, backends []Backend, role string) (Backend, string) {
	reachable := map[string]bool{}
	for _, h := range inv.Backends {
		if h.Reachable {
			reachable[h.Backend] = true
		}
	}

	// Prefer an already-loaded model on a reachable backend: it answers
	// immediately instead of paging weights in from disk.
	type candidate struct {
		info    ModelInfo
		backend Backend
		score   int
	}
	var cands []candidate
	for _, b := range backends {
		if len(inv.Backends) > 0 && !reachable[b.Name()] {
			continue
		}
		for _, m := range inv.Models {
			if m.Backend != b.Name() {
				continue
			}
			score := roleFitScore(role, m)
			if score <= 0 {
				continue
			}
			if h, ok := findHealth(inv, b.Name()); ok && h.ModelLoaded != "" && h.ModelLoaded == m.ID {
				score += 15
			}
			cands = append(cands, candidate{info: m, backend: b, score: score})
		}
	}
	if len(cands) == 0 {
		return nil, ""
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].score != cands[j].score {
			return cands[i].score > cands[j].score
		}
		return cands[i].info.ID < cands[j].info.ID
	})
	return cands[0].backend, cands[0].info.ID
}

func findHealth(inv Inventory, name string) (Health, bool) {
	for _, h := range inv.Backends {
		if h.Backend == name {
			return h, true
		}
	}
	return Health{}, false
}

// roleFitScore rates how well a model suits a role.
//
// The ranking is deliberately conservative: a model only wins a role it is
// plausibly good at. An embedding model never wins a generation role, and a
// sub-billion-parameter model is demoted for reviewing because a false approval
// there silently corrupts the whole loop.
func roleFitScore(role string, m ModelInfo) int {
	if m.Role == "embedding" || m.Role == "reranker" {
		if role == "embeddings" && m.Role == "embedding" {
			return 60
		}
		return -1
	}

	name := strings.ToLower(m.ID)
	paramScore := sizeScore(name, m.SizeBytes)

	switch role {
	case "embeddings":
		return 1 // any chat model embeds nothing; let the caller fall back
	case "planner":
		return 40 + paramScore + bonusCoder(name)
	case "coder":
		return 45 + paramScore + bonusCoder(name)*2
	case "debugger":
		return 40 + paramScore + bonusCoder(name)
	case "reviewer":
		return 35 + paramScore + bonusCoder(name)
	case "summarizer":
		return 35 + paramScore
	default:
		return 30 + paramScore
	}
}

func bonusCoder(name string) int {
	if strings.Contains(name, "coder") || strings.Contains(name, "code") {
		return 8
	}
	return 0
}

// sizeScore converts model size into a role-neutral quality proxy.
func sizeScore(name string, size int64) int {
	switch {
	case strings.Contains(name, "27b"), strings.Contains(name, "32b"), strings.Contains(name, "30b"):
		return 22
	case strings.Contains(name, "14b"):
		return 18
	case strings.Contains(name, "70b"), strings.Contains(name, "72b"):
		return 24
	case strings.Contains(name, "8b"):
		return 14
	case strings.Contains(name, "7b"):
		return 12
	case strings.Contains(name, "3b"):
		return 8
	case strings.Contains(name, "1.5b"), strings.Contains(name, "1b"), strings.Contains(name, "1.1b"):
		return 5
	}
	gb := float64(size) / (1 << 30)
	switch {
	case gb >= 20:
		return 22
	case gb >= 10:
		return 18
	case gb >= 5:
		return 13
	case gb >= 2:
		return 9
	case gb >= 1:
		return 6
	default:
		return 3
	}
}

// Chat performs a completion for a role.
//
// Fallback is real fallback: if the routed backend errors, the next reachable
// backend is tried rather than the failure being propagated. A local agent
// losing its only server mid-task should degrade to another local model if one
// exists, and the statistics record that it did.
func (r *Router) Chat(ctx context.Context, role string, msgs []Message, opts Request) (Response, error) {
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	backend, route, err := r.Resolve(role)
	if err != nil {
		return Response{}, err
	}

	req := opts
	req.Messages = msgs
	if req.Model == "" {
		req.Model = route.Model
	}
	if req.Temperature == 0 && route.Temperature != 0 {
		req.Temperature = route.Temperature
	}
	if req.MaxTokens == 0 {
		req.MaxTokens = route.MaxTokens
	}

	start := time.Now()
	resp, err := backend.Chat(ctx, req)
	if err == nil {
		r.record(role, true, resp, route, time.Since(start), nil)
		return resp, nil
	}

	firstErr := err
	attempts := 1
	for _, alt := range r.alternateBackends(backend.Name()) {
		if attempts >= r.chatAttempts {
			break
		}
		select {
		case <-ctx.Done():
			r.record(role, false, Response{}, route, time.Since(start), firstErr)
			return Response{}, fmt.Errorf("llm: role %q timed out after %d backend attempt(s): %w",
				role, attempts, ctx.Err())
		default:
		}
		resp, aerr := alt.Chat(ctx, req)
		attempts++
		if aerr == nil {
			resp.Attempts = attempts
			fallbackRoute := Route{Role: role, Backend: alt.Name(), Model: req.Model, Source: "fallback"}
			r.record(role, true, resp, fallbackRoute, time.Since(start), firstErr)
			return resp, nil
		}
		firstErr = fmt.Errorf("%s: %v (after %s: %v)", alt.Name(), aerr, backend.Name(), err)
	}

	r.record(role, false, Response{}, route, time.Since(start), firstErr)
	return Response{}, fmt.Errorf("llm: role %q failed on %d backend(s): %w", role, attempts, firstErr)
}

func (r *Router) alternateBackends(exclude string) []Backend {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Backend
	for _, n := range r.order {
		if n == exclude {
			continue
		}
		out = append(out, r.backends[n])
	}
	return out
}

func (r *Router) record(role string, ok bool, resp Response, route Route, took time.Duration, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.stats[role]
	if s == nil {
		s = &RoleStats{Role: role}
		r.stats[role] = s
	}
	s.Calls++
	s.TotalTime += took
	if !ok {
		s.Failures++
		if err != nil {
			s.LastError = err.Error()
		}
		return
	}
	s.Backend = route.Backend
	s.Model = resp.Model
	s.Tokens += resp.PromptTokens + resp.CompletionTokens
	if resp.Attempts > 1 {
		s.Retries += resp.Attempts - 1
	}
	if err != nil {
		s.LastError = err.Error()
	}
}

// Embed requests vectors for a role.
//
// Unlike Chat, embedding has no fallback chain on purpose: a vector from model A
// is not interchangeable with one from model B, because mixing vector spaces
// produces similarity scores that look reasonable and are meaningless. If the
// routed backend cannot embed, the caller is told, so it can use lexical
// retrieval instead.
func (r *Router) Embed(ctx context.Context, role string, inputs []string) ([][]float32, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	backend, route, err := r.Resolve(role)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	resp, err := backend.Embed(ctx, EmbedRequest{Model: route.Model, Inputs: inputs})
	if err != nil {
		r.mu.Lock()
		s := r.stats[role]
		if s == nil {
			s = &RoleStats{Role: role}
			r.stats[role] = s
		}
		s.Calls++
		s.Failures++
		s.LastError = err.Error()
		r.mu.Unlock()
		return nil, err
	}
	if len(resp.Vectors) != len(inputs) {
		return nil, fmt.Errorf("llm: backend %q returned %d vectors for %d inputs", backend.Name(), len(resp.Vectors), len(inputs))
	}
	r.record(role, true, Response{Model: resp.Model}, route, 0, nil)
	return resp.Vectors, nil
}

// HasBackends reports whether any backend is registered.
func (r *Router) HasBackends() bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.backends) > 0
}

// Describe renders the routing table for a terminal.
func (r *Router) Describe() string {
	var b strings.Builder
	b.WriteString("MODEL ROUTING\n")
	b.WriteString("=============\n")
	if len(r.routes) == 0 {
		b.WriteString("  (no explicit routes; roles resolve automatically from the inventory)\n")
	}
	for _, rt := range r.Routes() {
		fmt.Fprintf(&b, "  %-11s -> %-18s model=%-40s temp=%.2f max=%-5d [%s]\n",
			rt.Role, rt.Backend, orDash(rt.Model), rt.Temperature, rt.MaxTokens, rt.Source)
	}
	if stats := r.Stats(); len(stats) > 0 {
		b.WriteString("\nUsage:\n")
		for _, s := range stats {
			fmt.Fprintf(&b, "  %-11s calls=%-4d failures=%-3d retries=%-3d tokens=%-8d avg=%-10s model=%s\n",
				s.Role, s.Calls, s.Failures, s.Retries, s.Tokens, s.TotalTime.Round(1e6), orDash(s.Model))
			if s.LastError != "" {
				fmt.Fprintf(&b, "      last error: %s\n", s.LastError)
			}
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}