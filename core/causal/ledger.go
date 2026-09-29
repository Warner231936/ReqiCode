// Package causal implements intervention logging and effect attribution for the
// spiral loop. It answers the question no current agent framework can state:
// which change caused this result?
//
// The design constraint that makes this possible is the semi-state itself.
// Because every artifact carries Provenance{UnitID, Revision} and every
// CodeProposal has a stable ID, an intervention can be recorded, later
// correlated with a test outcome, and in principle replayed in reverse.
package causal

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

// Objective is a measurable the system tries to improve. Credit is assigned
// per-objective rather than globally, because a change can be good for
// compile-success and neutral for test-pass simultaneously.
type Objective string

const (
	ObjCompile   Objective = "compile"
	ObjTestPass  Objective = "test_pass"
	ObjCoverage  Objective = "coverage"
	ObjSecurity Objective = "security"
	ObjConflict Objective = "conflict_reduction"
)

// Direction records whether higher is better for an objective.
func (o Objective) HigherIsBetter() bool {
	return o != ObjConflict
}

// Intervention is a single change to the workspace, viewed as a causal act.
//
// This is deliberately a thin wrapper over CodeProposal rather than a new type.
// The proposal already exists, already has an ID, and already records what the
// proposing unit believed the effect would be. The intervention adds only the
// bookkeeping needed for attribution.
type Intervention struct {
	ID            string              `json:"id"`
	ProposalID    string              `json:"proposal_id"`
	Revision      int                 `json:"revision"`
	Unit          string              `json:"unit"`
	Target        string              `json:"target"`
	Operation     state.CodeOperation `json:"operation"`
	Predicted     string              `json:"predicted_effect"`
	Strategy      string              `json:"strategy,omitempty"`
	RecordedAt    time.Time           `json:"recorded_at"`

	// Tokens is the completion-token cost of the LLM call that produced this
	// intervention, attributed back to it. Cost attribution is what makes the
	// attention manager cost-aware in Pillar 3.
	Tokens int `json:"tokens"`

	// Filled in by attribution once ground truth arrives.
	Effects    []Effect `json:"effects,omitempty"`
	Resolved   bool     `json:"resolved"`
	NetCredit  float64  `json:"net_credit"`
	Confidence float64  `json:"credit_confidence"`
}

// Effect is an observed outcome attributed to an intervention.
type Effect struct {
	Objective   Objective `json:"objective"`
	Delta       float64   `json:"delta"`
	Before      float64   `json:"before"`
	After       float64   `json:"after"`
	EvidenceIDs []string  `json:"evidence_ids,omitempty"`
	Revision    int       `json:"revision"`
	At          time.Time `json:"at"`
}

// Improvement reports whether the effect moved its objective in the good
// direction.
func (e Effect) Improvement() bool {
	if e.Objective.HigherIsBetter() {
		return e.After > e.Before
	}
	return e.After < e.Before
}

// Ledger is the causal record of the spiral: interventions, their attributed
// effects, and the running credit assignment.
type Ledger struct {
	mu           sync.RWMutex
	interventions []*Intervention
	byProposal   map[string]*Intervention
	// baseline is the objective vector observed before any pending
	// intervention had a chance to take effect.
	baseline     map[Objective]float64
	// pending holds interventions recorded since the last attribution pass.
	pending      []string
	// lastRevision is the revision at which the last attribution ran.
	lastRevision int
}

// NewLedger creates an empty causal ledger.
func NewLedger() *Ledger {
	return &Ledger{
		interventions: make([]*Intervention, 0, 16),
		byProposal:    make(map[string]*Intervention),
		baseline:      make(map[Objective]float64),
	}
}

// Record logs an applied proposal as an intervention.
//
// Recording rejected or failed proposals matters as much as recording successes:
// a proposal that was applied and then reverted is strong negative evidence
// about that strategy, and losing that signal is how a system repeats
// mistakes forever.
func (l *Ledger) Record(p state.CodeProposal, strategy string, tokens int) *Intervention {
	l.mu.Lock()
	defer l.mu.Unlock()

	if iv, ok := l.byProposal[p.ID]; ok {
		return iv
	}

	iv := &Intervention{
		ID:         fmt.Sprintf("iv-%s", p.ID),
		ProposalID: p.ID,
		Revision:   p.Provenance.Revision,
		Unit:       p.OriginatingUnit,
		Target:     p.File,
		Operation:  p.Operation,
		Predicted:  p.ExpectedEffect,
		Strategy:   strategy,
		Tokens:     tokens,
		RecordedAt: time.Now(),
	}

	l.interventions = append(l.interventions, iv)
	l.byProposal[p.ID] = iv
	l.pending = append(l.pending, iv.ID)
	return iv
}

