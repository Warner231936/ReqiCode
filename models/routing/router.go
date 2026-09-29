package routing

import (
	"github.com/kilo/spiral-codemaker/models/provider"
)

type ModelCapability string

const (
	CapFast        ModelCapability = "fast"
	CapReasoning   ModelCapability = "reasoning"
	CapSpecialize  ModelCapability = "specialize"
	CapClassification ModelCapability = "classification"
	CapEmbed       ModelCapability = "embed"
)

type ModelConfig struct {
	Name       string            `json:"name"`
	Provider   string            `json:"provider"`
	Capability ModelCapability   `json:"capability"`
	MaxTokens  int               `json:"max_tokens"`
	Temperature float64          `json:"temperature"`
}

type Router struct {
	models    []ModelConfig
	providers map[string]provider.ModelProvider
	active    map[ModelCapability]string
}

func NewRouter() *Router {
	return &Router{
		models:    []ModelConfig{},
		providers: make(map[string]provider.ModelProvider),
		active:    make(map[ModelCapability]string),
	}
}

func (r *Router) AddProvider(name string, p provider.ModelProvider) {
	r.providers[name] = p
}

func (r *Router) AddModel(cfg ModelConfig) {
	r.models = append(r.models, cfg)
}

func (r *Router) SetActiveModel(capability ModelCapability, modelName string) {
	r.active[capability] = modelName
}

func (r *Router) GetProvider(capability ModelCapability) (provider.ModelProvider, string, bool) {
	if activeName, ok := r.active[capability]; ok {
		for _, m := range r.models {
			if m.Name == activeName && m.Capability == capability {
				p, ok := r.providers[m.Provider]
				return p, m.Name, ok
			}
		}
	}
	for _, m := range r.models {
		if m.Capability == capability {
			p, ok := r.providers[m.Provider]
			return p, m.Name, ok
		}
	}
	if len(r.providers) == 0 {
		return nil, "", false
	}
	for name, p := range r.providers {
		return p, name, true
	}
	return nil, "", false
}

func (r *Router) GetByName(name string) (provider.ModelProvider, bool) {
	for _, m := range r.models {
		if m.Name == name {
			p, ok := r.providers[m.Provider]
			return p, ok
		}
	}
	return nil, false
}

func (r *Router) ConfigureDefaults() {
	r.AddModel(ModelConfig{
		Name:        "mock-fast",
		Provider:    "mock",
		Capability:  CapFast,
		MaxTokens:   512,
		Temperature: 0.7,
	})
	r.AddModel(ModelConfig{
		Name:        "mock-reasoning",
		Provider:    "mock",
		Capability:  CapReasoning,
		MaxTokens:   2048,
		Temperature: 0.5,
	})
	r.AddModel(ModelConfig{
		Name:        "mock-specialize",
		Provider:    "mock",
		Capability:  CapSpecialize,
		MaxTokens:   1024,
		Temperature: 0.3,
	})
	r.AddModel(ModelConfig{
		Name:        "mock-classification",
		Provider:    "mock",
		Capability:  CapClassification,
		MaxTokens:   256,
		Temperature: 0.1,
	})
	r.AddModel(ModelConfig{
		Name:        "mock-embed",
		Provider:    "mock",
		Capability:  CapEmbed,
		MaxTokens:   512,
		Temperature: 0.0,
	})
}

func (r *Router) Providers() map[string]provider.ModelProvider {
	return r.providers
}
