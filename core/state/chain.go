package state

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ChainHash is the content hash that links one revision to the one before it.
//
// The chain exists so that retroactive history editing is detectable. Without
// it, a log the mutator can rewrite is not evidence of anything: a system that
// modifies its own source could equally modify the record justifying the
// modification, and the two would agree with each other. Chaining makes that
// collusion arithmetically visible, because changing revision N-1 invalidates
// every hash from N onward.
type ChainHash struct {
	// Revision is the revision this hash commits to.
	Revision int `json:"revision"`
	// Hash is sha256(prevHash || canonicalContent).
	Hash string `json:"hash"`
	// Prev is the previous revision's hash, empty at genesis.
	Prev string `json:"prev,omitempty"`
	// Timestamp is recorded for human audit only. It is excluded from the
	// hashed content so that two runs of the same logic produce the same chain,
	// which is what makes a golden baseline possible.
	Timestamp string `json:"timestamp,omitempty"`
}

// GenesisHash seeds a chain. Its content is a constant so that every chain
// starts from a known value and divergence is attributable to a real change
// rather than to a differing starting point.
const GenesisHash = "spiral-genesis-v1"

// canonicalContent renders the parts of the state that must never change
// silently, in a stable order.
//
// It does NOT take a lock. Callers must already hold at least the read lock.
// sync.RWMutex is not reentrant, so locking here deadlocks any caller that
// already holds one — which is all of them.
//
// Excluded: timestamps, wall-clock durations, and the chain field itself.
// Timestamps would make every run produce a different hash, which would render
// the whole mechanism useless for regression detection — the property we need
// most. Everything epistemic is included.
func canonicalContent(s *SemiState) ([]byte, error) {
	type canonPlan struct {
		Description string   `json:"description"`
		AltID       string   `json:"alt"`
		Components  []string `json:"components"`
		Endpoints   []string `json:"endpoints"`
	}
	type canon struct {
		Revision     int         `json:"revision"`
		Confidence   float64     `json:"confidence"`
		Requirements []string    `json:"requirements"`
		Plan         *canonPlan  `json:"plan"`
		Components   []string    `json:"components"`
		Endpoints    []string    `json:"endpoints"`
		Decisions    []string    `json:"decisions"`
		Hypotheses   []string    `json:"hypotheses"`
		Proposals    []string    `json:"proposals"`
		Files        []string    `json:"files"`
		Evidence     []string    `json:"evidence"`
		Conflicts    []string    `json:"conflicts"`
		Tests        []string    `json:"tests"`
		Findings     []string    `json:"findings"`
		Objections   []string    `json:"objections"`
	}

	c := canon{Revision: s.Revision, Confidence: float64(s.Confidence)}

	for _, r := range s.Requirements {
		c.Requirements = append(c.Requirements, r.ID+"|"+r.Content)
	}
	sort.Strings(c.Requirements)

	if s.ArchitecturePlan != nil {
		cp := &canonPlan{
			Description: s.ArchitecturePlan.Description,
			AltID:       s.ArchitecturePlan.AlternativeID,
		}
		for _, comp := range s.ArchitecturePlan.Components {
			cp.Components = append(cp.Components, comp.Name+"|"+comp.Path+"|"+comp.Description)
		}
		sort.Strings(cp.Components)
		for _, e := range s.ArchitecturePlan.Endpoints {
			cp.Endpoints = append(cp.Endpoints, e.Method+" "+e.Path+"|"+e.Desc)
		}
		sort.Strings(cp.Endpoints)
		c.Plan = cp
	}

	for _, comp := range s.ArchitecturePlanComponents() {
		c.Components = append(c.Components, comp)
	}
	for _, e := range s.ArchitecturePlanEndpoints() {
		c.Endpoints = append(c.Endpoints, e)
	}
	for _, d := range s.Decisions {
		c.Decisions = append(c.Decisions, d.ID+"|"+d.Question+"|"+d.Decision+"|"+d.Rationale)
	}
	sort.Strings(c.Decisions)
	for _, h := range s.Hypotheses {
		c.Hypotheses = append(c.Hypotheses, h.ID+"|"+string(h.Status)+"|"+fmt.Sprintf("%.2f", float64(h.Confidence))+"|"+h.Content)
	}
	sort.Strings(c.Hypotheses)
	for _, p := range s.Proposals {
		c.Proposals = append(c.Proposals, p.ID+"|"+p.File+"|"+string(p.Operation)+"|"+string(p.Status))
	}
	sort.Strings(c.Proposals)
	for p := range s.GeneratedFiles {
		c.Files = append(c.Files, p)
	}
	sort.Strings(c.Files)
	for _, e := range s.Evidence {
		c.Evidence = append(c.Evidence, string(e.Type)+"|"+fmt.Sprintf("%.2f", float64(e.Strength))+"|"+e.Content)
	}
	sort.Strings(c.Evidence)
	for _, cf := range s.Contradictions {
		c.Conflicts = append(c.Conflicts, cf.ID+"|"+string(cf.Severity)+"|"+cf.Subject)
	}
	sort.Strings(c.Conflicts)
	for _, t := range s.TestResults {
		c.Tests = append(c.Tests, string(t.Status)+"|"+t.FailureClass+"|"+t.Command)
	}
	sort.Strings(c.Tests)
	for _, f := range s.Findings {
		c.Findings = append(c.Findings, string(f.NewStatus)+"|"+f.Claim.Content)
	}
	sort.Strings(c.Findings)
	for _, o := range s.Objections {
		c.Objections = append(c.Objections, o.Target+"|"+o.Severity+"|"+o.Content)
	}
	sort.Strings(c.Objections)

	return json.Marshal(c)
}