// ObserveBaseline captures the current objective vector as the comparison point
// for subsequent interventions. Call this before a batch of changes.
func (l *Ledger) ObserveBaseline(ss *state.SemiState) map[Objective]float64 {
	vec := Objectives(ss)
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range vec {
		l.baseline[k] = v
	}
	return vec
}

// Objectives derives the measurable objective vector from a state snapshot.
// This is the ground truth against which interventions are judged.
func Objectives(ss *state.SemiState) map[Objective]float64 {
	vec := make(map[Objective]float64, 5)
	if ss == nil {
		return vec
	}

	// Compile and test-pass share the test-result signal but are scored
	// separately because a build failure and an assertion failure call for
	// different repairs.
	results := ss.GetTestResults()
	if len(results) == 0 {
		vec[ObjCompile] = 0
		vec[ObjTestPass] = 0
	} else {
		last := results[len(results)-1]
		switch {
		case last.Status == state.TestPassed:
			vec[ObjCompile] = 1
			vec[ObjTestPass] = 1
		case strings.Contains(last.FailureClass, "build"):
			vec[ObjCompile] = 0
			vec[ObjTestPass] = 0
		default:
			vec[ObjCompile] = 1
			vec[ObjTestPass] = 0
		}
	}

	// Coverage is parsed from test output when the runner emitted a profile.
	vec[ObjCoverage] = parseCoverage(results)

	// Security proxy: unraised objections divided by generated files. Falling as
	// new files are covered is the signal we can get without a real analyzer.
	files := float64(len(ss.GeneratedFiles))
	if files == 0 {
		vec[ObjSecurity] = 1
	} else {
		vec[ObjSecurity] = 1 - float64(len(ss.GetObjections()))/files
		if vec[ObjSecurity] < 0 {
			vec[ObjSecurity] = 0
		}
	}

	// Active conflicts are strictly bad, hence the inverted direction.
	vec[ObjConflict] = float64(len(ss.Contradictions))

	return vec
}

// parseCoverage extracts a coverage percentage from test output when present.
// Returns 0 when the runner did not emit coverage, which is honest: a missing
// measurement must not be silently treated as a passing one.
func parseCoverage(results []state.TestResult) float64 {
	if len(results) == 0 {
		return 0
	}
	last := results[len(results)-1]
	out := last.Stdout
	idx := strings.LastIndex(out, "coverage:")
	if idx == -1 {
		return 0
	}
	tail := out[idx+len("coverage:"):]
	if len(tail) < 4 {
		return 0
	}
	var pct float64
	if _, err := fmt.Sscanf(tail[:4], "%f", &pct); err != nil {
		return 0
	}
	return pct / 100
}

// Attribution is the result of one credit-assignment pass.
type Attribution struct {
	Revision      int                `json:"revision"`
	Attributed    []string           `json:"attributed"`
	Before        map[Objective]float64 `json:"before"`
	After         map[Objective]float64 `json:"after"`
	Deltas        map[Objective]float64 `json:"deltas"`
	Explanation   string             `json:"explanation"`
	BaselineValid bool               `json:"baseline_valid"`
}

// Attribute correlates every pending intervention with the change in the
// objective vector since it was recorded.
//
// The honest caveat, stated plainly: with N simultaneous pending interventions
// and one observation, exact causal decomposition is underdetermined. This
// implementation performs *joint* attribution — every pending intervention
// shares credit for the observed delta — and marks its confidence accordingly.
// It is an upper bound on credit, not a proof. Exact attribution requires the
// counterfactual replay path, which is the next milestone.
func (l *Ledger) Attribute(ss *state.SemiState) Attribution {
	l.mu.Lock()
	defer l.mu.Unlock()

	after := Objectives(ss)
	attr := Attribution{
		Revision: ss.Revision,
		Before:   make(map[Objective]float64, len(l.baseline)),
		After:    after,
		Deltas:   make(map[Objective]float64, len(after)),
	}

	for k, v := range l.baseline {
		attr.Before[k] = v
	}
	for k, v := range after {
		attr.Deltas[k] = v - l.baseline[k]
	}

	if len(l.pending) == 0 {
		attr.Explanation = "no pending interventions; nothing to attribute"
		l.baseline = after
		return attr
	}

	attributed := make([]string, 0, len(l.pending))
	for _, id := range l.pending {
		iv := l.interventionByIDLocked(id)
		if iv == nil {
			continue
		}
		for obj, delta := range attr.Deltas {
			if delta == 0 {
				continue
			}
			iv.Effects = append(iv.Effects, Effect{
				Objective: obj,
				Delta:     delta,
				Before:    attr.Before[obj],
				After:     attr.After[obj],
				Revision:  ss.Revision,
				At:        time.Now(),
			})
		}
		iv.NetCredit, iv.Confidence = scoreIntervention(iv, len(l.pending))
		iv.Resolved = true
		attributed = append(attributed, iv.ID)
	}

	attr.Attributed = attributed
	attr.BaselineValid = len(l.baseline) > 0
	attr.Explanation = fmt.Sprintf(
		"jointly attributed %d objectives across %d pending interventions (confidence discounted by joint attribution)",
		len(attr.Deltas), len(attributed))

	l.pending = nil
	l.baseline = after
	l.lastRevision = ss.Revision
	return attr
}

