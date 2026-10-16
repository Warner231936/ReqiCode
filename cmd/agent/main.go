// Command agent is a local-first autonomous coding agent.
//
// It inspects a repository, plans, edits, runs the project's own build and tests,
// reads the failures, iterates, reviews its own changes, and records what it
// learned. Every model it uses runs on this machine; no paid API is required or
// contacted.
//
//	agent doctor                       check the environment
//	agent models                       inventory local models and routing
//	agent inspect                      report the project and its verification commands
//	agent run "fix the auth bug"       plan, implement, verify, iterate, review
//	agent plan "add authentication"    produce a plan file
//	agent apply --plan plan.json       execute a plan file's edits and verify them
//	agent status                       show the latest task and run counters
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kilo/spiral-codemaker/agent/cli"
)

func main() {
	// Ctrl-C cancels the context, which unwinds the tool loop cleanly and lets the
	// task state be persisted. The task remains resumable, which is the point of
	// writing state after every step rather than only at the end.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	env := cli.Env{
		Stdout:      os.Stdout,
		Stderr:      os.Stderr,
		Stdin:       os.Stdin,
		Getenv:      os.Getenv,
		Args:        os.Args[1:],
		Interactive: isInteractive(),
	}

	// A generous default ceiling so a forgotten run cannot occupy a machine
	// indefinitely; every individual command has its own shorter timeout.
	ctx, cancel := context.WithTimeout(ctx, 2*time.Hour)
	defer cancel()

	code := cli.Run(ctx, env)
	os.Exit(code)
}

// isInteractive reports whether a person is present to answer a prompt.
//
// A prompt needs both an attached terminal and real input. Checking only for a
// terminal would still block on a pipeline that has no one to answer, which is the
// failure this exists to prevent.
func isInteractive() bool {
	if os.Getenv("AGENT_NONINTERACTIVE") != "" {
		return false
	}
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}