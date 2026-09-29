package converge

import (
	"testing"

	"github.com/kilo/spiral-codemaker/core/state"
)

func newState(rev int) *state.SemiState {
	ss := state.NewSemiState()
	ss.Revision = rev
	return ss
}

func TestPotentialIsPureAndZeroForEmptyState(t *testing.T) {
	a := ComputePotential(newState(1))
	b := ComputePotential(newState(1))
	if a.Value != b.Value {
		t.Fatalf("potential not pure: %v vs %v", a.Value, b.Value)
	}
	if a.HasTestResult {
		t.Error("empty state should report no test result")
	}
}

func TestPotentialRisesWithPassingTests(t *testing.T) {
	ss := newState(1)
	ss.AddTestResult(state.TestResult{
		ID: "t1", Command: "go test ./...", Status: state.TestPassed,
	})
	withPass := ComputePotential(ss).Value

	ss2 := newState(1)
	ss2.AddTestResult(state.TestResult{
		ID: "t1", Command: "go test ./...", Status: state.TestFailed,
		FailureClass: "test_assertion_failure",
	})
	withFail := ComputePotential(ss2).Value

	if withPass <= withFail {
		t.Errorf("passing tests should outscore failing: pass=%.4f fail=%.4f", withPass, withFail)
	}
}

func TestBuildFailureScoresWorseThanAssertionFailure(t *testing.T) {
	buildFail := newState(1)
	buildFail.AddTestResult(state.TestResult{
		Command: "go test ./...", Status: state.TestFailed, FailureClass: "build_error",
	})
	assertFail := newState(1)
	assertFail.AddTestResult(state.TestResult{
		Command: "go test ./...", Status: state.TestFailed, FailureClass: "test_assertion_failure",
	})

	if ComputePotential(buildFail).Value >= ComputePotential(assertFail).Value {
		t.Error("a build error must score strictly worse than an assertion failure")
	}
}

func TestActiveConflictReducesPotential(t *testing.T) {
	clean := newState(1)
	withConflict := newState(1)
	withConflict.AddConflict(state.Conflict{
		ID: "c1", Subject: "storage", Severity: state.ConflictHigh, Resolved: false,
	})

	if ComputePotential(withConflict).Value >= ComputePotential(clean).Value {
		t.Error("an unresolved conflict must reduce the potential")
	}
}

func TestResolvedConflictIsLessHarmfulThanActive(t *testing.T) {
	active := newState(1)
	active.AddConflict(state.Conflict{ID: "c1", Severity: state.ConflictHigh, Resolved: false})

	resolved := newState(1)
	resolved.AddConflict(state.Conflict{ID: "c1", Severity: state.ConflictHigh, Resolved: true})

	if ComputePotential(resolved).Value <= ComputePotential(active).Value {
		t.Error("a resolved conflict must score better than an active one")
	}
}

func TestFingerprintStableAcrossEquivalentStates(t *testing.T) {
	build := func() *state.SemiState {
		ss := state.NewSemiState()
		ss.SetArchitecturePlan(&state.ArchitecturePlan{
			Description: "Go CLI",
			Components: []state.ComponentSpec{
				{Name: "main", Path: "cmd/main.go"},
				{Name: "core", Path: "pkg/core.go"},
			},
			Endpoints: []state.EndpointSpec{
				{Method: "GET", Path: "/b"},
				{Method: "GET", Path: "/a"},
			},
		})
		return ss
	}

	if StateFingerprint(build()) != StateFingerprint(build()) {
		t.Error("equivalent states must produce identical fingerprints")
	}
}

func TestFingerprintDetectsSemanticDifference(t *testing.T) {
	a := state.NewSemiState()
	a.SetArchitecturePlan(&state.ArchitecturePlan{Description: "plan A"})

	b := state.NewSemiState()
	b.SetArchitecturePlan(&state.ArchitecturePlan{Description: "plan B"})

	if StateFingerprint(a) == StateFingerprint(b) {
		t.Error("different architectures must produce different fingerprints")
	}
}

