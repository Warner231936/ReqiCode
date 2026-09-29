package scheduler

import (
	"context"
	"sync"
	"time"

	"github.com/kilo/spiral-codemaker/core/attention"
	"github.com/kilo/spiral-codemaker/core/events"
	"github.com/kilo/spiral-codemaker/core/state"
	"github.com/kilo/spiral-codemaker/core/units"
)

type Schedule struct {
	Interval time.Duration
	Cadence  state.CadenceType
	UnitID   string
}

type Scheduler struct {
	mu          sync.RWMutex
	ctx         context.Context
	cancel      context.CancelFunc
	registry    *units.Registry
	bus         *events.EventBus
	attention   *attention.Manager
	semiState   *state.SemiState
	schedules   map[string]Schedule
	stopCh      chan struct{}
	wg          sync.WaitGroup
	lastResults map[string]int64
}

func NewScheduler(registry *units.Registry, bus *events.EventBus, am *attention.Manager, semiState *state.SemiState) *Scheduler {
	ctx, cancel := context.WithCancel(context.Background())
	return &Scheduler{
		ctx:         ctx,
		cancel:      cancel,
		registry:    registry,
		bus:         bus,
		attention:   am,
		semiState:   semiState,
		schedules:   make(map[string]Schedule),
		stopCh:      make(chan struct{}),
		lastResults: make(map[string]int64),
	}
}

func (s *Scheduler) AddSchedule(unitID string, interval time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cadence := s.inferCadence(interval)
	s.schedules[unitID] = Schedule{
		Interval: interval,
		Cadence:  cadence,
		UnitID:   unitID,
	}
}

func (s *Scheduler) inferCadence(d time.Duration) state.CadenceType {
	switch {
	case d < 2*time.Second:
		return state.CadenceFast
	case d < 10*time.Second:
		return state.CadenceMedium
	default:
		return state.CadenceSlow
	}
}

func (s *Scheduler) Start() {
	s.bus.Subscribe(events.EventAttentionChanged, s.onAttentionChanged)
	s.bus.Subscribe(events.EventTestFailed, s.onTriggerEvent)
	s.bus.Subscribe(events.EventCodeApplied, s.onTriggerEvent)
	s.bus.Subscribe(events.EventSpiralIterationCompleted, s.onTriggerEvent)

	s.mu.RLock()
	for unitID, schedule := range s.schedules {
		s.wg.Add(1)
		go s.runUnitPeriodic(unitID, schedule)
	}
	s.mu.RUnlock()
}

func (s *Scheduler) runUnitPeriodic(unitID string, schedule Schedule) {
	defer s.wg.Done()
	ticker := time.NewTicker(schedule.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.stopCh:
			return
		case <-ticker.C:
			s.tryRunUnit(unitID)
		}
	}
}

func (s *Scheduler) tryRunUnit(unitID string) {
	s.mu.RLock()
	schedule, ok := s.schedules[unitID]
	s.mu.RUnlock()
	if !ok {
		return
	}

	unit, exists := s.registry.Get(unitID)
	if !exists {
		return
	}

	att := s.attention.Get(unitID)
	if att.Weight < state.AttentionThreshold {
		return
	}

	s.runUnit(unit, schedule)
}

func (s *Scheduler) runUnit(unit units.Unit, schedule Schedule) {
	ctx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	start := time.Now()
	results, err := unit.Run(ctx, s.semiState, s.bus)
	elapsed := time.Since(start)

	s.mu.Lock()
	s.lastResults[unit.ID()] = time.Now().Unix()
	s.mu.Unlock()

	if err != nil {
		s.bus.Publish(events.EventUnitCompleted, unit.ID(), map[string]interface{}{
			"unit_id": unit.ID(),
			"error":   err.Error(),
		})
		return
	}

	for _, ev := range results {
		s.bus.PublishEvent(ev)
	}

	s.bus.Publish(events.EventUnitCompleted, unit.ID(), map[string]interface{}{
		"unit_id":     unit.ID(),
		"duration_ms": elapsed.Milliseconds(),
	})
}

func (s *Scheduler) RunOnce(unitID string) units.UnitResult {
	unit, exists := s.registry.Get(unitID)
	if !exists {
		return units.UnitResult{UnitID: unitID, Error: ErrUnitNotFound}
	}

	s.mu.RLock()
	schedule, ok := s.schedules[unitID]
	s.mu.RUnlock()
	if !ok {
		schedule = Schedule{Interval: 1 * time.Second, Cadence: state.CadenceManual, UnitID: unitID}
	}

	s.runUnit(unit, schedule)
	return units.UnitResult{UnitID: unitID}
}

func (s *Scheduler) RunByRole(role state.UnitRole) []units.UnitResult {
	matched := s.registry.FindByRole(role)
	results := []units.UnitResult{}
	for _, unit := range matched {
		results = append(results, s.RunOnce(unit.ID()))
	}
	return results
}

func (s *Scheduler) onAttentionChanged(_ context.Context, _ events.Event) {
}

func (s *Scheduler) onTriggerEvent(_ context.Context, event events.Event) {
	for unitID := range s.schedules {
		unit, exists := s.registry.Get(unitID)
		if !exists {
			continue
		}
		if unit.Activation() == state.ActivationOnEvent {
			att := s.attention.Get(unitID)
			if att.Weight > state.AttentionThreshold {
				go s.RunOnce(unitID)
			}
		}
	}
}

func (s *Scheduler) Stop() {
	s.cancel()
	close(s.stopCh)
	s.wg.Wait()
}

func (s *Scheduler) Schedules() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]string, 0, len(s.schedules))
	for k := range s.schedules {
		result = append(result, k)
	}
	return result
}

var ErrUnitNotFound = errUnitNotFound{}

type errUnitNotFound struct{}

func (errUnitNotFound) Error() string { return "unit not found" }
