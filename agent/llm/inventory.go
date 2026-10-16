package llm

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Inventory is a point-in-time picture of the local AI environment.
type Inventory struct {
	ScannedAt time.Time     `json:"scanned_at"`
	Backends  []Health      `json:"backends"`
	Models    []ModelInfo   `json:"models"`
	GGUF      []GGUFModel   `json:"gguf"`
	Notes     []string      `json:"notes,omitempty"`
	Errors    []string      `json:"errors,omitempty"`
}

// GGUFModel is one local weights file.
type GGUFModel struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	Params    string `json:"params,omitempty"`
	Quant     string `json:"quant,omitempty"`
	Family    string `json:"family,omitempty"`
	Role      string `json:"role"`
	Score     int    `json:"score"`
	Partial   bool   `json:"partial,omitempty"`
}

// InventoryScanner walks the machine for usable models.
type InventoryScanner struct {
	Backends []Backend
	Dirs     []string
	// MaxDepth bounds recursion below each root. Deep model trees exist, and a
	// scan that descends without limit can take minutes on a machine with many
	// weight shards.
	MaxDepth int
}

// Scan collects backends, their models, and local GGUF weights.
//
// Backend probing and filesystem walking are independent, so both run even if one
// fails: a machine with no reachable server still has weights on disk, and a
// machine with a server may have its weights elsewhere. Reporting only half the
// picture is what makes an inventory untrustworthy.
func (s *InventoryScanner) Scan(ctx context.Context) Inventory {
	if s.MaxDepth == 0 {
		s.MaxDepth = 4
	}
	inv := Inventory{ScannedAt: time.Now()}

	for _, b := range s.Backends {
		h := b.Health(ctx)
		inv.Backends = append(inv.Backends, h)
		models, err := b.ListModels(ctx)
		if err != nil {
			inv.Errors = append(inv.Errors, fmt.Sprintf("%s: %v", b.Name(), err))
			continue
		}
		for i := range models {
			models[i].Backend = b.Name()
			models[i].Role = classifyModel(models[i].ID)
		}
		inv.Models = append(inv.Models, models...)
	}

	inv.GGUF = s.scanGGUF()
	for i := range inv.GGUF {
		inv.Models = append(inv.Models, ModelInfo{
			ID:        inv.GGUF[i].Name,
			Backend:   "llama.cpp",
			SizeBytes: inv.GGUF[i].SizeBytes,
			Family:    inv.GGUF[i].Family,
			Params:    inv.GGUF[i].Params,
			Quant:     inv.GGUF[i].Quant,
			Source:    "gguf",
		})
	}

	sort.SliceStable(inv.Models, func(i, j int) bool {
		if inv.Models[i].Source != inv.Models[j].Source {
			return inv.Models[i].Source < inv.Models[j].Source
		}
		return inv.Models[i].ID < inv.Models[j].ID
	})
	inv.Notes = describeInventory(inv)
	return inv
}

// scanGGUF finds weights on disk and scores them by likely usefulness.
func (s *InventoryScanner) scanGGUF() []GGUFModel {
	var out []GGUFModel
	seen := map[string]bool{}
	for _, dir := range s.Dirs {
		if dir == "" {
			continue
		}
		root := filepath.Clean(dir)
		info, err := os.Stat(root)
		if err != nil {
			continue
		}
		if !info.IsDir() {
			if g, ok := describeGGUF(root, info.Size()); ok && !seen[root] {
				seen[root] = true
				out = append(out, g)
			}
			continue
		}
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				name := d.Name()
				if strings.HasPrefix(name, ".") || name == "node_modules" {
					return filepath.SkipDir
				}
				if depth(root, p) >= s.MaxDepth {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(strings.ToLower(p), ".gguf") {
				return nil
			}
			fi, ferr := d.Info()
			if ferr != nil {
				return nil
			}
			g, ok := describeGGUF(p, fi.Size())
			if !ok || seen[p] {
				return nil
			}
			seen[p] = true
			out = append(out, g)
			return nil
		})
	}

	// Sharded weights are unusable on their own. A directory whose only files are
	// shards is reported once, as the base name, so an operator sees the model
	// rather than four fragments of it.
	merged := mergeShards(out)
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].Score != merged[j].Score {
			return merged[i].Score > merged[j].Score
		}
		return merged[i].Name < merged[j].Name
	})
	return merged
}

