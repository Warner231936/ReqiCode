package workspace

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kilo/spiral-codemaker/code/patches"
	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Workspace struct {
	rootPath   string
	patcher    *patches.Patcher
	proposer   *proposals.Proposer
	validator  *proposals.Validator
}

func NewWorkspace(rootPath string) (*Workspace, error) {
	if err := os.MkdirAll(rootPath, 0755); err != nil {
		return nil, err
	}
	return &Workspace{
		rootPath:  rootPath,
		patcher:   patches.NewPatcher(rootPath),
		proposer:  proposals.NewProposer(),
		validator: proposals.NewValidator(),
	}, nil
}

func (w *Workspace) RootPath() string {
	return w.rootPath
}

func (w *Workspace) CreateProposal(op state.CodeOperation, file, after, reason, unitID string, conf state.Confidence, expectedEffect string, deps []string) state.CodeProposal {
	return w.proposer.CreateProposal(op, file, after, reason, unitID, conf, expectedEffect, deps)
}

func (w *Workspace) ApplyProposal(proposal state.CodeProposal) error {
	if err := w.validator.Validate(proposal); err != nil {
		return err
	}
	return w.patcher.Apply(proposal)
}

func (w *Workspace) WriteFile(relPath, content string) error {
	return w.patcher.WriteFile(relPath, content)
}

func (w *Workspace) ReadFile(relPath string) (string, error) {
	return w.patcher.ReadFile(relPath)
}

func (w *Workspace) FileExists(relPath string) bool {
	return w.patcher.FileExists(relPath)
}

func (w *Workspace) ListFiles() ([]string, error) {
	return w.patcher.ListFiles()
}

func (w *Workspace) EnsureDir(relPath string) error {
	full := filepath.Join(w.rootPath, relPath)
	return os.MkdirAll(full, 0755)
}

func (w *Workspace) CleanDir(relPath string) error {
	full := filepath.Join(w.rootPath, relPath)
	if _, err := os.Stat(full); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(full)
}

func (w *Workspace) Path(relPath string) string {
	return filepath.Join(w.rootPath, relPath)
}

func (w *Workspace) ModuleName() string {
	content, err := os.ReadFile(filepath.Join(w.rootPath, "go.mod"))
	if err != nil {
		return fmt.Sprintf("module unnamed\n\ngo 1.21\n")
	}
	return string(content)
}

func (w *Workspace) InitGoModule(moduleName string) error {
	content := fmt.Sprintf("module %s\n\ngo 1.21\n", moduleName)
	return os.WriteFile(filepath.Join(w.rootPath, "go.mod"), []byte(content), 0644)
}
