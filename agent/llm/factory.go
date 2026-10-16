package llm

import (
	"context"
	"fmt"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/config"
)

// Factory turns configuration into live backends.
type Factory struct {
	cfg *config.Config
}

// NewFactory builds a factory for a configuration.
func NewFactory(cfg *config.Config) *Factory { return &Factory{cfg: cfg} }

// Build creates a backend for each enabled configuration entry.
//
// A backend that fails to construct is reported rather than dropped silently, and
// construction never performs a network call: reachability is decided by the
// inventory scan, which can be told to skip a backend that was merely slow to
// answer. Separating construction from probing keeps a dead server from turning
// into a startup error.
func (f *Factory) Build() ([]Backend, []string, error) {
	var (
		out     []Backend
		warnings []string
	)
	for _, b := range f.cfg.Models.Backends {
		if !b.IsEnabled() {
			continue
		}
		backend, err := f.buildOne(b)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("backend %q: %v", b.Name, err))
			continue
		}
		out = append(out, backend)
	}
	return out, warnings, nil
}

func (f *Factory) buildOne(b config.Backend) (Backend, error) {
	kind := strings.ToLower(strings.TrimSpace(b.Type))
	if kind == "" {
		// Inferring the kind from the URL keeps hand-edited configuration short
		// without making the inference authoritative: a URL that says 11434 is
		// almost certainly Ollama, and a wrong guess still produces a clear
		// error rather than a silent misbehaviour.
		if strings.Contains(b.BaseURL, "11434") {
			kind = "ollama"
		} else {
			kind = "openai"
		}
	}

	switch kind {
	case "ollama":
		return NewOllamaBackend(OllamaOptions{
			Name:       b.Name,
			BaseURL:    b.BaseURL,
			Model:      b.Model,
			TimeoutSec: b.TimeoutSec,
			KeepAlive:  b.Options["keep_alive"],
		}), nil
	case "openai", "openai-compatible", "llamacpp", "llama.cpp", "vllm", "lmstudio":
		return NewOpenAIBackend(OpenAIOptions{
			Name:       b.Name,
			BaseURL:    b.BaseURL,
			APIKey:     b.APIKey,
			Model:      b.Model,
			TimeoutSec: b.TimeoutSec,
		}), nil
	case "mock", "offline", "none":
		return NewMockBackend(b.Name), nil
	default:
		return nil, fmt.Errorf("unknown backend type %q", b.Type)
	}
}

// BuildRouter constructs a router with backends, routes, and an inventory.
func BuildRouter(ctx context.Context, cfg *config.Config, scan bool) (*Router, Inventory, []string, error) {
	router := NewRouter(RouterOptions{Timeout: cfg.CommandTimeout() * 3})

	backends, warnings, err := NewFactory(cfg).Build()
	if err != nil {
		return nil, Inventory{}, warnings, err
	}
	for _, b := range backends {
		router.AddBackend(b)
	}

	// Routes are installed before the inventory so an explicit route always
	// wins over automatic selection.
	for role, rt := range cfg.Models.Routes {
		route := Route{
			Role:        role,
			Backend:     rt.Backend,
			Model:       rt.Model,
			Temperature: rt.Temperature,
			MaxTokens:   rt.MaxTokens,
			Source:      "config",
		}
		if route.Temperature == 0 {
			switch role {
			case config.RoleReviewer:
				route.Temperature = 0.1
			case config.RoleCoder, config.RoleDebugger:
				route.Temperature = 0.2
			case config.RoleSummarizer:
				route.Temperature = 0.3
			default:
				route.Temperature = 0.4
			}
		}
		if route.MaxTokens == 0 {
			route.MaxTokens = cfg.Limits.MaxTokensPerCall
		}
		router.SetRoute(route)
	}

	var inv Inventory
	if scan {
		scanner := &InventoryScanner{Backends: backends, Dirs: cfg.Models.ModelDirs}
		inv = scanner.Scan(ctx)
		warnings = append(warnings, inv.Errors...)
	}
	router.SetInventory(inv)

	// Fill in an unbound model on every route using whatever the inventory
	// suggests, so the routes table an operator sees is the one actually used.
	complete := completeRoutes(router, cfg)
	router.SetRoutes(complete)

	return router, inv, warnings, nil
}

func completeRoutes(r *Router, cfg *config.Config) []Route {
	var out []Route
	for _, role := range config.KnownRoles {
		rt, ok := r.RouteFor(role)
		if !ok {
			backend, resolved, err := r.Resolve(role)
			if err != nil {
				continue
			}
			rt = resolved
			rt.Role = role
			rt.Backend = backend.Name()
			rt.Source = "auto"
			if rt.Temperature == 0 {
				rt.Temperature = 0.4
			}
			if rt.MaxTokens == 0 {
				rt.MaxTokens = cfg.Limits.MaxTokensPerCall
			}
		}
		out = append(out, rt)
	}
	return out
}