// mergeShards collapses llama.cpp split files into one entry.
func mergeShards(models []GGUFModel) []GGUFModel {
	byKey := map[string]*GGUFModel{}
	var order []string
	for i := range models {
		m := models[i]
		base := filepath.Base(m.Path)
		if !strings.Contains(base, "-0000") {
			continue
		}
		prefix := base[:strings.Index(base, "-0000")]
		key := filepath.Dir(m.Path) + "/" + prefix
		if existing, ok := byKey[key]; ok {
			existing.SizeBytes += m.SizeBytes
			existing.Partial = true
			continue
		}
		cp := m
		cp.Name = prefix + ".gguf"
		cp.Partial = true
		byKey[key] = &cp
		order = append(order, key)
	}

	drop := map[string]bool{}
	for _, k := range order {
		m := byKey[k]
		m.Role = classifyModel(m.Name)
		m.Score = scoreGGUF(*m)
		for i := range models {
			p := filepath.Base(models[i].Path)
			if strings.Contains(p, "-0000") && filepath.Dir(models[i].Path)+"/"+p[:strings.Index(p, "-0000")] == k {
				drop[models[i].Path] = true
			}
		}
	}

	var out []GGUFModel
	seen := map[string]bool{}
	for i := range models {
		if drop[models[i].Path] {
			continue
		}
		out = append(out, models[i])
		seen[models[i].Path] = true
	}
	for _, k := range order {
		out = append(out, *byKey[k])
	}
	return out
}

func depth(root, p string) int {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return 0
	}
	if rel == "." {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(rel), "/"))
}

// describeGGUF builds a GGUFModel from a path and size, rejecting files that are
// vocabularies or otherwise not a usable set of weights.
//
// The size floor matters: llama.cpp ships ~40 test vocab files in its models
// directory, and reporting them as models would fill the inventory with noise
// that an operator then has to filter by eye.
func describeGGUF(path string, size int64) (GGUFModel, bool) {
	base := filepath.Base(path)
	if size < 8*1024*1024 {
		return GGUFModel{}, false
	}
	lower := strings.ToLower(base)
	if strings.Contains(lower, "vocab") {
		return GGUFModel{}, false
	}
	g := GGUFModel{
		Path:      path,
		Name:      base,
		SizeBytes: size,
		Params:    detectParams(base, size),
		Quant:     detectQuant(base),
		Family:    detectFamily(base),
	}
	g.Role = classifyModel(g.Name)
	g.Score = scoreGGUF(g)
	return g, true
}

// scoreGGUF ranks a weights file by how likely it is to be usable for coding work.
//
// Size is the primary signal because it is the only one available without loading
// the file, and the ordering it induces matches capability closely enough in
// practice. Name signals adjust it: an explicit coder tag is worth more than a
// size advantage, and an embedding or reranker tag disqualifies the file from
// chat roles entirely.
func scoreGGUF(g GGUFModel) int {
	if classifyModel(g.Name) == "embedding" {
		return 1
	}
	score := 0
	gb := float64(g.SizeBytes) / (1 << 30)
	switch {
	case gb >= 30:
		score = 70
	case gb >= 16:
		score = 60
	case gb >= 9:
		score = 55
	case gb >= 6:
		score = 50
	case gb >= 4:
		score = 45
	case gb >= 2:
		score = 35
	case gb >= 1:
		score = 25
	default:
		score = 10
	}

	name := strings.ToLower(g.Name)
	switch {
	case strings.Contains(name, "coder"), strings.Contains(name, "code"):
		score += 12
	case strings.Contains(name, "instruct"), strings.Contains(name, "it"):
		score += 6
	case strings.Contains(name, "embed"), strings.Contains(name, "bge"), strings.Contains(name, "e5"):
		score -= 40
	case strings.Contains(name, "tinylama"), strings.Contains(name, "1.1b"), strings.Contains(name, "0.5b"):
		score -= 12
	case strings.Contains(name, "base"):
		score -= 8
	}
	if g.Quant != "" && (strings.Contains(strings.ToLower(g.Quant), "f16") || strings.Contains(strings.ToLower(g.Quant), "f32")) {
		score -= 3
	}
	if score < 0 {
		score = 0
	}
	return score
}

