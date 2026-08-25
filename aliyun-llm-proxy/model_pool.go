package main

import (
	"math"
	"math/rand"
	"sort"
	"sync"
	"time"
)

type modelState struct {
	Config              modelConfig
	InFlight            int
	Successes           uint64
	Failures            uint64
	Throttles           uint64
	TotalLatencyMS      float64
	InputTokens         uint64
	OutputTokens        uint64
	Adoptions           uint64
	HedgeParticipations uint64
	HedgeWins           uint64
	DiscardedResponses  uint64
	CooldownUntil       time.Time
	CooldownReason      string
	Unavailable         bool
	UnavailableReason   string
	RequestTimes        []time.Time
	SecondTimes         []time.Time
	LatenciesMS         []float64
}

type modelSnapshot struct {
	ID                  string  `json:"id"`
	Enabled             bool    `json:"enabled"`
	Role                string  `json:"role"`
	RoutingPriority     int     `json:"routing_priority"`
	RPM                 int     `json:"rpm"`
	TPM                 int     `json:"tpm"`
	MinIntervalSeconds  float64 `json:"min_interval_seconds"`
	InFlight            int     `json:"in_flight"`
	RequestsLastMinute  int     `json:"requests_last_minute"`
	Successes           uint64  `json:"successes"`
	Failures            uint64  `json:"failures"`
	Throttles           uint64  `json:"throttles"`
	AverageLatencyMS    float64 `json:"average_latency_ms"`
	P50LatencyMS        float64 `json:"p50_latency_ms"`
	P95LatencyMS        float64 `json:"p95_latency_ms"`
	LastLatencyMS       float64 `json:"last_latency_ms"`
	InputTokens         uint64  `json:"input_tokens"`
	OutputTokens        uint64  `json:"output_tokens"`
	Attempts            uint64  `json:"attempts"`
	Adoptions           uint64  `json:"adoptions"`
	AdoptionRate        float64 `json:"adoption_rate"`
	HedgeParticipations uint64  `json:"hedge_participations"`
	HedgeWins           uint64  `json:"hedge_wins"`
	HedgeWinRate        float64 `json:"hedge_win_rate"`
	DiscardedResponses  uint64  `json:"discarded_responses"`
	CooldownSeconds     float64 `json:"cooldown_seconds"`
	CooldownReason      string  `json:"cooldown_reason"`
	Unavailable         bool    `json:"unavailable"`
	UnavailableReason   string  `json:"unavailable_reason"`
}

type modelPool struct {
	mu                sync.Mutex
	states            []*modelState
	cursor            int
	selectionStrategy string
	safetyRatio       float64
	routeWait         time.Duration
}

func newModelPool(cfg config, unavailable map[string]unavailableRecord, persisted map[string]modelPersistentMetrics) *modelPool {
	states := make([]*modelState, 0, len(cfg.Models))
	for _, model := range cfg.Models {
		state := &modelState{Config: model}
		if metrics, exists := persisted[model.ID]; exists {
			state.Successes = metrics.Successes
			state.Failures = metrics.Failures
			state.Throttles = metrics.Throttles
			state.TotalLatencyMS = metrics.TotalLatencyMS
			state.InputTokens = metrics.InputTokens
			state.OutputTokens = metrics.OutputTokens
			state.Adoptions = metrics.Adoptions
			state.HedgeParticipations = metrics.HedgeParticipations
			state.HedgeWins = metrics.HedgeWins
			state.DiscardedResponses = metrics.DiscardedResponses
			state.LatenciesMS = append([]float64(nil), metrics.LatenciesMS...)
		}
		if saved, exists := unavailable[model.ID]; exists {
			state.Unavailable = true
			state.UnavailableReason = saved.Code
			if state.UnavailableReason == "" {
				state.UnavailableReason = saved.Message
			}
			if state.UnavailableReason == "" {
				state.UnavailableReason = "unavailable"
			}
		}
		states = append(states, state)
	}
	return &modelPool{states: states, selectionStrategy: cfg.SelectionStrategy, safetyRatio: cfg.RPMSafetyRatio, routeWait: time.Duration(cfg.RouteWaitSeconds * float64(time.Second))}
}

func purgeTimes(values []time.Time, cutoff time.Time) []time.Time {
	index := 0
	for index < len(values) && !values[index].After(cutoff) {
		index++
	}
	if index == 0 {
		return values
	}
	return append(values[:0], values[index:]...)
}

func (p *modelPool) purge(state *modelState, now time.Time) {
	state.RequestTimes = purgeTimes(state.RequestTimes, now.Add(-time.Minute))
	state.SecondTimes = purgeTimes(state.SecondTimes, now.Add(-time.Second))
}

