package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type regressionRoundTripper func(*http.Request) (*http.Response, error)

func (roundTrip regressionRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}

func regressionResponse(status int, body string, headers http.Header) *http.Response {
	if headers == nil {
		headers = make(http.Header)
	}
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func regressionModelSnapshots(proxy *proxy) map[string]modelSnapshot {
	result := make(map[string]modelSnapshot)
	for _, snapshot := range proxy.pool.snapshot() {
		result[snapshot.ID] = snapshot
	}
	return result
}

func TestProxyRegressionUnauthorizedUpstreamIsNotRetried(t *testing.T) {
	proxy := newTestProxy(t, testConfig("http://upstream.invalid", testModel("model-a"), testModel("model-b")), "bad-key")
	var calls atomic.Int64
	models := make(chan string, 2)
	proxy.client = &http.Client{Transport: regressionRoundTripper(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		models <- fmt.Sprint(body["model"])
		return regressionResponse(http.StatusUnauthorized, `{"error":{"code":"InvalidApiKey","message":"bad key"}}`, nil), nil
	})}

	response, selected, attempts := proxy.route(map[string]any{
		"model":    "caller-alias",
		"messages": []any{map[string]any{"role": "user", "content": "text"}},
	}, false)

	if response == nil || response.Status != http.StatusUnauthorized {
		t.Fatalf("response = %#v", response)
	}
	if selected != nil {
		t.Fatalf("selected model = %#v", selected)
	}
	if got := strings.Join(attempts, ","); got != "model-a" {
		t.Fatalf("attempts = %q", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("upstream calls = %d", got)
	}
	select {
	case model := <-models:
		if model != "model-a" {
			t.Fatalf("upstream model = %q", model)
		}
	default:
		t.Fatal("upstream model was not recorded")
	}

	snapshots := regressionModelSnapshots(proxy)
	first, second := snapshots["model-a"], snapshots["model-b"]
	if first.Attempts != 1 || first.Failures != 1 || first.Throttles != 0 || first.Adoptions != 0 {
		t.Fatalf("model-a metrics = %#v", first)
	}
	if first.CooldownSeconds != 0 {
		t.Fatalf("401 unexpectedly cooled model-a for %.1fs", first.CooldownSeconds)
	}
	if second.Attempts != 0 {
		t.Fatalf("model-b was retried: %#v", second)
	}
}

func TestProxyRegressionLowFrequencyThrottleSkipsPeersAndCoolsModel(t *testing.T) {
	lowFrequencyModel := func(id string) modelConfig {
		return modelConfig{
			ID: id, Enabled: true, RPM: 60_000, TPM: 1_000_000,
			MinIntervalSeconds: 30, RoutingPriority: 0, RateClass: "low-frequency", Role: "test",
		}
	}
	cfg := testConfig("http://upstream.invalid", lowFrequencyModel("low-a"), lowFrequencyModel("low-b"), testModel("general"))
	proxy := newTestProxy(t, cfg, "sk-upstream")

	var bodiesMu sync.Mutex
	receivedBodies := make([]map[string]any, 0, 2)
	proxy.client = &http.Client{Transport: regressionRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		bodiesMu.Lock()
		receivedBodies = append(receivedBodies, body)
		bodiesMu.Unlock()
		if body["model"] == "low-a" {
			return regressionResponse(http.StatusTooManyRequests, `{"error":{"code":"Throttling.RateQuota","message":"limited"}}`, http.Header{"Retry-After": []string{"30"}}), nil
		}
		return regressionResponse(http.StatusOK, `{"model":"general","choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`, nil), nil
	})}

	requestBody := map[string]any{
		"model": "caller-can-use-any-name",
		"messages": []any{
			map[string]any{"role": "system", "content": "Preserve paragraph formatting."},
			map[string]any{"role": "user", "content": "Translate to Chinese:\n\nHello"},
		},
		"temperature": .25,
		"max_tokens":  321,
		"top_p":       .9,
		"stream":      false,
	}
	response, selected, attempts := proxy.route(requestBody, false)
	if response == nil || response.Status != http.StatusOK || selected == nil || selected.Config.ID != "general" {
		t.Fatalf("response=%#v selected=%#v", response, selected)
	}
	if got := strings.Join(attempts, ","); got != "low-a,general" {
		t.Fatalf("attempts = %q", got)
	}
	if requestBody["model"] != "caller-can-use-any-name" {
		t.Fatalf("caller body was mutated: %#v", requestBody)
	}

	expectedBytes, err := json.Marshal(requestBody)
	if err != nil {
		t.Fatal(err)
	}
	var canonicalRequest map[string]any
	if err := json.Unmarshal(expectedBytes, &canonicalRequest); err != nil {
		t.Fatal(err)
	}
	bodiesMu.Lock()
	bodies := append([]map[string]any(nil), receivedBodies...)
	bodiesMu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("upstream bodies = %#v", bodies)
	}
	for index, model := range []string{"low-a", "general"} {
		expected := make(map[string]any, len(canonicalRequest))
		for key, value := range canonicalRequest {
			expected[key] = value
		}
		expected["model"] = model
		if !reflect.DeepEqual(bodies[index], expected) {
			t.Fatalf("body %d = %#v, want %#v", index, bodies[index], expected)
		}
	}

	snapshots := regressionModelSnapshots(proxy)
	throttled, skipped, fallback := snapshots["low-a"], snapshots["low-b"], snapshots["general"]
	if throttled.Attempts != 1 || throttled.Failures != 1 || throttled.Throttles != 1 {
		t.Fatalf("low-a metrics = %#v", throttled)
	}
	if throttled.CooldownSeconds < 25 || throttled.CooldownReason != "Throttling.RateQuota" {
		t.Fatalf("low-a cooldown = %.1fs, reason=%q", throttled.CooldownSeconds, throttled.CooldownReason)
	}
	if skipped.Attempts != 0 {
		t.Fatalf("low-frequency peer was attempted: %#v", skipped)
	}
	if fallback.Successes != 1 || fallback.Adoptions != 1 || fallback.InputTokens != 4 || fallback.OutputTokens != 2 {
		t.Fatalf("general metrics = %#v", fallback)
	}
}

