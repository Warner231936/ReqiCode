// Package cognition is the agent's reasoning layer: modes, the prompt that frames
// a decision, the decision protocol the model must follow, the planner, the
// reviewer, and the loop that ties them together.
//
// Reasoning is kept separate from tool execution on purpose. The model produces a
// decision, this package decides whether that decision is well-formed and
// permitted, and the tool layer performs it. A model that both reasons and acts
// cannot be constrained, audited, or retried, because the reasoning and the side
// effect arrive together.
package cognition

import (
	"fmt"
	"sort"
	"strings"

	"github.com/kilo/spiral-codemaker/agent/tools"
)

// Mode is an operating mode with its own tool allow-list and objective.
type Mode string

const (
	Explore   Mode = tools.ModeExplore
	// PlanMode is the planning mode. It is not named Plan because that name
	// belongs to the plan artefact type, and a mode constant shadowing an artefact
	// type makes every signature that mentions either ambiguous.
	PlanMode  Mode = tools.ModePlan
	Implement Mode = tools.ModeImplement
	Debug     Mode = tools.ModeDebug
	Test      Mode = tools.ModeTest
	Review    Mode = tools.ModeReview
	Refactor  Mode = tools.ModeRefactor
	Explain   Mode = tools.ModeExplain
)

// AllModes lists every mode.
var AllModes = []Mode{Explore, PlanMode, Implement, Debug, Test, Review, Refactor, Explain}

// ParseMode validates a mode name.
func ParseMode(s string) (Mode, error) {
	m := Mode(strings.ToLower(strings.TrimSpace(s)))
	if m == "" {
		return Explore, nil
	}
	for _, known := range AllModes {
		if known == m {
			return known, nil
		}
	}
	names := make([]string, 0, len(AllModes))
	for _, known := range AllModes {
		names = append(names, string(known))
	}
	return "", fmt.Errorf("unknown mode %q; valid modes: %s", s, strings.Join(names, ", "))
}

// ModeSpec describes a mode's intent and constraints.
type ModeSpec struct {
	Name Mode `json:"name"`
	// Objective is one sentence telling the model what success means.
	Objective string `json:"objective"`
	// MustProduce names what a completed step looks like for this mode.
	MustProduce string `json:"must_produce"`
	// RequiresVerification says whether success must be backed by evidence.
	RequiresVerification bool `json:"requires_verification"`
	// AllowsChanges says whether the mode may modify the workspace.
	AllowsChanges bool `json:"allows_changes"`
	// Guidance is added to the system prompt for this mode.
	Guidance string `json:"guidance"`
}

// specs is the mode table. The guidance is what makes modes more than labels: an
// EXPLORE run and a REVIEW run see different tools, but they also see different
// instructions, and a model told "review this" will produce a critique while a
// model told "explain this" will produce prose.
var specs = map[Mode]ModeSpec{
	Explore: {
		Name:        Explore,
		Objective:   "Locate the code and facts relevant to the request without changing anything.",
		MustProduce: "A list of relevant file paths with a one-line reason for each, plus what you could not determine.",
		Guidance: `You are in EXPLORE mode. You may read, search, list, and inspect git history.
You must not modify any file. Do not propose edits; locate and report instead.
Search before you conclude. A claim about what the code does must cite a file and line.`,
	},
	PlanMode: {
		Name:        PlanMode,
		Objective:   "Produce an ordered, verifiable plan for the requested change.",
		MustProduce: "Numbered steps, each naming the file to change and how success will be checked.",
		Guidance: `You are in PLAN mode. Produce a plan; do not apply it.
Each step must name the files it touches and how it will be verified.
Prefer the smallest plan that fully solves the request. Identify the verification command early.`,
	},
	Implement: {
		Name:                  Implement,
		Objective:             "Make the change the request describes, verified by the project's own build and tests.",
		MustProduce:           "Edits applied and a passing verification run, or an explicit statement of what could not be achieved and why.",
		AllowsChanges:         true,
		RequiresVerification:  true,
		Guidance: `You are in IMPLEMENT mode. Apply the change with the edit tools.
Read a file before you edit it, and use edit_file with the exact current text or apply_patch with a unified diff.
Never claim success without running the build or tests. If verification fails, read the actual error and fix the cause.
Do not modify files unrelated to the request.`,
	},
	Debug: {
		Name:                  Debug,
		Objective:             "Find the root cause of a failure and fix it.",
		MustProduce:           "A stated root cause supported by evidence, plus a fix whose verification passes.",
		AllowsChanges:         true,
		RequiresVerification:  true,
		Guidance: `You are in DEBUG mode. Start from the actual failure output, not from a guess.
Reproduce the failure first. Identify the specific line or value that causes it.
State the root cause explicitly before you fix it, so it can be checked.
Do not change behaviour that the failure output does not implicate.`,
	},
	Test: {
		Name:        Test,
		Objective:   "Run the project's tests and report what they say.",
		MustProduce: "Test results with the names of failing tests and their assertions.",
		Guidance: `You are in TEST mode. Run the detected test command and report the result.
Quote the failing assertion, not a summary. Do not change code to make tests pass
unless the test is demonstrably wrong.`,
	},
	Review: {
		Name:        Review,
		Objective:   "Critique the current changes and decide whether they should be accepted.",
		MustProduce: "A verdict (accept or reject) with specific, actionable findings.",
		Guidance: `You are in REVIEW mode. Look for reasons the change is wrong.
Check: does it solve the request; were unrelated files touched; what regressed;
what breaks at boundaries; is the test coverage sufficient; is anything duplicated;
are there obvious security problems; does it fit the existing architecture?
Name specific files and lines. An empty finding list means you did not look.`,
	},
	Refactor: {
		Name:                  Refactor,
		Objective:             "Restructure code without changing behaviour, verified by tests.",
		MustProduce:           "A restructured tree whose tests still pass.",
		AllowsChanges:         true,
		RequiresVerification:  true,
		Guidance: `You are in REFACTOR mode. Behaviour must not change.
Run the tests before you start and record the result. After each step, run them again.
If a test fails after a refactor, the refactor is wrong; revert rather than "fix" the test.`,
	},
	Explain: {
		Name:        Explain,
		Objective:   "Explain how something works, citing the code.",
		MustProduce: "An explanation grounded in specific files and lines.",
		Guidance: `You are in EXPLAIN mode. Do not modify anything.
Cite files and lines for every claim. Explain the actual code, not the intended design.`,
	},
}

// Spec returns the spec for a mode.
func (m Mode) Spec() ModeSpec {
	if s, ok := specs[m]; ok {
		return s
	}
	return specs[Explore]
}

// ModeTable renders the mode table for documentation and `agent modes`.
func ModeTable() string {
	order := []Mode{Explore, PlanMode, Implement, Debug, Test, Review, Refactor, Explain}
	var b strings.Builder
	b.WriteString("MODE      CHANGES  VERIFY  OBJECTIVE\n")
	for _, m := range order {
		s := specs[m]
		changes := "no"
		if s.AllowsChanges {
			changes = "yes"
		}
		verify := "no"
		if s.RequiresVerification {
			verify = "yes"
		}
		fmt.Fprintf(&b, "%-9s %-7s %-7s %s\n", s.Name, changes, verify, s.Objective)
	}
	return b.String()
}

// ToolSummary lists the tools a mode may call.
func ToolSummary(reg *tools.Registry, mode Mode) []string {
	var out []string
	for _, t := range reg.ForMode(string(mode)) {
		marker := ""
		if t.Mutates() {
			marker = "*"
		}
		out = append(out, t.Name()+marker)
	}
	sort.Strings(out)
	return out
}