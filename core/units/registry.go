package units

import (
	"sync"

	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
)

type Registry struct {
	mu     sync.RWMutex
	units  map[string]Unit
	bus    *events.EventBus
}

func NewRegistry(bus *events.EventBus) *Registry {
	return &Registry{
		units: make(map[string]Unit),
		bus:   bus,
	}
}

func (r *Registry) Register(u Unit) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.units[u.ID()] = u
}

func (r *Registry) Get(id string) (Unit, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	u, ok := r.units[id]
	return u, ok
}

func (r *Registry) All() []Unit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Unit, 0, len(r.units))
	for _, u := range r.units {
		result = append(result, u)
	}
	return result
}

func (r *Registry) AllActive() []Unit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Unit, 0)
	for _, u := range r.units {
		if u.IsActive() {
			result = append(result, u)
		}
	}
	return result
}

func (r *Registry) FindByRole(role state.UnitRole) []Unit {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Unit, 0)
	for _, u := range r.units {
		if u.Role() == role {
			result = append(result, u)
		}
	}
	return result
}

func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.units)
}

func (r *Registry) IDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]string, 0, len(r.units))
	for id := range r.units {
		result = append(result, id)
	}
	return result
}

type UnitResult struct {
	UnitID   string
	Events   []events.Event
	Error    error
	Duration int64
}

type ExecutionRecord struct {
	UnitID  string         `json:"unit_id"`
	Timestep int64         `json:"timestep"`
	Events  []events.Event `json:"events"`
	Error   string         `json:"error,omitempty"`
	Duration int64         `json:"duration_ms"`
}