func TestProxyRegressionPrimaryCanWinAfterHedgeStarts(t *testing.T) {
	cfg := testConfig("http://upstream.invalid", testModel("primary"), testModel("backup"))
	cfg.Hedging.Enabled = true
	cfg.Hedging.DelaySeconds = .01
	proxy := newTestProxy(t, cfg, "sk-upstream")

	primaryStarted := make(chan struct{}, 1)
	backupStarted := make(chan struct{}, 1)
	allowPrimary := make(chan struct{})
	allowBackup := make(chan struct{})
	var releasePrimaryOnce, releaseBackupOnce sync.Once
	releasePrimary := func() { releasePrimaryOnce.Do(func() { close(allowPrimary) }) }
	releaseBackup := func() { releaseBackupOnce.Do(func() { close(allowBackup) }) }
	t.Cleanup(releasePrimary)
	t.Cleanup(releaseBackup)

	proxy.client = &http.Client{Transport: regressionRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		model := fmt.Sprint(body["model"])
		switch model {
		case "primary":
			primaryStarted <- struct{}{}
			<-allowPrimary
		case "backup":
			backupStarted <- struct{}{}
			<-allowBackup
		default:
			return nil, fmt.Errorf("unexpected model %q", model)
		}
		return regressionResponse(http.StatusOK, `{"model":"`+model+`","choices":[]}`, nil), nil
	})}

	type routeResult struct {
		response *upstreamResponse
		selected *modelState
		attempts []string
	}
	routeDone := make(chan routeResult, 1)
	go func() {
		response, selected, attempts := proxy.route(map[string]any{"messages": []any{}}, false)
		routeDone <- routeResult{response: response, selected: selected, attempts: attempts}
	}()

	await := func(name string, signal <-chan struct{}) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(time.Second):
			t.Fatalf("timed out waiting for %s", name)
		}
	}
	await("primary request", primaryStarted)
	await("hedge request", backupStarted)
	releasePrimary()

	var result routeResult
	select {
	case result = <-routeDone:
	case <-time.After(time.Second):
		t.Fatal("primary did not complete the route")
	}
	if result.response == nil || result.response.Status != http.StatusOK || result.selected == nil || result.selected.Config.ID != "primary" {
		t.Fatalf("route result = %#v", result)
	}
	if got := strings.Join(result.attempts, ","); got != "primary,backup" {
		t.Fatalf("attempts = %q", got)
	}

	releaseBackup()
	deadline := time.Now().Add(time.Second)
	for {
		if regressionModelSnapshots(proxy)["backup"].DiscardedResponses == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("completed hedge response was not discarded")
		}
		time.Sleep(time.Millisecond)
	}

	snapshots := regressionModelSnapshots(proxy)
	primary, backup := snapshots["primary"], snapshots["backup"]
	if primary.Successes != 1 || primary.Adoptions != 1 || primary.HedgeWins != 0 || primary.DiscardedResponses != 0 {
		t.Fatalf("primary metrics = %#v", primary)
	}
	if backup.Successes != 1 || backup.Adoptions != 0 || backup.HedgeParticipations != 1 || backup.HedgeWins != 0 || backup.DiscardedResponses != 1 {
		t.Fatalf("backup metrics = %#v", backup)
	}
	if got := proxy.persistentClientSnapshot().HedgedRequests; got != 1 {
		t.Fatalf("hedged requests = %d", got)
	}
}

