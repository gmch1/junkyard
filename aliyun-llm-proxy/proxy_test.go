package main

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testConfig(upstream string, models ...modelConfig) config {
	cfg := defaultConfig()
	cfg.Host = "127.0.0.1"
	cfg.Port = 39299
	cfg.UpstreamBaseURL = upstream
	cfg.RequestTimeoutSeconds = 3
	cfg.RouteWaitSeconds = .05
	cfg.MetricsFlushIntervalSeconds = 60
	cfg.Hedging = hedgeConfig{Enabled: false, DelaySeconds: .05, MaxConcurrentBackups: 2}
	cfg.SelectionStrategy = "round_robin"
	cfg.Models = models
	return cfg
}

func testModel(id string) modelConfig {
	return modelConfig{ID: id, Enabled: true, RPM: 60_000, TPM: 1_000_000, RoutingPriority: 10, Role: "test"}
}

func newTestProxy(t *testing.T, cfg config, upstreamKey string) *proxy {
	t.Helper()
	directory := t.TempDir()
	store := newUnavailableStore(filepath.Join(directory, "unavailable_models.json"))
	metrics, err := newMetricsStore(filepath.Join(directory, "metrics.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := newProxy(cfg, "ap-client", upstreamKey, store, metrics)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(proxy.close)
	return proxy
}

func TestSecretFilesAreAtomicAndPrivate(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ALIYUN_PROXY_STATE_DIR", directory)
	if err := writeSecret("dashscope.key", "sk-test-upstream-key"); err != nil {
		t.Fatal(err)
	}
	if got := readSecret("dashscope.key"); got != "sk-test-upstream-key" {
		t.Fatalf("secret = %q", got)
	}
	info, err := os.Stat(filepath.Join(directory, "dashscope.key"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode = %o", info.Mode().Perm())
	}
	key1, err := ensureClientKey()
	if err != nil {
		t.Fatal(err)
	}
	key2, _ := ensureClientKey()
	if key1 != key2 || !strings.HasPrefix(key1, "ap-") {
		t.Fatalf("client keys = %q, %q", key1, key2)
	}
}

func TestConfigMigrationAddsNewModelsWithoutDroppingOverrides(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ALIYUN_PROXY_STATE_DIR", directory)
	legacy := config{
		Version: 8, Host: "127.0.0.1", Port: 39282, UpstreamBaseURL: "https://example.test/v1",
		ModelAlias: "custom-alias", RequestTimeoutSeconds: 30, RouteWaitSeconds: 1,
		RPMSafetyRatio: .8, DefaultCooldownSeconds: 15, SelectionStrategy: "round_robin",
		MetricsFlushIntervalSeconds: 5, Hedging: hedgeConfig{Enabled: true, DelaySeconds: 2, MaxConcurrentBackups: 1},
		Models: []modelConfig{{ID: "custom-model", Enabled: true, RPM: 123, RoutingPriority: 3}},
	}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(filepath.Join(directory, "proxy.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	resolved, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Version != configVersion || resolved.Models[0].ID != "custom-model" || resolved.Models[0].RPM != 123 {
		t.Fatalf("migration lost override: %#v", resolved.Models[0])
	}
	found := false
	for _, model := range resolved.Models {
		if model.ID == "qwen-mt-flash" {
			found = true
		}
	}
	if !found {
		t.Fatal("new default models were not appended")
	}
}

func TestLocalServiceReadinessDoesNotRequireDisabledDashboard(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	})}
	go server.Serve(listener)
	defer server.Close()
	cfg := testConfig("https://example.invalid", testModel("one"))
	cfg.Port = listener.Addr().(*net.TCPAddr).Port
	cfg.DashboardEnabled = false
	if !localServiceReady(cfg) {
		t.Fatal("healthy service with disabled dashboard was considered unavailable")
	}
	if got := localURL(config{Host: "::1", Port: 39281}, "/health"); got != "http://[::1]:39281/health" {
		t.Fatalf("IPv6 URL = %q", got)
	}
}

func TestModelPoolHonorsPriorityStreamAndMinimumInterval(t *testing.T) {
	streamNo := false
	cfg := testConfig("https://example.invalid",
		modelConfig{ID: "mt", Enabled: true, RPM: 60, RoutingPriority: 0, MinIntervalSeconds: 30, StreamCompatible: &streamNo},
		modelConfig{ID: "fallback", Enabled: true, RPM: 60_000, RoutingPriority: 10},
	)
	pool := newModelPool(cfg, nil, nil)
	state := pool.acquire(acquireOptions{Excluded: map[string]bool{}, Wait: 0})
	if state == nil || state.Config.ID != "mt" {
		t.Fatalf("first model = %#v", state)
	}
	pool.failure(state, "test", 0, false)
	state = pool.acquire(acquireOptions{Excluded: map[string]bool{}, Wait: 0})
	if state == nil || state.Config.ID != "fallback" {
		t.Fatalf("interval fallback = %#v", state)
	}
	pool.failure(state, "test", 0, false)
	state = pool.acquire(acquireOptions{Excluded: map[string]bool{}, RequireIncrementalStream: true, Wait: 0})
	if state == nil || state.Config.ID != "fallback" {
		t.Fatalf("stream fallback = %#v", state)
	}
}

func TestProxyRetriesThrottledModelAndPreservesPayload(t *testing.T) {
	var mu sync.Mutex
	attempts := []string{}
	var receivedTemperature float64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		mu.Lock()
		attempts = append(attempts, model)
		receivedTemperature, _ = body["temperature"].(float64)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if model == "one" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"Throttling.RateQuota","message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[],"usage":{"prompt_tokens":4,"completion_tokens":2}}`)
	}))
	defer upstream.Close()
	proxy := newTestProxy(t, testConfig(upstream.URL, testModel("one"), testModel("two")), "sk-upstream")
	response, state, routed := proxy.route(map[string]any{
		"model": "caller", "messages": []any{map[string]any{"role": "user", "content": "hello"}}, "temperature": .25,
	}, false)
	if response.Status != http.StatusOK || state == nil || state.Config.ID != "two" {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	if strings.Join(routed, ",") != "one,two" {
		t.Fatalf("routed = %v", routed)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(attempts, ",") != "one,two" || receivedTemperature != .25 {
		t.Fatalf("attempts=%v temperature=%v", attempts, receivedTemperature)
	}
}

func TestSlowPrimaryLaunchesHedgeAndFastestWins(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "slow" {
			time.Sleep(180 * time.Millisecond)
		} else {
			time.Sleep(10 * time.Millisecond)
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	cfg := testConfig(upstream.URL, testModel("slow"), testModel("fast"))
	cfg.Hedging.Enabled = true
	cfg.Hedging.DelaySeconds = .04
	proxy := newTestProxy(t, cfg, "sk-upstream")
	response, state, attempts := proxy.route(map[string]any{"messages": []any{}}, false)
	if response.Status != 200 || state == nil || state.Config.ID != "fast" || strings.Join(attempts, ",") != "slow,fast" {
		t.Fatalf("status=%d state=%#v attempts=%v", response.Status, state, attempts)
	}
	deadline := time.Now().Add(time.Second)
	for proxy.pool.snapshot()[0].InFlight != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	snapshots := proxy.pool.snapshot()
	if snapshots[1].HedgeWins != 1 || snapshots[0].InFlight != 0 || snapshots[0].Successes != 0 || snapshots[0].Failures != 0 || snapshots[0].CooldownSeconds != 0 {
		t.Fatalf("hedge metrics = %#v", snapshots)
	}
}

func TestStreamingHedgeSelectsFirstContentAndPreservesStream(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, _ := w.(http.Flusher)
		if body["model"] == "slow" {
			time.Sleep(160 * time.Millisecond)
			_, _ = io.WriteString(w, "data: slow\n\n")
		} else {
			time.Sleep(10 * time.Millisecond)
			_, _ = io.WriteString(w, "data: fast-first\n\n")
			flusher.Flush()
			time.Sleep(10 * time.Millisecond)
			_, _ = io.WriteString(w, "data: fast-last\n\n")
		}
		flusher.Flush()
	}))
	defer upstream.Close()
	cfg := testConfig(upstream.URL, testModel("slow"), testModel("fast"))
	cfg.Hedging.Enabled = true
	cfg.Hedging.DelaySeconds = .03
	proxy := newTestProxy(t, cfg, "sk-upstream")
	response, state, attempts := proxy.route(map[string]any{"messages": []any{}, "stream": true}, true)
	if state == nil || state.Config.ID != "fast" || strings.Join(attempts, ",") != "slow,fast" {
		t.Fatalf("state=%#v attempts=%v", state, attempts)
	}
	remainder, err := io.ReadAll(response.Stream)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Stream.Close()
	content := string(response.Prefetched) + string(remainder)
	if content != "data: fast-first\n\ndata: fast-last\n\n" {
		t.Fatalf("stream = %q", content)
	}
}