// classifyModel assigns a role from a model name.
//
// This is a heuristic over names and metadata, not a capability probe, and it is
// used only for reporting and for default route selection. Anything that depends
// on knowing a model's real capability must ask the model, not the name.
func classifyModel(name string) string {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "embed"), strings.Contains(n, "bge"), strings.Contains(n, "e5-"),
		strings.Contains(n, "nomic-embed"), strings.Contains(n, "minilm"):
		return "embedding"
	case strings.Contains(n, "rerank"):
		return "reranker"
	case strings.Contains(n, "coder"), strings.Contains(n, "code"), strings.Contains(n, "deepseek-coder"):
		return "coder"
	case strings.Contains(n, "tinylama"), strings.Contains(n, "llama3.2"), strings.Contains(n, "llama-3.2"),
		strings.Contains(n, "qwen2.5-1.5b"), strings.Contains(n, "qwen3-1.7b"), strings.Contains(n, "small"),
		strings.Contains(n, "mini"), strings.Contains(n, "-1b"), strings.Contains(n, "-3b"):
		return "fast"
	case strings.Contains(n, "llama3.1"), strings.Contains(n, "llama3.2"), strings.Contains(n, "llama-3"),
		strings.Contains(n, "qwen"), strings.Contains(n, "mistral"), strings.Contains(n, "dolphin"),
		strings.Contains(n, "gemma"), strings.Contains(n, "granite"), strings.Contains(n, "phi"):
		return "reasoning"
	default:
		return "general"
	}
}

func detectParams(name string, size int64) string {
	l := strings.ToLower(name)
	// An explicit parameter count in the filename is authoritative.
	for _, marker := range []string{"0.5b", "1.1b", "1.5b", "3b", "7b", "8b", "13b", "14b", "27b", "30b", "32b", "70b", "72b", "120b", "235b"} {
		if strings.Contains(l, marker) {
			return strings.ToUpper(marker)
		}
	}
	gb := size / (1 << 30)
	switch {
	case gb >= 120:
		return "~120B"
	case gb >= 60:
		return "~65B"
	case gb >= 28:
		return "~30B"
	case gb >= 15:
		return "~14B"
	case gb >= 6:
		return "~7B"
	case gb >= 3:
		return "~3B"
	case gb >= 1:
		return "~1B"
	default:
		return ""
	}
}

func detectQuant(name string) string {
	l := strings.ToLower(name)
	switch {
	case strings.Contains(l, "q2_k"):
		return "Q2_K"
	case strings.Contains(l, "q3_k"):
		return "Q3_K_M"
	case strings.Contains(l, "q4_k_m"):
		return "Q4_K_M"
	case strings.Contains(l, "q4_0"):
		return "Q4_0"
	case strings.Contains(l, "q5_k_m"):
		return "Q5_K_M"
	case strings.Contains(l, "q5_0"):
		return "Q5_0"
	case strings.Contains(l, "q6_k"):
		return "Q6_K"
	case strings.Contains(l, "q8_0"):
		return "Q8_0"
	case strings.Contains(l, "f16"):
		return "F16"
	case strings.Contains(l, "f32"):
		return "F32"
	default:
		return ""
	}
}

func detectFamily(name string) string {
	l := strings.ToLower(name)
	families := []string{"qwen", "llama", "deepseek", "mistral", "mixtral", "gemma", "dolphin", "phi",
		"granite", "codellama", "starcoder", "gpt", "yi", "command-r"}
	for _, f := range families {
		if strings.Contains(l, f) {
			return f
		}
	}
	return ""
}

func describeInventory(inv Inventory) []string {
	var notes []string
	var reachable []string
	for _, h := range inv.Backends {
		if h.Reachable {
			reachable = append(reachable, h.Backend)
		}
	}
	switch len(reachable) {
	case 0:
		notes = append(notes, "No inference backend is currently reachable. The agent will run in deterministic offline mode unless a server is started (see: agent doctor).")
	case 1:
		notes = append(notes, "One inference backend reachable: "+reachable[0]+".")
	default:
		notes = append(notes, "Multiple inference backends reachable: "+strings.Join(reachable, ", ")+".")
	}
	if len(inv.GGUF) > 0 {
		notes = append(notes, fmt.Sprintf("%d local GGUF weight file(s) discovered; no download is required to use them via llama.cpp.", len(inv.GGUF)))
	}
	return notes
}

