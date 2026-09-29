package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/system"
	"github.com/kilo/spiral-codemaker/models/provider"
	"github.com/kilo/spiral-codemaker/models/registry"
	"github.com/kilo/spiral-codemaker/models/routing"
	"github.com/kilo/spiral-codemaker/web"
)

type Server struct {
	orch         *system.Orchestrator
	bus          *events.EventBus
	semiState    *state.SemiState
	host         string
	server       *http.Server
	subscription *SubscriptionManager
	modelReg     *registry.ModelRegistry
	runManager   *RunManager
}

type SubscriptionManager struct {
	subs map[chan []byte]bool
}

func NewSubscriptionManager() *SubscriptionManager {
	return &SubscriptionManager{
		subs: make(map[chan []byte]bool),
	}
}

func (sm *SubscriptionManager) Subscribe() chan []byte {
	ch := make(chan []byte, 100)
	sm.subs[ch] = true
	return ch
}

func (sm *SubscriptionManager) Unsubscribe(ch chan []byte) {
	delete(sm.subs, ch)
	close(ch)
}

func (sm *SubscriptionManager) Broadcast(data []byte) {
	for ch := range sm.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

type Snapshot struct {
	Timestamp        time.Time            `json:"timestamp"`
	Revision         int                  `json:"revision"`
	Confidence       float64              `json:"confidence"`
	ActiveUnits      []state.UnitMeta     `json:"active_units"`
	AttentionMap     map[string]float64   `json:"attention_map"`
	Hypotheses       []state.Hypothesis   `json:"hypotheses"`
	Evidence         []state.Evidence     `json:"evidence"`
	Proposals        []state.CodeProposal `json:"proposals"`
	TestResults      []state.TestResult   `json:"test_results"`
	Decisions        []state.Decision     `json:"decisions"`
	Findings         []state.FindingRecord `json:"findings"`
	Objections       []state.Objection   `json:"objections"`
	Conflicts        []state.Conflict    `json:"conflicts"`
	Requirements     []state.Requirement `json:"requirements"`
	ArchitecturePlan *state.ArchitecturePlan `json:"architecture_plan,omitempty"`
	GeneratedFiles   map[string]state.FileEntry `json:"generated_files"`
	Unresolved       []string          `json:"unresolved"`
}

func copyMap(m map[string]float64) map[string]float64 {
	r := make(map[string]float64, len(m))
	for k, v := range m {
		r[k] = v
	}
	return r
}

func copyFileMap(m map[string]state.FileEntry) map[string]state.FileEntry {
	r := make(map[string]state.FileEntry, len(m))
	for k, v := range m {
		r[k] = v
	}
	return r
}

func (s *Server) copyPlan(p *state.ArchitecturePlan) *state.ArchitecturePlan {
	if p == nil {
		return nil
	}
	cp := *p
	return &cp
}

func NewServer(orch *system.Orchestrator, host string) *Server {
	return NewServerWithRegistry(orch, host, nil)
}

func NewServerWithRegistry(orch *system.Orchestrator, host string, modelReg *registry.ModelRegistry) *Server {
	srv := &Server{
		orch:         orch,
		bus:          orch.Bus(),
		semiState:    orch.SemiState(),
		host:         host,
		subscription: NewSubscriptionManager(),
		modelReg:     modelReg,
	}
	srv.setupRoutes()
	return srv
}

func NewDaemonServer(host string, configPath string) *Server {
	runMgr := NewRunManager(configPath, func(runID string, event any) {})
	srv := &Server{
		host:         host,
		orch:         nil,
		bus:          events.NewEventBus(),
		semiState:    state.NewSemiState(),
		subscription: NewSubscriptionManager(),
		modelReg:     runMgr.ModelRegistry(),
		runManager:   runMgr,
	}
	srv.setupRoutes()
	return srv
}

func (s *Server) setupRoutes() {
	if s.bus != nil {
		s.bus.Subscribe(events.EventTestPassed, s.onEvent)
		s.bus.Subscribe(events.EventTestFailed, s.onEvent)
		s.bus.Subscribe(events.EventCodeApplied, s.onEvent)
		s.bus.Subscribe(events.EventSpiralIterationCompleted, s.onEvent)
		s.bus.Subscribe(events.EventAttentionChanged, s.onEvent)
		s.bus.Subscribe(events.EventConflictDetected, s.onEvent)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/snapshot", s.handleSnapshot)
	mux.HandleFunc("/api/iterations", s.handleIterations)
	mux.HandleFunc("/api/units", s.handleUnits)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/attention", s.handleAttention)
	mux.HandleFunc("/api/report", s.handleReport)
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	})
	mux.HandleFunc("/api/stream", s.handleStream)
	mux.HandleFunc("/api/file/", s.handleFile)
	mux.HandleFunc("/api/models", s.handleModels)
	mux.HandleFunc("/api/models/scan", s.handleScanGGUF)
	mux.HandleFunc("/api/models/hf-search", s.handleHFSearch)
	mux.HandleFunc("/api/models/add-provider", s.handleAddProvider)
	mux.HandleFunc("/api/models/activate", s.handleActivateModel)
	mux.HandleFunc("/api/models/gguf-add", s.handleAddGGUFModel)
	mux.HandleFunc("/api/models/providers", s.handleListProviders)
	mux.HandleFunc("/api/models/download", s.handleDownloadModel)
	mux.HandleFunc("/api/models/convert", s.handleConvertModel)
	mux.HandleFunc("/api/models/download-progress", s.handleDownloadProgress)
	mux.HandleFunc("/api/models/downloaded", s.handleListDownloaded)
	mux.HandleFunc("/api/gpu", s.handleGPUInfo)
	mux.HandleFunc("/api/llamacpp", s.handleLlamaCppStatus)
	mux.HandleFunc("/api/llamacpp/load", s.handleLlamaCppLoadModel)

	mux.HandleFunc("/api/runs", s.handleRuns)
	mux.HandleFunc("/api/runs/submit", s.handleSubmitRun)
	mux.HandleFunc("/api/runs/", s.handleRun)

	webHandler, err := web.Handler()
	if err != nil {
		mux.HandleFunc("/dashboard", s.handleIndex)
		mux.HandleFunc("/", s.handleIndex)
	} else {
		mux.Handle("/dashboard/", http.StripPrefix("/dashboard/", webHandler))
		mux.HandleFunc("/", s.handleIndex)
	}

	s.server = &http.Server{
		Addr:    s.host,
		Handler: mux,
	}
}

