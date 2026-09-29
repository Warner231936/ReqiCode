package converge

import (
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/state"
)

// Trajectory is a single point on the potential-function path.
type Trajectory struct {
	Revision    int       `json:"revision"`
	Potential   Potential `json:"potential"`
	Fingerprint string    `json:"fingerprint"`
	Timestamp   time.Time `json:"timestamp"`
}

// Phase classifies what the spiral should do next based on the shape of the
// potential trajectory. This replaces maxIterations as the termination
// criterion: the loop stops when the system has provably extracted everything it
// can, not when a counter runs out.
type Phase string

const (
	// PhaseProductive: potential is rising. Keep going.
	PhaseProductive Phase = "productive"
	// PhaseConverged: potential is flat and no new evidence is arriving. Stop.
	PhaseConverged Phase = "converged"
	// PhaseOscillating: the system is cycling between two states. Force
	// differentiation rather than letting the LLM rediscover the same fork.
	PhaseOscillating Phase = "oscillating"
	// PhaseRegressing: potential is falling. Roll back to the best known
	// revision and change strategy.
	PhaseRegressing Phase = "regressing"
	// PhaseInsufficientData: not enough points to judge. Keep going.
	PhaseInsufficientData Phase = "insufficient_data"
	// PhaseUnknown: no history at all.
	PhaseUnknown Phase = "unknown"
)

// Assessment is the full verdict for one decision point.
type Assessment struct {
	Phase         Phase     `json:"phase"`
	Reason        string    `json:"reason"`
	Delta         float64   `json:"delta"`
	PlateauStreak int       `json:"plateau_streak"`
	CycleLength   int       `json:"cycle_length,omitempty"`
	BestRevision  int       `json:"best_revision"`
	ShouldStop    bool      `json:"should_stop"`
	ShouldRollback bool     `json:"should_rollback"`
	// ForceDifferentiate asks the caller to mutate a design parameter rather
	// than re-deriving the same architecture.
	ForceDifferentiate bool `json:"force_differentiate"`
}

// Epsilon is the threshold below which a potential change counts as "no change".
// Tuned to sit above scheduler jitter and well below a single real test flip.
const Epsilon = 0.01

// PlateauTolerance is how many consecutive no-change iterations are tolerated
// before declaring convergence. Three is deliberate: it is enough to distinguish
// a genuine plateau from a single quiet iteration.
const PlateauTolerance = 3

// Tracker maintains the potential trajectory and derives convergence verdicts.
type Tracker struct {
	mu    sync.Mutex
	trace []Trajectory
}

// NewTracker creates an empty tracker.
func NewTracker() *Tracker {
	return &Tracker{trace: make([]Trajectory, 0, 16)}
}

// Observe records the current state and returns the potential at that revision.
func (t *Tracker) Observe(ss *state.SemiState) Potential {
	t.mu.Lock()
	defer t.mu.Unlock()

	p := ComputePotential(ss)
	t.trace = append(t.trace, Trajectory{
		Revision:    ss.Revision,
		Potential:   p,
		Fingerprint: StateFingerprint(ss),
		Timestamp:   time.Now(),
	})
	return p
}

// Trace returns a copy of the recorded trajectory.
func (t *Tracker) Trace() []Trajectory {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]Trajectory(nil), t.trace...)
}

// Len reports how many points have been observed.
func (t *Tracker) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.trace)
}