// Format renders an inventory for a terminal.
func (inv Inventory) Format() string {
	var b strings.Builder
	b.WriteString("LOCAL AI INVENTORY\n")
	b.WriteString("===================\n\n")

	b.WriteString("Backends:\n")
	if len(inv.Backends) == 0 {
		b.WriteString("  (none configured or discovered)\n")
	}
	for _, h := range inv.Backends {
		status := "unreachable"
		if h.Reachable {
			status = "reachable"
		}
		fmt.Fprintf(&b, "  [%s] %-18s kind=%-8s latency=%-8s models=%d",
			status, h.Backend, h.Kind, h.Latency.Round(time.Millisecond), h.ModelCount)
		if h.Detail != "" {
			fmt.Fprintf(&b, "  %s", h.Detail)
		}
		b.WriteString("\n")
	}
	if len(inv.Errors) > 0 {
		b.WriteString("\nBackend errors:\n")
		for _, e := range inv.Errors {
			fmt.Fprintf(&b, "  - %s\n", e)
		}
	}

	b.WriteString("\nModels by backend:\n")
	byBackend := map[string][]ModelInfo{}
	for _, m := range inv.Models {
		byBackend[m.Backend] = append(byBackend[m.Backend], m)
	}
	if len(byBackend) == 0 {
		b.WriteString("  (none discovered)\n")
	}
	names := make([]string, 0, len(byBackend))
	for k := range byBackend {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, backend := range names {
		fmt.Fprintf(&b, "  %s:\n", backend)
		for _, m := range byBackend[backend] {
			fmt.Fprintf(&b, "    %-44s role=%-9s", m.ID, m.Role)
			if m.SizeBytes > 0 {
				fmt.Fprintf(&b, " %6.2f GB", float64(m.SizeBytes)/(1<<30))
			}
			if m.Params != "" {
				fmt.Fprintf(&b, " params=%s", m.Params)
			}
			if m.Quant != "" {
				fmt.Fprintf(&b, " quant=%s", m.Quant)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("\nLocal GGUF weights:\n")
	if len(inv.GGUF) == 0 {
		b.WriteString("  (none discovered)\n")
	}
	for _, g := range inv.GGUF {
		fmt.Fprintf(&b, "  %6.2f GB  %-56s role=%-9s score=%-3d %s\n",
			float64(g.SizeBytes)/(1<<30), g.Name, g.Role, g.Score, g.Path)
	}

	if len(inv.Notes) > 0 {
		b.WriteString("\nNotes:\n")
		for _, n := range inv.Notes {
			fmt.Fprintf(&b, "  - %s\n", n)
		}
	}
	return b.String()
}

// Best returns the highest-scoring GGUF model, which is what an operator almost
// always wants when asking "what should I run".
func (inv Inventory) Best() (GGUFModel, bool) {
	var best GGUFModel
	found := false
	for _, g := range inv.GGUF {
		if g.Role == "embedding" || g.Role == "reranker" {
			continue
		}
		if !found || g.Score > best.Score {
			best = g
			found = true
		}
	}
	return best, found
}

// BestEmbedding returns the highest-scoring embedding model.
func (inv Inventory) BestEmbedding() (GGUFModel, bool) {
	var best GGUFModel
	found := false
	for _, g := range inv.GGUF {
		if g.Role != "embedding" {
			continue
		}
		if !found || g.Score > best.Score {
			best = g
			found = true
		}
	}
	return best, found
}

// ChatModels returns non-embedding model IDs grouped by backend, preferring chat
// roles over embedding roles.
func (inv Inventory) ChatModels() []ModelInfo {
	var out []ModelInfo
	for _, m := range inv.Models {
		if m.Role == "embedding" || m.Role == "reranker" {
			continue
		}
		out = append(out, m)
	}
	return out
}