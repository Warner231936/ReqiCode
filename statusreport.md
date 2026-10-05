# statusreport.md

**Date:** 2026-10-05
**Repo:** `github.com/Warner231936/ReqiCode`
**Branch:** `main` @ `0fde802`
**Working tree:** 1 modified file, 3 untracked directories

---

## 1. Answer to the pending question: no new git repo needed

This is **already a git repository** — 11 commits, remote configured, working tree
clean except for the items below. Answering the coordination question directly:
**git is the answer**, with one important caveat about how two agents share a
directory (section 6).

---

## 2. Current State

### Committed and pushed

```
0fde802  Redo state of the art.md against the measured present
e533adb  Add final.md: verified capabilities, known limits, and scorecard
acb8793  Independent verification: derive tests from the type system, and find two real bugs
c589004  Teach the system to recognise it needs a bigger model, and run a 14B
d5555bf  Add the self-modification path (Blocker 4) and measure where it stands
ddacd31  Use the LLM for code generation, and make that observable
c2079a9  Close readiness Blocker 2: state-machine property tests and claim lifecycle
9c6514b  Close readiness Blocker 6: canary harness for trial-before-promote
d5555bf^ 56e65b3  Initial commit: typed epistemic state, Phase 0 instrumentation
```

### Metrics

| Measure | Value |
|---|---|
| Commits | 11 |
| Source files (excluding parallel work) | 89 |
| Test packages | 42 |
| Test functions | 335 |
| Build status (`core/`, `cmd/`, `models/`, `tests/`) | **clean** |
| Vet status | clean |
| Test status | **18/18 packages passing** |
| Local model | Qwen2.5-Coder-14B-Instruct Q4_K_M, `localhost:8090`, 367–448 tok/s |
| API keys in use | none |

---

## 3. What Works — Measured

| Capability | Evidence |
|---|---|
| **Self-improvement, no model involved** | `core/converge`: 23→27 tests, 86.5%→87.1% coverage, promoted |
| **Self-defect discovery** | Two nil-dereference bugs found in own source, 4 seconds, no model |
| **Cannot damage itself** | Live source untouched across 20+ trials including 20 consecutive failures |
| **Four independent safety gates** | Each deliberately broken to confirm it fires |
| **Tamper-evident history** | SHA-256 chain; break located at first non-following link |
| **Signed causal attribution** | Per-intervention credit, regression weighted 1.5× |
| **Model adequacy self-diagnosis** | `modelcheck` reports first-attempt success rate, throughput, dominant failure cause |
| **Fully local operation** | No keys, no external calls in the run path |

---

## 4. In-Flight, Uncommitted — My Work

**One file modified, not yet committed:**

`core/units/test_designer.go` — fixes a regression I introduced.

**The bug:** the template test designer assumed the template file layout. When the
LLM code generator produced its own layout, the tests still called `Process`, and
the workspace failed to compile with `undefined: Process`. The LLM path was
running at 2-of-5 calls succeeded with confidence 0.25.

**The fix:** the test designer now scans the files the code generator *actually
produced* using `core/apiscan`, and synthesises smoke tests against the real API
when the generated layout differs from the plan's assumption. The generated code
wins the disagreement, because the generated code is what the compiler sees.

**A bug in my own fix**, caught by the existing test suite before commit:

The first version picked the *first generated file alphabetically* to decide the
layout. That is `cmd/cli/main.go`, whose package contains `main` and none of the
symbols the template tests call — so the fix broke 5 tests including the canary
end-to-end suite. Corrected to scan every generated package and choose the one
exposing the most callable functions.

**Status:** all 18 packages green with the fix in place. **Not yet committed.**

**One known remaining failure on the LLM path:** `sort` imported and not used in
`pkg/core/core_test.go`. Diagnosed but not yet fixed.

---

## 5. In-Flight — Parallel Builder's Work

A second code builder is working in this directory. Untracked:

```
.agent/                          config/state directory
agent/                           48 files, 453 KB
cmd/agent/                       CLI entry point
```

Packages include `agent/cli`, `agent/cognition`, `agent/config`, `agent/context`,
`agent/fsignore`, `agent/llm`, `agent/memory`, `agent/verify`.

### Current state: **does not compile**

```
agent/verify/failure.go:151: undefined: RunResult
agent/verify/failure.go:173: undefined: RunResult
agent/verify/failure.go:189: undefined: RunResult
agent/verify/failure.go:199: undefined: RunResult
```

`go build ./...` fails on this package.

### Blast radius

| Scope | Status |
|---|---|
| My packages (`core/`, `cmd/spiral/`, `models/`, `tests/`) | **unaffected, 18/18 green** |
| Full `go build ./...` | **broken** by `agent/verify` |
| Full `go test ./...` | **blocked** — cannot enumerate cleanly while a sibling package fails |

This is the concrete cost of two agents sharing one working directory: their
in-flight breakage blocks my ability to verify my own work with a single command.

---

## 6. How to Work Together

The blocker is not git — it's that `go build ./...` and `go test ./...` operate
on the whole module, so one agent's broken package blinds the other. Three
options, in order of preference:

### Option A — Git worktrees (recommended)

Each agent gets its own checkout; worktrees share the object database but not the
working tree.

```bash
git worktree add ../reqicode-mine -b agent/semi-state
```

Isolation: total. Neither sees the other's uncommitted work. Cost: separate
`go build` cache misses initially, and merging needs intent.

**This is what I'd pick.** The current failure mode — my work unverifiable
because a sibling package doesn't compile — is exactly what worktrees prevent.

### Option B — Separate branches, disciplined commits

Stay in one directory, but commit before switching:

```bash
git add -A && git commit -m "..."   # always commit before handing over
```

Isolation: none. Benefit: clear history of who changed what. Cost: constant
context switching, and a mid-edit handover leaves the tree broken for both.

### Option C — Agree on a build boundary

If we stay in one directory, agree that:
- `agent/` belongs to them, `core/` and `tests/` belong to me
- Each of us verifies with a **scoped** command rather than `./...`
- Changes crossing the boundary get a commit and a message

I already verified scoped commands work: `go build ./core/... ./cmd/...` returns
clean while `agent/verify` is broken.

**My recommendation:** Option A. It removes the coordination problem instead of
managing it.

---

## 7. Immediate Actions

| # | Action | Owner |
|---|---|---|
| 1 | Commit my `test_designer.go` fix (18/18 green) | me |
| 2 | Fix the unused `sort` import on the LLM path | me |
| 3 | Decide on worktrees vs. scoped builds | both |
| 4 | Finish `agent/verify` so `go build ./...` is green again | them |
| 5 | Add `agent/` to `.gitignore` until it compiles, or fix first | them |

---

## 8. Honest Summary

The system works as documented: it improves its own coverage and finds real
defects **with no model in the loop**, and it has never once modified its own
source with a rejected change across 20+ trials.

The model-driven code generation path is **still degraded** — one known unused
import outstanding, plus the structural limitation that the model cannot write
its own tests (0/20 attempts across three model sizes). That second limitation is
permanent at this model class and is the reason the AST-derived verification path
exists.

Meanwhile the repository has two agents in one working directory, and the
practical cost is already visible: a sibling package failing to compile blocks
verification of unrelated work. That is a workflow problem with a known fix, and
it is worth solving before either of us writes much more code.