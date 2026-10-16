// Package context decides what the model sees.
//
// A repository does not fit in a context window, and a local 7B model's window is
// smaller still. The choice of what to include therefore determines whether the
// agent works at all: too little and the model edits code it has not seen, too
// much and the relevant lines are buried under unrelated ones.
//
// Retrieval here is hybrid. Lexical search finds the exact symbol the request
// names, which embeddings are bad at because they blur exact identifiers. Dense
// retrieval finds the conceptually related code, which lexical search misses
// entirely when the request and the code use different words. Ranking combines
// both, with lexical evidence weighted higher because a wrong lexical hit is
// obvious and a wrong dense hit is not.
package context

import (
	"bufio"
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/fsignore"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/tools"
	"github.com/kilo/spiral-codemaker/agent/verify"
)

// Chunk is one retrievable piece of a file.
type Chunk struct {
	Path    string `json:"path"`
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Content string `json:"content"`
	// Kind is file, symbol, or prose. A symbol chunk is a whole function or type,
	// which is the unit a code change actually reasons about.
	Kind  string `json:"kind"`
	Name  string `json:"name,omitempty"`
	Tokens int   `json:"tokens"`
}

// Chunked is a file split into retrievable pieces.
type Chunked struct {
	Path   string  `json:"path"`
	Chunks []Chunk `json:"chunks"`
	Lines  int     `json:"lines"`
}

// Options configures the manager.
type Options struct {
	WorkspacePath string
	IgnoreGlobs   []string
	MaxFileBytes  int
	MaxFiles      int
	MaxResults    int
	IncludeTests  bool
	MaxContextKB  int
	// Embedder is optional. When nil, retrieval is lexical only, which is a
	// genuine reduction in quality but not a failure.
	Embedder Embedder
	Memory    *memory.Store
}

// Embedder turns text into vectors.
type Embedder interface {
	Embed(ctx context.Context, inputs []string) ([][]float32, error)
	Describe() string
}

// Manager builds and serves context.
type Manager struct {
	opts     Options
	ws       *tools.Workspace
	ignore   fsignore.Matcher
	project  *verify.Project
	indexed  []Chunk
	files    map[string]*Chunked
	indexedAt time.Time
}

// New builds a manager.
func New(opts Options) (*Manager, error) {
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = 256 * 1024
	}
	if opts.MaxFiles <= 0 {
		opts.MaxFiles = 600
	}
	if opts.MaxResults <= 0 {
		opts.MaxResults = 12
	}
	if opts.MaxContextKB <= 0 {
		opts.MaxContextKB = 24
	}
	project, err := verify.Detect(opts.WorkspacePath, opts.IgnoreGlobs)
	if err != nil {
		project = &verify.Project{Root: opts.WorkspacePath, Ecosystem: verify.EcosystemNone}
	}
	// An empty ignore list would index .git and build output as source, so the
	// default set is applied whenever the configuration supplies none.
	ignorePatterns := opts.IgnoreGlobs
	if len(ignorePatterns) == 0 {
		ignorePatterns = fsignore.DefaultPatterns
	}
	return &Manager{
		opts:    opts,
		ws:      tools.NewWorkspace(opts.WorkspacePath),
		ignore:  fsignore.New(ignorePatterns),
		project: project,
		files:   map[string]*Chunked{},
	}, nil
}

// Project returns the detected project.
func (m *Manager) Project() *verify.Project { return m.project }

// Workspace returns the workspace root.
func (m *Manager) Workspace() string { return m.opts.WorkspacePath }

