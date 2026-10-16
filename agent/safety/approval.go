package safety

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Approval records a decision about one command.
type Approval struct {
	Granted bool   `json:"granted"`
	By      string `json:"by"` // interactive | auto-safe | auto-confirm | denied | scripted
	Reason  string `json:"reason"`
	At      time.Time `json:"at"`
}

// Request asks for a decision.
type Request struct {
	Command  string   `json:"command"`
	Verdict  Verdict  `json:"verdict"`
	Attempt  int      `json:"attempt"`
	History  []string `json:"history,omitempty"`
}

// Approver decides whether a command may run.
type Approver interface {
	Approve(ctx context.Context, req Request) (Approval, error)
}

// AutoApprover applies policy without a human.
//
// SAFE runs. CONFIRM is denied unless unattended confirmation was explicitly
// enabled, and BLOCKED is always denied. Denying by default is what makes
// non-interactive use safe: an agent running in CI must not be able to delete a
// directory just because nobody was there to say no.
type AutoApprover struct {
	AutoConfirm bool
}

// NewAutoApprover builds a non-interactive approver.
func NewAutoApprover(autoConfirm bool) *AutoApprover {
	return &AutoApprover{AutoConfirm: autoConfirm}
}

func (a *AutoApprover) Approve(ctx context.Context, req Request) (Approval, error) {
	switch req.Verdict.Class {
	case Safe:
		return Approval{Granted: true, By: "auto-safe", Reason: "classified SAFE by policy", At: time.Now()}, nil
	case Confirm:
		if a.AutoConfirm {
			return Approval{Granted: true, By: "auto-confirm",
				Reason: "confirmation-class command accepted because unattended confirmation is enabled", At: time.Now()}, nil
		}
		return Approval{Granted: false, By: "denied",
			Reason: "requires confirmation and no interactive approver is available; re-run interactively or set safety.auto_approve_confirm", At: time.Now()}, nil
	default:
		return Approval{Granted: false, By: "denied",
			Reason: "BLOCKED by policy: " + req.Verdict.Reason, At: time.Now()}, nil
	}
}

// InteractiveApprover asks the operator on a terminal.
type InteractiveApprover struct {
	In  io.Reader
	Out io.Writer
	// PromptPrefix labels where the question is being asked.
	PromptPrefix string
	mu           sync.Mutex
}

// NewInteractiveApprover reads from stdin and writes to stderr.
//
// stderr rather than stdout so that piping the agent's stdout into a file does
// not capture the prompts and leave an unattended caller waiting forever for an
// answer it can never see.
func NewInteractiveApprover() *InteractiveApprover {
	return &InteractiveApprover{In: os.Stdin, Out: os.Stderr, PromptPrefix: "agent"}
}

// Approve prompts for a decision, with a timeout so a missing answer cannot hang
// a run indefinitely.
func (a *InteractiveApprover) Approve(ctx context.Context, req Request) (Approval, error) {
	if req.Verdict.Class == Safe {
		return Approval{Granted: true, By: "auto-safe", Reason: "classified SAFE by policy", At: time.Now()}, nil
	}
	if req.Verdict.Class == Blocked {
		return Approval{Granted: false, By: "denied",
			Reason: "BLOCKED by policy: " + req.Verdict.Reason, At: time.Now()}, nil
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	in := a.In
	if in == nil {
		in = os.Stdin
	}
	out := a.Out
	if out == nil {
		out = os.Stderr
	}

	fmt.Fprintf(out, "\n=== CONFIRMATION REQUIRED (%s) ===\n", req.Verdict.Class)
	fmt.Fprintf(out, "command : %s\n", req.Command)
	fmt.Fprintf(out, "rule    : %s\n", req.Verdict.Rule)
	fmt.Fprintf(out, "reason  : %s\n", req.Verdict.Reason)
	fmt.Fprintf(out, "cwd     : %s\n", req.Verdict.Segment)
	if len(req.History) > 0 {
		fmt.Fprintf(out, "recent failures:\n")
		for _, h := range req.History {
			fmt.Fprintf(out, "  - %s\n", h)
		}
	}
	fmt.Fprint(out, "proceed? [y/N/a(always for this rule)/d(details)]: ")

	reader := bufio.NewReader(in)
	type result struct {
		answer string
		err    error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := reader.ReadString('\n')
		ch <- result{answer: strings.TrimSpace(line), err: err}
	}()

	var timeout <-chan time.Time
	if dl, ok := ctx.Deadline(); ok {
		timer := time.NewTimer(time.Until(dl))
		defer timer.Stop()
		timeout = timer.C
	}

	select {
	case <-ctx.Done():
		return Approval{}, ctx.Err()
	case <-timeout:
		return Approval{Granted: false, By: "denied", Reason: "no response before the deadline", At: time.Now()}, nil
	case res := <-ch:
		if res.err != nil && res.answer == "" {
			return Approval{Granted: false, By: "denied", Reason: "no input available: " + res.err.Error(), At: time.Now()}, nil
		}
		switch strings.ToLower(res.answer) {
		case "y", "yes":
			return Approval{Granted: true, By: "interactive", Reason: "approved at the terminal", At: time.Now()}, nil
		case "a", "always":
			return Approval{Granted: true, By: "interactive-always", Reason: "approved for this rule", At: time.Now()}, nil
		default:
			return Approval{Granted: false, By: "denied", Reason: "declined at the terminal", At: time.Now()}, nil
		}
	}
}

// ScriptedApprover answers from a fixed list, for tests and for --yes/--no modes.
type ScriptedApprover struct {
	mu        sync.Mutex
	responses []bool
	calls     int
	Requests  []Request
}

// NewScriptedApprover builds an approver returning the given answers in order.
// The last answer repeats once the list is exhausted.
func NewScriptedApprover(answers ...bool) *ScriptedApprover {
	return &ScriptedApprover{responses: answers}
}

// Approve returns the next scripted answer.
func (s *ScriptedApprover) Approve(ctx context.Context, req Request) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests = append(s.Requests, req)
	idx := s.calls
	s.calls++
	granted := false
	if len(s.responses) == 0 {
		granted = false
	} else if idx >= len(s.responses) {
		granted = s.responses[len(s.responses)-1]
	} else {
		granted = s.responses[idx]
	}
	by := "scripted-deny"
	if granted {
		by = "scripted-allow"
	}
	return Approval{Granted: granted, By: by, Reason: "scripted decision", At: time.Now()}, nil
}

// Calls reports how many decisions were requested.
func (s *ScriptedApprover) Calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls
}

// ChainApprover tries each approver in order.
//
// Interactive-first with auto-fallback is what lets the same binary work for a
// person at a terminal and for an unattended run: if a decision cannot be asked,
// the automatic approver decides rather than the process hanging.
type ChainApprover struct {
	Approvers []Approver
}

// Approve tries each approver, returning the first grant.
func (c *ChainApprover) Approve(ctx context.Context, req Request) (Approval, error) {
	var last Approval
	for _, a := range c.Approvers {
		ap, err := a.Approve(ctx, req)
		if err != nil {
			continue
		}
		if ap.Granted {
			return ap, nil
		}
		last = ap
	}
	if last.By == "" {
		last = Approval{Granted: false, By: "denied", Reason: "no approver available", At: time.Now()}
	}
	return last, nil
}