func (s *Server) onEvent(_ context.Context, event events.Event) {
	data, _ := json.Marshal(event)
	s.subscription.Broadcast(data)
}

func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.orch != nil && s.orch.SemiState() != nil {
		clone := s.orch.SemiState().Snapshot()
		snap := &Snapshot{
			Timestamp:        time.Now().UTC(),
			Revision:         clone.Revision,
			Confidence:       float64(clone.Confidence),
			ActiveUnits:      clone.ActiveUnits,
			AttentionMap:     copyMap(clone.AttentionMap),
			Hypotheses:       clone.Hypotheses,
			Evidence:         clone.Evidence,
			Proposals:        clone.Proposals,
			TestResults:      clone.TestResults,
			Decisions:        clone.Decisions,
			Findings:         clone.Findings,
			Objections:       clone.Objections,
			Conflicts:        clone.Contradictions,
			Requirements:     clone.Requirements,
			ArchitecturePlan: s.copyPlan(clone.ArchitecturePlan),
			GeneratedFiles:   copyFileMap(clone.GeneratedFiles),
			Unresolved:       clone.Unresolved,
		}
		json.NewEncoder(w).Encode(snap)
		return
	}

	json.NewEncoder(w).Encode(&Snapshot{
		Timestamp:  time.Now().UTC(),
		Confidence: 0.5,
	})
}

