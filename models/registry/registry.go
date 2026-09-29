package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type ModelKind string

const (
	KindGGUF       ModelKind = "gguf"
	KindHuggingFace ModelKind = "huggingface"
	KindMock        ModelKind = "mock"
	KindOpenAI      ModelKind = "openai"
	KindOllama      ModelKind = "ollama"
)

type ModelEntry struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Kind          ModelKind           `json:"kind"`
	Provider      string              `json:"provider"`
	ModelID       string              `json:"model_id"`
	Capability    routing.ModelCapability `json:"capability"`
	Path          string              `json:"path,omitempty"`
	MaxTokens     int                 `json:"max_tokens,omitempty"`
	Temperature   float64             `json:"temperature,omitempty"`
	Status        ModelStatus         `json:"status"`
	SizeMB        int64               `json:"size_mb,omitempty"`
	Description   string              `json:"description,omitempty"`
	Tags          []string            `json:"tags,omitempty"`
}

type ModelStatus string

const (
	ModelStatusAvailable ModelStatus = "available"
	ModelStatusMissing   ModelStatus = "missing"
	ModelStatusError     ModelStatus = "error"
)

type Config struct {
	Providers map[string]ProviderConfigEntry `json:"providers"`
	Models    []ModelEntry                   `json:"models"`
	ActiveCap modelCapabilityMap             `json:"active_capabilities"`
}

type ProviderConfigEntry struct {
	Type   string                 `json:"type"`
	URL    string                 `json:"url,omitempty"`
	Token  string                 `json:"token,omitempty"`
	Extra  map[string]any         `json:"extra,omitempty"`
}

type modelCapabilityMap map[string]string

type ModelRegistry struct {
	mu           sync.RWMutex
	router       *routing.Router
	config       *Config
	configPath   string
	ggufScanner  *GGUFScanner
	hfLister     *HFModelLister
	downloader   *LocalModelDownloader
	providers    map[string]provider.ModelProvider
}

func NewModelRegistry(configPath string) *ModelRegistry {
	router := routing.NewRouter()
	router.AddProvider("mock", provider.NewMockProvider("mock"))
	router.ConfigureDefaults()

	r := &ModelRegistry{
		router:      router,
		configPath:  configPath,
		providers:   make(map[string]provider.ModelProvider),
		ggufScanner: NewGGUFScanner(),
		hfLister:    NewHFModelLister(""),
		downloader:  NewLocalModelDownloader(filepath.Join(os.TempDir(), "spiral-models")),
	}

	r.providers["mock"] = provider.NewMockProvider("mock")

	r.config = &Config{
		Providers: make(map[string]ProviderConfigEntry),
		Models:    r.defaultModels(),
		ActiveCap: make(modelCapabilityMap),
	}

	if configPath != "" {
		if err := r.LoadConfig(); err == nil {
			r.applyConfig()
		}
	}

	return r
}

func (r *ModelRegistry) defaultModels() []ModelEntry {
	return []ModelEntry{
		{
			ID:         "model-mock-fast",
			Name:       "mock-fast",
			Kind:       KindMock,
			Provider:   "mock",
			ModelID:    "mock-fast",
			Capability: routing.CapFast,
			Status:     ModelStatusAvailable,
			MaxTokens:  512,
			Temperature: 0.7,
		},
		{
			ID:         "model-mock-reasoning",
			Name:       "mock-reasoning",
			Kind:       KindMock,
			Provider:   "mock",
			ModelID:    "mock-reasoning",
			Capability: routing.CapReasoning,
			Status:     ModelStatusAvailable,
			MaxTokens:  2048,
			Temperature: 0.5,
		},
		{
			ID:         "model-mock-specialize",
			Name:       "mock-specialize",
			Kind:       KindMock,
			Provider:   "mock",
			ModelID:    "mock-specialize",
			Capability: routing.CapSpecialize,
			Status:     ModelStatusAvailable,
			MaxTokens:  1024,
			Temperature: 0.3,
		},
		{
			ID:         "model-mock-classification",
			Name:       "mock-classification",
			Kind:       KindMock,
			Provider:   "mock",
			ModelID:    "mock-classification",
			Capability: routing.CapClassification,
			Status:     ModelStatusAvailable,
			MaxTokens:  256,
			Temperature: 0.1,
		},
		{
			ID:         "model-mock-embed",
			Name:       "mock-embed",
			Kind:       KindMock,
			Provider:   "mock",
			ModelID:    "mock-embed",
			Capability: routing.CapEmbed,
			Status:     ModelStatusAvailable,
			MaxTokens:  512,
			Temperature: 0.0,
		},
	}
}

