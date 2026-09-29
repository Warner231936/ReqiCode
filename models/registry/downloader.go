package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/models/routing"
)

type DownloadProgress struct {
	ModelID      string  `json:"model_id"`
	TotalBytes   int64   `json:"total_bytes"`
	Downloaded   int64   `json:"downloaded"`
	Percent      int     `json:"percent"`
	Speed        int64   `json:"speed_bps"`
	Status       string  `json:"status"`
	Error        string  `json:"error,omitempty"`
}

type LocalModelDownloader struct {
	cacheDir string
	client   *http.Client
	mu       sync.Mutex
	progress map[string]*DownloadProgress
}

type hfRepoFile struct {
	Rfilename string `json:"rfilename"`
	Size      int64  `json:"size"`
}

type hfRepoInfo struct {
	ID      string       `json:"id"`
	Files   []hfRepoFile `json:"children"`
}

func NewLocalModelDownloader(cacheDir string) *LocalModelDownloader {
	if cacheDir == "" {
		cacheDir = filepath.Join(os.TempDir(), "spiral-models")
	}
	os.MkdirAll(cacheDir, 0755)
	return &LocalModelDownloader{
		cacheDir: cacheDir,
		client:   &http.Client{Timeout: 0},
		progress: make(map[string]*DownloadProgress),
	}
}

func (d *LocalModelDownloader) ListRepoFiles(ctx context.Context, modelID, apiToken string) ([]hfRepoFile, error) {
	apiURL := fmt.Sprintf("https://huggingface.co/api/models/%s", modelID)
	req, err := http.NewRequestWithContext(ctx, "GET", apiURL, nil)
	if err != nil {
		return nil, err
	}
	if apiToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HF API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HF API returned %d", resp.StatusCode)
	}

	var info hfRepoInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return nil, fmt.Errorf("failed to parse repo info: %w", err)
	}

	return info.Files, nil
}

func (d *LocalModelDownloader) DownloadModel(ctx context.Context, modelID, apiToken string) (string, error) {
	localDir := filepath.Join(d.cacheDir, modelID)

	if _, err := os.Stat(filepath.Join(localDir, "config.json")); err == nil {
		return localDir, nil
	}

	files, err := d.ListRepoFiles(ctx, modelID, apiToken)
	if err != nil {
		return "", fmt.Errorf("listing repo files: %w", err)
	}

	importantFiles := make(map[string]bool)
	for _, f := range files {
		lower := strings.ToLower(f.Rfilename)
		if strings.HasSuffix(lower, ".gguf") ||
			strings.HasSuffix(lower, ".bin") ||
			strings.HasSuffix(lower, ".safetensors") ||
			strings.HasSuffix(lower, ".json") ||
			strings.HasSuffix(lower, ".txt") ||
			f.Rfilename == "special_tokens_map.json" {
			importantFiles[f.Rfilename] = true
		}
	}

	d.mu.Lock()
	d.progress[modelID] = &DownloadProgress{
		ModelID:  modelID,
		Status:   "downloading",
	}
	d.mu.Unlock()

	for _, f := range files {
		if !importantFiles[f.Rfilename] {
			continue
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		default:
		}

		fileURL := fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, f.Rfilename)
		destPath := filepath.Join(localDir, f.Rfilename)

		if err := os.MkdirAll(filepath.Dir(destPath), 0755); err != nil {
			return "", err
		}

		if _, err := os.Stat(destPath); err == nil {
			continue
		}

		if err := d.downloadFile(ctx, fileURL, destPath, apiToken, modelID, f.Size); err != nil {
			d.mu.Lock()
			d.progress[modelID].Error = err.Error()
			d.progress[modelID].Status = "error"
			d.mu.Unlock()
			return "", err
		}
	}

	d.mu.Lock()
	d.progress[modelID].Status = "complete"
	d.mu.Unlock()

	return localDir, nil
}

func (d *LocalModelDownloader) downloadFile(ctx context.Context, url, destPath, apiToken, modelID string, totalSize int64) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	if apiToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", apiToken))
	}
	req.Header.Set("Range", "bytes=0-")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusPartialContent {
		return fmt.Errorf("download failed with status %d", resp.StatusCode)
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	var downloaded int64
	startTime := time.Now()

	progress := newProgressWriter(d, modelID, totalSize, &downloaded, startTime)
	reader := io.TeeReader(resp.Body, progress)
	if _, err := io.Copy(out, reader); err != nil {
		return err
	}

	return nil
}

type progressWriter struct {
	downloader *LocalModelDownloader
	modelID    string
	total      int64
	downloaded *int64
	start      time.Time
	lastUpdate time.Time
}

