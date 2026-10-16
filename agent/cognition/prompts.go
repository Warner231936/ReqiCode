package cognition

import (
	gocontext "context"
	"encoding/json"
	"fmt"
	"strings"

	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/tools"
)

// Prompter builds the messages for each kind of model call.
//
// Prompt construction is separated from the loop so the prompt can be inspected,
// logged, and tested without running a task. When a run misbehaves, the first
// question is always "what was it actually told", and that has to be answerable.
type Prompter struct {
	Mode      Mode
	Brief     *agentcontext.Brief
	Catalogue string
	Workspace string
	Extra     []llm.Message
}

// System renders the system prompt.
//
// The three rules at the end are stated unconditionally, in every mode: a model
// that has just been shown a repository and asked to change it will produce
// confident prose about code it has not read unless told otherwise.
func (p *Prompter) System() string {
	spec := p.Mode.Spec()
	var b strings.Builder

	b.WriteString("You are a local-first autonomous coding agent operating on a real source repository.\n")
	b.WriteString("You act by calling tools. You do not have a shell of your own beyond the tools listed.\n\n")

	b.WriteString(spec.Guidance)
	b.WriteString("\n\n")

	b.WriteString("## Repository map\n")
	if p.Workspace != "" {
		fmt.Fprintf(&b, "workspace: %s\n", p.Workspace)
	}

	b.WriteString("\n## Tools available to you in this mode\n")
	b.WriteString(p.Catalogue)
	b.WriteString("\n\n")

	b.WriteString("## Rules\n")
	b.WriteString("1. Reply with exactly ONE JSON object and nothing else. No prose before or after it.\n")
	b.WriteString("2. The object must have an \"action\" field, one of: \"tool\", \"finish\", \"fail\", \"ask\".\n")
	b.WriteString("3. To use a tool: {\"thought\":\"why\",\"action\":\"tool\",\"tool\":\"NAME\",\"args\":{...}}\n")
	b.WriteString("4. To end successfully: {\"thought\":\"why\",\"action\":\"finish\",\"summary\":\"what you did\",\"evidence\":[...]}\n")
	b.WriteString("5. To give up: {\"thought\":\"why\",\"action\":\"fail\",\"reason\":\"what blocked you\"}\n")
	b.WriteString("6. Read a file before editing it. Never guess a file's contents.\n")
	b.WriteString("7. Never claim a change works without running build_project or run_tests.\n")
	b.WriteString("8. One tool call per reply. You will be shown the result and asked again.\n")

	if p.Brief != nil && p.Brief.Dropped > 0 {
		fmt.Fprintf(&b, "\nNOTE: %d retrieved code chunk(s) did not fit the context budget. If what you need is not shown, search for it with search_code or find_symbol rather than assuming it does not exist.\n", p.Brief.Dropped)
	}
	return b.String()
}

// User renders the task prompt.
func (p *Prompter) User(request string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Task\n%s\n\n", strings.TrimSpace(request))

	if p.Brief != nil {
		if p.Brief.Render() != "" {
			b.WriteString(p.Brief.Render())
			b.WriteString("\n")
		}
	}

	b.WriteString("## What to do now\n")
	switch p.Mode {
	case PlanMode:
		b.WriteString("Produce a plan. Reply with {\"thought\":...,\"action\":\"plan\",\"plan\":[{\"description\":...,\"files\":[...],\"verify\":...}]}.\n")
	case Review:
		b.WriteString("Review the current changes. Start by inspecting the diff and the changed files, then reply with your verdict.\n")
	case Explore, Explain:
		b.WriteString("Investigate. Use search_code, find_symbol, read_file, and git_log. Reply with finish and a summary of what you found, citing files.\n")
	default:
		b.WriteString("Decide the single most useful next action. Reply with one JSON object.\n")
	}
	return b.String()
}

