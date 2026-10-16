package cognition

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/tools"
)

// Action is what the model decided to do next.
type Action string

const (
	// ActionTool runs a tool and continues the loop.
	ActionTool Action = "tool"
	// ActionFinish ends the step successfully.
	ActionFinish Action = "finish"
	// ActionFail ends the step as a failure, with a stated reason.
	ActionFail Action = "fail"
	// ActionPlan emits a plan instead of acting.
	ActionPlan Action = "plan"
	// ActionAsk surfaces a question the agent cannot answer alone.
	ActionAsk Action = "ask"
)

// Decision is one model turn.
type Decision struct {
	Thought string         `json:"thought"`
	Action  Action         `json:"action"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Summary string         `json:"summary,omitempty"`
	Reason  string         `json:"reason,omitempty"`
	// Evidence lists the observations that justify a finish.
	Evidence []string `json:"evidence,omitempty"`
	// Confidence is the model's own estimate, recorded but never trusted blindly.
	Confidence float64 `json:"confidence,omitempty"`
	// Plan is populated for ActionPlan.
	Plan []PlanStep `json:"plan,omitempty"`
	// Raw preserves the original text for the log when parsing was lossy.
	Raw string `json:"-"`
	// ParseError explains a decoding failure so the loop can report it precisely.
	ParseError string `json:"-"`
	// Duration is how long the model took.
	Duration time.Duration `json:"-"`
	// Model and Backend record which model produced this decision.
	Model   string `json:"model,omitempty"`
	Backend string `json:"backend,omitempty"`
}

// PlanStep is one planned step.
//
// Raw preserves the original JSON object so a step can carry a concrete edit —
// content, old_text/new_text, or a diff — under whatever key the author used,
// without the step type having to enumerate every possible spelling.
type PlanStep struct {
	ID          int      `json:"id"`
	Description string   `json:"description"`
	Files       []string `json:"files,omitempty"`
	Tool        string   `json:"tool,omitempty"`
	Verify      string   `json:"verify,omitempty"`
	Rationale   string   `json:"rationale,omitempty"`

	// Raw is the decoded step object. Not serialised, because it duplicates the
	// typed fields above.
	Raw map[string]any `json:"-"`
}

// raw returns the step's original fields, never nil.
func (s PlanStep) raw() map[string]any {
	if s.Raw != nil {
		return s.Raw
	}
	return map[string]any{}
}

// Valid reports whether a tool name is real and permitted in this mode.
func (d Decision) Valid(reg *tools.Registry, mode Mode) error {
	if d.Thought == "" {
		return fmt.Errorf("decision has no thought field")
	}
	switch d.Action {
	case ActionTool:
		t, ok := reg.Get(d.Tool)
		if !ok {
			return fmt.Errorf("unknown tool %q; available: %s", d.Tool, strings.Join(reg.Names(), ", "))
		}
		permitted := false
		for _, m := range reg.ForMode(string(mode)) {
			if m.Name() == t.Name() {
				permitted = true
				break
			}
		}
		if !permitted {
			return fmt.Errorf("tool %q is not permitted in %s mode; permitted tools: %s",
				d.Tool, mode, strings.Join(ToolSummary(reg, mode), ", "))
		}
		if t.Mutates() && !mode.Spec().AllowsChanges {
			return fmt.Errorf("tool %q modifies the workspace and %s mode does not allow changes", d.Tool, mode)
		}
		// A required argument that is missing would otherwise be discovered as a
		// tool error one round trip later, which wastes a model call to learn
		// something the schema already said.
		for _, p := range t.Params() {
			if p.Required {
				if _, ok := d.Args[p.Name]; !ok {
					return fmt.Errorf("tool %q requires argument %q (%s)", d.Tool, p.Name, p.Description)
				}
			}
		}
		return nil
	case ActionFinish:
		if d.Summary == "" {
			return fmt.Errorf("finish requires a summary of what was done")
		}
		return nil
	case ActionFail:
		if d.Reason == "" {
			return fmt.Errorf("fail requires a reason")
		}
		return nil
	case ActionPlan:
		if len(d.Plan) == 0 {
			return fmt.Errorf("plan requires at least one step")
		}
		return nil
	case ActionAsk:
		if d.Reason == "" {
			return fmt.Errorf("ask requires the question")
		}
		return nil
	default:
		return fmt.Errorf("unknown action %q; use one of: tool, finish, fail, plan, ask", d.Action)
	}
}

// ParseDecision decodes a model turn.
//
// The parser is lenient about shape and strict about semantics: the JSON envelope
// is recovered aggressively, then the resulting decision is validated against the
// registry and the mode. Recovering the envelope is worth the leniency because a
// small local model wraps JSON in prose constantly, and rejecting the whole turn
// for a missing code fence wastes the round trip; the semantic check is where
// strictness pays, because an unknown tool or a missing required argument is a
// real error the loop should not paper over.
func ParseDecision(text string) Decision {
	d, err := parseStrict(text)
	if err == nil {
		d.Raw = text
		return d
	}
	d.Raw = text
	d.ParseError = err.Error()

	// Recovery: the model may have written a sentence naming the tool and its
	// arguments rather than a JSON object. A single well-formed tool call is
	// recoverable; anything more ambiguous is not, and guessing would be worse
	// than reporting that the output could not be parsed.
	if fallback := recoverToolCall(text); fallback != nil {
		fallback.Raw = text
		fallback.ParseError = err.Error()
		return *fallback
	}
	return d
}

func parseStrict(text string) (Decision, error) {
	raw, err := llm.ExtractObject(text)
	if err != nil {
		return Decision{}, err
	}
	var d Decision
	if err := decodeFlexible(raw, &d); err != nil {
		return Decision{}, err
	}
	normaliseAction(&d)
	return d, nil
}

// decodeFlexible accepts the common key aliases.
//
// Models rename keys constantly. Accepting a documented set of aliases is what
// stops a correct decision from being discarded over "tool_name" versus "tool".
func decodeFlexible(raw map[string]any, d *Decision) error {
	pick := func(keys ...string) (any, bool) {
		for _, k := range keys {
			if v, ok := raw[k]; ok {
				return v, true
			}
		}
		return nil, false
	}

	if v, ok := pick("thought", "reasoning", "thinking", "analysis", "thoughts"); ok {
		d.Thought = toString(v)
	}
	if v, ok := pick("action", "type", "next", "kind"); ok {
		d.Action = Action(strings.ToLower(strings.TrimSpace(toString(v))))
	}
	if v, ok := pick("tool", "tool_name", "function", "name", "command"); ok {
		d.Tool = strings.TrimSpace(toString(v))
	}
	if v, ok := pick("args", "arguments", "parameters", "params", "input", "with"); ok {
		d.Args = toMap(v)
	}
	if v, ok := pick("summary", "result", "answer", "done", "message"); ok {
		d.Summary = toString(v)
	}
	if v, ok := pick("reason", "why", "error", "obstacle"); ok {
		d.Reason = toString(v)
	}
	if v, ok := pick("evidence", "proof", "observations", "basis"); ok {
		d.Evidence = toStringSlice(v)
	}
	if v, ok := pick("confidence", "certainty"); ok {
		d.Confidence = toFloat(v)
	}
	if v, ok := pick("plan", "steps"); ok {
		d.Plan = toPlan(v)
	}

	// A nested tool-call envelope, as OpenAI-shaped outputs produce.
	if inner, ok := pick("tool_call", "function_call", "toolCall"); ok {
		if m := toMap(inner); m != nil {
			if name := toString(m["name"]); name != "" {
				d.Tool = name
			}
			if a := toMap(m["arguments"]); a != nil {
				d.Args = a
			}
			if d.Action == "" {
				d.Action = ActionTool
			}
		}
	}
	return nil
}

func normaliseAction(d *Decision) {
	switch d.Action {
	case "", ActionTool:
		if d.Tool != "" {
			d.Action = ActionTool
		}
	case "done", "complete", "completed", "success", "finished":
		d.Action = ActionFinish
	case "give_up", "giveup", "stop", "abort", "blocked":
		d.Action = ActionFail
	case "question", "ask_user", "request":
		d.Action = ActionAsk
	}
}

func recoverToolCall(text string) *Decision {
	lower := strings.ToLower(text)
	toolName := ""
	for _, candidate := range knownToolNames {
		if strings.Contains(lower, candidate) {
			toolName = candidate
			break
		}
	}
	if toolName == "" {
		return nil
	}
	args := extractInlineArgs(text, toolName)
	if args == nil {
		args = map[string]any{}
	}
	return &Decision{
		Thought: strings.TrimSpace(firstSentence(text)),
		Action:  ActionTool,
		Tool:    toolName,
		Args:    args,
		Summary: "recovered from unstructured output",
	}
}

// knownToolNames is the set recoverToolCall will match against, populated at
// registry construction.
var knownToolNames []string

// SetKnownTools records the registered tool names for output recovery.
func SetKnownTools(names []string) { knownToolNames = append([]string{}, names...) }

// extractInlineArgs pulls "path" followed by a quoted value out of prose.
//
// Only the quoted form is accepted. Guessing an argument from an unquoted run of
// text would silently produce a path that does not exist, and the tool would then
// fail with a confusing error instead of the model learning that its output format
// was wrong.
func extractInlineArgs(text, tool string) map[string]any {
	idx := strings.Index(strings.ToLower(text), strings.ToLower(tool))
	if idx < 0 {
		return nil
	}
	tail := text[idx+len(tool):]
	if len(tail) > 800 {
		tail = tail[:800]
	}
	out := map[string]any{}
	for _, key := range []string{"path", "file", "query", "symbol", "command", "content", "new_text", "old_text"} {
		pat := key
		i := strings.Index(strings.ToLower(tail), pat)
		if i < 0 {
			continue
		}
		rest := tail[i+len(pat):]
		rest = strings.TrimLeft(rest, " \t:=\"'")
		if rest == "" {
			continue
		}
		if strings.HasPrefix(rest, "\"") {
			if end := strings.Index(rest[1:], "\""); end >= 0 {
				out[key] = rest[1 : 1+end]
				continue
			}
		}
		if strings.HasPrefix(rest, "'") {
			if end := strings.Index(rest[1:], "'"); end >= 0 {
				out[key] = rest[1 : 1+end]
				continue
			}
		}
		if fields := strings.Fields(rest); len(fields) > 0 {
			out[key] = strings.Trim(fields[0], "\"'`,;")
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	for _, sep := range []string{". ", "\n"} {
		if i := strings.Index(s, sep); i > 0 {
			return s[:i]
		}
	}
	if len(s) > 300 {
		return s[:300]
	}
	return s
}

