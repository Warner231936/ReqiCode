package patches

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/kilo/spiral-codemaker/core/state"
)

type Patcher struct {
	workspace string
}

func NewPatcher(workspace string) *Patcher {
	return &Patcher{workspace: workspace}
}

func (p *Patcher) Apply(proposal state.CodeProposal) error {
	fullPath := filepath.Join(p.workspace, proposal.File)

	switch proposal.Operation {
	case state.OpCreate:
		return p.applyCreate(fullPath, proposal.After)
	case state.OpModify:
		return p.applyModify(fullPath, proposal.Before, proposal.After)
	case state.OpDelete:
		return p.applyDelete(fullPath)
	case state.OpMove:
		return p.applyMove(fullPath, proposal.Before, proposal.After)
	case state.OpRename:
		return p.applyRename(fullPath, proposal.Before)
	default:
		return fmt.Errorf("unknown operation: %s", proposal.Operation)
	}
}

func (p *Patcher) applyCreate(fullPath, content string) error {
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	if _, err := os.Stat(fullPath); err == nil {
		return fmt.Errorf("file already exists: %s", fullPath)
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

func (p *Patcher) applyModify(fullPath, before, after string) error {
	current, err := os.ReadFile(fullPath)
	if err != nil {
		return fmt.Errorf("file not found for modify: %s", fullPath)
	}
	if before != "" && !strings.Contains(string(current), before) {
		return fmt.Errorf("before-content mismatch for %s", fullPath)
	}
	modified := string(current)
	if before != "" {
		modified = strings.Replace(modified, before, after, 1)
	} else {
		modified = after
	}
	return os.WriteFile(fullPath, []byte(modified), 0644)
}

func (p *Patcher) applyDelete(fullPath string) error {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return nil
	}
	return os.Remove(fullPath)
}

func (p *Patcher) applyMove(fullPath, before, after string) error {
	src := filepath.Join(p.workspace, before)
	if _, err := os.Stat(src); os.IsNotExist(err) {
		return fmt.Errorf("source not found for move: %s", src)
	}
	return os.Rename(src, fullPath)
}

func (p *Patcher) applyRename(fullPath, newName string) error {
	if _, err := os.Stat(fullPath); os.IsNotExist(err) {
		return fmt.Errorf("file not found for rename: %s", fullPath)
	}
	newPath := filepath.Join(filepath.Dir(fullPath), newName)
	return os.Rename(fullPath, newPath)
}

func (p *Patcher) Workspace() string {
	return p.workspace
}

func (p *Patcher) EnsureDir(path string) error {
	return os.MkdirAll(path, 0755)
}

func (p *Patcher) WriteFile(relPath, content string) error {
	fullPath := filepath.Join(p.workspace, relPath)
	dir := filepath.Dir(fullPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}

func (p *Patcher) ReadFile(relPath string) (string, error) {
	fullPath := filepath.Join(p.workspace, relPath)
	data, err := os.ReadFile(fullPath)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (p *Patcher) FileExists(relPath string) bool {
	fullPath := filepath.Join(p.workspace, relPath)
	_, err := os.Stat(fullPath)
	return err == nil
}

func (p *Patcher) ListFiles() ([]string, error) {
	var files []string
	err := filepath.Walk(p.workspace, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			rel, _ := filepath.Rel(p.workspace, path)
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}