func (s *Server) handleIterations(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.orch != nil && s.orch.SpiralManager() != nil {
		json.NewEncoder(w).Encode(s.orch.SpiralManager().Records())
		return
	}

	records := []map[string]any{
		{"iteration_id": 0, "summary": "no active run"},
	}
	json.NewEncoder(w).Encode(records)
}

func (s *Server) handleUnits(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.orch != nil && s.orch.Registry() != nil {
		json.NewEncoder(w).Encode(s.orch.Registry().All())
		return
	}

	json.NewEncoder(w).Encode([]map[string]any{
		{"id": "none", "name": "no active units", "role": "idle"},
	})
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"message": "events are streamed via /api/stream"})
}

func (s *Server) handleAttention(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.orch != nil && s.orch.Attention() != nil {
		json.NewEncoder(w).Encode(s.orch.Attention().GetAll())
		return
	}

	json.NewEncoder(w).Encode(map[string]float64{})
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain")
	if s.orch != nil {
		fmt.Fprintln(w, s.orch.FinalReport())
	} else {
		fmt.Fprintln(w, "No active run. Submit a run to get started.")
	}
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := s.subscription.Subscribe()
	defer s.subscription.Unsubscribe(ch)

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}
	notifier := r.Context().Done()
	for {
		select {
		case data := <-ch:
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		case <-notifier:
			return
		}
	}
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	filePath := r.URL.Path[len("/api/file/"):]
	if s.orch != nil && s.orch.Workspace() != nil {
		content, err := s.orch.Workspace().ReadFile(filePath)
		if err != nil {
			http.Error(w, "file not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, content)
		return
	}
	http.Error(w, "no active workspace", http.StatusNotFound)
}

func (s *Server) handleRuns(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if s.runManager == nil {
		json.NewEncoder(w).Encode([]RunSummary{})
		return
	}

	runs := s.runManager.ListRuns()
	summaries := make([]RunSummary, 0, len(runs))
	for _, run := range runs {
		summaries = append(summaries, RunSummary{
			ID:         run.ID,
			Intent:     run.Config.Intent,
			Status:     run.Status,
			StartTime:  run.StartTime,
			EndTime:    run.EndTime,
			OutputPath: run.OutputPath,
			Error:      run.Error,
		})
	}
	json.NewEncoder(w).Encode(summaries)
}

func (s *Server) handleSubmitRun(w http.ResponseWriter, r *http.Request) {
	if s.runManager == nil {
		http.Error(w, "daemon mode required", http.StatusServiceUnavailable)
		return
	}

	var config RunConfig
	if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}

	result, err := s.runManager.SubmitRun(config)
	if err != nil {
		http.Error(w, fmt.Sprintf("submit error: %v", err), http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"status": "submitted",
		"run_id": result.ID,
	})
}