func (r *ModelRegistry) Router() *routing.Router {
	return r.router
}

func (r *ModelRegistry) SaveConfig() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.MarshalIndent(r.config, "", "  ")
	if err != nil {
		return err
	}

	dir := filepath.Dir(r.configPath)
	if dir != "" {
		os.MkdirAll(dir, 0755)
	}
	return os.WriteFile(r.configPath, data, 0644)
}

func (r *ModelRegistry) LoadConfig() error {
	data, err := os.ReadFile(r.configPath)
	if err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return json.Unmarshal(data, r.config)
}

func (r *ModelRegistry) applyConfig() {
	router := routing.NewRouter()
	router.AddProvider("mock", provider.NewMockProvider("mock"))

	for name, pcfg := range r.config.Providers {
		p := r.createProvider(pcfg)
		if p != nil {
			r.providers[name] = p
			router.AddProvider(name, p)
		}
	}

	for _, m := range r.config.Models {
		router.AddModel(routing.ModelConfig{
			Name:        m.Name,
			Provider:    m.Provider,
			Capability:  m.Capability,
			MaxTokens:   m.MaxTokens,
			Temperature: m.Temperature,
		})
	}

	if len(r.config.Models) == 0 {
		router.ConfigureDefaults()
	}

	r.router = router
}

func (r *ModelRegistry) createProvider(pcfg ProviderConfigEntry) provider.ModelProvider {
	switch pcfg.Type {
	case "mock":
		return provider.NewMockProvider("mock")
	case "huggingface":
		model, _ := pcfg.Extra["model"].(string)
		cacheDir, _ := pcfg.Extra["cache_dir"].(string)
		return provider.NewHuggingFaceProvider(provider.HFProviderConfig{
			APIToken: pcfg.Token,
			Endpoint: pcfg.URL,
			Model:    model,
			CacheDir: cacheDir,
		})
	case "gguf":
		modelPath, _ := pcfg.Extra["model_path"].(string)
		ngpu := 0
		if v, ok := pcfg.Extra["n_gpu_layers"].(int); ok {
			ngpu = v
		}
		return provider.NewGGUFProvider(provider.GGUFProviderConfig{
			ServerURL:  pcfg.URL,
			ModelPath:  modelPath,
			UseCUDA:    pcfg.Extra["use_cuda"] == true,
			NGPULayers: ngpu,
		})
	case "ollama":
		return provider.NewOllamaProvider(provider.OllamaProviderConfig{
			ServerURL: pcfg.URL,
		})
	default:
		return nil
	}
}

func (r *ModelRegistry) DownloadModel(ctx context.Context, modelID, apiToken string) (string, error) {
	return r.downloader.DownloadModel(ctx, modelID, apiToken)
}

func (r *ModelRegistry) GetDownloadProgress(modelID string) *DownloadProgress {
	return r.downloader.GetProgress(modelID)
}

func (r *ModelRegistry) ListDownloadedModels() []ModelEntry {
	return r.downloader.ListDownloadedModels()
}

func (r *ModelRegistry) CacheDir() string {
	return r.downloader.CacheDir()
}