// Messages assembles the full message list.
func (p *Prompter) Messages(request string) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: p.System()}}
	msgs = append(msgs, p.Extra...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: p.User(request)})
	return msgs
}

// RepairMessages assembles a repair turn after a verification failure.
func (p *Prompter) RepairMessages(request, failureBrief, previousAttempts string) []llm.Message {
	msgs := []llm.Message{{Role: llm.RoleSystem, Content: p.System()}}
	msgs = append(msgs, p.Extra...)
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: p.User(request)})
	msgs = append(msgs, llm.Message{Role: llm.RoleAssistant,
		Content: `{"thought":"attempting an implementation","action":"tool","tool":"read_file","args":{"path":"."}}`})
	msgs = append(msgs, llm.Message{Role: llm.RoleUser, Content: failureBrief})
	if previousAttempts != "" {
		msgs = append(msgs, llm.Message{Role: llm.RoleUser,
			Content: "## What you already tried this round\n" + previousAttempts +
				"\n\nChoose a different approach. Do not repeat a failed step."})
	}
	msgs = append(msgs, llm.Message{Role: llm.RoleUser,
		Content: "Reply with one JSON object describing the single next action. If you cannot fix it, reply with action \"fail\" and explain precisely what blocks you."})
	return msgs
}

// PlanOnly asks for a plan and nothing else.
//
// The planner is a separate call from the executor because a plan produced as a
// side effect of doing the work is a rationalisation of whatever was already done,
// not a plan.
func PlanOnly(mode Mode, catalogue, workspace, request string, brief *agentcontext.Brief) []llm.Message {
	p := &Prompter{Mode: mode, Brief: brief, Catalogue: catalogue, Workspace: workspace}
	return []llm.Message{
		{Role: llm.RoleSystem, Content: p.System()},
		{Role: llm.RoleUser, Content: p.User(request)},
	}
}

// ReviewPrompt asks a model to critique a change.
//
// The questions are fixed and adversarial on purpose. An open-ended "any issues?"
// prompt is answered with "looks good" by almost every model, including ones that
// would have caught a real bug if asked directly.
func ReviewPrompt(request string, diff, changedFiles, verification, priorFindings string) (system, user string) {
	system = `You are an adversarial code reviewer. Your job is to find reasons this change is wrong, not to approve it.
You are given the request, the diff, the changed files, and the verification output.
Report only defects you can point at in the provided text. Do not speculate about code you were not shown.
A review that finds nothing and cites nothing is treated as a failed review.`

	var b strings.Builder
	b.WriteString("## Request that was implemented\n")
	b.WriteString(strings.TrimSpace(request))
	b.WriteString("\n\n## Files changed\n")
	b.WriteString(changedFiles)
	b.WriteString("\n\n## Diff\n```diff\n")
	b.WriteString(diff)
	b.WriteString("\n```\n\n## Verification output\n")
	b.WriteString(verification)
	b.WriteString("\n\n## Answer each of these\n")
	for i, q := range reviewQuestions {
		fmt.Fprintf(&b, "%d. %s\n", i+1, q)
	}
	if priorFindings != "" {
		b.WriteString("\n## Findings from an earlier review round\n")
		b.WriteString(priorFindings)
		b.WriteString("\nConfirm each was fixed, or state that it was not.\n")
	}
	b.WriteString("\nReply with exactly one JSON object:\n")
	b.WriteString(`{"thought":"your overall assessment","verdict":"accept|reject",` +
		`"findings":[{"severity":"critical|high|medium|low","file":"path","line":123,"problem":"what is wrong","fix":"what to change"}],` +
		`"summary":"one paragraph"}`)
	b.WriteString("\nReject when there is any critical or high finding, when the change does not solve the request, ")
	b.WriteString("or when verification did not pass and the reason is unaddressed.\n")
	return system, b.String()
}