func (s *Server) handleRun(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Path[len("/api/runs/"):]
	w.Header().Set("Content-Type", "application/json")

	if s.runManager == nil {
		http.Error(w, "daemon mode required", http.StatusServiceUnavailable)
		return
	}

	if runID == "submit" {
		w.WriteHeader(http.StatusNotFound)
		return
	}

	run := s.runManager.GetRun(runID)
	if run == nil {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	json.NewEncoder(w).Encode(map[string]any{
		"id":          run.ID,
		"status":      run.Status,
		"start_time":  run.StartTime,
		"end_time":    run.EndTime,
		"output_path": run.OutputPath,
		"error":       run.Error,
		"config":      run.Config,
		"iterations":  s.iterationsForRun(run),
		"files":       s.filesForRun(run),
	})
}

func (s *Server) iterationsForRun(run *RunResult) []map[string]any {
	if run.Orch == nil || run.Orch.SpiralManager() == nil {
		return []map[string]any{}
	}
	records := run.Orch.SpiralManager().Records()
	result := make([]map[string]any, 0, len(records))
	for _, rec := range records {
		result = append(result, map[string]any{
			"iteration_id": rec.ID,
			"phase":        rec.Phase,
			"summary":      rec.Summary,
			"changes":      len(rec.Changes),
			"evidence":     len(rec.NewEvidence),
		})
	}
	return result
}

func (s *Server) filesForRun(run *RunResult) []string {
	if run.Orch == nil || run.Orch.Workspace() == nil {
		return []string{}
	}
	files, err := run.Orch.Workspace().ListFiles()
	if err != nil {
		return []string{}
	}
	return files
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.modelReg == nil {
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}
	json.NewEncoder(w).Encode(s.modelReg.ListModels())
}

func (s *Server) handleListProviders(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.modelReg == nil {
		json.NewEncoder(w).Encode([]interface{}{})
		return
	}
	json.NewEncoder(w).Encode(s.modelReg.ListProviders())
}

func (s *Server) handleScanGGUF(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	drivePath := r.URL.Query().Get("path")
	if drivePath == "" {
		http.Error(w, "path query param required", http.StatusBadRequest)
		return
	}
	entries, err := s.modelReg.ScanForGGUF(r.Context(), drivePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("scan error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

func (s *Server) handleHFSearch(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	query := r.URL.Query().Get("q")
	if query == "" {
		http.Error(w, "q query param required", http.StatusBadRequest)
		return
	}
	limit := 20
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			limit = n
		}
	}
	entries, err := s.modelReg.ListHFModels(r.Context(), query, limit)
	if err != nil {
		http.Error(w, fmt.Sprintf("hf search error: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(entries)
}

type addProviderRequest struct {
	Name  string         `json:"name"`
	Type  string         `json:"type"`
	URL   string         `json:"url,omitempty"`
	Token string         `json:"token,omitempty"`
	Extra map[string]any `json:"extra,omitempty"`
}

func (s *Server) handleAddProvider(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req addProviderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Type == "" {
		http.Error(w, "name and type are required", http.StatusBadRequest)
		return
	}

	s.modelReg.AddProvider(req.Name, req.Type, req.URL, req.Token, req.Extra)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "provider": req.Name})
}

type activateModelRequest struct {
	Capability string `json:"capability"`
	ModelName  string `json:"model_name"`
}

func (s *Server) handleActivateModel(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req activateModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}
	if req.Capability == "" || req.ModelName == "" {
		http.Error(w, "capability and model_name are required", http.StatusBadRequest)
		return
	}

	s.modelReg.SetActiveModel(routing.ModelCapability(req.Capability), req.ModelName)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "capability": req.Capability, "model": req.ModelName})
}

func (s *Server) handleAddGGUFModel(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := r.URL.Query().Get("path")
	if path == "" {
		path = r.FormValue("path")
	}
	if path == "" {
		http.Error(w, "path query or form param required", http.StatusBadRequest)
		return
	}

	if err := s.modelReg.AddGGUFModel(path); err != nil {
		http.Error(w, fmt.Sprintf("error adding GGUF model: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "path": path})
}

type downloadRequest struct {
	ModelID  string `json:"model_id"`
	APIToken string `json:"api_token,omitempty"`
}

func (s *Server) handleDownloadModel(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req downloadRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}
	if req.ModelID == "" {
		http.Error(w, "model_id is required", http.StatusBadRequest)
		return
	}

	go func() {
		s.modelReg.DownloadModel(r.Context(), req.ModelID, req.APIToken)
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "started",
		"model_id":  req.ModelID,
		"cache_dir": s.modelReg.CacheDir(),
	})
}

type convertRequest struct {
	ModelID  string `json:"model_id"`
	APIToken string `json:"api_token,omitempty"`
}

func (s *Server) handleConvertModel(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var req convertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}
	if req.ModelID == "" {
		http.Error(w, "model_id is required", http.StatusBadRequest)
		return
	}

	if s.modelReg.HasGGUF(req.ModelID) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"status":    "already_converted",
			"model_id":  req.ModelID,
			"gguf_path": s.modelReg.FindGGUFPath(req.ModelID),
		})
		return
	}

	go func() {
		s.modelReg.DownloadModel(r.Context(), req.ModelID, req.APIToken)
		s.modelReg.ConvertToGGUF(r.Context(), "", req.ModelID, "")
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"status":   "conversion_started",
		"model_id": req.ModelID,
	})
}