func (r *ModelRegistry) ConvertToGGUF(ctx context.Context, sourceDir, modelID, outputPath string) (string, error) {
	return r.downloader.ConvertToGGUF(ctx, sourceDir, modelID, outputPath)
}

func (r *ModelRegistry) HasGGUF(modelID string) bool {
	return r.downloader.HasGGUF(modelID)
}

func (r *ModelRegistry) FindGGUFPath(modelID string) string {
	return r.downloader.FindGGUFPath(modelID)
}

func (r *ModelRegistry) ListModels() []ModelEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ModelEntry{}, r.config.Models...)
}

func (r *ModelRegistry) ListProviders() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.providers))
	for name := range r.providers {
		names = append(names, name)
	}
	return names
}

func (r *ModelRegistry) ScanForGGUF(ctx context.Context, drivePath string) ([]ModelEntry, error) {
	entries, err := r.ggufScanner.Scan(ctx, drivePath)
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (r *ModelRegistry) AddGGUFModel(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	entry := ModelEntry{
		ID:         fmt.Sprintf("gguf-%d", len(r.config.Models)+1),
		Name:       fmt.Sprintf("gguf-%s", info.Name()),
		Kind:       KindGGUF,
		Provider:   "gguf-default",
		ModelID:    info.Name(),
		Capability: routing.CapFast,
		Path:       path,
		Status:     ModelStatusAvailable,
		SizeMB:     info.Size() / (1024 * 1024),
	}

	r.mu.Lock()
	r.config.Models = append(r.config.Models, entry)
	r.mu.Unlock()

	return r.SaveConfig()
}

func (r *ModelRegistry) AddProvider(name, ptype, url, token string, extra map[string]any) {
	r.mu.Lock()
	r.config.Providers[name] = ProviderConfigEntry{
		Type:  ptype,
		URL:   url,
		Token: token,
		Extra: extra,
	}
	r.mu.Unlock()

	p := r.createProvider(r.config.Providers[name])
	if p != nil {
		r.providers[name] = p
		r.router.AddProvider(name, p)
	}
}

func (r *ModelRegistry) AddModel(name, providerName string, capability routing.ModelCapability, maxTokens int, temperature float64) {
	r.mu.Lock()
	entry := ModelEntry{
		Name:       name,
		Kind:       ModelKind(providerName),
		Provider:   providerName,
		ModelID:    name,
		Capability: capability,
		MaxTokens:  maxTokens,
		Temperature: temperature,
		Status:     ModelStatusAvailable,
	}
	r.config.Models = append(r.config.Models, entry)
	r.mu.Unlock()

	r.router.AddModel(routing.ModelConfig{
		Name:        name,
		Provider:    providerName,
		Capability:  capability,
		MaxTokens:   maxTokens,
		Temperature: temperature,
	})
}

func (r *ModelRegistry) ListHFModels(ctx context.Context, query string, limit int) ([]ModelEntry, error) {
	return r.hfLister.ListModels(ctx, query, limit)
}

func (r *ModelRegistry) DownloadHFModel(ctx context.Context, modelID string) error {
	p, ok := r.providers["huggingface"].(*provider.HuggingFaceProvider)
	if !ok {
		return fmt.Errorf("huggingface provider not configured")
	}
	return p.DownloadModel(ctx, modelID, "")
}

func (r *ModelRegistry) SetActiveModel(capability routing.ModelCapability, modelName string) {
	r.mu.Lock()
	r.config.ActiveCap[string(capability)] = modelName
	r.mu.Unlock()

	if r.router != nil {
		r.router.SetActiveModel(capability, modelName)
	}
}

func (r *ModelRegistry) ActiveModels() map[string]string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]string, len(r.config.ActiveCap))
	for k, v := range r.config.ActiveCap {
		result[k] = v
	}
	return result
}

func (r *ModelRegistry) Providers() map[string]provider.ModelProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make(map[string]provider.ModelProvider, len(r.providers))
	for k, v := range r.providers {
		result[k] = v
	}
	return result
}
