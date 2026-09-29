package registry

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type LlamaCppManager struct {
	installDir string
	client     *http.Client
}

const (
	llamaCppBaseURL = "https://github.com/ggerganov/llama.cpp"
	llamaCppGGMLURL = "https://github.com/ggml-org/llama.cpp"
)

type LlamaCppStatus struct {
	Installed     bool   `json:"installed"`
	Version       string `json:"version"`
	Path          string `json:"path"`
	GPUBackend    string `json:"gpu_backend"`
	GPUDevices    []string `json:"gpu_devices"`
	ServerRunning bool   `json:"server_running"`
}

func NewLlamaCppManager(installDir string) *LlamaCppManager {
	if installDir == "" {
		installDir = filepath.Join(os.TempDir(), "spiral-tools", "llama-cpp")
		if wd, err := os.Getwd(); err == nil {
			candidate := filepath.Join(wd, "third_party", "llama-cpp")
			if _, err := os.Stat(candidate); err == nil {
				installDir = candidate
			}
		}
	}
	return &LlamaCppManager{
		installDir: installDir,
		client:     &http.Client{Timeout: 60 * time.Second},
	}
}

func DefaultLlamaCppDir() string {
	if wd, err := os.Getwd(); err == nil {
		return filepath.Join(wd, "third_party", "llama-cpp")
	}
	return filepath.Join(os.TempDir(), "spiral-tools", "llama-cpp")
}

func (m *LlamaCppManager) Status() LlamaCppStatus {
	status := LlamaCppStatus{
		Installed: false,
		Path:      m.installDir,
		GPUBackend: "none",
	}

	binPath := m.binaryPath()
	if _, err := os.Stat(binPath); err == nil {
		status.Installed = true

		cmd := exec.CommandContext(context.Background(), binPath, "--version")
		if out, err := cmd.Output(); err == nil {
			version := strings.TrimSpace(string(out))
			status.Version = version
			if strings.Contains(strings.ToLower(version), "cuda") || strings.Contains(strings.ToLower(version), "gpu") {
				status.GPUBackend = "cuda"
				status.GPUDevices = m.detectGPUs()
			}
		}
	}

	status.ServerRunning = m.isServerRunning()

	return status
}

func (m *LlamaCppManager) binaryPath() string {
	if runtime.GOOS == "windows" {
		candidates := []string{"llama-server.exe", "server.exe"}
		for _, c := range candidates {
			p := filepath.Join(m.installDir, c)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
		return filepath.Join(m.installDir, "llama-server.exe")
	}
	return filepath.Join(m.installDir, "server")
}

func (m *LlamaCppManager) detectGPUs() []string {
	gpus := []string{}

	if runtime.GOOS == "windows" {
		if out, err := exec.Command("nvidia-smi", "--query-gpu=name", "--format=csv,noheader").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line != "" {
					gpus = append(gpus, line)
				}
			}
		}
	} else {
		if out, err := exec.Command("lspci", "-nn").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if strings.Contains(strings.ToLower(line), "nvidia") || strings.Contains(strings.ToLower(line), "vga") {
					gpus = append(gpus, strings.TrimSpace(line))
				}
			}
		}
	}

	return gpus
}

func (m *LlamaCppManager) Install(ctx context.Context) error {
	if err := os.MkdirAll(m.installDir, 0755); err != nil {
		return err
	}

	binPath := m.binaryPath()
	if _, err := os.Stat(binPath); err == nil {
		return nil
	}

	return fmt.Errorf("llama.cpp server binary not found at %s. Please build it with CUDA support:\n  git clone %s\n  cd llama.cpp\n  LLAMA_CUDA=1 make server\n  Copy 'server' binary to %s", binPath, llamaCppBaseURL, m.installDir)
}

func (m *LlamaCppManager) StartServer(modelPath string, nGPULayers int) (*exec.Cmd, error) {
	binPath := m.binaryPath()
	if _, err := os.Stat(binPath); err != nil {
		return nil, fmt.Errorf("llama.cpp server binary not found; run 'spiral setup' first")
	}

	args := []string{
		"--model", modelPath,
		"--host", "0.0.0.0",
		"--port", "8080",
		"--threads", "4",
	}

	if nGPULayers > 0 {
		args = append(args, "--n-gpu-layers", fmt.Sprintf("%d", nGPULayers))
	}

	if status := m.Status(); status.GPUBackend == "cuda" {
		args = append(args, "--mmq")
	}

	cmd := exec.Command(binPath, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("failed to start llama.cpp server: %w", err)
	}

	return cmd, nil
}

func (m *LlamaCppManager) StopServer() error {
	resp, err := http.Get("http://localhost:8080/shutdown")
	if err == nil {
		resp.Body.Close()
	}
	return nil
}

func (m *LlamaCppManager) isServerRunning() bool {
	resp, err := m.client.Get("http://localhost:8080/health")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

func (m *LlamaCppManager) InstallDir() string {
	return m.installDir
}

func (m *LlamaCppManager) DownloadPrebuiltRelease(ctx context.Context, version string) error {
	if version == "" {
		version = "latest"
	}

	var assetURL string
	arch := runtime.GOARCH
	switch runtime.GOOS {
	case "windows":
		assetURL = fmt.Sprintf("%s/releases/download/b11205/llama-b11205-bin-win-cuda-12.4-x64.zip", llamaCppGGMLURL)
	case "linux":
		if arch == "arm64" {
			assetURL = fmt.Sprintf("%s/releases/latest/download/llama-cpp-server-linux-aarch64.tar.gz", llamaCppBaseURL)
		} else {
			assetURL = fmt.Sprintf("%s/releases/latest/download/llama-cpp-server-linux-x86_64.tar.gz", llamaCppBaseURL)
		}
	case "darwin":
		if arch == "arm64" {
			assetURL = fmt.Sprintf("%s/releases/latest/download/llama-cpp-server-darwin-arm64.tar.gz", llamaCppBaseURL)
		} else {
			assetURL = fmt.Sprintf("%s/releases/latest/download/llama-cpp-server-darwin-x86_64.tar.gz", llamaCppBaseURL)
		}
	}

	req, err := http.NewRequestWithContext(ctx, "GET", assetURL, nil)
	if err != nil {
		return err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download returned %d", resp.StatusCode)
	}

	archivePath := filepath.Join(m.installDir, "llama-cpp-archive")
	os.MkdirAll(m.installDir, 0755)
	out, err := os.Create(archivePath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, resp.Body); err != nil {
		return fmt.Errorf("failed to save archive: %w", err)
	}

	return m.extractArchive(archivePath)
}

func (m *LlamaCppManager) extractArchive(archivePath string) error {
	ext := strings.ToLower(filepath.Ext(archivePath))
	if ext == ".zip" {
		return unzip(archivePath, m.installDir)
	}
	return unzip(archivePath, m.installDir)
}

func unzip(zipPath, dest string) error {
	archive, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("failed to open zip: %w", err)
	}
	defer archive.Close()

	if err := os.MkdirAll(dest, 0755); err != nil {
		return err
	}

	for _, f := range archive.File {
		filePath := filepath.Join(dest, f.Name)
		if f.FileInfo().IsDir() {
			os.MkdirAll(filePath, 0755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
			return err
		}

		srcFile, err := f.Open()
		if err != nil {
			return err
		}

		dstFile, err := os.Create(filePath)
		if err != nil {
			srcFile.Close()
			return err
		}

		if _, err := io.Copy(dstFile, srcFile); err != nil {
			srcFile.Close()
			dstFile.Close()
			return err
		}

		srcFile.Close()
		dstFile.Close()
	}

	return nil
}