func (s *Server) handleDownloadProgress(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	modelID := r.URL.Query().Get("model_id")
	if modelID == "" {
		http.Error(w, "model_id query param required", http.StatusBadRequest)
		return
	}
	progress := s.modelReg.GetDownloadProgress(modelID)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(progress)
}

func (s *Server) handleListDownloaded(w http.ResponseWriter, r *http.Request) {
	if s.modelReg == nil {
		http.Error(w, "model registry not configured", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(s.modelReg.ListDownloadedModels())
}

func (s *Server) handleGPUInfo(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if s.modelReg == nil {
		json.NewEncoder(w).Encode(map[string]any{"available": false})
		return
	}

	providers := s.modelReg.Providers()
	for _, p := range providers {
		if gguf, ok := p.(*provider.GGUFProvider); ok {
			info := gguf.GPUInfo()
			json.NewEncoder(w).Encode(info)
			return
		}
	}

	json.NewEncoder(w).Encode(map[string]any{
		"available": false,
		"vendor":    "unknown",
		"backend":   "cpu",
	})
}

func (s *Server) handleLlamaCppStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	llamaMgr := registry.NewLlamaCppManager(registry.DefaultLlamaCppDir())
	status := llamaMgr.Status()

	json.NewEncoder(w).Encode(map[string]any{
		"installed":      status.Installed,
		"version":        status.Version,
		"path":           status.Path,
		"gpu_backend":    status.GPUBackend,
		"gpu_devices":    status.GPUDevices,
		"server_running": status.ServerRunning,
	})
}

type loadLlamaModelRequest struct {
	ModelPath  string `json:"model_path"`
	NGPULayers int    `json:"n_gpu_layers"`
}

func (s *Server) handleLlamaCppLoadModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req loadLlamaModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("decode error: %v", err), http.StatusBadRequest)
		return
	}
	if req.ModelPath == "" {
		http.Error(w, "model_path is required", http.StatusBadRequest)
		return
	}

	llamaMgr := registry.NewLlamaCppManager(registry.DefaultLlamaCppDir())
	status := llamaMgr.Status()

	if !status.Installed {
		http.Error(w, "llama.cpp not installed; run 'spiral setup' first", http.StatusNotFound)
		return
	}

	if status.ServerRunning {
		llamaMgr.StopServer()
	}

	cmd, err := llamaMgr.StartServer(req.ModelPath, req.NGPULayers)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to start server: %v", err), http.StatusInternalServerError)
		return
	}

	go func() {
		time.Sleep(2 * time.Second)
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":       "started",
		"pid":          cmd.Process.Pid,
		"model_path":   req.ModelPath,
		"n_gpu_layers": req.NGPULayers,
	})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	html := `<!DOCTYPE html>
<html>
<head><title>Spiral CodeMaker Dashboard</title></head>
<body>
<h1>Spiral CodeMaker</h1>
<p>Dashboard at <a href="/dashboard/">/dashboard/</a></p>
<p>API Endpoints:</p>
<ul>
<li><a href="/api/snapshot">/api/snapshot</a></li>
<li><a href="/api/iterations">/api/iterations</a></li>
<li><a href="/api/units">/api/units</a></li>
<li><a href="/api/attention">/api/attention</a></li>
<li><a href="/api/report">/api/report</a></li>
<li><a href="/api/runs">/api/runs</a></li>
<li><a href="/api/models">/api/models</a></li>
<li><a href="/api/health">/api/health</a></li>
</ul>
</body>
</html>`
	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, html)
}

func (s *Server) Start() error {
	return s.server.ListenAndServe()
}

func (s *Server) Handler() http.Handler {
	return s.server.Handler
}

func (s *Server) ModelRegistry() *registry.ModelRegistry {
	return s.modelReg
}