func TestProxyRegressionWinnerCancelsHedgeLoser(t *testing.T) {
	cfg := testConfig("http://upstream.invalid", testModel("primary"), testModel("backup"))
	cfg.Hedging.Enabled = true
	cfg.Hedging.DelaySeconds = .01
	proxy := newTestProxy(t, cfg, "sk-upstream")

	primaryStarted := make(chan struct{})
	backupStarted := make(chan struct{})
	backupCanceled := make(chan struct{})
	proxy.client = &http.Client{Transport: regressionRoundTripper(func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		if body["model"] == "primary" {
			close(primaryStarted)
			<-backupStarted
			return regressionResponse(http.StatusOK, `{"choices":[]}`, nil), nil
		}
		if body["model"] == "backup" {
			close(backupStarted)
			<-request.Context().Done()
			close(backupCanceled)
			return nil, request.Context().Err()
		}
		return nil, fmt.Errorf("unexpected model %v", body["model"])
	})}

	response, selected, attempts := proxy.route(map[string]any{"messages": []any{}}, false)
	if response == nil || response.Status != http.StatusOK || selected == nil || selected.Config.ID != "primary" {
		t.Fatalf("response=%#v selected=%#v", response, selected)
	}
	if got := strings.Join(attempts, ","); got != "primary,backup" {
		t.Fatalf("attempts=%q", got)
	}
	select {
	case <-primaryStarted:
	default:
		t.Fatal("primary did not start")
	}
	select {
	case <-backupCanceled:
	case <-time.After(time.Second):
		t.Fatal("winning response did not cancel the hedge loser")
	}

	deadline := time.Now().Add(time.Second)
	for {
		snapshots := regressionModelSnapshots(proxy)
		if snapshots["backup"].InFlight == 0 && len(proxy.hedgeSlots) == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("loser resources not released: backup=%#v hedge_slots=%d", snapshots["backup"], len(proxy.hedgeSlots))
		}
		time.Sleep(time.Millisecond)
	}
	snapshots := regressionModelSnapshots(proxy)
	if backup := snapshots["backup"]; backup.Failures != 0 || backup.CooldownSeconds != 0 || backup.Attempts != 0 || backup.HedgeParticipations != 1 {
		t.Fatalf("canceled loser changed metrics: %#v", backup)
	}
	if primary := snapshots["primary"]; primary.Successes != 1 || primary.Adoptions != 1 || primary.HedgeWins != 0 {
		t.Fatalf("winner metrics=%#v", primary)
	}
}