func (p *modelPool) hasLocalCapacity(state *modelState, now time.Time) bool {
	p.purge(state, now)
	if state.Config.MinIntervalSeconds > 0 && len(state.RequestTimes) > 0 {
		minimum := time.Duration(state.Config.MinIntervalSeconds * float64(time.Second))
		if state.RequestTimes[len(state.RequestTimes)-1].After(now.Add(-minimum)) {
			return false
		}
	}
	rpm := max(1, int(float64(state.Config.RPM)*p.safetyRatio))
	rps := max(1, int((float64(state.Config.RPM)/60)*p.safetyRatio))
	return len(state.RequestTimes) < rpm && len(state.SecondTimes) < rps
}

type acquireOptions struct {
	Excluded                 map[string]bool
	RequireIncrementalStream bool
	ExcludeLowFrequency      bool
	ExcludeMT                bool
	Wait                     time.Duration
}

func (p *modelPool) acquire(options acquireOptions) *modelState {
	wait := options.Wait
	if wait < 0 {
		wait = p.routeWait
	}
	deadline := time.Now().Add(wait)
	for {
		now := time.Now()
		p.mu.Lock()
		type candidate struct {
			priority, offset, index int
			state                   *modelState
		}
		available := make([]candidate, 0, len(p.states))
		for offset := range p.states {
			index := (p.cursor + offset) % len(p.states)
			state := p.states[index]
			streamCompatible := state.Config.StreamCompatible == nil || *state.Config.StreamCompatible
			if options.Excluded[state.Config.ID] || !state.Config.Enabled || state.Unavailable || now.Before(state.CooldownUntil) || (options.RequireIncrementalStream && !streamCompatible) || (options.ExcludeLowFrequency && state.Config.RateClass == "low-frequency") || (options.ExcludeMT && state.Config.Adapter == "qwen-mt") || !p.hasLocalCapacity(state, now) {
				continue
			}
			available = append(available, candidate{state.Config.RoutingPriority, offset, index, state})
		}
		if len(available) > 0 {
			selected := available[0]
			if p.selectionStrategy == "random_within_priority" {
				bestPriority := selected.priority
				for _, item := range available {
					if item.priority < bestPriority {
						bestPriority = item.priority
					}
				}
				lowestInFlight := math.MaxInt
				for _, item := range available {
					if item.priority == bestPriority && item.state.InFlight < lowestInFlight {
						lowestInFlight = item.state.InFlight
					}
				}
				candidates := make([]candidate, 0, len(available))
				for _, item := range available {
					if item.priority == bestPriority && item.state.InFlight == lowestInFlight {
						candidates = append(candidates, item)
					}
				}
				selected = candidates[rand.Intn(len(candidates))]
			} else {
				for _, item := range available[1:] {
					if item.priority < selected.priority || (item.priority == selected.priority && item.offset < selected.offset) {
						selected = item
					}
				}
			}
			selected.state.RequestTimes = append(selected.state.RequestTimes, now)
			selected.state.SecondTimes = append(selected.state.SecondTimes, now)
			selected.state.InFlight++
			p.cursor = (selected.index + 1) % len(p.states)
			p.mu.Unlock()
			return selected.state
		}
		p.mu.Unlock()
		if !now.Before(deadline) {
			return nil
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func appendLimited(values []float64, value float64, limit int) []float64 {
	values = append(values, value)
	if len(values) > limit {
		values = append(values[:0], values[len(values)-limit:]...)
	}
	return values
}

func (p *modelPool) success(state *modelState, latencyMS float64, promptTokens, completionTokens uint64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state.InFlight = max(0, state.InFlight-1)
	state.Successes++
	state.TotalLatencyMS += latencyMS
	state.LatenciesMS = appendLimited(state.LatenciesMS, latencyMS, 200)
	state.InputTokens += promptTokens
	state.OutputTokens += completionTokens
	if !time.Now().Before(state.CooldownUntil) {
		state.CooldownUntil = time.Time{}
		state.CooldownReason = ""
	}
}

func (p *modelPool) failure(state *modelState, reason string, cooldown time.Duration, throttled bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state.InFlight = max(0, state.InFlight-1)
	state.Failures++
	if throttled {
		state.Throttles++
	}
	if cooldown > 0 {
		until := time.Now().Add(cooldown)
		if until.After(state.CooldownUntil) {
			state.CooldownUntil = until
			state.CooldownReason = reason
		}
	}
}

func (p *modelPool) markHedgeParticipation(state *modelState) {
	p.mu.Lock()
	state.HedgeParticipations++
	p.mu.Unlock()
}
func (p *modelPool) adopt(state *modelState, hedgeWinner bool) {
	p.mu.Lock()
	state.Adoptions++
	if hedgeWinner {
		state.HedgeWins++
	}
	p.mu.Unlock()
}
func (p *modelPool) discard(state *modelState) {
	p.mu.Lock()
	state.DiscardedResponses++
	p.mu.Unlock()
}
func (p *modelPool) disable(state *modelState, reason string) {
	p.mu.Lock()
	state.Unavailable = true
	state.UnavailableReason = reason
	state.CooldownUntil = time.Time{}
	state.CooldownReason = ""
	p.mu.Unlock()
}

func (p *modelPool) setEnabled(modelID string, enabled, clearUnavailable bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, state := range p.states {
		if state.Config.ID == modelID {
			state.Config.Enabled = enabled
			if enabled && clearUnavailable {
				state.Unavailable = false
				state.UnavailableReason = ""
				state.CooldownUntil = time.Time{}
				state.CooldownReason = ""
			}
			return true
		}
	}
	return false
}

func (p *modelPool) unavailableStatus(modelID string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, state := range p.states {
		if state.Config.ID == modelID {
			return state.Unavailable, true
		}
	}
	return false, false
}

func (p *modelPool) persistentSnapshot() []modelPersistentMetrics {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]modelPersistentMetrics, 0, len(p.states))
	for _, state := range p.states {
		result = append(result, modelPersistentMetrics{ID: state.Config.ID, Successes: state.Successes, Failures: state.Failures, Throttles: state.Throttles, TotalLatencyMS: state.TotalLatencyMS, InputTokens: state.InputTokens, OutputTokens: state.OutputTokens, Adoptions: state.Adoptions, HedgeParticipations: state.HedgeParticipations, HedgeWins: state.HedgeWins, DiscardedResponses: state.DiscardedResponses, LatenciesMS: append([]float64(nil), state.LatenciesMS...)})
	}
	return result
}

func rounded(value float64) float64 { return math.Round(value*10) / 10 }

func percentile(sortedValues []float64, fraction float64) float64 {
	if len(sortedValues) == 0 {
		return 0
	}
	index := max(0, int(math.Ceil(float64(len(sortedValues))*fraction))-1)
	return rounded(sortedValues[index])
}

func (p *modelPool) snapshot() []modelSnapshot {
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]modelSnapshot, 0, len(p.states))
	for _, state := range p.states {
		p.purge(state, now)
		latencies := append([]float64(nil), state.LatenciesMS...)
		sort.Float64s(latencies)
		attempts := state.Successes + state.Failures
		average, adoptionRate, hedgeWinRate, last := 0.0, 0.0, 0.0, 0.0
		if state.Successes > 0 {
			average = state.TotalLatencyMS / float64(state.Successes)
		}
		if attempts > 0 {
			adoptionRate = float64(state.Adoptions) / float64(attempts) * 100
		}
		if state.HedgeParticipations > 0 {
			hedgeWinRate = float64(state.HedgeWins) / float64(state.HedgeParticipations) * 100
		}
		if len(state.LatenciesMS) > 0 {
			last = state.LatenciesMS[len(state.LatenciesMS)-1]
		}
		cooldown := max(0, time.Until(state.CooldownUntil).Seconds())
		result = append(result, modelSnapshot{ID: state.Config.ID, Enabled: state.Config.Enabled, Role: state.Config.Role, RoutingPriority: state.Config.RoutingPriority, RPM: state.Config.RPM, TPM: state.Config.TPM, MinIntervalSeconds: state.Config.MinIntervalSeconds, InFlight: state.InFlight, RequestsLastMinute: len(state.RequestTimes), Successes: state.Successes, Failures: state.Failures, Throttles: state.Throttles, AverageLatencyMS: rounded(average), P50LatencyMS: percentile(latencies, .50), P95LatencyMS: percentile(latencies, .95), LastLatencyMS: rounded(last), InputTokens: state.InputTokens, OutputTokens: state.OutputTokens, Attempts: attempts, Adoptions: state.Adoptions, AdoptionRate: rounded(adoptionRate), HedgeParticipations: state.HedgeParticipations, HedgeWins: state.HedgeWins, HedgeWinRate: rounded(hedgeWinRate), DiscardedResponses: state.DiscardedResponses, CooldownSeconds: rounded(cooldown), CooldownReason: state.CooldownReason, Unavailable: state.Unavailable, UnavailableReason: state.UnavailableReason})
	}
	return result
}
