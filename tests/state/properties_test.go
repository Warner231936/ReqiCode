package state_test

import (
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/kilo/spiral-codemaker/core/converge"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/leanovate/gopter"
	gen "github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

// Property tests for the semi-state algebra.
//
// These are not example tests with more cases. Each asserts an invariant that
// must hold for *any* reachable state, generated over hundreds of inputs. The
// distinction matters because self-modification edits exactly the code these
// invariants protect: a change to SemiState that breaks revision monotonicity or
// admits an illegal claim transition must be caught by the machine rather than
// discovered later as a corrupted history.

const propRuns = 200

// genConfidence produces the four named confidence levels.
func genConfidence() gopter.Gen {
	return gen.OneConstOf(
		state.ConfidenceLow,
		state.ConfidenceMedium,
		state.ConfidenceHigh,
		state.ConfidenceCertain,
	)
}

func genNonEmptyString() gopter.Gen {
	return gen.SliceOf(genAlpha()).SuchThat(func(vs []rune) bool { return len(vs) > 0 }).Map(func(vs []rune) string {
		return string(vs)
	})
}

func genAlpha() gopter.Gen {
	return gen.RuneRange('a', 'z')
}
func genTestStatus() gopter.Gen {
	return gen.OneConstOf(
		state.TestPassed, state.TestFailed, state.TestPending, state.TestSkipped,
	)
}

func genSeverity() gopter.Gen {
	return gen.OneConstOf(
		state.ConflictLow, state.ConflictMedium, state.ConflictHigh, state.ConflictCritical,
	)
}

func genStatus() gopter.Gen {
	return gen.OneConstOf(
		state.HypothesisProposed, state.HypothesisAccepted,
		state.HypothesisRejected, state.HypothesisSuperseded,
	)
}

// ---------------------------------------------------------------------------
// Invariant: Confidence is always within its declared bounds.
// ---------------------------------------------------------------------------

func TestPropConfidenceAlwaysBounded(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("SetConfidence never leaves [0,1]", prop.ForAll(
		func(c state.Confidence) bool {
			ss := state.NewSemiState()
			ss.SetConfidence(c)
			got := float64(ss.GetConfidence())
			return got >= 0 && got <= 1
		},
		genConfidence(),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: revision is monotonically non-decreasing under any call
// interleaving, and strictly increasing per IncrementRevision.
// ---------------------------------------------------------------------------

func TestPropRevisionMonotonicUnderSequentialUse(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("revision never decreases as evidence accumulates", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			prev := ss.Revision
			for i := 0; i < n; i++ {
				ss.IncrementRevision()
				if ss.Revision <= prev {
					return false
				}
				prev = ss.Revision
			}
			return true
		},
		gen.IntRange(0, 64),
	))

	properties.Property("arbitrary mutation never lowers the revision", prop.ForAll(
		func(n int, c state.Confidence) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.IncrementRevision()
			}
			at := ss.Revision
			ss.SetConfidence(c)
			ss.AddEvidence(state.Evidence{Type: state.EvidenceAnalysis, Content: "x"})
			ss.AddDecision(state.Decision{ID: "d", Decision: "x"})
			ss.AddObjection(state.Objection{Content: "x", Severity: "low"})
			return ss.Revision == at
		},
		gen.IntRange(0, 32),
		genConfidence(),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: every Provenance revision is non-negative. A negative revision
// would make the tamper chain ambiguous.
// ---------------------------------------------------------------------------

func TestPropProvenanceRevisionNonNegative(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("appending artifacts never produces a negative revision", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddEvidence(state.Evidence{Type: state.EvidenceAnalysis, Content: "c"})
				ss.AddHypothesis(state.Hypothesis{ID: fmt.Sprintf("h%d", i)})
				ss.AddDecision(state.Decision{ID: fmt.Sprintf("d%d", i)})
				if ss.Revision < 0 {
					return false
				}
			}
			for _, e := range ss.GetEvidence() {
				if e.Provenance.Revision < 0 {
					return false
				}
			}
			return true
		},
		gen.IntRange(0, 40),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: Snapshot/Clone round-trip fidelity. A clone that loses or alters
// state is a silently corrupted fork, which the canary would then measure as
// "no difference".
// ---------------------------------------------------------------------------

func TestPropCloneRoundTripFidelity(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("clone preserves counts of every collection", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				id := fmt.Sprintf("id-%d", i)
				ss.AddEvidence(state.Evidence{Type: state.EvidenceAnalysis, Content: id, Strength: state.ConfidenceHigh})
				ss.AddHypothesis(state.Hypothesis{ID: id, Content: id, Status: state.HypothesisProposed})
				ss.AddDecision(state.Decision{ID: id, Decision: id})
				ss.AddTestResult(state.TestResult{ID: id, Command: id, Status: state.TestPassed})
				ss.AddFile(state.FileEntry{Path: id + ".go", Content: id})
			}
			clone := ss.Clone()
			return len(clone.GetEvidence()) == len(ss.GetEvidence()) &&
				len(clone.GetHypotheses()) == len(ss.GetHypotheses()) &&
				len(clone.GetDecisions()) == len(ss.GetDecisions()) &&
				len(clone.GetTestResults()) == len(ss.GetTestResults()) &&
				len(clone.GetGeneratedFiles()) == len(ss.GetGeneratedFiles()) &&
				clone.Revision == ss.Revision
		},
		gen.IntRange(0, 40),
	))

	properties.TestingRun(t)
}

