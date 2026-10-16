package cognition

import (
	"context"
	"fmt"
	"strings"
	"time"

	agentcontext "github.com/kilo/spiral-codemaker/agent/context"
	"github.com/kilo/spiral-codemaker/agent/llm"
	"github.com/kilo/spiral-codemaker/agent/memory"
	"github.com/kilo/spiral-codemaker/agent/task"
	"github.com/kilo/spiral-codemaker/agent/tools"
)

// execOutcome is how an execution round ended.
type execOutcome string

const (
	execFinished execOutcome = "finished"
	execAborted  execOutcome = "aborted"
)

// executor runs the tool-calling conversation for one round.
//
// The executor owns the conversation with the model and nothing else. It does not
// decide whether the task succeeded, does not run verification, and does not write
// to memory: those belong to the caller. Keeping the split sharp is what makes an
// execution round resumable and repeatable, which is what the repair loop needs.
type executor struct {
	agent     *Agent
	mode      Mode
	request   string
	brief     *agentcontext.Brief
	catalogue string
	plan      *Plan
	result    *Result
	task      *task.Task

	// messages carries the current round's conversation.
	messages []llm.Message
	// failureBrief is the repair context for this round, empty on the first.
	failureBrief string

	abortReason  string
	toolCalls    int
	modelCalls   int
	filesRead    []string
	filesChanged []string
}

// newRound resets the conversation so a repair starts from fresh context.
//
// Reusing the conversation would be cheaper but worse: after several failed
// rounds the transcript is dominated by rejected attempts, and a model given that
// history tends to retry something already tried. The failure brief carries forward
// the one thing that needs to be remembered.
func (e *executor) newRound(failureBrief string) {
	e.messages = nil
	e.failureBrief = failureBrief
}

// run executes one round of tool calls.
func (e *executor) run(ctx context.Context) (execOutcome, error) {
	e.messages = nil
	return e.step(ctx)
}

// runOnce executes a single decision for a review-driven repair round.
func (e *executor) runOnce(ctx context.Context) error {
	_, err := e.step(ctx)
	return err
}

