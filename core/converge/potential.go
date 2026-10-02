// Package converge implements Phase 0 instrumentation for the spiral: a scalar
// potential function over the semi-state, convergence classification of its
// trajectory, oscillation detection, and Pareto frontier tracking.
//
// The potential function is the Lyapunov analogue for the spiral loop. It gives
// the system a principled answer to "is this iteration making progress?" instead
// of relying on maxIterations as a hard stop.
package converge

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/core/state"
)

// Potential is a scalar objective over a SemiState. Higher is better.
//
// The components are deliberately normalized to [0,1] and combined with fixed
// weights so that trajectories are comparable across runs. Weights encode the
// belief that a passing test suite dominates a marginal improvement in internal
// tidiness; see Weights for the rationale.
type Potential struct {
	Revision int     `json:"revision"`
	Value    float64 `json:"value"`

	TestPassRate   float64 `json:"test_pass_rate"`
	CompileSuccess float64 `json:"compile_success"`
	EvidenceDensity float64 `json:"evidence_density"`
	ResolutionRate float64 `json:"resolution_rate"`
	ConflictPenalty float64 `json:"conflict_penalty"`
	ObjectionPenalty float64 `json:"objection_penalty"`
	ConfidenceTerm float64 `json:"confidence_term"`
	DecisionStability float64 `json:"decision_stability"`

	HasTestResult bool `json:"has_test_result"`
	HasFiles      bool `json:"has_files"`
}

// Weights are the coefficients of the potential function. They are exposed so
// experiments can vary them and observe trajectory changes.
//
// The ordering encodes a clear priority: correctness first, evidence second,
// hygiene last. A state that compiles and passes tests outranks a state that is
// beautifully documented and does not build.
var Weights = struct {
	TestPassRate      float64
	CompileSuccess   float64
	EvidenceDensity   float64
	ResolutionRate    float64
	ConfidenceTerm    float64
	ConflictPenalty   float64
	ObjectionPenalty  float64
	DecisionStability float64
}{
	TestPassRate:      0.32,
	CompileSuccess:    0.18,
	EvidenceDensity:   0.13,
	ResolutionRate:    0.08,
	ConfidenceTerm:    0.12,
	ConflictPenalty:   0.07,
	ObjectionPenalty:  0.05,
	DecisionStability: 0.05,
}

// ComputePotential derives the scalar objective from the current state.
//
// The function is pure: same state in, same potential out. That property is what
// makes trajectories trustworthy, because it means a change in the potential
// between two revisions is attributable to state change and nothing else.
func ComputePotential(ss *state.SemiState) Potential {
	// The nil guard must come before any dereference.
	//
	// It did not: the revision read below preceded the nil check, so the guard was
	// dead code and ComputePotential panicked on a nil state. Found by
	// core/verify, which synthesises a smoke test calling every exported function
	// with zero arguments -- including nil, for a pointer parameter.
	if ss == nil {
		return Potential{}
	}

	p := Potential{Revision: ss.Revision}

	p.ConfidenceTerm = float64(ss.Confidence)
	p.HasFiles = ss.HasGeneratedFiles()

	// Test pass rate over all recorded results. Repeated identical results are
	// de-duplicated by command+status so that re-running the same suite does not
	// inflate the rate.
	p.TestPassRate, p.HasTestResult = testPassRate(ss.GetTestResults())
	p.CompileSuccess = compileSuccess(ss.GetTestResults())
	p.EvidenceDensity = evidenceDensity(ss)
	p.ResolutionRate = resolutionRate(ss)
	p.ConflictPenalty = conflictPenalty(ss)
	p.ObjectionPenalty = objectionPenalty(ss)
	p.DecisionStability = decisionStability(ss)

	p.Value = Weights.TestPassRate*p.TestPassRate +
		Weights.CompileSuccess*p.CompileSuccess +
		Weights.EvidenceDensity*p.EvidenceDensity +
		Weights.ResolutionRate*p.ResolutionRate +
		Weights.ConfidenceTerm*p.ConfidenceTerm +
		Weights.DecisionStability*p.DecisionStability -
		Weights.ConflictPenalty*p.ConflictPenalty -
		Weights.ObjectionPenalty*p.ObjectionPenalty

	return p
}

func testPassRate(results []state.TestResult) (float64, bool) {
	if len(results) == 0 {
		return 0, false
	}
	type key struct{ cmd, status string }
	seen := map[key]bool{}
	passed, total := 0, 0
	for _, r := range results {
		k := key{r.Command, string(r.Status)}
		if seen[k] {
			continue
		}
		seen[k] = true
		total++
		if r.Status == state.TestPassed {
			passed++
		}
	}
	return float64(passed) / float64(total), true
}

// compileSuccess reports whether the most recent test result indicates the
// workspace built at all. A build failure is categorically worse than an
// assertion failure, so it is scored separately from the general pass rate.
func compileSuccess(results []state.TestResult) float64 {
	if len(results) == 0 {
		return 0
	}
	last := results[len(results)-1]
	if last.Status == state.TestPassed {
		return 1
	}
	if strings.Contains(last.FailureClass, "build") {
		return 0
	}
	return 0.5
}

