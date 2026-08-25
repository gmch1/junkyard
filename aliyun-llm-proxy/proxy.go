package main

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxRequestSize = 5 << 20

var retryableStatuses = map[int]bool{429: true, 500: true, 502: true, 503: true, 504: true}
var unavailableCodes = map[string]bool{"ModelNotFound": true, "Model.AccessDenied": true, "model_not_found": true}
var throttleCodes = map[string]bool{"Throttling": true, "Throttling.RateQuota": true, "Throttling.AllocationQuota": true, "Throttling.BurstRate": true, "Throttling.Concurrency": true, "LimitRequests": true, "limit_requests": true, "limit_burst_rate": true, "ResourceExhausted": true, "insufficient_quota": true}

type upstreamResponse struct {
	Status     int
	Headers    http.Header
	Body       []byte
	Stream     io.ReadCloser
	Prefetched []byte
}

type attemptResult struct {
	State               *modelState
	Lane                string
	Response            *upstreamResponse
	Success             bool
	Retry               bool
	ExcludeLowFrequency bool
	ExcludeMT           bool
}

type routeRace struct {
	mu       sync.Mutex
	resolved bool
	results  chan attemptResult
}

type proxy struct {
	cfg             config
	clientKey       string
	client          *http.Client
	pool            *modelPool
	unavailable     *unavailableStore
	metrics         *metricsStore
	startedAt       time.Time
	upstreamMu      sync.RWMutex
	upstreamKey     string
	metricsMu       sync.Mutex
	clientMetrics   clientPersistentMetrics
	processSampleAt time.Time
	processRSSMB    float64
	processCPU      float64
	configMu        sync.Mutex
	hedgeSlots      chan struct{}
	stopMetrics     chan struct{}
	metricsDone     chan struct{}
	closeOnce       sync.Once
}

func newProxy(cfg config, clientKey, upstreamKey string, unavailable *unavailableStore, metrics *metricsStore) (*proxy, error) {
	clientMetrics, persistedModels, err := metrics.load()
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 32
	transport.IdleConnTimeout = 90 * time.Second
	p := &proxy{
		cfg: cfg, clientKey: clientKey, upstreamKey: upstreamKey, unavailable: unavailable, metrics: metrics,
		client: &http.Client{Transport: transport, Timeout: time.Duration(cfg.RequestTimeoutSeconds * float64(time.Second))},
		pool:   newModelPool(cfg, unavailable.snapshot(), persistedModels), startedAt: time.Now(), clientMetrics: clientMetrics,
		hedgeSlots: make(chan struct{}, cfg.Hedging.MaxConcurrentBackups), stopMetrics: make(chan struct{}), metricsDone: make(chan struct{}),
	}
	go p.metricsLoop()
	return p, nil
}

func (p *proxy) authorized(header string) bool {
	expected := "Bearer " + p.clientKey
	header = strings.TrimSpace(header)
	return len(header) == len(expected) && subtle.ConstantTimeCompare([]byte(header), []byte(expected)) == 1
}

func (p *proxy) reloadUpstreamKey() error {
	value := readSecret("dashscope.key")
	if value == "" {
		return errors.New("DashScope API Key is empty")
	}
	p.upstreamMu.Lock()
	p.upstreamKey = value
	p.upstreamMu.Unlock()
	return nil
}

func (p *proxy) updateUpstreamKey(value string) error {
	if err := writeSecret("dashscope.key", value); err != nil {
		return err
	}
	return p.reloadUpstreamKey()
}

func parseUpstreamError(body []byte) (string, string) {
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil {
		text := string(body)
		if len(text) > 500 {
			text = text[:500]
		}
		return "", text
	}
	if nested, ok := payload["error"].(map[string]any); ok {
		payload = nested
	}
	code, _ := payload["code"].(string)
	message := fmt.Sprint(payload["message"])
	if message == "<nil>" {
		message = ""
	}
	return code, message
}

func retryAfter(headers http.Header, fallback float64) time.Duration {
	value, err := strconv.ParseFloat(strings.TrimSpace(headers.Get("Retry-After")), 64)
	if err != nil {
		value = fallback
	}
	value = min(300, max(.1, value))
	return time.Duration(value * float64(time.Second))
}