// Assess classifies the current trajectory shape.
//
// The order of checks matters. Oscillation is detected before regression because
// a two-cycle produces a near-zero delta that would otherwise be misread as a
// healthy plateau; and regression is checked before productivity because a
// falling potential must trigger rollback regardless of how recently it fell.
func (t *Tracker) Assess() Assessment {
	t.mu.Lock()
	defer t.mu.Unlock()

	n := len(t.trace)
	if n == 0 {
		return Assessment{Phase: PhaseUnknown, Reason: "no trajectory recorded"}
	}

	best := t.bestIdx()

	a := Assessment{BestRevision: t.trace[best].Revision}

	if n < 2 {
		a.Phase = PhaseInsufficientData
		a.Reason = fmt.Sprintf("only %d point(s) observed; need at least 2 to assess trend", n)
		return a
	}

	cur := t.trace[n-1].Potential.Value
	prev := t.trace[n-2].Potential.Value
	a.Delta = cur - prev

	// Oscillation: revision N matches N-2 (same fingerprint) but revision N-1
	// differed. That is a textbook two-cycle.
	if n >= 3 && t.trace[n-1].Fingerprint != "" &&
		t.trace[n-1].Fingerprint == t.trace[n-3].Fingerprint &&
		t.trace[n-1].Fingerprint != t.trace[n-2].Fingerprint {
		a.Phase = PhaseOscillating
		a.CycleLength = 2
		a.ForceDifferentiate = true
		a.Reason = fmt.Sprintf("state at revision %d is semantically identical to revision %d while revision %d differed: two-cycle detected",
			t.trace[n-1].Revision, t.trace[n-3].Revision, t.trace[n-2].Revision)
		return a
	}

	// Longest cycle we can detect without exponential blowup.
	if cyc := t.detectCycle(); cyc > 0 {
		a.Phase = PhaseOscillating
		a.CycleLength = cyc
		a.ForceDifferentiate = true
		a.Reason = fmt.Sprintf("repeated state fingerprint with period %d", cyc)
		return a
	}

	// Plateau streak: how many consecutive iterations have moved less than
	// epsilon.
	a.PlateauStreak = t.plateauStreak()

	bestVal := t.trace[best].Potential.Value
	switch {
	case cur < bestVal-Epsilon:
		a.Phase = PhaseRegressing
		a.ShouldRollback = true
		a.Reason = fmt.Sprintf("potential %.4f is below best-known %.4f at revision %d",
			cur, bestVal, t.trace[best].Revision)
		return a

	case a.Delta > Epsilon:
		a.Phase = PhaseProductive
		a.Reason = fmt.Sprintf("potential rising (+%.4f, now %.4f)", a.Delta, cur)
		return a

	case a.PlateauStreak >= PlateauTolerance:
		a.Phase = PhaseConverged
		a.ShouldStop = true
		a.Reason = fmt.Sprintf("potential flat at %.4f for %d consecutive iterations; no further extraction available",
			cur, a.PlateauStreak)
		return a

	default:
		a.Phase = PhaseProductive
		a.Reason = fmt.Sprintf("potential flat at %.4f but only %d iteration(s) below plateau tolerance %d",
			cur, a.PlateauStreak, PlateauTolerance)
		return a
	}
}

func (t *Tracker) bestIdx() int {
	best := 0
	for i := 1; i < len(t.trace); i++ {
		if t.trace[i].Potential.Value > t.trace[best].Potential.Value {
			best = i
		}
	}
	return best
}

func (t *Tracker) plateauStreak() int {
	streak := 0
	for i := len(t.trace) - 1; i > 0; i-- {
		d := t.trace[i].Potential.Value - t.trace[i-1].Potential.Value
		if d > Epsilon || d < -Epsilon {
			break
		}
		streak++
	}
	return streak
}

// detectCycle looks for a repeated fingerprint within a bounded recent window.
// Bounded deliberately: full cycle detection over unbounded history is
// quadratic, and a cycle longer than the window is not actionable anyway.
//
// A period of 1 is explicitly not a cycle: consecutive identical states are a
// plateau, which is convergence, not thrashing. Returning 0 here is what lets
// the plateau-streak check decide the phase correctly.
func (t *Tracker) detectCycle() int {
	const window = 8
	start := len(t.trace) - window
	if start < 0 {
		start = 0
	}
	if len(t.trace)-start < 3 {
		return 0
	}
	cur := t.trace[len(t.trace)-1].Fingerprint
	if cur == "" {
		return 0
	}
	for i := len(t.trace) - 2; i >= start; i-- {
		if t.trace[i].Fingerprint == cur {
			period := len(t.trace) - 1 - i
			if period < 2 {
				return 0
			}
			return period
		}
	}
	return 0
}

// ---------------------------------------------------------------------------
// Pareto frontier
// ---------------------------------------------------------------------------