func toString(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case nil:
		return ""
	default:
		return fmt.Sprint(t)
	}
}

func toMap(v any) map[string]any {
	switch t := v.(type) {
	case map[string]any:
		return t
	case string:
		// Models sometimes emit arguments as a JSON string rather than an object.
		var m map[string]any
		if err := json.Unmarshal([]byte(t), &m); err == nil {
			return m
		}
	}
	return map[string]any{}
}

func toStringSlice(v any) []string {
	switch t := v.(type) {
	case []string:
		return t
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			out = append(out, toString(item))
		}
		return out
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

func toFloat(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case string:
		var f float64
		if _, err := fmt.Sscanf(t, "%g", &f); err == nil {
			return f
		}
	}
	return 0
}

func toPlan(v any) []PlanStep {
	var raw []any
	switch t := v.(type) {
	case []any:
		raw = t
	case map[string]any:
		if inner, ok := t["steps"].([]any); ok {
			raw = inner
		}
	case string:
		var decoded []any
		if err := json.Unmarshal([]byte(t), &decoded); err == nil {
			raw = decoded
		}
	default:
		return nil
	}
	var out []PlanStep
	for i, item := range raw {
		m := toMap(item)
		desc := ""
		for _, key := range []string{"description", "step", "task", "action", "title", "what"} {
			if v := toString(m[key]); v != "" {
				desc = v
				break
			}
		}
		if desc == "" {
			desc = toString(item)
		}
		if desc == "" {
			continue
		}
		step := PlanStep{ID: i + 1, Description: desc, Raw: m}
		for _, key := range []string{"files", "file", "paths"} {
			if list := toStringSlice(m[key]); len(list) > 0 {
				step.Files = list
				break
			}
		}
		for _, key := range []string{"tool", "action_name", "verb"} {
			if s := toString(m[key]); s != "" {
				step.Tool = s
				break
			}
		}
		for _, key := range []string{"verify", "verification", "check"} {
			if s := toString(m[key]); s != "" {
				step.Verify = s
				break
			}
		}
		for _, key := range []string{"rationale", "why", "reason"} {
			if s := toString(m[key]); s != "" {
				step.Rationale = s
				break
			}
		}
		out = append(out, step)
	}
	return out
}