func TestPropCloneIsIndependent(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("mutating a clone never affects the original", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddDecision(state.Decision{ID: fmt.Sprintf("d%d", i), Decision: "orig"})
			}
			clone := ss.Clone()
			for i := 0; i < n; i++ {
				clone.AddDecision(state.Decision{ID: fmt.Sprintf("x%d", i), Decision: "clone"})
			}
			return len(ss.GetDecisions()) == n && len(clone.GetDecisions()) == 2*n
		},
		gen.IntRange(0, 20),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: getters never leak the internal slice. A getter that returned the
// backing array would let a caller mutate state without taking the lock.
// ---------------------------------------------------------------------------

func TestPropGettersDoNotLeakInternals(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("mutating a returned slice cannot reach the state", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddDecision(state.Decision{ID: fmt.Sprintf("d%d", i)})
			}
			got := ss.GetDecisions()
			for i := range got {
				got[i].Decision = "tampered"
			}
			for _, d := range ss.GetDecisions() {
				if d.Decision == "tampered" {
					return false
				}
			}
			return true
		},
		gen.IntRange(1, 30),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: claim status transitions are legal.
//
// This is the one the existing suite does not cover at all. A status field with
// no transition rule means a unit can move a REJECTED claim back to ACCEPTED
// silently, and nothing in the system would notice. That is precisely the
// "belief can die" property the whole claim design depends on.
// ---------------------------------------------------------------------------

func TestPropClaimTransitionLegality(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	// Start from a terminal status, attempt any transition, and require that the
	// claim does not end up resurrected.
	//
	// The assertion is on the status *after* the update compared against the
	// status *before* it. An earlier version of this property compared the
	// post-update status against the requested value, which made it vacuous: it
	// could never fail, because UpdateHypothesisStatus sets exactly that value.
	// A property test that cannot fail is a test that asserts nothing.
	properties.Property("a terminal claim cannot be resurrected", prop.ForAll(
		func(to state.HypothesisStatus) bool {
			ss := state.NewSemiState()
			ss.AddHypothesis(state.Hypothesis{
				ID: "h1", Content: "c", Status: state.HypothesisRejected,
			})
			ss.UpdateHypothesisStatus("h1", to)

			for _, h := range ss.GetHypotheses() {
				// REJECTED is terminal. A rejected claim that reports itself as
				// ACCEPTED means the evidence that killed it has been silently
				// discarded, which is the whole failure the lifecycle exists to
				// prevent.
				if h.Status == state.HypothesisAccepted || h.Status == state.HypothesisProposed {
					return false
				}
			}
			return true
		},
		genStatus(),
	))

	properties.Property("only PROPOSED to terminal is reachable from any status", prop.ForAll(
		func(from, to state.HypothesisStatus) bool {
			ss := state.NewSemiState()
			ss.AddHypothesis(state.Hypothesis{ID: "h1", Content: "c", Status: from})
			ss.UpdateHypothesisStatus("h1", to)
			for _, h := range ss.GetHypotheses() {
				if !state.LegalHypothesisTransition(from, to) {
					// The state accepted a transition the rules forbid.
					if h.Status == to {
						return false
					}
				}
			}
			return true
		},
		genStatus(),
		genStatus(),
	))

	properties.TestingRun(t)
}

