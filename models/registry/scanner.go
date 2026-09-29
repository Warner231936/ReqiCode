package registry

import (
	"context"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/kilo/spiral-codemaker/models/routing"
)

type GGUFScanner struct{}

func NewGGUFScanner() *GGUFScanner {
	return &GGUFScanner{}
}

func (s *GGUFScanner) Scan(ctx context.Context, rootPath string) ([]ModelEntry, error) {
	var entries []ModelEntry

	err := filepath.WalkDir(rootPath, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" || d.Name() == "node_modules" || d.Name() == "vendor" {
				return filepath.SkipDir
			}
			return nil
		}

		name := d.Name()
		if !strings.HasSuffix(strings.ToLower(name), ".gguf") {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		entry := ModelEntry{
			ID:         fmt.Sprintf("gguf-%s", strings.TrimSuffix(name, ".gguf")),
			Name:       strings.TrimSuffix(name, ".gguf"),
			Kind:       KindGGUF,
			Provider:   "gguf-local",
			ModelID:    name,
			Capability: detectGGUFCapability(name),
			Path:       path,
			Status:     ModelStatusAvailable,
			SizeMB:     info.Size() / (1024 * 1024),
			Description: fmt.Sprintf("Local GGUF model: %s", name),
		}
		entries = append(entries, entry)

		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		return nil
	})

	if err != nil {
		return nil, err
	}

	return entries, nil
}

func (s *GGUFScanner) ScanDrives(ctx context.Context, drives []string) ([]ModelEntry, error) {
	var all []ModelEntry
	for _, drive := range drives {
		entries, err := s.Scan(ctx, drive)
		if err != nil {
			continue
		}
		all = append(all, entries...)
	}
	return all, nil
}

func detectGGUFCapability(filename string) routing.ModelCapability {
	lower := strings.ToLower(filename)
	if strings.Contains(lower, "code") || strings.Contains(lower, "coder") || strings.Contains(lower, "starcoder") {
		return routing.CapSpecialize
	}
	if strings.Contains(lower, "reason") || strings.Contains(lower, "qwen") || strings.Contains(lower, "deepseek") {
		return routing.CapReasoning
	}
	if strings.Contains(lower, "embed") || strings.Contains(lower, "bge") {
		return routing.CapEmbed
	}
	return routing.CapFast
}
