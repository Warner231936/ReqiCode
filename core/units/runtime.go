package units

import (
	"github.com/kilo/spiral-codemaker/code/proposals"
	"github.com/kilo/spiral-codemaker/code/workspace"
	"github.com/kilo/spiral-codemaker/core/causal"
	"github.com/kilo/spiral-codemaker/persistence"
	"github.com/kilo/spiral-codemaker/core/attention"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/spiral"
	"github.com/kilo/spiral-codemaker/execution/sandbox"
	"github.com/kilo/spiral-codemaker/models/routing"
)

type Runtime struct {
	SemiState   *state.SemiState
	Bus         *events.EventBus
	Attention   *attention.Manager
	Workspace   *workspace.Workspace
	Sandbox     *sandbox.Sandbox
	Router      *routing.Router
	Spiral      *spiral.Manager
	Proposals   *proposals.Proposer
	Memory      *persistence.PersistentMemory
	Config      *Config
	LLM         *LLMClient
	// Ledger records applied proposals as causal interventions and assigns
	// credit to them once ground truth (test results) arrives. Injected rather
	// than constructed here so the orchestrator owns the single ledger instance
	// that the loop also uses for attribution.
	Ledger *causal.Ledger
}

type Config struct {
	ProjectRoot   string
	OutputPath    string
	MaxIterations int
	Debug         bool
}

func NewRuntime(ss *state.SemiState, bus *events.EventBus, am *attention.Manager, ws *workspace.Workspace, sb *sandbox.Sandbox, router *routing.Router, sp *spiral.Manager, prop *proposals.Proposer, mem *persistence.PersistentMemory, config *Config) *Runtime {
	llm := NewLLMClient(router)
	return &Runtime{
		SemiState: ss,
		Bus:       bus,
		Attention: am,
		Workspace: ws,
		Sandbox:   sb,
		Router:    router,
		Spiral:    sp,
		Proposals: prop,
		Memory:    mem,
		Config:    config,
		LLM:       llm,
	}
}