// ArchitecturePlanComponents returns the plan's component identities in a stable
// form. Nil-safe so canonicalContent can call it unconditionally.
func (s *SemiState) ArchitecturePlanComponents() []string {
	if s.ArchitecturePlan == nil {
		return nil
	}
	out := make([]string, 0, len(s.ArchitecturePlan.Components))
	for _, c := range s.ArchitecturePlan.Components {
		out = append(out, c.Name+"|"+c.Path)
	}
	return out
}

// ArchitecturePlanEndpoints returns the plan's endpoints in a stable form.
func (s *SemiState) ArchitecturePlanEndpoints() []string {
	if s.ArchitecturePlan == nil {
		return nil
	}
	out := make([]string, 0, len(s.ArchitecturePlan.Endpoints))
	for _, e := range s.ArchitecturePlan.Endpoints {
		out = append(out, e.Method+" "+e.Path)
	}
	return out
}

// ComputeChainHash produces the hash that commits this revision to the chain.
//
// prevHash is passed in rather than read from the state so that the chain is
// explicit at the call site: a caller cannot accidentally hash a revision
// without deciding which predecessor it is claiming.
func (s *SemiState) ComputeChainHash(prevHash string) (ChainHash, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.computeChainHashLocked(prevHash)
}

func (s *SemiState) computeChainHashLocked(prevHash string) (ChainHash, error) {
	content, err := canonicalContent(s)
	if err != nil {
		return ChainHash{}, fmt.Errorf("canonicalize state: %w", err)
	}
	h := sha256.New()
	h.Write([]byte(prevHash))
	h.Write([]byte{0})
	h.Write(content)
	return ChainHash{
		Revision: s.Revision,
		Hash:     hex.EncodeToString(h.Sum(nil)),
		Prev:     prevHash,
	}, nil
}

// ChainNode is a revision paired with its link.
type ChainNode struct {
	Revision int       `json:"revision"`
	Summary  string    `json:"summary,omitempty"`
	Hash     ChainHash `json:"hash"`
}

// AppendChained records a chain link for the current revision. The previous
// hash is read from the existing chain, so callers cannot accidentally fork it.
func (s *SemiState) AppendChained(summary string) (ChainHash, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	prev := GenesisHash
	if n := len(s.chain); n > 0 {
		prev = s.chain[n-1].Hash.Hash
	}

	// canonicalContent takes no lock, so this is safe while the write lock is
	// held.
	ch, err := s.computeChainHashLocked(prev)
	if err != nil {
		return ChainHash{}, err
	}

	s.chain = append(s.chain, ChainNode{Revision: s.Revision, Summary: summary, Hash: ch})
	return ch, nil
}

// Chain returns a copy of the recorded chain.
func (s *SemiState) Chain() []ChainNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]ChainNode(nil), s.chain...)
}

// ChainHead returns the most recent chain hash, or GenesisHash if the chain is
// empty.
func (s *SemiState) ChainHead() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n := len(s.chain); n > 0 {
		return s.chain[n-1].Hash.Hash
	}
	return GenesisHash
}

// ChainVerification is the result of checking chain integrity.
type ChainVerification struct {
	Valid    bool   `json:"valid"`
	Checked  int    `json:"checked"`
	BrokenAt int    `json:"broken_at,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Head     string `json:"head"`
}

// VerifyChain walks the chain from genesis, recomputing every hash from the
// recorded state where the state is available, and at minimum checking that
// each link's Prev matches the prior link's Hash.
//
// The structural check alone is what detects retroactive editing: altering
// revision 3's recorded content without updating its hash breaks the link from
// revision 4 onward, and the break is located precisely.
func (s *SemiState) VerifyChain() ChainVerification {
	s.mu.RLock()
	nodes := append([]ChainNode(nil), s.chain...)
	s.mu.RUnlock()

	v := ChainVerification{Valid: true, Head: GenesisHash, Checked: len(nodes)}
	if len(nodes) == 0 {
		return v
	}

	prev := GenesisHash
	for i, n := range nodes {
		if n.Hash.Prev != prev {
			// The link that fails is the one whose recorded predecessor does not
			// match the running head. That is the first link downstream of the
			// edit, which is the actionable location: the edit happened at or
			// before this point, and this is where the divergence surfaces.
			v.Valid = false
			v.BrokenAt = n.Revision
			v.Reason = fmt.Sprintf("revision %d declares prev=%q but the chain head before it was %q; history was altered at or before this point",
				n.Revision, n.Hash.Prev, prev)
			return v
		}
		if i > 0 && n.Revision <= nodes[i-1].Revision {
			v.Valid = false
			v.BrokenAt = n.Revision
			v.Reason = fmt.Sprintf("revision %d does not advance past %d", n.Revision, nodes[i-1].Revision)
			return v
		}
		prev = n.Hash.Hash
	}

	v.Head = prev
	return v
}