// Point is one entry on the Pareto frontier: a revision that is not dominated by
// any other revision on the tracked objectives.
type Point struct {
	Revision   int     `json:"revision"`
	PassRate   float64 `json:"pass_rate"`
	Coverage   float64 `json:"coverage"`
	TokenCost  int     `json:"token_cost"`
	Complexity int     `json:"complexity"`
	Potential  float64 `json:"potential"`
}

// ParetoPoint accumulates the full objective vector for one revision.
type ParetoPoint struct {
	Revision   int
	PassRate   float64
	Coverage   float64
	TokenCost  int
	Complexity int
}

// ParetoFrontier tracks the non-dominated set of (pass, coverage, -cost,
// -complexity) across revisions.
//
// The point of this is that the spiral can stop at any moment and return the
// best revision found rather than whatever the last iteration happened to
// produce. Without it, a final exploratory iteration can degrade a working
// result, and the system reports its worst attempt as its answer.
type ParetoFrontier struct {
	mu     sync.Mutex
	points []ParetoPoint
}

// NewParetoFrontier creates an empty frontier.
func NewParetoFrontier() *ParetoFrontier {
	return &ParetoFrontier{points: make([]ParetoPoint, 0, 8)}
}

// Add inserts a point and prunes anything it dominates.
func (f *ParetoFrontier) Add(p ParetoPoint) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for _, existing := range f.points {
		if existing.Revision == p.Revision {
			return
		}
	}
	f.points = append(f.points, p)
	f.pruneLocked()
}

// pruneLocked keeps only non-dominated points. Higher is better on PassRate and
// Coverage; lower is better on TokenCost and Complexity.
func (f *ParetoFrontier) pruneLocked() {
	sort.SliceStable(f.points, func(i, j int) bool {
		return f.points[i].Revision < f.points[j].Revision
	})

	kept := make([]ParetoPoint, 0, len(f.points))
	for _, cand := range f.points {
		dominated := false
		for _, k := range kept {
			if dominates(k, cand) {
				dominated = true
				break
			}
		}
		if !dominated {
			// Drop any previously kept point that the candidate dominates.
			filtered := kept[:0]
			for _, k := range kept {
				if !dominates(cand, k) {
					filtered = append(filtered, k)
				}
			}
			kept = filtered
			kept = append(kept, cand)
		}
	}
	f.points = kept
}

func dominates(a, b ParetoPoint) bool {
	// Maximize pass and coverage, minimize cost and complexity.
	betterOrEqual := a.PassRate >= b.PassRate &&
		a.Coverage >= b.Coverage &&
		a.TokenCost <= b.TokenCost &&
		a.Complexity <= b.Complexity
	strictlyBetter := a.PassRate > b.PassRate ||
		a.Coverage > b.Coverage ||
		a.TokenCost < b.TokenCost ||
		a.Complexity < b.Complexity
	return betterOrEqual && strictlyBetter
}

// Best returns the frontier point with the highest potential proxy (pass rate
// first, then coverage, then lower cost). Ties break toward lower cost so the
// frontier prefers the cheaper solution.
func (f *ParetoFrontier) Best() (ParetoPoint, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if len(f.points) == 0 {
		return ParetoPoint{}, false
	}
	best := f.points[0]
	for _, p := range f.points[1:] {
		if p.PassRate > best.PassRate ||
			(p.PassRate == best.PassRate && p.Coverage > best.Coverage) ||
			(p.PassRate == best.PassRate && p.Coverage == best.Coverage && p.TokenCost < best.TokenCost) {
			best = p
		}
	}
	return best, true
}

// Len reports frontier size.
func (f *ParetoFrontier) Len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.points)
}

// Snapshot converts the frontier to serializable Points.
func (f *ParetoFrontier) Snapshot(potentialOf func(int) float64) []Point {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]Point, 0, len(f.points))
	for _, p := range f.points {
		pt := Point{
			Revision:   p.Revision,
			PassRate:   p.PassRate,
			Coverage:   p.Coverage,
			TokenCost:  p.TokenCost,
			Complexity: p.Complexity,
		}
		if potentialOf != nil {
			pt.Potential = potentialOf(p.Revision)
		}
		out = append(out, pt)
	}
	return out
}