// Index walks the workspace and splits source files into chunks.
//
// Indexing is lazy and cached: it happens on the first retrieval and is reused
// afterwards, so `agent inspect` does not pay for a full walk it will not use.
func (m *Manager) Index(ctx context.Context) error {
	if len(m.indexed) > 0 {
		return nil
	}
	root := m.opts.WorkspacePath
	seen := 0

	err := filepath.Walk(root, func(path string, info fs.FileInfo, werr error) error {
		if werr != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel := m.ws.Rel(path)
		if info.IsDir() {
			if m.ignore.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if m.ignore.Match(rel, false) {
			return nil
		}
		seen++
		if seen > m.opts.MaxFiles {
			return filepath.SkipAll
		}
		if info.Size() > int64(m.opts.MaxFileBytes) || info.Size() == 0 {
			return nil
		}
		if !m.opts.IncludeTests && isTestPath(rel) {
			return nil
		}
		if !isRetrievable(rel) {
			return nil
		}
		ch, err := chunkFile(path, rel, info.Size())
		if err != nil {
			return nil
		}
		m.files[rel] = ch
		m.indexed = append(m.indexed, ch.Chunks...)
		return nil
	})
	if err != nil && err != filepath.SkipAll {
		return err
	}
	m.indexedAt = time.Now()
	return nil
}

// IndexedAt reports when the index was built.
func (m *Manager) IndexedAt() time.Time { return m.indexedAt }

// FileCount returns how many files were indexed.
func (m *Manager) FileCount() int { return len(m.files) }

// ChunkCount returns how many chunks were indexed.
func (m *Manager) ChunkCount() int { return len(m.indexed) }

// scored is one candidate chunk with its two independent relevance signals.
type scored struct {
	chunk Chunk
	// lexRank is the chunk's position in the lexical ranking, or -1.
	lexRank int
	// lexScore is the raw lexical weight, normalised to 0..1.
	lexScore float64
	// denseScore is cosine similarity to the query, already 0..1.
	denseScore float64
	// hasDense records whether a real embedding was computed.
	hasDense bool
}

// combined weighs the two signals.
//
// Lexical evidence dominates because it is verifiable: if the request says
// "Authenticate" and the chunk defines Authenticate, that is almost certainly the
// right chunk, and a dense score near zero should not be able to bury it. Dense
// evidence contributes enough to surface conceptually related code that shares no
// vocabulary with the request, which is the failure lexical search cannot fix.
func (s scored) combined() float64 {
	const (
		lexWeight    = 0.65
		denseWeight  = 0.35
		rankDiscount = 0.25
	)
	if !s.hasDense {
		return s.lexScore
	}
	// Reward a chunk that lexical search also liked, without letting that reward
	// dominate: 1.0 is perfect lexical agreement, 0 is not in the lexical set.
	lexBonus := 0.0
	if s.lexRank >= 0 {
		lexBonus = 1.0 - float64(s.lexRank)/float64(maxInt(1, 64))
		if lexBonus < 0 {
			lexBonus = 0
		}
	}
	return lexWeight*(s.lexScore+rankDiscount*lexBonus) + denseWeight*s.denseScore
}

// Retrieve returns the chunks most relevant to a request, ranked.
func (m *Manager) Retrieve(ctx context.Context, request string, limit int) ([]Chunk, error) {
	if err := m.Index(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = m.opts.MaxResults
	}
	terms := memory.ExtractTerms(request)
	lexical := rankLexical(m.indexed, terms, request)

	maxLex := 0.0
	for _, hit := range lexical {
		if hit.weight > maxLex {
			maxLex = hit.weight
		}
	}
	if maxLex <= 0 {
		return nil, nil
	}

	cands := make([]scored, 0, len(lexical))
	for i, hit := range lexical {
		cands = append(cands, scored{chunk: hit.chunk, lexRank: i, lexScore: hit.weight / maxLex})
	}

	if m.opts.Embedder != nil {
		m.applyDense(ctx, cands, request, limit)
	}

	sort.SliceStable(cands, func(i, j int) bool {
		ci, cj := cands[i].combined(), cands[j].combined()
		if ci != cj {
			return ci > cj
		}
		// Prefer the more focused chunk when relevance ties.
		return len(cands[i].chunk.Content) < len(cands[j].chunk.Content)
	})

	out := make([]Chunk, 0, limit)
	for i := 0; i < len(cands) && i < limit; i++ {
		out = append(out, cands[i].chunk)
	}
	return out, nil
}

// applyDense adds embedding similarity to a candidate set.
//
// Embedding is attempted over a bounded shortlist rather than the whole index:
// embedding a large repository with a local model is slow, and the chunks worth
// scoring are overwhelmingly the ones lexical search already surfaced plus the
// ones whose names match. If anything goes wrong the candidates keep their lexical
// scores unchanged, because degraded retrieval is far better than none.
func (m *Manager) applyDense(ctx context.Context, cands []scored, request string, limit int) {
	target := cands
	if len(target) > 400 {
		target = target[:400]
	}
	// Add name matches that lexical ranking missed, so a symbol named after the
	// request is considered even when the body never mentions the term.
	extra := nameMatches(m.indexed, memory.ExtractTerms(request))
	if len(extra) > 0 {
		known := map[string]bool{}
		for _, c := range target {
			known[c.chunk.Key()] = true
		}
		for _, ch := range extra {
			if !known[ch.Key()] {
				cands = append(cands, scored{chunk: ch, lexRank: -1, lexScore: 0})
			}
		}
		if len(cands) > 400 {
			cands = cands[:400]
		}
		target = cands
	}

	if len(target) == 0 {
		return
	}
	inputs := make([]string, len(target)+1)
	for i, c := range target {
		inputs[i] = c.chunk.Path + "\n" + c.chunk.Name + "\n" + c.chunk.Content
	}
	inputs[len(target)] = request

	vecs, err := m.opts.Embedder.Embed(ctx, inputs)
	if err != nil || len(vecs) != len(inputs) {
		return
	}
	query := vecs[len(target)]
	if len(query) == 0 {
		return
	}
	for i := range target {
		sim := cosine(query, vecs[i])
		if sim < 0 {
			sim = 0
		}
		target[i].denseScore = sim
		target[i].hasDense = true
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// Key is a chunk's identity.
func (c Chunk) Key() string { return fmt.Sprintf("%s:%d-%d", c.Path, c.Start, c.End) }

func diff(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

// RetrieveForTerms exposes term extraction for callers building their own queries.
func RetrieveForTerms(ctx context.Context, m *Manager, request string, limit int) ([]Chunk, error) {
	return m.Retrieve(ctx, request, limit)
}

// rankLexical scores chunks by term overlap with the request and returns them in
// descending weight order.
func rankLexical(chunks []Chunk, terms []string, request string) []lexicalHit {
	type entry struct {
		c Chunk
		s float64
	}
	var out []entry
	lowerRequest := strings.ToLower(request)

	for _, c := range chunks {
		lower := strings.ToLower(c.Content)
		lowerPath := strings.ToLower(c.Path)
		var score float64
		matched := 0
		for _, t := range terms {
			t = strings.ToLower(t)
			if t == "" {
				continue
			}
			hits := strings.Count(lower, t)
			if hits == 0 {
				continue
			}
			matched++
			// Diminishing returns per term: a file containing a word 50 times is
			// not 50 times more relevant than one containing it once.
			score += 1 + minFloat(math_log1p(float64(hits)), 2.0)
			// A hit in the path is much stronger evidence than one in the body,
			// because a file named after a concept usually is that concept.
			if strings.Contains(lowerPath, t) {
				score += 3
			}
			// A hit in the chunk's symbol name is the strongest lexical evidence
			// available short of the request naming the symbol outright.
			if c.Name != "" && strings.Contains(strings.ToLower(c.Name), t) {
				score += 6
			}
		}
		if matched == 0 {
			continue
		}
		// Reward chunks that matched more distinct terms over one that repeated a
		// single term.
		score *= 1 + 0.35*float64(matched-1)
		if c.Name != "" && strings.Contains(lowerRequest, strings.ToLower(c.Name)) {
			score += 4
		}
		out = append(out, entry{c, score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].s != out[j].s {
			return out[i].s > out[j].s
		}
		// Tie-break toward smaller chunks: a focused symbol beats a whole file
		// when both are equally relevant.
		return len(out[i].c.Content) < len(out[j].c.Content)
	})

	res := make([]lexicalHit, 0, len(out))
	for _, e := range out {
		res = append(res, lexicalHit{chunk: e.c, weight: e.s})
	}
	return res
}

// lexicalHit is a chunk with its lexical weight.
type lexicalHit struct {
	chunk  Chunk
	weight float64
}

// nameMatches finds chunks whose symbol or path name contains a search term.
func nameMatches(chunks []Chunk, terms []string) []Chunk {
	var out []Chunk
	for _, c := range chunks {
		lname := strings.ToLower(c.Name)
		lpath := strings.ToLower(c.Path)
		for _, t := range terms {
			t = strings.ToLower(t)
			if len(t) < 3 {
				continue
			}
			if (c.Name != "" && strings.Contains(lname, t)) || strings.Contains(lpath, t) {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (sqrt(na) * sqrt(nb))
}

func sqrt(f float64) float64 {
	if f <= 0 {
		return 0
	}
	x := f
	for i := 0; i < 20; i++ {
		x = 0.5 * (x + f/x)
	}
	return x
}

func topN(in []Chunk, n int) []Chunk {
	if len(in) <= n {
		return in
	}
	return in[:n]
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// math_log1p is log(1+x) for x >= 0, used for diminishing returns on term hits.
func math_log1p(x float64) float64 {
	if x <= 0 {
		return 0
	}
	// ln(1+x) via the identity ln(1+x) = 2*atanh(x/(2+x)), which converges fast
	// for the small counts involved and avoids importing math for one call.
	t := x / (2 + x)
	t2 := t * t
	sum := t
	term := t
	for k := 3; k < 12; k += 2 {
		term *= t2
		sum += term / float64(k)
	}
	return 2 * sum
}

var symbolBoundary = regexp.MustCompile(`(?m)^(?:func|type|class|def|const|var|interface|struct|enum|impl|module|package|namespace|fn|pub)\s+`)

// chunkFile splits a file into retrievable chunks.
//
// Whole-function chunks are preferred because a code change reasons about a
// function, not about an arbitrary window of lines that cuts one in half. When
// the file's structure is not recognisable, a sliding window is used so that
// prose and configuration are still retrievable.
func chunkFile(path, rel string, size int64) (*Chunked, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if len(lines) == 0 {
		return &Chunked{Path: rel}, nil
	}

	out := &Chunked{Path: rel, Lines: len(lines)}
	var (
		curStart int
		curName  string
		curKind  = "prose"
		cur      []string
	)
	flush := func() {
		if len(cur) == 0 {
			return
		}
		content := strings.Join(cur, "\n")
		out.Chunks = append(out.Chunks, Chunk{
			Path: rel, Start: curStart + 1, End: curStart + len(cur),
			Content: content, Kind: curKind, Name: curName,
			Tokens: estimateTokens(content),
		})
		cur = nil
		curName = ""
		curKind = "prose"
	}

	bracedLang := isBracedLanguage(rel)
	for i, line := range lines {
		if symbolBoundary.MatchString(line) && (bracedLang || strings.Contains(line, "func ") || strings.Contains(line, "def ")) {
			// Only start a new symbol chunk when the previous one is closed,
			// which is tracked by brace depth for braced languages. Starting a new
			// chunk inside a function would shatter it.
			if len(cur) == 0 || depthOf(cur) == 0 {
				flush()
				curStart = i
				curName = symbolName(line)
				curKind = "symbol"
			}
		}
		cur = append(cur, line)
	}
	flush()

	if len(out.Chunks) == 0 {
		out.Chunks = windowChunks(rel, lines)
	}
	return out, nil
}

func windowChunks(rel string, lines []string) []Chunk {
	const size = 60
	const overlap = 10
	var out []Chunk
	for start := 0; start < len(lines); start += size - overlap {
		end := start + size
		if end > len(lines) {
			end = len(lines)
		}
		content := strings.Join(lines[start:end], "\n")
		out = append(out, Chunk{
			Path: rel, Start: start + 1, End: end, Content: content,
			Kind: "prose", Tokens: estimateTokens(content),
		})
		if end == len(lines) {
			break
		}
	}
	return out
}

func depthOf(lines []string) int {
	depth := 0
	for _, l := range lines {
		depth += strings.Count(l, "{") - strings.Count(l, "}")
		if depth < 0 {
			depth = 0
		}
	}
	return depth
}

func symbolName(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return ""
	}
	name := fields[1]
	name = strings.TrimLeft(name, "*&")
	if i := strings.IndexAny(name, "(<[ \t"); i > 0 {
		name = name[:i]
	}
	return name
}

func isBracedLanguage(rel string) bool {
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".go", ".js", ".jsx", ".ts", ".tsx", ".rs", ".java", ".cs", ".c", ".h", ".cpp", ".hpp", ".swift", ".kt", ".scala":
		return true
	}
	return false
}

func isTestPath(rel string) bool {
	l := strings.ToLower(rel)
	base := filepath.Base(l)
	return strings.Contains(base, "_test.") || strings.Contains(base, "test_") ||
		strings.Contains(base, ".test.") || strings.Contains(base, ".spec.") ||
		strings.HasPrefix(l, "test/") || strings.HasPrefix(l, "tests/") ||
		strings.Contains(l, "/test/") || strings.Contains(l, "/tests/")
}

var retrievableExt = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true,
	".rs": true, ".java": true, ".cs": true, ".c": true, ".h": true, ".cpp": true,
	".hpp": true, ".rb": true, ".php": true, ".swift": true, ".kt": true,
	".md": true, ".txt": true, ".toml": true, ".yaml": true, ".yml": true,
	".json": true, ".sql": true, ".sh": true, ".ps1": true, ".mod": true,
}

func isRetrievable(rel string) bool {
	if retrievableExt[strings.ToLower(filepath.Ext(rel))] {
		return true
	}
	base := filepath.Base(rel)
	switch base {
	case "Dockerfile", "Makefile", "CMakeLists.txt", "go.mod", "go.sum", ".gitignore":
		return true
	}
	return false
}

// estimateTokens approximates token count from character count.
func estimateTokens(s string) int {
	if s == "" {
		return 0
	}
	n := len(s) / 4
	if n == 0 {
		n = 1
	}
	return n
}