func TestConcurrentRequestsReserveDifferentModels(t *testing.T) {
	arrived := make(chan string, 2)
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		arrived <- body["model"].(string)
		<-release
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	proxy := newTestProxy(t, testConfig(upstream.URL, testModel("one"), testModel("two")), "sk-upstream")
	var wait sync.WaitGroup
	wait.Add(2)
	for range 2 {
		go func() {
			defer wait.Done()
			proxy.route(map[string]any{"messages": []any{}}, false)
		}()
	}
	first, second := <-arrived, <-arrived
	close(release)
	wait.Wait()
	if first == second {
		t.Fatalf("both requests selected %s", first)
	}
}

func TestAllocationQuotaPermanentlyDisablesModel(t *testing.T) {
	directory := t.TempDir()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["model"] == "probe" {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"AllocationQuota.FreeTierOnly","message":"quota exhausted"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()
	cfg := testConfig(upstream.URL,
		modelConfig{ID: "probe", Enabled: true, RPM: 60_000, RoutingPriority: 10, DisableOnAllocationQuota: true},
		testModel("fallback"),
	)
	store := newUnavailableStore(filepath.Join(directory, "unavailable.json"))
	metrics, _ := newMetricsStore(filepath.Join(directory, "metrics.sqlite3"))
	proxy, err := newProxy(cfg, "ap-client", "sk-upstream", store, metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.close()
	response, state, _ := proxy.route(map[string]any{"messages": []any{}}, false)
	if response.Status != 200 || state == nil || state.Config.ID != "fallback" {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	if !proxy.pool.snapshot()[0].Unavailable {
		t.Fatal("quota-exhausted model stayed available")
	}
	if _, exists := store.snapshot()["probe"]; !exists {
		t.Fatal("unavailable state was not persisted")
	}
}

func TestQwenMTAdapterIsOnlyPayloadException(t *testing.T) {
	messages := []any{
		map[string]any{"role": "system", "content": "Translate into Traditional Mandarin Chinese."},
		map[string]any{"role": "user", "content": "Translate to Traditional Mandarin Chinese: Hello"},
	}
	body := map[string]any{"messages": messages, "temperature": .7, "stream": false}
	mtState := &modelState{Config: modelConfig{ID: "qwen-mt-flash", Adapter: "qwen-mt", DefaultTargetLanguage: "Chinese"}}
	payload, err := upstreamPayload(body, mtState)
	if err != nil {
		t.Fatal(err)
	}
	options, _ := payload["translation_options"].(map[string]string)
	translatedMessages, _ := payload["messages"].([]map[string]any)
	if options["target_lang"] != "zh_tw" || translatedMessages[0]["content"] != "Hello" {
		t.Fatalf("MT payload = %#v", payload)
	}
	if _, exists := payload["temperature"]; exists {
		t.Fatal("MT adapter leaked unsupported caller option")
	}
	general := &modelState{Config: testModel("general")}
	generalPayload, err := upstreamPayload(body, general)
	if err != nil || generalPayload["temperature"] != .7 || generalPayload["messages"] == nil {
		t.Fatalf("general payload = %#v, %v", generalPayload, err)
	}
	if qwenMTModelSupports("qwen-mt-lite", "ro") {
		t.Fatal("qwen-mt-lite unexpectedly supports full-only language")
	}
}

func TestDashboardAuthenticationAndConfiguration(t *testing.T) {
	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	proxy := newTestProxy(t, testConfig("https://example.invalid", testModel("one")), "")
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}

	home := httptest.NewRequest(http.MethodGet, "/", nil)
	home.RemoteAddr = "127.0.0.1:50000"
	homeResult := httptest.NewRecorder()
	server.ServeHTTP(homeResult, home)
	if homeResult.Code != 200 || !strings.Contains(homeResult.Body.String(), "root") {
		t.Fatalf("dashboard status=%d body=%q", homeResult.Code, homeResult.Body.String())
	}

	remote := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	remote.RemoteAddr = "192.168.1.9:50000"
	remoteResult := httptest.NewRecorder()
	server.ServeHTTP(remoteResult, remote)
	if remoteResult.Code != http.StatusForbidden {
		t.Fatalf("remote status = %d", remoteResult.Code)
	}

	models := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	models.RemoteAddr = "192.168.1.9:50000"
	modelsResult := httptest.NewRecorder()
	server.ServeHTTP(modelsResult, models)
	if modelsResult.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", modelsResult.Code)
	}

	save := httptest.NewRequest(http.MethodPost, "/admin/upstream-key", strings.NewReader(`{"api_key":"sk-test-upstream-key"}`))
	save.RemoteAddr = "[::1]:50000"
	save.Header.Set("X-Aliyun-Proxy-Admin", "1")
	saveResult := httptest.NewRecorder()
	server.ServeHTTP(saveResult, save)
	if saveResult.Code != 200 || readSecret("dashscope.key") != "sk-test-upstream-key" {
		t.Fatalf("save status=%d body=%s", saveResult.Code, saveResult.Body.String())
	}
}

func TestMetricsPersistAcrossProxyInstances(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "metrics.sqlite3")
	cfg := testConfig("https://example.invalid", testModel("one"))
	metrics, err := newMetricsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	first, err := newProxy(cfg, "ap-client", "sk-upstream", newUnavailableStore(filepath.Join(directory, "unavailable.json")), metrics)
	if err != nil {
		t.Fatal(err)
	}
	first.recordClientResponse(200, 12)
	first.pool.success(first.pool.states[0], 8, 3, 2)
	first.pool.adopt(first.pool.states[0], false)
	first.close()

	metrics, err = newMetricsStore(path)
	if err != nil {
		t.Fatal(err)
	}
	second, err := newProxy(cfg, "ap-client", "sk-upstream", newUnavailableStore(filepath.Join(directory, "unavailable.json")), metrics)
	if err != nil {
		t.Fatal(err)
	}
	defer second.close()
	status := second.status("http://127.0.0.1:39299/v1")
	client := status["client"].(clientSnapshot)
	models := status["models"].([]modelSnapshot)
	if client.Requests != 1 || models[0].Successes != 1 || models[0].InputTokens != 3 || models[0].Adoptions != 1 {
		t.Fatalf("persisted client=%#v model=%#v", client, models[0])
	}
}

func TestDashboardCanDisableModelButNotLastEnabledModel(t *testing.T) {
	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	cfg := testConfig("https://example.invalid", testModel("one"), testModel("two"))
	proxy := newTestProxy(t, cfg, "sk-upstream")
	server, _ := newAPIServer(proxy)
	toggle := func(model string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/proxy/models/enabled", strings.NewReader(`{"model":"`+model+`","enabled":false}`))
		request.RemoteAddr = "127.0.0.1:50000"
		request.Header.Set("X-Proxy-Dashboard", "1")
		result := httptest.NewRecorder()
		server.ServeHTTP(result, request)
		return result
	}
	if result := toggle("one"); result.Code != 200 {
		t.Fatalf("first disable = %d, %s", result.Code, result.Body.String())
	}
	if result := toggle("two"); result.Code != http.StatusConflict {
		t.Fatalf("last disable = %d, %s", result.Code, result.Body.String())
	}
}