var reviewQuestions = []string{
	"Does the change actually solve the request, or something adjacent to it?",
	"Which files were modified that the request did not require, and why?",
	"What behaviour that previously worked could this change break?",
	"What happens at the boundary conditions: empty input, zero, nil, maximum size, concurrent access?",
	"Is any test missing that would have caught the most likely future regression?",
	"Is there code duplicated from elsewhere in the repository rather than reused?",
	"Is there an obvious security, data-loss, or correctness problem in the diff?",
	"Does the change fit the existing architecture, or introduce a parallel one?",
}

// SummarizePrompt asks for a short task-level summary.
func SummarizePrompt(request, transcriptDigest, verification string) (system, user string) {
	system = `You summarise a completed coding task for the person who requested it.
Report what changed, what was verified, and what remains uncertain. Do not claim more than the evidence supports.`
	user = fmt.Sprintf(`## Request
%s

## What the agent did
%s

## Verification
%s

Reply with one JSON object:
{"summary":"three sentences or fewer","files_changed":["a/b.go"],"verified":true|false,"open_issues":["..."],"confidence":0.0-1.0}`,
		strings.TrimSpace(request), transcriptDigest, verification)
	return system, user
}

// ParseReview decodes a reviewer response.
func ParseReview(text string) (ReviewResult, error) {
	raw, err := llm.ExtractObject(text)
	if err != nil {
		return ReviewResult{}, err
	}
	var out ReviewResult
	for _, k := range []string{"verdict", "decision", "result", "status"} {
		if v := strings.ToLower(strings.TrimSpace(toString(raw[k]))); v != "" {
			out.Verdict = normaliseVerdict(v)
			break
		}
	}
	for _, k := range []string{"summary", "assessment", "thought", "reasoning"} {
		if v := toString(raw[k]); v != "" {
			out.Summary = v
			break
		}
	}
	for _, k := range []string{"findings", "issues", "problems", "concerns", "comments"} {
		list, ok := raw[k].([]any)
		if !ok {
			continue
		}
		for _, item := range list {
			m := toMap(item)
			f := Finding{
				Severity: strings.ToLower(strings.TrimSpace(toString(m["severity"]))),
				File:     toString(m["file"]),
				Line:     int(toFloat(m["line"])),
				Problem:  toString(m["problem"]),
				Fix:      toString(m["fix"]),
			}
			if f.Problem == "" {
				for _, alt := range []string{"issue", "message", "description", "text", "what"} {
					if v := toString(m[alt]); v != "" {
						f.Problem = v
						break
					}
				}
			}
			if f.Problem == "" {
				continue
			}
			if f.Severity == "" {
				f.Severity = "medium"
			}
			out.Findings = append(out.Findings, f)
		}
		break
	}

	// A reviewer that lists critical findings but says "accept" is being
	// inconsistent, and believing the verdict would defeat the point of the
	// stage. The findings win.
	if out.Verdict == "" {
		out.Verdict = "reject"
		if len(out.Findings) == 0 {
			out.Verdict = "reject"
		}
	}
	for _, f := range out.Findings {
		if f.Severity == "critical" || f.Severity == "high" {
			if out.Verdict == "accept" {
				out.Verdict = "reject"
				out.Summary += " (verdict downgraded: the review lists a blocking finding)"
			}
			break
		}
	}
	return out, nil
}

func normaliseVerdict(v string) string {
	switch v {
	case "accept", "approved", "approve", "lgtm", "ok", "pass", "ship":
		return "accept"
	case "reject", "rejected", "block", "blocked", "fail", "needs-work", "changes-requested":
		return "reject"
	default:
		return v
	}
}

// ReviewResult is a reviewer's verdict.
type ReviewResult struct {
	Verdict  string    `json:"verdict"`
	Summary  string    `json:"summary"`
	Findings []Finding `json:"findings"`
}

// Accepted reports whether the review approved the change.
func (r ReviewResult) Accepted() bool { return strings.EqualFold(r.Verdict, "accept") }

