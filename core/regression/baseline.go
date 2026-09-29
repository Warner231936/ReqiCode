// Package regression implements the regression baseline that must exist before
// the system is allowed to modify its own source.
//
// The problem this solves is specific and severe. Self-modification means
// editing files that other units depend on. A test suite that asserts *current*
// behaviour does not catch a regression, because a regression is precisely a
// change to behaviour that the suite does not happen to cover. A self-edit that
// breaks an untested path passes silently, and the system never learns it did.
//
// The fix is to record what the system is supposed to produce, in a canonical
// form, and require any change to that output to be an explicit decision rather
// than a side effect.
package regression

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/core/state"
)

// Projection is the canonical, deterministic summary of a run that a baseline
// pins.
//
// Determinism is the whole design constraint. Wall-clock time, durations,
// revision counters that vary with scheduling order, and map iteration order are
// all excluded. What remains is what the system *believed* and *decided*, which
// is what a regression actually changes.
type Projection struct {
	SchemaVersion int      `json:"schema_version"`
	Name          string   `json:"name"`
	Confidence    float64  `json:"confidence"`
	Requirements  []string `json:"requirements"`
	Plan          *Plan    `json:"plan"`
	Files         []string `json:"files"`
	Decisions     []string `json:"decisions"`
	Hypotheses    []string `json:"hypotheses"`
	Proposals     []string `json:"proposals"`
	TestOutcomes  []string `json:"test_outcomes"`
	EvidenceKinds map[string]int `json:"evidence_kinds"`
	FindingStates map[string]int `json:"finding_states"`
	ConflictCount int      `json:"conflict_count"`
	ChainHead     string   `json:"chain_head"`
}

// Plan is the canonical architecture projection.
type Plan struct {
	Description string   `json:"description"`
	AltID       string   `json:"alt"`
	Components  []string `json:"components"`
	Endpoints   []string `json:"endpoints"`
}

// SchemaVersion is bumped whenever the projection shape changes, so a stale
// baseline is detected rather than silently mismatched.
const SchemaVersion = 1

// Project builds the canonical projection of a state.
//
// name identifies the scenario; it is part of the baseline so that a fixture
// is self-describing and cannot be swapped for another scenario's file.
func Project(name string, ss *state.SemiState) Projection {
	p := Projection{
		SchemaVersion: SchemaVersion,
		Name:          name,
		EvidenceKinds: map[string]int{},
		FindingStates: map[string]int{},
	}
	// Nil is checked before anything else touches the state. Projecting an
	// absent state is a legitimate call — it is how a gate records "nothing
	// produced" — and it must not panic.
	if ss == nil {
		p.ChainHead = state.GenesisHash
		return p
	}
	p.ChainHead = ss.ChainHead()

	p.Confidence = float64(ss.GetConfidence())

	for _, r := range ss.GetRequirements() {
		p.Requirements = append(p.Requirements, r.ID+"|"+r.Content)
	}
	sort.Strings(p.Requirements)

	if plan := ss.GetArchitecturePlan(); plan != nil {
		cp := &Plan{Description: plan.Description, AltID: plan.AlternativeID}
		for _, c := range plan.Components {
			cp.Components = append(cp.Components, c.Name+"|"+c.Path+"|"+c.Description)
		}
		sort.Strings(cp.Components)
		for _, e := range plan.Endpoints {
			cp.Endpoints = append(cp.Endpoints, e.Method+" "+e.Path)
		}
		sort.Strings(cp.Endpoints)
		p.Plan = cp
	}

	for f := range ss.GetGeneratedFiles() {
		p.Files = append(p.Files, f)
	}
	sort.Strings(p.Files)

	for _, d := range ss.GetDecisions() {
		p.Decisions = append(p.Decisions, d.ID+"|"+d.Decision)
	}
	sort.Strings(p.Decisions)

	for _, h := range ss.GetHypotheses() {
		p.Hypotheses = append(p.Hypotheses, h.ID+"|"+string(h.Status)+"|"+h.Content)
	}
	sort.Strings(p.Hypotheses)

	for _, pr := range ss.GetProposals() {
		p.Proposals = append(p.Proposals, pr.File+"|"+string(pr.Operation)+"|"+string(pr.Status))
	}
	sort.Strings(p.Proposals)

	for _, t := range ss.GetTestResults() {
		p.TestOutcomes = append(p.TestOutcomes, string(t.Status)+"|"+t.FailureClass)
	}
	sort.Strings(p.TestOutcomes)

	// Evidence is aggregated by kind rather than listed. A single added
	// evidence line should not read as a regression; a change in the *shape* of
	// what the system observed is what matters.
	for _, e := range ss.GetEvidence() {
		p.EvidenceKinds[string(e.Type)]++
	}
	for _, f := range ss.Findings {
		p.FindingStates[string(f.NewStatus)]++
	}
	p.ConflictCount = len(ss.Contradictions)

	return p
}

