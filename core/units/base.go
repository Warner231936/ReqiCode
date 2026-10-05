package units

import (
	"context"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Unit interface {
	ID() string
	Role() state.UnitRole
	Name() string
	Cadence() state.CadenceType
	Activation() state.ActivationType
	Dependencies() []string
	Run(ctx context.Context, semiState *state.SemiState, bus *events.EventBus) ([]events.Event, error)
	LocalMemory() map[string]interface{}
	SetConfidence(c state.Confidence)
	Confidence() state.Confidence
	AttentionWeight() float64
	IsActive() bool
}

type BaseUnit struct {
	mu         sync.RWMutex
	id         string
	role       state.UnitRole
	name       string
	cadence    state.CadenceType
	activation state.ActivationType
	deps       []string
	confidence state.Confidence
	attention  float64
	active     bool
	localMem   map[string]interface{}
	provenance state.Provenance
}

func NewBaseUnit(id string, role state.UnitRole, name string, cadence state.CadenceType, activation state.ActivationType) *BaseUnit {
	return &BaseUnit{
		id:         id,
		role:       role,
		name:       name,
		cadence:    cadence,
		activation: activation,
		deps:       []string{},
		confidence: state.ConfidenceMedium,
		attention:  0.1,
		active:     true,
		localMem:   make(map[string]interface{}),
		provenance: state.NewProvenance(id),
	}
}

func (u *BaseUnit) ID() string                       { return u.id }
func (u *BaseUnit) Role() state.UnitRole             { return u.role }
func (u *BaseUnit) Name() string                     { return u.name }
func (u *BaseUnit) Cadence() state.CadenceType       { return u.cadence }
func (u *BaseUnit) Activation() state.ActivationType { return u.activation }
func (u *BaseUnit) Dependencies() []string           { return u.deps }
func (u *BaseUnit) Confidence() state.Confidence     { return u.confidence }
func (u *BaseUnit) AttentionWeight() float64         { return u.attention }
func (u *BaseUnit) IsActive() bool                   { return u.active }
func (u *BaseUnit) Provenance() state.Provenance     { return u.provenance }

func (u *BaseUnit) SetConfidence(c state.Confidence) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.confidence = c
}

func (u *BaseUnit) SetAttention(w float64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.attention = w
}

func (u *BaseUnit) SetDependencies(deps []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.deps = deps
}

func (u *BaseUnit) SetActive(active bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.active = active
}

func (u *BaseUnit) SetLocalMemory(key string, value interface{}) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.localMem[key] = value
}

func (u *BaseUnit) GetLocalMemory(key string) (interface{}, bool) {
	u.mu.RLock()
	defer u.mu.RUnlock()
	v, ok := u.localMem[key]
	return v, ok
}

func (u *BaseUnit) LocalMemory() map[string]interface{} {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.localMem
}

func (u *BaseUnit) RecordRun() {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.provenance.CreatedAt = time.Now().UTC()
	u.provenance.Revision++
}