func (pw progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	*pw.downloaded += int64(n)

	now := time.Now()
	if now.Sub(pw.lastUpdate) > 500*time.Millisecond || *pw.downloaded == pw.total {
		pw.downloader.mu.Lock()
		pw.downloader.progress[pw.modelID] = &DownloadProgress{
			ModelID:    pw.modelID,
			TotalBytes: pw.total,
			Downloaded: *pw.downloaded,
			Percent:    int(float64(*pw.downloaded) / float64(pw.total) * 100),
			Speed:      int64(float64(*pw.downloaded) / now.Sub(pw.start).Seconds()),
			Status:     "downloading",
		}
		pw.downloader.mu.Unlock()
		pw.lastUpdate = now
	}

	return n, nil
}

func newProgressWriter(d *LocalModelDownloader, modelID string, total int64, downloaded *int64, start time.Time) io.Writer {
	return progressWriter{
		downloader: d,
		modelID:    modelID,
		total:      total,
		downloaded: downloaded,
		start:      start,
		lastUpdate: time.Now(),
	}
}

func (d *LocalModelDownloader) GetProgress(modelID string) *DownloadProgress {
	d.mu.Lock()
	defer d.mu.Unlock()
	if p, ok := d.progress[modelID]; ok {
		return p
	}
	return &DownloadProgress{
		ModelID: modelID,
		Status:  "not_found",
	}
}

func (d *LocalModelDownloader) ListDownloadedModels() []ModelEntry {
	var entries []ModelEntry

	dirs, err := filepath.Glob(filepath.Join(d.cacheDir, "*"))
	if err != nil {
		return entries
	}

	for _, dir := range dirs {
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			continue
		}
		name := info.Name()
		entries = append(entries, ModelEntry{
			ID:          fmt.Sprintf("local-%s", name),
			Name:        name,
			Kind:        KindHuggingFace,
			Provider:    "huggingface-local",
			ModelID:     name,
			Capability:  routing.CapFast,
			Path:        dir,
			Status:      ModelStatusAvailable,
			SizeMB:      info.Size() / (1024 * 1024),
			Description: fmt.Sprintf("Downloaded model: %s", name),
		})
	}

	return entries
}

func (d *LocalModelDownloader) CacheDir() string {
	return d.cacheDir
}

func (d *LocalModelDownloader) ConvertToGGUF(ctx context.Context, sourceDir, modelID, outputPath string) (string, error) {
	if sourceDir == "" {
		sourceDir = filepath.Join(d.cacheDir, modelID)
	}

	ggufPath := d.FindGGUFPath(modelID)
	if ggufPath != "" {
		return ggufPath, nil
	}

	converterPath := d.findConverterScript()
	if converterPath == "" {
		return "", fmt.Errorf("llama.cpp converter (convert_hf_to_gguf.py) not found. Run 'spiral setup' to install llama.cpp first")
	}

	outputFile := filepath.Join(d.cacheDir, modelID+".Q4_K_M.gguf")
	if outputPath != "" {
		outputFile = outputPath
	}

	if _, err := os.Stat(outputFile); err == nil {
		return outputFile, nil
	}

	pythonBin := d.findPython()
	if pythonBin == "" {
		return "", fmt.Errorf("python not found; install Python 3 to convert models to GGUF")
	}

	cmd := exec.CommandContext(ctx, pythonBin, converterPath, sourceDir, "--outfile", outputFile, "--outtype", "q4_k_m")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Dir = filepath.Dir(converterPath)

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("conversion failed: %w", err)
	}

	return outputFile, nil
}

func (d *LocalModelDownloader) findConverterScript() string {
	candidates := []string{
		filepath.Join(d.cacheDir, "llama-cpp", "convert_hf_to_gguf.py"),
		filepath.Join(DefaultLlamaCppDir(), "convert_hf_to_gguf.py"),
		filepath.Join("third_party", "llama-cpp", "convert_hf_to_gguf.py"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

func (d *LocalModelDownloader) findPython() string {
	for _, name := range []string{"python3", "python"} {
		if path, err := exec.LookPath(name); err == nil {
			return path
		}
	}
	return ""
}

func (d *LocalModelDownloader) HasGGUF(modelID string) bool {
	modelDir := filepath.Join(d.cacheDir, modelID)
	entries, err := os.ReadDir(modelDir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
			return true
		}
	}

	cachePath := filepath.Join(d.cacheDir, modelID+".Q4_K_M.gguf")
	_, err = os.Stat(cachePath)
	return err == nil
}

func (d *LocalModelDownloader) FindGGUFPath(modelID string) string {
	modelDir := filepath.Join(d.cacheDir, modelID)
	entries, err := os.ReadDir(modelDir)
	if err == nil {
		for _, e := range entries {
			if strings.HasSuffix(strings.ToLower(e.Name()), ".gguf") {
				return filepath.Join(modelDir, e.Name())
			}
		}
	}

	cachePath := filepath.Join(d.cacheDir, modelID+".Q4_K_M.gguf")
	if _, err := os.Stat(cachePath); err == nil {
		return cachePath
	}

	return ""
}