func TestPropHypothesisUpdateTouchesExactlyOne(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("updating one hypothesis leaves the others untouched", prop.ForAll(
		func(n int, target int) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddHypothesis(state.Hypothesis{
					ID: fmt.Sprintf("h%d", i), Content: "c", Status: state.HypothesisProposed,
				})
			}
			id := fmt.Sprintf("h%d", target%n)
			ss.UpdateHypothesisStatus(id, state.HypothesisAccepted)
			accepted := 0
			for _, h := range ss.GetHypotheses() {
				if h.Status == state.HypothesisAccepted {
					accepted++
				}
			}
			return accepted == 1
		},
		gen.IntRange(1, 25),
		gen.IntRange(0, 40),
	))

	properties.TestingRun(t)
}

func TestPropUpdatingUnknownHypothesisIsSafe(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("updating a nonexistent id changes nothing", prop.ForAll(
		func(name string) bool {
			ss := state.NewSemiState()
			ss.AddHypothesis(state.Hypothesis{ID: "real", Content: "c", Status: state.HypothesisProposed})
			before := len(ss.GetHypotheses())
			ss.UpdateHypothesisStatus(name, state.HypothesisRejected)
			after := ss.GetHypotheses()
			return len(after) == before && after[0].Status == state.HypothesisProposed
		},
		genNonEmptyString(),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: conflict accounting is additive and never corrupts.
// ---------------------------------------------------------------------------

func TestPropConflictCounting(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("every added conflict is retained", prop.ForAll(
		func(n int, sev state.ConflictSeverity) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddConflict(state.Conflict{
					ID:       fmt.Sprintf("c%d", i),
					Subject:  "s",
					Severity: sev,
				})
			}
			return len(ss.Contradictions) == n
		},
		gen.IntRange(0, 30),
		genSeverity(),
	))

	properties.TestingRun(t)
}

func TestPropHasTestFilesDetectsTestFiles(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("HasTestFiles agrees with whether a _test.go file exists", prop.ForAll(
		func(paths []string) bool {
			ss := state.NewSemiState()
			expect := false
			for _, p := range paths {
				ss.AddFile(state.FileEntry{Path: p})
				if strings.HasSuffix(p, "_test.go") {
					expect = true
				}
			}
			return ss.HasTestFiles() == expect
		},
		gen.SliceOf(genNonEmptyString()).Map(func(s []string) []string { return s }),
	))

	properties.TestingRun(t)
}

func TestPropHasFailedTestsMatchesLastResult(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("HasFailedTests reflects the most recent result", prop.ForAll(
		func(n int, st state.TestStatus) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddTestResult(state.TestResult{ID: fmt.Sprintf("t%d", i), Status: state.TestPassed})
			}
			ss.AddTestResult(state.TestResult{ID: "last", Status: st})
			return ss.HasFailedTests() == (st == state.TestFailed)
		},
		gen.IntRange(0, 20),
		genTestStatus(),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: the chain hash is a function of content only, and content changes
// are always reflected.
// ---------------------------------------------------------------------------

func TestPropChainHashIsContentAddressed(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("different confidence yields a different hash", prop.ForAll(
		func(c state.Confidence) bool {
			a := state.NewSemiState()
			a.SetConfidence(c)
			ha, _ := a.ComputeChainHash(state.GenesisHash)

			b := state.NewSemiState()
			b.SetConfidence(nextConfidence(c))
			hb, _ := b.ComputeChainHash(state.GenesisHash)
			return ha.Hash != hb.Hash
		},
		genConfidence(),
	))

	properties.Property("identical content always yields an identical hash", prop.ForAll(
		func(c state.Confidence, n int) bool {
			build := func() string {
				ss := state.NewSemiState()
				ss.SetConfidence(c)
				for i := 0; i < n; i++ {
					ss.AddDecision(state.Decision{ID: fmt.Sprintf("d%d", i), Decision: "x"})
				}
				h, _ := ss.ComputeChainHash(state.GenesisHash)
				return h.Hash
			}
			return build() == build()
		},
		genConfidence(),
		gen.IntRange(0, 20),
	))

	properties.TestingRun(t)
}

func nextConfidence(c state.Confidence) state.Confidence {
	switch c {
	case state.ConfidenceLow:
		return state.ConfidenceMedium
	case state.ConfidenceMedium:
		return state.ConfidenceHigh
	case state.ConfidenceHigh:
		return state.ConfidenceCertain
	default:
		return state.ConfidenceLow
	}
}

// ---------------------------------------------------------------------------
// Invariant: concurrent mutation is safe and lossless under the documented API.
// ---------------------------------------------------------------------------

func TestPropConcurrentAppendLosesNothing(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 20

	properties := gopter.NewProperties(parameters)

	properties.Property("concurrent appends under the public API are not lost", prop.ForAll(
		func(workers, perWorker int) bool {
			ss := state.NewSemiState()
			var wg sync.WaitGroup
			for w := 0; w < workers; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					for i := 0; i < perWorker; i++ {
						ss.AddEvidence(state.Evidence{
							Type:    state.EvidenceAnalysis,
							Content: fmt.Sprintf("w%d-i%d", w, i),
						})
					}
				}(w)
			}
			wg.Wait()
			return len(ss.GetEvidence()) == workers*perWorker
		},
		gen.IntRange(1, 8),
		gen.IntRange(1, 20),
	))

	properties.TestingRun(t)
}