// Drift is one specific difference between expected and actual.
type Drift struct {
	Field    string   `json:"field"`
	Kind     string   `json:"kind"` // added | removed | changed
	Expected []string `json:"expected,omitempty"`
	Actual   []string `json:"actual,omitempty"`
	Detail   string   `json:"detail,omitempty"`
}

// Verdict is the outcome of a gate check.
type Verdict struct {
	Name     string  `json:"name"`
	Pass     bool    `json:"pass"`
	Drifts   []Drift `json:"drifts,omitempty"`
	Summary  string  `json:"summary"`
	Baseline string  `json:"baseline"`
}

// Gate compares live projections against pinned baselines.
type Gate struct {
	dir string
}

// NewGate creates a gate rooted at a baseline directory.
func NewGate(dir string) *Gate {
	return &Gate{dir: dir}
}

// Dir returns the baseline directory.
func (g *Gate) Dir() string { return g.dir }

func (g *Gate) path(name string) string {
	return filepath.Join(g.dir, name+".json")
}

// Path returns the file a scenario's baseline lives in, for reporting.
func (g *Gate) Path(name string) string {
	return g.path(name)
}

// Capture writes a projection as the pinned baseline, overwriting any previous
// one. Overwriting is a deliberate, reviewable act: it is how a behaviour
// change becomes the new expected behaviour, and it must be visible in version
// control rather than happening silently during a run.
func (g *Gate) Capture(p Projection) error {
	if err := os.MkdirAll(g.dir, 0o755); err != nil {
		return fmt.Errorf("create baseline dir: %w", err)
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal projection: %w", err)
	}
	if err := os.WriteFile(g.path(p.Name), data, 0o644); err != nil {
		return fmt.Errorf("write baseline: %w", err)
	}
	return nil
}

// Load reads a pinned baseline.
func (g *Gate) Load(name string) (Projection, error) {
	data, err := os.ReadFile(g.path(name))
	if err != nil {
		return Projection{}, fmt.Errorf("read baseline %q: %w", name, err)
	}
	var p Projection
	if err := json.Unmarshal(data, &p); err != nil {
		return Projection{}, fmt.Errorf("parse baseline %q: %w", name, err)
	}
	return p, nil
}

// Check compares actual against the pinned baseline.
//
// A missing baseline is a failure, not a pass. Defaulting to "no baseline, so
// nothing can be wrong" is how a gate silently stops gating.
func (g *Gate) Check(actual Projection) Verdict {
	v := Verdict{Name: actual.Name, Baseline: g.path(actual.Name)}

	expected, err := g.Load(actual.Name)
	if err != nil {
		v.Pass = false
		v.Summary = "FAIL: no pinned baseline (" + err.Error() + "); run baseline capture"
		return v
	}

	if expected.SchemaVersion != actual.SchemaVersion {
		v.Pass = false
		v.Summary = fmt.Sprintf("FAIL: baseline schema v%d, current v%d; re-capture",
			expected.SchemaVersion, actual.SchemaVersion)
		return v
	}

	v.Drifts = diff(expected, actual)
	v.Pass = len(v.Drifts) == 0
	if v.Pass {
		v.Summary = "PASS: no drift from baseline"
	} else {
		v.Summary = fmt.Sprintf("FAIL: %d drift(s) from baseline", len(v.Drifts))
	}
	return v
}

// CheckAll gates a set of projections in one call.
func (g *Gate) CheckAll(actuals []Projection) []Verdict {
	out := make([]Verdict, 0, len(actuals))
	for _, a := range actuals {
		out = append(out, g.Check(a))
	}
	return out
}