// step is the tool-calling conversation for one round.
func (e *executor) step(ctx context.Context) (execOutcome, error) {
	a := e.agent
	reg := a.Tools.Registry

	prompter := &Prompter{
		Mode: e.mode, Brief: e.brief, Catalogue: e.catalogue,
		Workspace: a.Tools.WS.Root,
	}
	messages := prompter.RepairMessages(e.request, e.failureBrief, "")
	if e.failureBrief == "" {
		messages = prompter.Messages(e.request)
	}

	maxSteps := a.MaxSteps
	if maxSteps <= 0 {
		maxSteps = 60
	}

	// Repeated identical outcomes are the signature of a loop: a model that keeps
	// asking for the same thing, or a tool that keeps failing the same way. The
	// count is tracked so the loop can stop and report rather than spend its
	// budget re-asking.
	repeats := map[string]int{}

	for step := 1; step <= maxSteps; step++ {
		if err := ctx.Err(); err != nil {
			return execAborted, err
		}
		if e.toolCalls >= a.MaxToolCalls {
			e.abortReason = fmt.Sprintf("tool call budget exhausted (%d calls)", e.toolCalls)
			a.logWarn(PhaseExecute, "%s", e.abortReason)
			return execAborted, nil
		}

		role := roleForPlan(e.mode)
		if e.mode == Implement || e.mode == Debug || e.mode == Refactor || e.mode == Test {
			role = "coder"
		}
		start := time.Now()
		resp, err := a.Router.Chat(ctx, role, messages, llm.Request{JSONMode: true})
		e.modelCalls++
		if err != nil {
			a.logFailure(PhaseExecute, "model call failed", err.Error(),
				map[string]any{"step": step, "role": role})
			e.abortReason = "language model call failed: " + err.Error()
			return execAborted, nil
		}
		a.Log.Model(string(PhaseExecute), role, resp.Backend, resp.Model, "ok",
			time.Since(start), resp.PromptTokens, resp.CompletionTokens, nil)

		d := ParseDecision(resp.Content)
		d.Duration = time.Since(start)
		d.Model = resp.Model
		d.Backend = resp.Backend

		if d.ParseError != "" {
			a.logWarn(PhaseExecute, "model output was not clean JSON (recovered if possible): %s", d.ParseError)
		}

		if verr := d.Valid(reg, e.mode); verr != nil {
			// A rejected decision is fed back rather than executed. The message is
			// specific on purpose: it names the permitted tools or the missing
			// argument, so the next turn can be correct rather than merely different.
			a.logFailure(PhaseExecute, "rejected decision: "+verr.Error(),
				truncate(resp.Content, 1200), map[string]any{"step": step})
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: truncate(resp.Content, 2000)},
				llm.Message{Role: llm.RoleUser, Content: fmt.Sprintf(
					"That reply is not valid: %v\nReply again with one JSON object.", verr)})
			if step >= 3 && repeats["invalid"] >= 2 {
				e.abortReason = "the model produced three consecutive unusable replies: " + verr.Error()
				return execAborted, nil
			}
			repeats["invalid"]++
			continue
		}

		switch d.Action {
		case ActionFinish:
			e.recordSummary(d)
			e.markStepsComplete()
			a.logInfo(PhaseExecute, "model reported completion: "+truncate(d.Summary, 200))
			return execFinished, nil

		case ActionFail:
			e.abortReason = d.Reason
			a.logFailure(PhaseExecute, "model reported it could not proceed", d.Reason, nil)
			return execAborted, nil

		case ActionAsk:
			e.abortReason = "the model needs an answer it cannot obtain: " + d.Reason
			e.result.OpenIssues = appendUnique(e.result.OpenIssues, d.Reason)
			return execAborted, nil

		case ActionPlan:
			// Planning during execution means the model discovered its plan was
			// wrong. The new plan replaces the old one and execution continues.
			if len(d.Plan) > 0 {
				e.plan.Steps = d.Plan
				e.task.Steps = toTaskSteps(d.Plan)
				a.save(e.task)
				a.logInfo(PhaseExecute, fmt.Sprintf("model revised the plan mid-execution (%d steps)", len(d.Plan)))
			}
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: truncate(resp.Content, 2000)},
				llm.Message{Role: llm.RoleUser, Content: "Plan noted. Now execute the first step with a tool call."})
			continue

		case ActionTool:
			sig := d.Tool + "|" + argSignature(d.Args)
			repeats[sig]++
			if repeats[sig] >= 3 {
				e.abortReason = fmt.Sprintf("the model repeated %s three times with identical arguments; "+
					"it is not making progress on this step", d.Tool)
				a.logFailure(PhaseExecute, "repeated action detected", e.abortReason, nil)
				return execAborted, nil
			}

			res := e.invoke(ctx, d)
			messages = append(messages,
				llm.Message{Role: llm.RoleAssistant, Content: truncate(resp.Content, 3000)},
				llm.Message{Role: llm.RoleUser, Content: renderToolResult(res)})

			if res.Success {
				e.noteChange(d, res)
			} else {
				a.logFailure(PhaseExecute,
					fmt.Sprintf("%s failed (%s)", d.Tool, firstNonEmpty(res.Class, "no_class")),
					firstNonEmpty(res.Error, truncate(res.Stderr, 800)),
					map[string]any{"tool": d.Tool})
				if a.Memory != nil {
					a.Memory.RecordFailure(memoryFailure(d.Tool, res))
				}
			}
			e.task.Attempts = e.toolCalls
			e.saveTask()
		}
	}

	e.abortReason = fmt.Sprintf("the model did not finish within %d steps", maxSteps)
	a.logFailure(PhaseExecute, "step budget exhausted", e.abortReason, nil)
	return execAborted, nil
}

// invoke runs a tool call and records it.
func (e *executor) invoke(ctx context.Context, d Decision) tools.Result {
	a := e.agent
	res := a.Tools.Registry.Invoke(ctx, d.Tool, tools.NewArgs(d.Args))
	e.toolCalls++
	if res.Changed {
		for _, p := range changedPaths(d) {
			e.filesChanged = appendUnique(e.filesChanged, p)
			e.task.AddFileChanged(p)
			a.lastChanged = e.filesChanged
		}
	}
	if t, ok := a.Tools.Registry.Get(d.Tool); ok && strings.HasPrefix(t.Name(), "read") {
		if p := argsPath(d.Args); p != "" {
			e.filesRead = appendUnique(e.filesRead, p)
			e.task.AddFileRead(p)
		}
	}
	summary := firstNonEmpty(res.Hint, truncate(res.Stdout, 120), res.Error)
	a.Log.Tool(string(PhaseExecute), d.Tool, d.Args, res.Success, summary,
		time.Duration(res.DurationMS)*time.Millisecond, res.ExitCode)
	return res
}

func (e *executor) noteChange(d Decision, res tools.Result) {
	if !res.Changed {
		return
	}
	e.task.CurrentStep = nextStepIndex(e.task)
	e.agent.progress("  changed: %s", strings.Join(changedPaths(d), ", "))
}

func (e *executor) recordSummary(d Decision) {
	e.result.Summary = d.Summary
	for _, ev := range d.Evidence {
		e.result.Evidence = appendUnique(e.result.Evidence, ev)
	}
	if d.Confidence > 0 {
		e.result.Evidence = appendUnique(e.result.Evidence,
			fmt.Sprintf("model confidence %.2f", d.Confidence))
	}
	e.result.Transcript = append(e.result.Transcript, Event{
		Kind: EventModel, Success: true,
		Summary: truncate(d.Thought, 200),
		Result:  truncate(d.Summary, 800),
	})
}

