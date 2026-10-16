// Package obs records what the agent did, in a form that can be read after the
// fact.
//
// The log exists because the interesting failures are the ones the agent worked
// around: a patch rejected twice before it applied, a test that failed for a
// reason the classifier mislabelled, a reviewer rejection that changed nothing.
// Without a record, "the agent says it fixed it" is the only available account.
//
// Two forms are written per run: a JSONL event stream for machine analysis and a
// Markdown narrative for a person. Neither replaces the other; the JSON is
// complete and the Markdown is readable.
package obs

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// EventKind classifies a log event.
type EventKind string

const (
	EventRunStart     EventKind = "run_start"
	EventRunEnd       EventKind = "run_end"
	EventPhase        EventKind = "phase"
	EventModelCall    EventKind = "model_call"
	EventToolCall     EventKind = "tool_call"
	EventCommand      EventKind = "command"
	EventPolicy       EventKind = "policy_decision"
	EventPatch        EventKind = "patch"
	EventVerification EventKind = "verification"
	EventTest         EventKind = "test"
	EventFailure      EventKind = "failure"
	EventRetry        EventKind = "retry"
	EventReview       EventKind = "review"
	EventMemory       EventKind = "memory"
	EventWarning      EventKind = "warning"
	EventInfo         EventKind = "info"
)

// Event is one logged occurrence.
type Event struct {
	Seq      int             `json:"seq"`
	At       time.Time       `json:"at"`
	Kind     EventKind       `json:"kind"`
	Task     string          `json:"task,omitempty"`
	Phase    string          `json:"phase,omitempty"`
	Summary  string          `json:"summary,omitempty"`
	Detail   string          `json:"detail,omitempty"`
	Duration time.Duration   `json:"duration,omitempty"`
	Fields   map[string]any  `json:"fields,omitempty"`
}

// Logger writes events to a run directory.
type Logger struct {
	mu       sync.Mutex
	dir      string
	seq      int
	jsonl    *os.File
	narr     *os.File
	verbose  bool
	started  time.Time
	taskID   string
	closed   bool
	failures int
	warnings int
	toolCalls int
	modelCalls int
	commands  int
}

// New creates a logger writing under dir.
func New(dir string, verbose bool) (*Logger, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	jsonl, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	narr, err := os.OpenFile(filepath.Join(dir, "narrative.md"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		jsonl.Close()
		return nil, err
	}
	l := &Logger{dir: dir, jsonl: jsonl, narr: narr, verbose: verbose, started: time.Now()}
	fmt.Fprintf(narr, "# Agent run %s\n\n", startedAt().Format(time.RFC3339))
	return l, nil
}

// StartedAt reports when the run began.
func (l *Logger) StartedAt() time.Time { return l.started }

// Dir returns the log directory.
func (l *Logger) Dir() string { return l.dir }

// SetTask records the task id attached to subsequent events.
func (l *Logger) SetTask(id string) {
	l.mu.Lock()
	l.taskID = id
	l.mu.Unlock()
}

// Event appends an event.
func (l *Logger) Event(kind EventKind, phase, summary string, fields map[string]any) {
	l.log(Event{Kind: kind, Phase: phase, Summary: summary, Fields: fields})
}

// Info appends an informational event.
func (l *Logger) Info(phase, format string, args ...any) {
	l.log(Event{Kind: EventInfo, Phase: phase, Summary: fmt.Sprintf(format, args...)})
}

// Warn appends a warning.
func (l *Logger) Warn(phase, format string, args ...any) {
	l.mu.Lock()
	l.warnings++
	l.mu.Unlock()
	l.log(Event{Kind: EventWarning, Phase: phase, Summary: fmt.Sprintf(format, args...)})
}

// Failure appends a failure with its detail.
func (l *Logger) Failure(phase, summary, detail string, fields map[string]any) {
	l.mu.Lock()
	l.failures++
	l.mu.Unlock()
	l.log(Event{Kind: EventFailure, Phase: phase, Summary: summary, Detail: detail, Fields: fields})
}

// Tool records a tool invocation and its result.
func (l *Logger) Tool(phase, name string, args map[string]any, success bool, summary string, d time.Duration, exitCode *int) {
	l.mu.Lock()
	l.toolCalls++
	l.mu.Unlock()
	f := map[string]any{"tool": name, "success": success}
	if len(args) > 0 {
		f["args"] = args
	}
	if exitCode != nil {
		f["exit_code"] = *exitCode
	}
	l.log(Event{Kind: EventToolCall, Phase: phase, Summary: summary, Duration: d, Fields: f})
}

// Model records a model call.
func (l *Logger) Model(phase, role, backend, model, outcome string, d time.Duration, promptTokens, completionTokens int, err error) {
	l.mu.Lock()
	l.modelCalls++
	l.mu.Unlock()
	f := map[string]any{
		"role": role, "backend": backend, "model": model, "outcome": outcome,
		"prompt_tokens": promptTokens, "completion_tokens": completionTokens,
	}
	if err != nil {
		f["error"] = err.Error()
	}
	l.log(Event{Kind: EventModelCall, Phase: phase, Summary: fmt.Sprintf("%s via %s", role, backend),
		Duration: d, Fields: f})
}

// Command records an executed command.
func (l *Logger) Command(phase, cmdLine string, exitCode int, duration time.Duration, failed bool) {
	l.mu.Lock()
	l.commands++
	l.mu.Unlock()
	l.log(Event{Kind: EventCommand, Phase: phase, Summary: cmdLine,
		Duration: duration,
		Fields:   map[string]any{"exit_code": exitCode, "failed": failed, "command": cmdLine}})
}

func (l *Logger) log(e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.seq++
	e.Seq = l.seq
	e.At = time.Now()
	e.Task = l.taskID

	if e.Fields == nil {
		e.Fields = map[string]any{}
	}
	blob, err := json.Marshal(e)
	if err != nil {
		return
	}
	fmt.Fprintln(l.jsonl, string(blob))

	fmt.Fprintf(l.narr, "\n### [%d] %s %s — %s\n", e.Seq, e.At.Format("15:04:05"), e.Kind, e.Summary)
	if e.Phase != "" {
		fmt.Fprintf(l.narr, "phase: %s\n", e.Phase)
	}
	if e.Duration > 0 {
		fmt.Fprintf(l.narr, "duration: %s\n", e.Duration.Round(time.Millisecond))
	}
	if len(e.Fields) > 0 {
		keys := make([]string, 0, len(e.Fields))
		for k := range e.Fields {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := e.Fields[k]
			s := fmt.Sprint(v)
			if len(s) > 2000 {
				s = s[:2000] + "..."
			}
			fmt.Fprintf(l.narr, "- %s: %s\n", k, s)
		}
	}
	if e.Detail != "" {
		detail := e.Detail
		if len(detail) > 4000 {
			detail = detail[:4000] + "\n... [truncated]"
		}
		fmt.Fprintf(l.narr, "\n```\n%s\n```\n", detail)
	}
}

// Counts reports how much happened.
func (l *Logger) Counts() map[string]int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return map[string]int{
		"tool_calls":  l.toolCalls,
		"model_calls": l.modelCalls,
		"commands":    l.commands,
		"failures":    l.failures,
		"warnings":    l.warnings,
		"events":      l.seq,
	}
}