func classifyRetry(status int, code string, headers http.Header, defaultCooldown float64) (bool, time.Duration, bool) {
	code = strings.TrimSpace(code)
	if status == 429 || throttleCodes[code] || strings.HasPrefix(code, "Throttling") {
		fallback := defaultCooldown
		if strings.Contains(code, "BurstRate") || code == "limit_burst_rate" {
			fallback = 5
		} else if strings.Contains(code, "Concurrency") {
			fallback = 2
		}
		return true, retryAfter(headers, fallback), true
	}
	if retryableStatuses[status] {
		return true, retryAfter(headers, 5), false
	}
	if (status == 403 || status == 404) && unavailableCodes[code] {
		return true, retryAfter(headers, 300), false
	}
	return false, 0, false
}

func shouldDisableModel(status int, code, message string, model modelConfig) bool {
	codeLower, messageLower := strings.ToLower(code), strings.ToLower(message)
	if codeLower == "allocationquota.freetieronly" {
		return true
	}
	for _, marker := range []string{"free allocated quota exceeded", "free tier of the model has been exhausted", "hour allocated quota exceeded", "week allocated quota exceeded", "month allocated quota exceeded", "quota has been exhausted"} {
		if strings.Contains(messageLower, marker) {
			return true
		}
	}
	if model.DisableOnAllocationQuota && (strings.Contains(codeLower, "allocationquota") || strings.Contains(codeLower, "insufficient_quota")) {
		return true
	}
	return model.DisableOnAccessDenied && status == 403
}

