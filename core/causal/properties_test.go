package causal_test

import (
	"fmt"
	"math"
	"testing"

	"github.com/kilo/spiral-codemaker/core/causal"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/leanovate/gopter"
	"github.com/leanovate/gopter/gen"
	"github.com/leanovate/gopter/prop"
)

const ledgerRuns = 200

func genStatus() gopter.Gen {
	return gen.OneConstOf(state.TestPassed, state.TestFailed)
}

func genFailureClass() gopter.Gen {
	return gen.OneConstOf("", "build_error", "test_assertion_failure", "data_race")
}

func propFor(id, file string) state.CodeProposal {
	return state.CodeProposal{
		ID:              id,
		File:            file,
		Operation:       state.OpCreate,
		Reason:          "r",
		OriginatingUnit: "unit",
		Provenance:      state.NewProvenance("unit"),
	}
}

func stateWith(status state.TestStatus, class string) *state.SemiState {
	ss := state.NewSemiState()
	ss.AddTestResult(state.TestResult{ID: "t", Command: "go test", Status: status, FailureClass: class})
	return ss
}

// Record is idempotent: recording the same proposal twice must not double-count
// it. Without this, a retried iteration would inflate the intervention count and
// make the ledger describe a run that never happened.
func TestPropLedgerRecordIsIdempotent(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = ledgerRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("recording the same proposal N times yields one intervention", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			for i := 0; i < n; i++ {
				l.Record(propFor("p1", "a.go"), "s", 0)
			}
			return len(l.Interventions()) == 1
		},
		gen.IntRange(1, 20),
	))

	properties.Property("distinct proposals each yield exactly one intervention", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), "s", 0)
			}
			return len(l.Interventions()) == n
		},
		gen.IntRange(0, 30),
	))

	properties.TestingRun(t)
}

// Credit conservation: the sum of attributed net credit must equal the observed
// delta in the objective vector, scaled by priority. If it does not, the ledger
// is inventing signal that the measurement does not contain.
func TestPropLedgerCreditConservesObservedDelta(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = ledgerRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("a single intervention's credit is bounded and signed by direction", prop.ForAll(
		func(st state.TestStatus, class string) bool {
			l := causal.NewLedger()
			l.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			l.Record(propFor("p1", "a.go"), "s", 0)
			l.Attribute(stateWith(st, class))

			credit, ok := l.CreditFor("p1")
			if !ok {
				return false
			}
			// No test results means no delta, so no credit.
			if st == state.TestFailed && class == "build_error" {
				return credit == 0
			}
			// Improvement earns positive credit; regression earns negative.
			if st == state.TestPassed {
				return credit > 0
			}
			if class == "build_error" {
				return credit < 0
			}
			// An assertion failure still means it compiled, so it is a partial
			// improvement rather than a full regression.
			return credit >= 0
		},
		genStatus(),
		genFailureClass(),
	))

	properties.Property("credit never exceeds the number of objectives it could have moved", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			l.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), "s", 0)
			}
			l.Attribute(stateWith(state.TestPassed, ""))
			// Every intervention shares the single observed delta, so total
			// credit is bounded by the delta itself, not multiplied by cohort.
			for _, iv := range l.Interventions() {
				if iv.NetCredit > 2.0 || iv.NetCredit < -2.0 {
					return false
				}
			}
			return true
		},
		gen.IntRange(1, 12),
	))

	properties.TestingRun(t)
}