// Blocking returns the findings serious enough to require a fix.
func (r ReviewResult) Blocking() []Finding {
	var out []Finding
	for _, f := range r.Findings {
		if f.Severity == "critical" || f.Severity == "high" {
			out = append(out, f)
		}
	}
	return out
}

// Finding is one review finding.
type Finding struct {
	Severity string `json:"severity"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	Problem  string `json:"problem"`
	Fix      string `json:"fix,omitempty"`
}

// Render formats findings for the repair prompt.
func (r ReviewResult) Render() string {
	var b strings.Builder
	fmt.Fprintf(&b, "verdict: %s\n", r.Verdict)
	if r.Summary != "" {
		fmt.Fprintf(&b, "summary: %s\n", r.Summary)
	}
	if len(r.Findings) == 0 {
		b.WriteString("findings: none reported\n")
		return b.String()
	}
	b.WriteString("findings:\n")
	for i, f := range r.Findings {
		fmt.Fprintf(&b, "%d. [%s] %s\n", i+1, f.Severity, f.Problem)
		if f.File != "" {
			if f.Line > 0 {
				fmt.Fprintf(&b, "   at %s:%d\n", f.File, f.Line)
			} else {
				fmt.Fprintf(&b, "   at %s\n", f.File)
			}
		}
		if f.Fix != "" {
			fmt.Fprintf(&b, "   suggested fix: %s\n", f.Fix)
		}
	}
	return b.String()
}

// RenderFindings formats findings without the verdict, for a second round.
func RenderFindings(findings []Finding) string {
	if len(findings) == 0 {
		return ""
	}
	var b strings.Builder
	for i, f := range findings {
		fmt.Fprintf(&b, "%d. [%s] %s", i+1, f.Severity, f.Problem)
		if f.File != "" {
			fmt.Fprintf(&b, " (%s", f.File)
			if f.Line > 0 {
				fmt.Fprintf(&b, ":%d", f.Line)
			}
			b.WriteString(")")
		}
		b.WriteString("\n")
		if f.Fix != "" {
			fmt.Fprintf(&b, "   fix: %s\n", f.Fix)
		}
	}
	return b.String()
}

// transcriptDigest renders recent tool activity compactly.
func transcriptDigest(events []Event) string {
	if len(events) == 0 {
		return "(no tool calls recorded)"
	}
	var b strings.Builder
	for _, e := range events {
		switch e.Kind {
		case EventTool:
			mark := "ok"
			if !e.Success {
				mark = "FAILED"
			}
			fmt.Fprintf(&b, "- %s %s (%s)\n", e.Name, mark, truncate(e.Summary, 160))
		case EventFailure:
			fmt.Fprintf(&b, "- failure: %s\n", truncate(e.Summary, 200))
		}
	}
	return b.String()
}

// Event is one recorded action in a transcript.
type Event struct {
	Kind    EventKind
	Name    string
	Args    map[string]any
	Success bool
	Summary string
	Result  string
}

// EventKind classifies a transcript event.
type EventKind string

const (
	EventTool    EventKind = "tool"
	EventFailure EventKind = "failure"
	EventModel   EventKind = "model"
)

// Truncate shortens a string.
func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// JSONDump renders a value as indented JSON for logging.
func JSONDump(v any) string {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", v)
	}
	return string(data)
}

// toolCatalogue renders the tool list for a mode.
func toolCatalogue(reg *tools.Registry, mode Mode) string {
	return reg.RenderCatalogue(string(mode))
}

// contextFor builds a retrieval brief, tolerating failure.
//
// A retrieval failure is reported and the loop continues with less context, because
// a task that proceeds on partial context is recoverable and a task that refuses to
// start is not.
func contextFor(ctx gocontext.Context, m *agentcontext.Manager, request string, opts agentcontext.BriefOptions) (*agentcontext.Brief, error) {
	if m == nil {
		return &agentcontext.Brief{Request: request}, nil
	}
	return m.Build(ctx, request, opts)
}