func TestFingerprintIgnoresComponentOrder(t *testing.T) {
	a := state.NewSemiState()
	a.SetArchitecturePlan(&state.ArchitecturePlan{
		Components: []state.ComponentSpec{{Path: "a.go"}, {Path: "b.go"}},
	})
	b := state.NewSemiState()
	b.SetArchitecturePlan(&state.ArchitecturePlan{
		Components: []state.ComponentSpec{{Path: "b.go"}, {Path: "a.go"}},
	})

	if StateFingerprint(a) != StateFingerprint(b) {
		t.Error("component ordering must not change the fingerprint")
	}
}

func TestAssessUnknownOnEmptyTrace(t *testing.T) {
	tr := NewTracker()
	if got := tr.Assess().Phase; got != PhaseUnknown {
		t.Errorf("expected %s, got %s", PhaseUnknown, got)
	}
}

func TestAssessInsufficientDataOnFirstPoint(t *testing.T) {
	tr := NewTracker()
	tr.Observe(newState(1))
	if got := tr.Assess().Phase; got != PhaseInsufficientData {
		t.Errorf("expected %s, got %s", PhaseInsufficientData, got)
	}
}

func TestAssessProductiveWhenPotentialRises(t *testing.T) {
	tr := NewTracker()

	low := newState(1)
	tr.Observe(low)

	high := newState(2)
	high.SetConfidence(state.ConfidenceHigh)
	high.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Strength: state.ConfidenceCertain})
	tr.Observe(high)

	a := tr.Assess()
	if a.Phase != PhaseProductive {
		t.Errorf("expected %s, got %s (%s)", PhaseProductive, a.Phase, a.Reason)
	}
}

func TestAssessConvergedOnSustainedPlateau(t *testing.T) {
	tr := NewTracker()
	for i := 1; i <= PlateauTolerance+1; i++ {
		tr.Observe(newState(i))
	}

	a := tr.Assess()
	if a.Phase != PhaseConverged {
		t.Errorf("expected %s, got %s (%s)", PhaseConverged, a.Phase, a.Reason)
	}
	if !a.ShouldStop {
		t.Error("converged phase must request a stop")
	}
}

func TestAssessDetectsTwoCycle(t *testing.T) {
	tr := NewTracker()

	planA := state.NewSemiState()
	planA.Revision = 1
	planA.SetArchitecturePlan(&state.ArchitecturePlan{Description: "in-memory"})

	planB := state.NewSemiState()
	planB.Revision = 2
	planB.SetArchitecturePlan(&state.ArchitecturePlan{Description: "file-based"})

	tr.Observe(planA)
	tr.Observe(planB)
	tr.Observe(planA)

	a := tr.Assess()
	if a.Phase != PhaseOscillating {
		t.Errorf("expected %s, got %s (%s)", PhaseOscillating, a.Phase, a.Reason)
	}
	if !a.ForceDifferentiate {
		t.Error("oscillation must request forced differentiation")
	}
	if a.CycleLength != 2 {
		t.Errorf("expected cycle length 2, got %d", a.CycleLength)
	}
}

func TestAssessRegressingAfterGoodResult(t *testing.T) {
	tr := NewTracker()

	good := state.NewSemiState()
	good.Revision = 1
	good.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestPassed})
	tr.Observe(good)

	bad := state.NewSemiState()
	bad.Revision = 2
	bad.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: state.TestFailed, FailureClass: "build_error"})
	tr.Observe(bad)

	a := tr.Assess()
	if a.Phase != PhaseRegressing {
		t.Errorf("expected %s, got %s (%s)", PhaseRegressing, a.Phase, a.Reason)
	}
	if !a.ShouldRollback {
		t.Error("regression must request rollback")
	}
	if a.BestRevision != 1 {
		t.Errorf("best revision should be 1, got %d", a.BestRevision)
	}
}