// scoreIntervention converts recorded effects into a single credit number.
//
// Improvement on any objective earns credit. Regression costs credit, and
// regression on a low-priority objective costs less than regression on
// test-pass, because breaking a passing suite is worse than an aesthetic
// objection. Regression is weighted super-linearly because a broken build is
// strictly worse than not having attempted the change at all.
func scoreIntervention(iv *Intervention, cohortSize int) (credit, confidence float64) {
	if len(iv.Effects) == 0 {
		return 0, 0
	}

	var gains, losses float64
	for _, e := range iv.Effects {
		if e.Improvement() {
			gains += priority(e.Objective) * e.Delta
		} else {
			losses += priority(e.Objective) * (-e.Delta) * 1.5
		}
	}

	raw := gains - losses
	// A large cohort means joint attribution: this intervention cannot
	// distinguish its own contribution from its peers.
	confidence = 1 / float64(max(cohortSize, 1))
	if confidence > 0.5 {
		confidence = 0.5
	}
	if confidence < 0.1 {
		confidence = 0.1
	}
	return raw, confidence
}

func priority(o Objective) float64 {
	switch o {
	case ObjTestPass:
		return 1.0
	case ObjCompile:
		return 0.9
	case ObjConflict:
		return 0.7
	case ObjSecurity:
		return 0.5
	case ObjCoverage:
		return 0.3
	default:
		return 0.2
	}
}

func (l *Ledger) interventionByIDLocked(id string) *Intervention {
	for _, iv := range l.interventions {
		if iv.ID == id {
			return iv
		}
	}
	return nil
}

// Interventions returns all recorded interventions in order.
func (l *Ledger) Interventions() []Intervention {
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]Intervention, 0, len(l.interventions))
	for _, iv := range l.interventions {
		out = append(out, *iv)
	}
	return out
}

// TopStrategies ranks strategies by mean net credit.
//
// This is the input to the bandit in the next milestone: it answers "which kind
// of change has historically paid off here?" without needing a learned policy
// yet. A strategy with zero interventions is excluded rather than defaulted to
// zero, so that cold-start does not look like a proven-bad strategy.
func (l *Ledger) TopStrategies() []StrategyScore {
	l.mu.RLock()
	defer l.mu.RUnlock()

	type acc struct {
		total  float64
		count  int
	}
	byStrategy := make(map[string]*acc)

	for _, iv := range l.interventions {
		if iv.Strategy == "" {
			continue
		}
		a, ok := byStrategy[iv.Strategy]
		if !ok {
			a = &acc{}
			byStrategy[iv.Strategy] = a
		}
		a.total += iv.NetCredit
		a.count++
	}

	out := make([]StrategyScore, 0, len(byStrategy))
	for name, a := range byStrategy {
		out = append(out, StrategyScore{
			Strategy:   name,
			MeanCredit: a.total / float64(a.count),
			Samples:    a.count,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MeanCredit != out[j].MeanCredit {
			return out[i].MeanCredit > out[j].MeanCredit
		}
		return out[i].Samples > out[j].Samples
	})
	return out
}

// StrategyScore is the historical performance of one intervention strategy.
type StrategyScore struct {
	Strategy   string  `json:"strategy"`
	MeanCredit float64 `json:"mean_credit"`
	Samples    int     `json:"samples"`
}

// CreditFor returns the net credit assigned to a proposal ID.
func (l *Ledger) CreditFor(proposalID string) (float64, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	iv, ok := l.byProposal[proposalID]
	if !ok {
		return 0, false
	}
	return iv.NetCredit, true
}

// FailedApproaches returns strategies with strictly negative mean credit, ranked
// worst first. This is the payload for FailureMemory injection: known-bad
// approaches should be pre-registered as rejected hypotheses in a new run.
func (l *Ledger) FailedApproaches(threshold float64) []StrategyScore {
	scores := l.TopStrategies()
	failed := make([]StrategyScore, 0, len(scores))
	for _, s := range scores {
		if s.MeanCredit < threshold && s.Samples > 0 {
			failed = append(failed, s)
		}
	}
	return failed
}

// TotalTokens sums the token cost attributed to interventions.
func (l *Ledger) TotalTokens() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	total := 0
	for _, iv := range l.interventions {
		total += iv.Tokens
	}
	return total
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