// RenderPlan formats a plan for display and for persistence.
func RenderPlan(steps []PlanStep) string {
	var b strings.Builder
	for i, s := range steps {
		id := s.ID
		if id == 0 {
			id = i + 1
		}
		fmt.Fprintf(&b, "%d. %s\n", id, s.Description)
		if len(s.Files) > 0 {
			fmt.Fprintf(&b, "   files: %s\n", strings.Join(s.Files, ", "))
		}
		if s.Verify != "" {
			fmt.Fprintf(&b, "   verify: %s\n", s.Verify)
		}
		if s.Rationale != "" {
			fmt.Fprintf(&b, "   why: %s\n", s.Rationale)
		}
	}
	return b.String()
}

// FailureBrief renders verification output for a repair prompt.
//
// The diagnostics are extracted rather than the whole log, and the known-failure
// memory is included, because the most common wasted repair is a model
// re-attempting an approach already recorded as failed.
func FailureBrief(failureKind, diagnostics, advice string, store *memory.Store, previous []string) string {
	var b strings.Builder
	b.WriteString("## Verification failed\n")
	if failureKind != "" {
		fmt.Fprintf(&b, "classification: %s\n", failureKind)
	}
	if advice != "" {
		fmt.Fprintf(&b, "guidance: %s\n", advice)
	}
	if diagnostics != "" {
		b.WriteString("\n```\n")
		b.WriteString(diagnostics)
		b.WriteString("\n```\n")
	}
	if len(previous) > 0 {
		b.WriteString("\n## Attempts that already failed - do not repeat them\n")
		for i, p := range previous {
			if i >= 6 {
				break
			}
			fmt.Fprintf(&b, "- %s\n", p)
		}
	}
	if store != nil {
		if prior := store.Failures(false); len(prior) > 0 {
			b.WriteString("\n## Known failures in this project\n")
			for i, f := range prior {
				if i >= 5 {
					break
				}
				fmt.Fprintf(&b, "- [%s x%d] %s\n", f.Kind, f.Occurrences, firstSentence(f.Summary))
			}
		}
	}
	return b.String()
}