func (e *executor) markStepsComplete() {
	for i := range e.task.Steps {
		if e.task.Steps[i].Status == task.StepPending || e.task.Steps[i].Status == task.StepActive {
			e.task.Steps[i].Status = task.StepComplete
		}
	}
	e.saveTask()
}

func (e *executor) saveTask() {
	if e.agent.Tasks == nil {
		return
	}
	if err := e.agent.Tasks.Save(e.task); err != nil {
		e.agent.logWarn(PhaseExecute, "could not persist task: %v", err)
	}
}

// renderToolResult formats a tool result for the model.
//
// The structured fields matter: exit code and error class let a model tell "the
// command failed" from "the command was refused", which are different problems
// with different responses, and a merged string loses that distinction.
func renderToolResult(res tools.Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "tool: %s\n", res.Tool)
	fmt.Fprintf(&b, "success: %v\n", res.Success)
	if res.ExitCode != nil {
		fmt.Fprintf(&b, "exit_code: %d\n", *res.ExitCode)
	}
	if res.Class != "" {
		fmt.Fprintf(&b, "classification: %s\n", res.Class)
	}
	if res.Changed {
		b.WriteString("changed: true\n")
	}
	if res.Error != "" {
		fmt.Fprintf(&b, "error: %s\n", res.Error)
	}
	if res.Hint != "" {
		fmt.Fprintf(&b, "hint: %s\n", res.Hint)
	}
	if res.Stdout != "" {
		b.WriteString("output:\n---\n")
		b.WriteString(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("---\n")
	}
	if res.Stderr != "" {
		b.WriteString("stderr:\n---\n")
		b.WriteString(res.Stderr)
		if !strings.HasSuffix(res.Stderr, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("---\n")
	}
	if res.Truncated {
		b.WriteString("(output was truncated)\n")
	}
	b.WriteString("\nReply with the next single action as one JSON object.")
	return b.String()
}

// memoryFailure converts a failed tool result into a stored failure record.
//
// A tool failure is recorded because a tool that fails repeatedly across tasks is
// usually a misconfigured tool, not a transient problem, and finding that out
// before the third task is cheaper than after it.
func memoryFailure(toolName string, res tools.Result) memory.FailureRecord {
	summary := firstNonEmpty(res.Error, truncate(res.Stderr, 200), truncate(res.Stdout, 200))
	if summary == "" {
		summary = toolName + " failed with no diagnostic output"
	}
	return memory.FailureRecord{
		Kind:    firstNonEmpty(res.Class, "tool_failure"),
		Summary: fmt.Sprintf("%s: %s", toolName, summary),
		Context: truncate(res.Stdout, 1200),
	}
}

func argSignature(args map[string]any) string {
	if len(args) == 0 {
		return ""
	}
	keys := make([]string, 0, len(args))
	for k := range args {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, k+"="+truncate(toString(args[k]), 120))
	}
	return strings.Join(parts, "&")
}

func changedPaths(d Decision) []string {
	var out []string
	for _, key := range []string{"path", "file", "paths", "files"} {
		if v := strings.TrimSpace(toString(d.Args[key])); v != "" {
			return []string{v}
		}
	}
	if list := toStringSlice(d.Args["files"]); len(list) > 0 {
		return list
	}
	// A patch names its targets inside the diff text.
	if d.Tool == "apply_patch" {
		for _, line := range strings.Split(toString(d.Args["diff"]), "\n") {
			if strings.HasPrefix(line, "+++ ") {
				p := strings.TrimSpace(line[4:])
				if idx := strings.IndexAny(p, "\t"); idx >= 0 {
					p = strings.TrimSpace(p[:idx])
				}
				p = strings.TrimPrefix(p, "b/")
				if p != "" && p != "/dev/null" {
					out = append(out, p)
				}
			}
		}
	}
	return out
}

func argsPath(args map[string]any) string {
	for _, key := range []string{"path", "file"} {
		if v := strings.TrimSpace(toString(args[key])); v != "" {
			return v
		}
	}
	return ""
}

func nextStepIndex(t *task.Task) int {
	for i, s := range t.Steps {
		if s.Status != task.StepComplete && s.Status != task.StepSkipped {
			return i + 1
		}
	}
	return len(t.Steps)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func sortStrings(s []string) {
	for i := 0; i < len(s); i++ {
		for j := i + 1; j < len(s); j++ {
			if s[j] < s[i] {
				s[i], s[j] = s[j], s[i]
			}
		}
	}
}