// Close flushes and closes the log files.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	fmt.Fprintf(l.narr, "\n---\nrun duration: %s\n", time.Since(l.started).Round(time.Millisecond))
	err1 := l.jsonl.Close()
	err2 := l.narr.Close()
	if err1 != nil {
		return err1
	}
	return err2
}

// Prune keeps the newest keep run directories.
//
// Run directories accumulate one per invocation, so a workspace used daily for a
// year accumulates thousands. Pruning runs at startup, when it is cheap, rather
// than at the end of a run, when it would delay the result the operator is
// waiting for.
func Prune(parent string, keep int) (int, error) {
	if keep <= 0 {
		return 0, nil
	}
	entries, err := os.ReadDir(parent)
	if err != nil {
		return 0, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), "run-") {
			dirs = append(dirs, e.Name())
		}
	}
	if len(dirs) <= keep {
		return 0, nil
	}
	sort.Strings(dirs)
	removed := 0
	for _, d := range dirs[:len(dirs)-keep] {
		if err := os.RemoveAll(filepath.Join(parent, d)); err == nil {
			removed++
		}
	}
	return removed, nil
}

// RunDir builds the directory name for a run.
func RunDir(parent string, t time.Time) string {
	return filepath.Join(parent, "run-"+t.UTC().Format("20060102-150405"))
}

var startedAtOnce sync.Once
var startTime time.Time

func startedAt() time.Time {
	startedAtOnce.Do(func() { startTime = time.Now() })
	return startTime
}

// Summarize reads a run's JSONL and renders a one-screen summary.
func Summarize(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return "", err
	}
	counts := map[EventKind]int{}
	var failures []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		counts[e.Kind]++
		if e.Kind == EventFailure {
			failures = append(failures, fmt.Sprintf("%s: %s", e.Phase, e.Summary))
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Run log: %s\n", dir)
	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)
	for _, k := range kinds {
		fmt.Fprintf(&b, "  %-14s %d\n", k, counts[EventKind(k)])
	}
	if len(failures) > 0 {
		b.WriteString("\nfailures:\n")
		for i, f := range failures {
			if i >= 20 {
				fmt.Fprintf(&b, "  ... %d more\n", len(failures)-20)
				break
			}
			fmt.Fprintf(&b, "  - %s\n", f)
		}
	}
	return b.String(), nil
}