func TestPropConcurrentReadDuringWriteNeverPanics(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 20

	properties := gopter.NewProperties(parameters)

	properties.Property("reads concurrent with writes never observe a negative count", prop.ForAll(
		func(n int) bool {
			ss := state.NewSemiState()
			done := make(chan bool, 1)
			go func() {
				for i := 0; i < n; i++ {
					ss.AddDecision(state.Decision{ID: "d", Decision: "x"})
				}
				done <- true
			}()
			for i := 0; i < n; i++ {
				if len(ss.GetDecisions()) < 0 {
					return false
				}
				if len(ss.GetEvidence()) < 0 {
					return false
				}
			}
			<-done
			return true
		},
		gen.IntRange(1, 50),
	))

	properties.TestingRun(t)
}

// ---------------------------------------------------------------------------
// Invariant: the potential function stays finite and bounded. A NaN or
// infinity leaking into the potential would silently disable every canary
// comparison, because NaN comparisons are all false.
// ---------------------------------------------------------------------------

func TestPropPotentialAlwaysFinite(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = propRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("potential is always a finite number", prop.ForAll(
		func(n int, st state.TestStatus) bool {
			ss := state.NewSemiState()
			for i := 0; i < n; i++ {
				ss.AddTestResult(state.TestResult{ID: fmt.Sprintf("t%d", i), Status: st})
				ss.AddEvidence(state.Evidence{Type: state.EvidenceTestPass, Strength: state.ConfidenceHigh})
				ss.AddFile(state.FileEntry{Path: fmt.Sprintf("f%d.go", i)})
			}
			p := converge.ComputePotential(ss).Value
			return !math.IsNaN(p) && !math.IsInf(p, 0)
		},
		gen.IntRange(0, 40),
		genTestStatus(),
	))

	properties.TestingRun(t)
}