// diff produces precise, field-level drifts rather than a single boolean. A
// gate that only says "something changed" is not actionable; a gate that says
// "Decisions lost 2 entries and TestOutcomes gained a failure" is.
func diff(expected, actual Projection) []Drift {
	var drifts []Drift

	drifts = append(drifts, diffStrings("requirements", expected.Requirements, actual.Requirements)...)
	drifts = append(drifts, diffStrings("files", expected.Files, actual.Files)...)
	drifts = append(drifts, diffStrings("decisions", expected.Decisions, actual.Decisions)...)
	drifts = append(drifts, diffStrings("hypotheses", expected.Hypotheses, actual.Hypotheses)...)
	drifts = append(drifts, diffStrings("proposals", expected.Proposals, actual.Proposals)...)
	drifts = append(drifts, diffStrings("test_outcomes", expected.TestOutcomes, actual.TestOutcomes)...)
	drifts = append(drifts, diffCounts("evidence_kinds", expected.EvidenceKinds, actual.EvidenceKinds)...)

	if expected.ConflictCount != actual.ConflictCount {
		drifts = append(drifts, Drift{
			Field:  "conflict_count",
			Kind:   "changed",
			Detail: fmt.Sprintf("expected %d, got %d", expected.ConflictCount, actual.ConflictCount),
		})
	}

	if expected.Plan == nil && actual.Plan != nil {
		drifts = append(drifts, Drift{Field: "plan", Kind: "added", Detail: "baseline had no architecture plan"})
	} else if expected.Plan != nil && actual.Plan == nil {
		drifts = append(drifts, Drift{Field: "plan", Kind: "removed", Detail: "architecture plan is missing"})
	} else if expected.Plan != nil && actual.Plan != nil {
		drifts = append(drifts, diffStrings("plan.components", expected.Plan.Components, actual.Plan.Components)...)
		drifts = append(drifts, diffStrings("plan.endpoints", expected.Plan.Endpoints, actual.Plan.Endpoints)...)
		if expected.Plan.Description != actual.Plan.Description {
			drifts = append(drifts, Drift{
				Field:  "plan.description",
				Kind:   "changed",
				Detail: fmt.Sprintf("expected %q, got %q", expected.Plan.Description, actual.Plan.Description),
			})
		}
	}

	return drifts
}

func diffStrings(field string, expected, actual []string) []Drift {
	if equalStrings(expected, actual) {
		return nil
	}
	expSet := toSet(expected)
	actSet := toSet(actual)

	var added, removed []string
	for _, a := range actual {
		if !expSet[a] {
			added = append(added, a)
		}
	}
	for _, e := range expected {
		if !actSet[e] {
			removed = append(removed, e)
		}
	}

	out := make([]Drift, 0, 2)
	if len(removed) > 0 {
		out = append(out, Drift{Field: field, Kind: "removed", Expected: removed})
	}
	if len(added) > 0 {
		out = append(out, Drift{Field: field, Kind: "added", Actual: added})
	}
	if len(added) == 0 && len(removed) == 0 {
		// Same members, different order after sorting means equal, so this
		// indicates identical content; kept as a defensive branch.
		out = append(out, Drift{Field: field, Kind: "changed", Detail: "set membership differs"})
	}
	return out
}

func diffCounts(field string, expected, actual map[string]int) []Drift {
	keys := map[string]bool{}
	for k := range expected {
		keys[k] = true
	}
	for k := range actual {
		keys[k] = true
	}
	names := make([]string, 0, len(keys))
	for k := range keys {
		names = append(names, k)
	}
	sort.Strings(names)

	var out []Drift
	for _, k := range names {
		if expected[k] != actual[k] {
			out = append(out, Drift{
				Field:  field + "." + k,
				Kind:   "changed",
				Detail: fmt.Sprintf("expected %d, got %d", expected[k], actual[k]),
			})
		}
	}
	return out
}

func toSet(in []string) map[string]bool {
	m := make(map[string]bool, len(in))
	for _, s := range in {
		m[s] = true
	}
	return m
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// FormatVerdicts renders verdicts for a terminal, one drift per line so the
// reader can see exactly what moved rather than parsing a count.
func FormatVerdicts(vs []Verdict) string {
	var b strings.Builder
	for _, v := range vs {
		fmt.Fprintf(&b, "%s: %s\n", v.Name, v.Summary)
		for _, d := range v.Drifts {
			switch {
			case len(d.Actual) > 0:
				for _, a := range d.Actual {
					fmt.Fprintf(&b, "    + %s: %s\n", d.Field, a)
				}
			case len(d.Expected) > 0:
				for _, e := range d.Expected {
					fmt.Fprintf(&b, "    - %s: %s\n", d.Field, e)
				}
			default:
				fmt.Fprintf(&b, "    ~ %s: %s\n", d.Field, d.Detail)
			}
		}
	}
	return b.String()
}

// AllPass reports whether every verdict passed.
func AllPass(vs []Verdict) bool {
	for _, v := range vs {
		if !v.Pass {
			return false
		}
	}
	return len(vs) > 0
}