func TestTraceAndLen(t *testing.T) {
	tr := NewTracker()
	for i := 1; i <= 5; i++ {
		tr.Observe(newState(i))
	}
	if tr.Len() != 5 {
		t.Errorf("expected len 5, got %d", tr.Len())
	}
	if len(tr.Trace()) != 5 {
		t.Errorf("expected trace of 5, got %d", len(tr.Trace()))
	}
	tr.Observe(newState(6))
	if tr.Trace()[0].Revision != 1 {
		t.Error("trace must be ordered by observation")
	}
}

func TestParetoFrontierPrunesDominatedPoints(t *testing.T) {
	f := NewParetoFrontier()
	f.Add(ParetoPoint{Revision: 1, PassRate: 0.5, Coverage: 0.5, TokenCost: 100, Complexity: 10})
	f.Add(ParetoPoint{Revision: 2, PassRate: 1.0, Coverage: 1.0, TokenCost: 50, Complexity: 5})

	if f.Len() != 1 {
		t.Fatalf("revision 1 should be dominated and pruned, frontier size = %d", f.Len())
	}
	best, ok := f.Best()
	if !ok || best.Revision != 2 {
		t.Errorf("expected revision 2 as best, got %+v (ok=%v)", best, ok)
	}
}

func TestParetoFrontierKeepsTradeoffs(t *testing.T) {
	f := NewParetoFrontier()
	// Better pass rate but much more expensive: a genuine tradeoff.
	f.Add(ParetoPoint{Revision: 1, PassRate: 1.0, Coverage: 0.5, TokenCost: 1000, Complexity: 5})
	f.Add(ParetoPoint{Revision: 2, PassRate: 0.5, Coverage: 0.5, TokenCost: 10, Complexity: 5})

	if f.Len() != 2 {
		t.Fatalf("tradeoff points must both survive, frontier size = %d", f.Len())
	}
	best, _ := f.Best()
	if best.Revision != 1 {
		t.Errorf("expected the passing revision to be preferred, got %d", best.Revision)
	}
}

func TestParetoFrontierEmpty(t *testing.T) {
	f := NewParetoFrontier()
	if _, ok := f.Best(); ok {
		t.Error("empty frontier must report no best point")
	}
	if f.Len() != 0 {
		t.Error("empty frontier must have size 0")
	}
}

func TestParetoSnapshotIncludesPotential(t *testing.T) {
	f := NewParetoFrontier()
	f.Add(ParetoPoint{Revision: 7, PassRate: 1})
	snap := f.Snapshot(func(rev int) float64 { return 0.42 })
	if len(snap) != 1 || snap[0].Potential != 0.42 {
		t.Errorf("expected one point with potential 0.42, got %+v", snap)
	}
}

func TestParetoIgnoresDuplicateRevisions(t *testing.T) {
	f := NewParetoFrontier()
	f.Add(ParetoPoint{Revision: 1, PassRate: 0.5})
	f.Add(ParetoPoint{Revision: 1, PassRate: 0.9})
	if f.Len() != 1 {
		t.Errorf("duplicate revision must not create a second point, size = %d", f.Len())
	}
}

func TestPlateauStreakCountsFlatIterations(t *testing.T) {
	tr := NewTracker()
	tr.Observe(newState(1))
	tr.Observe(newState(2))
	tr.Observe(newState(3))
	if got := tr.plateauStreak(); got != 2 {
		t.Errorf("expected plateau streak 2, got %d", got)
	}
}

func TestDetectCycleWithinWindow(t *testing.T) {
	tr := NewTracker()
	// Build a 3-cycle: A, B, C, A.
	for i, desc := range []string{"A", "B", "C", "A"} {
		ss := state.NewSemiState()
		ss.Revision = i + 1
		ss.SetArchitecturePlan(&state.ArchitecturePlan{Description: desc})
		tr.Observe(ss)
	}
	if cyc := tr.detectCycle(); cyc != 3 {
		t.Errorf("expected cycle length 3, got %d", cyc)
	}
}

func TestObserveRecordsTimestamp(t *testing.T) {
	tr := NewTracker()
	tr.Observe(newState(1))
	if tr.Trace()[0].Timestamp.IsZero() {
		t.Error("trajectory timestamp must be set")
	}
}