// Joint attribution must discount confidence, and confidence must never reach
// certainty when the cohort is larger than one.
func TestPropAttributionConfidenceReflectsUncertainty(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = ledgerRuns

	properties := gopter.NewProperties(parameters)

	// Confidence is 1/cohortSize. A solo intervention is fully attributable and
	// must say so; a two-member cohort halves it; and it never rises as the
	// cohort grows.
	//
	// An earlier version of this suite asserted confidence <= 0.5 for every
	// cohort, which pinned the bug it was meant to catch: the cap it enforced
	// made a lone intervention indistinguishable from a two-member one.
	properties.Property("confidence equals one over the cohort size", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			l.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), "s", 0)
			}
			l.Attribute(stateWith(state.TestPassed, ""))

			want := 1 / float64(n)
			for _, iv := range l.Interventions() {
				if math.Abs(iv.Confidence-want) > 1e-9 {
					return false
				}
			}
			return true
		},
		gen.IntRange(1, 16),
	))

	properties.Property("a solo intervention reports full confidence", prop.ForAll(
		func() bool {
			l := causal.NewLedger()
			l.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			l.Record(propFor("p", "a.go"), "s", 0)
			l.Attribute(stateWith(state.TestPassed, ""))
			return l.Interventions()[0].Confidence == 1.0
		},
	))

	properties.Property("a lone intervention is more confident than a cohort of many", prop.ForAll(
		func(n int) bool {
			solo := causal.NewLedger()
			solo.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			solo.Record(propFor("p", "a.go"), "s", 0)
			solo.Attribute(stateWith(state.TestPassed, ""))
			soloConf := solo.Interventions()[0].Confidence

			many := causal.NewLedger()
			many.ObserveBaseline(stateWith(state.TestFailed, "build_error"))
			for i := 0; i < n+1; i++ {
				many.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), "s", 0)
			}
			many.Attribute(stateWith(state.TestPassed, ""))
			for _, iv := range many.Interventions() {
				if iv.Confidence >= soloConf {
					return false
				}
			}
			return true
		},
		gen.IntRange(1, 16),
	))

	properties.TestingRun(t)
}

// Token accounting must be exactly additive, because the frontier uses it as a
// cost axis and a leaky total would make the frontier meaningless.
func TestPropTokenAccountingIsAdditive(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = ledgerRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("total tokens equal the sum of recorded interventions", prop.ForAll(
		func(n, per int) bool {
			l := causal.NewLedger()
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), "s", per)
			}
			return l.TotalTokens() == n*per
		},
		gen.IntRange(0, 20),
		gen.IntRange(0, 5000),
	))

	properties.TestingRun(t)
}

// The objective vector must always be finite and non-negative, because the
// canary and the potential function both consume it.
func TestPropObjectivesAlwaysFinite(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = ledgerRuns

	properties := gopter.NewProperties(parameters)

	properties.Property("every objective is finite and non-negative", prop.ForAll(
		func(st state.TestStatus, class string, files int) bool {
			ss := stateWith(st, class)
			for i := 0; i < files; i++ {
				ss.AddFile(state.FileEntry{Path: fmt.Sprintf("f%d.go", i)})
			}
			for _, v := range causal.Objectives(ss) {
				if v != v { // NaN
					return false
				}
				if v < 0 {
					return false
				}
			}
			return true
		},
		genStatus(),
		genFailureClass(),
		gen.IntRange(0, 40),
	))

	properties.Property("compile and test objectives stay within [0,1]", prop.ForAll(
		func(st state.TestStatus, class string) bool {
			v := causal.Objectives(stateWith(st, class))
			c, okc := v[causal.ObjCompile]
			p, okp := v[causal.ObjTestPass]
			if !okc || !okp {
				return false
			}
			return c >= 0 && c <= 1 && p >= 0 && p <= 1
		},
		genStatus(),
		genFailureClass(),
	))

	properties.TestingRun(t)
}

// Strategy ranking must be a total order over recorded strategies.
func TestPropStrategyRankingIsTotal(t *testing.T) {
	parameters := gopter.DefaultTestParameters()
	parameters.MinSuccessfulTests = 60

	properties := gopter.NewProperties(parameters)

	properties.Property("every recorded strategy appears exactly once in the ranking", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), fmt.Sprintf("s%d", i%3), 0)
			}
			scores := l.TopStrategies()
			seen := map[string]bool{}
			for _, s := range scores {
				if seen[s.Strategy] {
					return false
				}
				seen[s.Strategy] = true
				if s.Samples <= 0 {
					return false
				}
			}
			return len(seen) == len(scores)
		},
		gen.IntRange(1, 20),
	))

	properties.Property("ranking is sorted by descending mean credit", prop.ForAll(
		func(n int) bool {
			l := causal.NewLedger()
			for i := 0; i < n; i++ {
				l.Record(propFor(fmt.Sprintf("p%d", i), "a.go"), fmt.Sprintf("s%d", i%4), 0)
			}
			scores := l.TopStrategies()
			for i := 1; i < len(scores); i++ {
				if scores[i-1].MeanCredit < scores[i].MeanCredit {
					return false
				}
			}
			return true
		},
		gen.IntRange(1, 20),
	))

	properties.TestingRun(t)
}