// evidenceDensity rewards accumulating evidence but saturates, because a large
// pile of weak evidence is not better than a small pile of strong evidence.
func evidenceDensity(ss *state.SemiState) float64 {
	ev := ss.GetEvidence()
	if len(ev) == 0 {
		return 0
	}
	var strength float64
	for _, e := range ev {
		strength += float64(e.Strength)
	}
	avg := strength / float64(len(ev))
	// Saturating count term: 20 pieces of evidence is "enough" for a full mark.
	count := float64(len(ev)) / 20
	if count > 1 {
		count = 1
	}
	return 0.6*avg + 0.4*count
}

// resolutionRate is the fraction of recorded findings that are no longer
// unverified. This is the epistemic term: a state where more claims have been
// adjudicated is genuinely better than one where they are merely accumulated.
func resolutionRate(ss *state.SemiState) float64 {
	findings := ss.Findings
	if len(findings) == 0 {
		return 0
	}
	resolved := 0
	for _, f := range findings {
		switch f.NewStatus {
		case state.FindingSupported, state.FindingResolved:
			resolved++
		case state.FindingContradicted, state.FindingRejected:
			// A rejected claim is also adjudicated: we know it is wrong.
			resolved++
		}
	}
	return float64(resolved) / float64(len(findings))
}

func conflictPenalty(ss *state.SemiState) float64 {
	total := len(ss.Contradictions)
	if total == 0 {
		return 0
	}
	active := 0
	for _, c := range ss.Contradictions {
		if !c.Resolved {
			active++
		}
	}
	p := float64(active) / float64(total)
	if active > 0 {
		// Presence of any active conflict is a hard signal; scale it so even one
		// critical conflict meaningfully dents the potential.
		p = 0.5*p + 0.5*float64(min(active, 4))/4
	}
	return p
}

func objectionPenalty(ss *state.SemiState) float64 {
	o := ss.GetObjections()
	if len(o) == 0 {
		return 0
	}
	var weighted float64
	for _, obj := range o {
		switch strings.ToLower(obj.Severity) {
		case "critical", "high":
			weighted += 1
		case "medium":
			weighted += 0.5
		default:
			weighted += 0.25
		}
	}
	return weighted / float64(len(o)*2)
}

// decisionStability measures whether the system keeps changing its mind. A
// system that rewrites its decisions every iteration is not converging, even if
// its confidence is high. Supersedes links in Decision give us the signal.
func decisionStability(ss *state.SemiState) float64 {
	decisions := ss.GetDecisions()
	if len(decisions) == 0 {
		return 1
	}
	var superseded int
	for _, d := range decisions {
		if d.Supersedes != "" {
			superseded++
		}
	}
	stability := 1 - float64(superseded)/float64(len(decisions))
	if stability < 0 {
		stability = 0
	}
	return stability
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// StateFingerprint produces a stable hash over the semantically load-bearing
// parts of a state. Two revisions with the same fingerprint are semantically
// identical, which is what makes two-cycle detection possible.
//
// Deliberately excluded: timestamps, revision counter, and file contents. Those
// change every iteration without implying the reasoning changed.
func StateFingerprint(ss *state.SemiState) string {
	if ss == nil {
		return ""
	}
	h := sha256.New()

	plan := ss.GetArchitecturePlan()
	if plan != nil {
		fmt.Fprintf(h, "plan:%s\n", plan.Description)
		fmt.Fprintf(h, "alt:%s\n", plan.AlternativeID)
		sorted := append([]state.ComponentSpec(nil), plan.Components...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
		for _, c := range sorted {
			fmt.Fprintf(h, "comp:%s|%s|%s\n", c.Name, c.Path, c.Description)
		}
		sortedEps := append([]state.EndpointSpec(nil), plan.Endpoints...)
		sort.Slice(sortedEps, func(i, j int) bool {
			if sortedEps[i].Path != sortedEps[j].Path {
				return sortedEps[i].Path < sortedEps[j].Path
			}
			return sortedEps[i].Method < sortedEps[j].Method
		})
		for _, e := range sortedEps {
			fmt.Fprintf(h, "ep:%s %s %s\n", e.Method, e.Path, e.Desc)
		}
	}

	decisions := ss.GetDecisions()
	sortedDec := append([]state.Decision(nil), decisions...)
	sort.Slice(sortedDec, func(i, j int) bool { return sortedDec[i].ID < sortedDec[j].ID })
	for _, d := range sortedDec {
		fmt.Fprintf(h, "dec:%s|%s|%s\n", d.ID, d.Question, d.Decision)
	}

	reqs := ss.GetRequirements()
	sortedReq := append([]state.Requirement(nil), reqs...)
	sort.Slice(sortedReq, func(i, j int) bool { return sortedReq[i].ID < sortedReq[j].ID })
	for _, r := range sortedReq {
		fmt.Fprintf(h, "req:%s|%s\n", r.ID, r.Content)
	}

	props := ss.GetProposals()
	paths := make([]string, 0, len(props))
	for _, p := range props {
		paths = append(paths, p.File+":"+string(p.Operation))
	}
	sort.Strings(paths)
	for _, p := range paths {
		fmt.Fprintf(h, "prop:%s\n", p)
	}

	// File paths matter; contents churn too much per iteration to be useful as a
	// stability signal.
	filePaths := make([]string, 0, len(ss.GeneratedFiles))
	for path := range ss.GeneratedFiles {
		filePaths = append(filePaths, path)
	}
	sort.Strings(filePaths)
	for _, p := range filePaths {
		fmt.Fprintf(h, "file:%s\n", p)
	}

	return hex.EncodeToString(h.Sum(nil))
}