func usageFromBody(body []byte) (uint64, uint64) {
	var payload struct {
		Usage struct {
			Prompt     uint64 `json:"prompt_tokens"`
			Completion uint64 `json:"completion_tokens"`
		} `json:"usage"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return 0, 0
	}
	return payload.Usage.Prompt, payload.Usage.Completion
}

func (p *proxy) upstreamRequest(body map[string]any, state *modelState, stream bool, timeout time.Duration) (*upstreamResponse, error) {
	payload, err := upstreamPayload(body, state)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	url := strings.TrimRight(p.cfg.UpstreamBaseURL, "/") + "/chat/completions"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	p.upstreamMu.RLock()
	key := p.upstreamKey
	p.upstreamMu.RUnlock()
	request.Header.Set("Authorization", "Bearer "+key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("User-Agent", "read-frog-aliyun-proxy/1.0")
	if stream {
		request.Header.Set("Accept", "text/event-stream")
	} else {
		request.Header.Set("Accept", "application/json")
	}
	response, err := p.client.Do(request)
	if err != nil {
		return nil, err
	}
	headers := response.Header.Clone()
	if response.StatusCode >= 200 && response.StatusCode < 300 && stream {
		buffer := make([]byte, 8192)
		count, readErr := response.Body.Read(buffer)
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			response.Body.Close()
			return nil, readErr
		}
		return &upstreamResponse{Status: response.StatusCode, Headers: headers, Stream: response.Body, Prefetched: append([]byte(nil), buffer[:count]...)}, nil
	}
	data, readErr := io.ReadAll(io.LimitReader(response.Body, maxRequestSize))
	response.Body.Close()
	if readErr != nil {
		return nil, readErr
	}
	return &upstreamResponse{Status: response.StatusCode, Headers: headers, Body: data}, nil
}

func (p *proxy) performAttempt(body map[string]any, state *modelState, stream bool, lane string) attemptResult {
	started := time.Now()
	response, err := p.upstreamRequest(body, state, stream, 0)
	latency := float64(time.Since(started).Microseconds()) / 1000
	if err != nil {
		var unsupported qwenMTUnsupportedLanguage
		if errors.As(err, &unsupported) {
			p.pool.failure(state, "unsupported_target_language", 0, false)
			log.Printf("model=%s lane=%s adapter=qwen-mt skipped=true target=%q", state.Config.ID, lane, unsupported.RawTarget)
			return attemptResult{State: state, Lane: lane, Retry: true, ExcludeMT: unsupported.LanguageCode == ""}
		}
		p.pool.failure(state, fmt.Sprintf("%T", err), 5*time.Second, false)
		log.Printf("model=%s lane=%s transport_error=%v fallback=true", state.Config.ID, lane, err)
		return attemptResult{State: state, Lane: lane, Retry: true}
	}
	if response.Status >= 200 && response.Status < 300 {
		prompt, completion := uint64(0), uint64(0)
		if !stream {
			prompt, completion = usageFromBody(response.Body)
		}
		p.pool.success(state, latency, prompt, completion)
		return attemptResult{State: state, Lane: lane, Response: response, Success: true}
	}
	code, message := parseUpstreamError(response.Body)
	mtError := isQwenMTLanguageError(response.Status, code, message, state.Config)
	permanent := shouldDisableModel(response.Status, code, message, state.Config)
	retry, cooldown, throttled := false, time.Duration(0), false
	if permanent || mtError {
		retry = true
	} else {
		retry, cooldown, throttled = classifyRetry(response.Status, code, response.Headers, p.cfg.DefaultCooldownSeconds)
	}
	reason := code
	if reason == "" {
		reason = fmt.Sprintf("HTTP %d", response.Status)
	}
	p.pool.failure(state, reason, cooldown, throttled)
	if permanent {
		if code != "" {
			reason = code
		} else if message != "" {
			reason = message
		}
		p.pool.disable(state, reason)
		if err := p.unavailable.mark(state.Config.ID, response.Status, code, message); err != nil {
			log.Printf("unavailable_state_write_failed model=%s error=%v", state.Config.ID, err)
		}
	}
	log.Printf("model=%s lane=%s status=%d code=%s cooldown=%s fallback=%t", state.Config.ID, lane, response.Status, code, cooldown, retry)
	return attemptResult{State: state, Lane: lane, Response: response, Retry: retry, ExcludeLowFrequency: throttled && state.Config.RateClass == "low-frequency", ExcludeMT: mtError}
}

func (p *proxy) discardAttempt(result attemptResult) {
	if result.Response != nil && result.Response.Stream != nil {
		_ = result.Response.Stream.Close()
	}
	p.pool.discard(result.State)
	log.Printf("model=%s lane=%s discarded=true", result.State.Config.ID, result.Lane)
}

func (p *proxy) publishAttempt(race *routeRace, result attemptResult) {
	race.mu.Lock()
	resolved := race.resolved
	if !resolved {
		race.results <- result
	}
	race.mu.Unlock()
	if resolved && result.Success {
		p.discardAttempt(result)
	}
}

func (p *proxy) attemptWorker(race *routeRace, body map[string]any, state *modelState, stream bool, lane string, hedgeSlot bool) {
	result := p.performAttempt(body, state, stream, lane)
	if hedgeSlot {
		<-p.hedgeSlots
	}
	p.publishAttempt(race, result)
}

func (p *proxy) resolveRace(race *routeRace) []attemptResult {
	race.mu.Lock()
	race.resolved = true
	race.mu.Unlock()
	results := []attemptResult{}
	for {
		select {
		case result := <-race.results:
			results = append(results, result)
		default:
			return results
		}
	}
}

func (p *proxy) route(body map[string]any, stream bool) (*upstreamResponse, *modelState, []string) {
	race := &routeRace{results: make(chan attemptResult, len(p.pool.states)+2)}
	excluded := map[string]bool{}
	attempts := []string{}
	var last *upstreamResponse
	active := 0
	hedgeChecked, hedgeLaunched := false, false
	excludeLow, excludeMT := false, false
	started := time.Now()
	hedgeDeadline := started.Add(time.Duration(p.cfg.Hedging.DelaySeconds * float64(time.Second)))
	startAttempt := func(lane string, wait time.Duration) bool {
		hedge := lane == "hedge"
		if hedge {
			select {
			case p.hedgeSlots <- struct{}{}:
			default:
				return false
			}
		}
		state := p.pool.acquire(acquireOptions{Excluded: excluded, RequireIncrementalStream: stream, ExcludeLowFrequency: excludeLow, ExcludeMT: excludeMT, Wait: wait})
		if state == nil {
			if hedge {
				<-p.hedgeSlots
			}
			return false
		}
		excluded[state.Config.ID] = true
		attempts = append(attempts, state.Config.ID)
		if hedge {
			p.pool.markHedgeParticipation(state)
		}
		active++
		go p.attemptWorker(race, body, state, stream, lane, hedge)
		return true
	}
	startAttempt("primary", -1)
	for active > 0 {
		var result attemptResult
		if p.cfg.Hedging.Enabled && !hedgeChecked {
			remaining := time.Until(hedgeDeadline)
			if remaining < 0 {
				remaining = 0
			}
			timer := time.NewTimer(remaining)
			select {
			case result = <-race.results:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
				hedgeChecked = true
				if startAttempt("hedge", 0) {
					hedgeLaunched = true
					p.metricsMu.Lock()
					p.clientMetrics.HedgedRequests++
					p.metricsMu.Unlock()
					log.Printf("hedge_started=true attempts=%s", strings.Join(attempts, ","))
				}
				continue
			}
		} else {
			result = <-race.results
		}
		active = max(0, active-1)
		if result.ExcludeLowFrequency {
			excludeLow = true
		}
		if result.ExcludeMT {
			excludeMT = true
		}
		if result.Success && result.Response != nil {
			queued := p.resolveRace(race)
			p.pool.adopt(result.State, result.Lane == "hedge")
			for _, other := range queued {
				if other.Success {
					p.discardAttempt(other)
				}
			}
			log.Printf("selected_model=%s lane=%s hedged=%t attempts=%s", result.State.Config.ID, result.Lane, hedgeLaunched, strings.Join(attempts, ","))
			return result.Response, result.State, attempts
		}
		if result.Response != nil {
			last = result.Response
		}
		if !result.Retry {
			queued := p.resolveRace(race)
			for _, other := range queued {
				if other.Success {
					p.discardAttempt(other)
				}
			}
			return result.Response, nil, attempts
		}
		wait := time.Duration(0)
		if active == 0 {
			wait = -1
		}
		startAttempt(result.Lane, wait)
	}
	p.resolveRace(race)
	if last != nil {
		return last, nil, attempts
	}
	bodyBytes, _ := json.Marshal(map[string]any{"error": map[string]any{"code": "proxy_model_pool_exhausted", "message": "All translation models are rate-limited, cooling down, or locally saturated.", "type": "proxy_error"}})
	return &upstreamResponse{Status: 429, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: bodyBytes}, nil, attempts
}

func (p *proxy) recordClientResponse(status int, latencyMS float64) {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	p.clientMetrics.Requests++
	if status >= 200 && status < 300 {
		p.clientMetrics.Successes++
	} else {
		p.clientMetrics.Failures++
	}
	p.clientMetrics.TotalLatencyMS += latencyMS
	p.clientMetrics.LatenciesMS = appendLimited(p.clientMetrics.LatenciesMS, latencyMS, 500)
}

func (p *proxy) persistentClientSnapshot() clientPersistentMetrics {
	p.metricsMu.Lock()
	defer p.metricsMu.Unlock()
	result := p.clientMetrics
	result.LatenciesMS = append([]float64(nil), result.LatenciesMS...)
	return result
}

func (p *proxy) flushMetrics() {
	if err := p.metrics.flush(p.persistentClientSnapshot(), p.pool.persistentSnapshot()); err != nil {
		log.Printf("metrics_flush_failed error=%v", err)
	}
}
func (p *proxy) metricsLoop() {
	defer close(p.metricsDone)
	ticker := time.NewTicker(time.Duration(p.cfg.MetricsFlushIntervalSeconds * float64(time.Second)))
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			p.flushMetrics()
		case <-p.stopMetrics:
			return
		}
	}
}
func (p *proxy) close() {
	p.closeOnce.Do(func() {
		close(p.stopMetrics)
		<-p.metricsDone
		p.flushMetrics()
		_ = p.metrics.close()
		p.client.CloseIdleConnections()
	})
}

type clientSnapshot struct {
	Requests         uint64  `json:"requests"`
	Successes        uint64  `json:"successes"`
	Failures         uint64  `json:"failures"`
	AverageLatencyMS float64 `json:"average_latency_ms"`
	P95LatencyMS     float64 `json:"p95_latency_ms"`
	LastLatencyMS    float64 `json:"last_latency_ms"`
}

func (p *proxy) clientSnapshot() clientSnapshot {
	metrics := p.persistentClientSnapshot()
	sortedValues := append([]float64(nil), metrics.LatenciesMS...)
	sort.Float64s(sortedValues)
	average, last := 0.0, 0.0
	if metrics.Requests > 0 {
		average = metrics.TotalLatencyMS / float64(metrics.Requests)
	}
	if len(metrics.LatenciesMS) > 0 {
		last = metrics.LatenciesMS[len(metrics.LatenciesMS)-1]
	}
	return clientSnapshot{metrics.Requests, metrics.Successes, metrics.Failures, rounded(average), percentile(sortedValues, .95), rounded(last)}
}

func (p *proxy) processMetrics() map[string]float64 {
	p.metricsMu.Lock()
	if time.Since(p.processSampleAt) < 2500*time.Millisecond {
		result := map[string]float64{"rss_mb": p.processRSSMB, "cpu_percent": p.processCPU}
		p.metricsMu.Unlock()
		return result
	}
	p.metricsMu.Unlock()

	output, err := exec.Command("/bin/ps", "-p", strconv.Itoa(os.Getpid()), "-o", "rss=,%cpu=").Output()
	if err == nil {
		fields := strings.Fields(string(output))
		if len(fields) >= 2 {
			rss, rssErr := strconv.ParseFloat(fields[0], 64)
			cpu, cpuErr := strconv.ParseFloat(fields[1], 64)
			if rssErr == nil && cpuErr == nil {
				p.metricsMu.Lock()
				p.processSampleAt = time.Now()
				p.processRSSMB = rounded(rss / 1024)
				p.processCPU = rounded(cpu)
				result := map[string]float64{"rss_mb": p.processRSSMB, "cpu_percent": p.processCPU}
				p.metricsMu.Unlock()
				return result
			}
		}
	}

	var memory runtime.MemStats
	runtime.ReadMemStats(&memory)
	p.metricsMu.Lock()
	if p.processRSSMB == 0 {
		p.processRSSMB = rounded(float64(memory.Sys) / (1024 * 1024))
	}
	result := map[string]float64{"rss_mb": p.processRSSMB, "cpu_percent": p.processCPU}
	p.metricsMu.Unlock()
	return result
}

func (p *proxy) status(baseURL string) map[string]any {
	models := p.pool.snapshot()
	client := p.clientSnapshot()
	var successes, failures, attempts, adoptions, hedgeWins, discarded, inFlight, requestsMinute, inputTokens, outputTokens, throttles uint64
	for _, model := range models {
		successes += model.Successes
		failures += model.Failures
		attempts += model.Attempts
		adoptions += model.Adoptions
		hedgeWins += model.HedgeWins
		discarded += model.DiscardedResponses
		inFlight += uint64(model.InFlight)
		requestsMinute += uint64(model.RequestsLastMinute)
		inputTokens += model.InputTokens
		outputTokens += model.OutputTokens
		throttles += model.Throttles
	}
	adoptionRate, hedgeWinRate := 0.0, 0.0
	if attempts > 0 {
		adoptionRate = float64(adoptions) / float64(attempts) * 100
	}
	p.metricsMu.Lock()
	hedgedRequests := p.clientMetrics.HedgedRequests
	p.metricsMu.Unlock()
	if hedgedRequests > 0 {
		hedgeWinRate = float64(hedgeWins) / float64(hedgedRequests) * 100
	}
	return map[string]any{"status": "running", "generated_at": time.Now().Format(time.RFC3339), "uptime_seconds": int(time.Since(p.startedAt).Seconds()), "base_url": baseURL, "model_alias": p.cfg.ModelAlias, "metrics_persistence": map[string]any{"enabled": true, "flush_interval_seconds": p.cfg.MetricsFlushIntervalSeconds, "last_flushed_at": p.metrics.lastFlush()}, "client": client, "process": p.processMetrics(), "hedging": map[string]any{"enabled": p.cfg.Hedging.Enabled, "delay_seconds": p.cfg.Hedging.DelaySeconds, "max_concurrent_backups": p.cfg.Hedging.MaxConcurrentBackups}, "totals": map[string]any{"model_successes": successes, "model_failures": failures, "upstream_attempts": attempts, "adoptions": adoptions, "adoption_rate": rounded(adoptionRate), "hedged_requests": hedgedRequests, "hedge_wins": hedgeWins, "hedge_win_rate": rounded(hedgeWinRate), "discarded_responses": discarded, "in_flight": inFlight, "requests_last_minute": requestsMinute, "input_tokens": inputTokens, "output_tokens": outputTokens, "throttles": throttles}, "models": models}
}

type modelProbeError struct {
	Status        int
	Code, Message string
}

func (e modelProbeError) Error() string {
	label := e.Code
	if label == "" {
		if e.Status > 0 {
			label = fmt.Sprintf("HTTP %d", e.Status)
		} else {
			label = "connection_error"
		}
	}
	detail := strings.TrimSpace(e.Message)
	if len(detail) > 300 {
		detail = detail[:300]
	}
	if detail != "" {
		return label + ": " + detail
	}
	return label
}

func (p *proxy) findModelConfig(modelID string) (modelConfig, bool) {
	p.configMu.Lock()
	defer p.configMu.Unlock()
	for _, model := range p.cfg.Models {
		if model.ID == modelID {
			return model, true
		}
	}
	return modelConfig{}, false
}

func (p *proxy) probeModel(modelID string) error {
	model, exists := p.findModelConfig(modelID)
	if !exists {
		return osErrNotExist
	}
	state := &modelState{Config: model}
	body := map[string]any{"model": modelID, "messages": []any{map[string]any{"role": "user", "content": "Translate to Chinese: Hello"}}, "temperature": 0, "max_tokens": 8, "stream": false}
	response, err := p.upstreamRequest(body, state, false, time.Duration(p.cfg.ModelProbeTimeoutSeconds*float64(time.Second)))
	if err != nil {
		return modelProbeError{Message: err.Error()}
	}
	if response.Status >= 200 && response.Status < 300 {
		return nil
	}
	code, message := parseUpstreamError(response.Body)
	return modelProbeError{Status: response.Status, Code: code, Message: message}
}

var osErrNotExist = errors.New("model not found")

func (p *proxy) updateModelEnabled(modelID string, enabled bool) (bool, error) {
	unavailable, exists := p.pool.unavailableStatus(modelID)
	if !exists {
		return false, osErrNotExist
	}
	probed := enabled && unavailable
	if probed {
		if err := p.probeModel(modelID); err != nil {
			return true, err
		}
	}
	p.configMu.Lock()
	index := -1
	enabledCount := 0
	for i, model := range p.cfg.Models {
		if model.Enabled {
			enabledCount++
		}
		if model.ID == modelID {
			index = i
		}
	}
	if index < 0 {
		p.configMu.Unlock()
		return probed, osErrNotExist
	}
	if !enabled && p.cfg.Models[index].Enabled && enabledCount <= 1 {
		p.configMu.Unlock()
		return probed, errors.New("at least one model must remain enabled")
	}
	p.cfg.Models[index].Enabled = enabled
	cfgCopy := p.cfg
	p.configMu.Unlock()
	if err := persistModelEnabled(cfgCopy, modelID, enabled); err != nil {
		return probed, err
	}
	if enabled {
		if _, err := p.unavailable.clear(modelID); err != nil {
			return probed, err
		}
	}
	p.pool.setEnabled(modelID, enabled, enabled)
	return probed, nil
}

func persistModelEnabled(runtimeConfig config, modelID string, enabled bool) error {
	persisted := runtimeConfig
	if data, err := os.ReadFile(statePath("proxy.json")); err == nil {
		candidate := defaultConfig()
		if json.Unmarshal(data, &candidate) == nil {
			persisted = candidate
		}
	}
	found := false
	for index := range persisted.Models {
		if persisted.Models[index].ID == modelID {
			persisted.Models[index].Enabled = enabled
			found = true
			break
		}
	}
	if !found {
		return osErrNotExist
	}
	return saveConfig(persisted)
}
