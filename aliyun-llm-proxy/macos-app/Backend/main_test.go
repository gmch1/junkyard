package main // macOS bundled backend

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func testConfig(upstream string, models ...string) config {
	configured := make([]modelConfig, 0, len(models))
	for _, id := range models {
		configured = append(configured, modelConfig{ID: id, Enabled: true})
	}
	return config{
		Version:               1,
		Host:                  "127.0.0.1",
		Port:                  defaultPort,
		UpstreamBaseURL:       upstream,
		ModelAlias:            modelAlias,
		RequestTimeoutSeconds: 5,
		Models:                configured,
	}
}

func TestSecretFilesAreAtomicAndPrivate(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("ALIYUN_PROXY_STATE_DIR", directory)

	if err := writeSecret("dashscope.key", "sk-test-upstream-key"); err != nil {
		t.Fatal(err)
	}
	if value := readSecret("dashscope.key"); value != "sk-test-upstream-key" {
		t.Fatalf("unexpected secret %q", value)
	}
	info, err := os.Stat(filepath.Join(directory, "dashscope.key"))
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("secret mode = %o, want 600", mode)
	}
	directoryInfo, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if mode := directoryInfo.Mode().Perm(); mode != 0o700 {
		t.Fatalf("state directory mode = %o, want 700", mode)
	}
}

func TestClientKeyIsGeneratedOnce(t *testing.T) {
	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	first, err := ensureClientKey()
	if err != nil {
		t.Fatal(err)
	}
	second, err := ensureClientKey()
	if err != nil {
		t.Fatal(err)
	}
	if first != second || !strings.HasPrefix(first, "ap-") || len(first) < 32 {
		t.Fatalf("unexpected client key behavior: %q %q", first, second)
	}
}

func TestCurrentProcessIsRecognized(t *testing.T) {
	if !sameExecutable(os.Getpid()) {
		t.Fatal("current backend process was not recognized")
	}
}

func TestProxyReplacesModelAndRequiresClientKey(t *testing.T) {
	var receivedModel string
	var receivedAuthorization string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuthorization = r.Header.Get("Authorization")
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		receivedModel, _ = body["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer upstream.Close()

	svc := newService(testConfig(upstream.URL, "qwen-test"), "sk-upstream")
	handler := svc.handler("ap-client")
	requestBody := `{"model":"caller-model","messages":[{"role":"user","content":"hello"}]}`

	unauthorized := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	unauthorizedResult := httptest.NewRecorder()
	handler.ServeHTTP(unauthorizedResult, unauthorized)
	if unauthorizedResult.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorizedResult.Code)
	}

	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(requestBody))
	request.Header.Set("Authorization", "Bearer ap-client")
	result := httptest.NewRecorder()
	handler.ServeHTTP(result, request)
	if result.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", result.Code, result.Body.String())
	}
	if receivedModel != "qwen-test" {
		t.Fatalf("upstream model = %q", receivedModel)
	}
	if receivedAuthorization != "Bearer sk-upstream" {
		t.Fatalf("upstream authorization = %q", receivedAuthorization)
	}
	if result.Header().Get("X-Proxy-Model") != "qwen-test" {
		t.Fatalf("proxy model header = %q", result.Header().Get("X-Proxy-Model"))
	}
}

func TestProxyRetriesThrottledModel(t *testing.T) {
	var mu sync.Mutex
	var attempts []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		model, _ := body["model"].(string)
		mu.Lock()
		attempts = append(attempts, model)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if model == "model-one" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"code":"Throttling.RateQuota","message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	svc := newService(testConfig(upstream.URL, "model-one", "model-two"), "sk-upstream")
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[]}`))
	request.Header.Set("Authorization", "Bearer ap-client")
	result := httptest.NewRecorder()
	svc.handler("ap-client").ServeHTTP(result, request)

	if result.Code != http.StatusOK {
		t.Fatalf("proxy status = %d, body = %s", result.Code, result.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(attempts, ",") != "model-one,model-two" {
		t.Fatalf("attempts = %v", attempts)
	}
	if result.Header().Get("X-Proxy-Attempts") != "model-one,model-two" {
		t.Fatalf("attempt header = %q", result.Header().Get("X-Proxy-Attempts"))
	}
}

func TestInvalidKeyIsRejected(t *testing.T) {
	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	if err := writeSecret("dashscope.key", "short"); err == nil {
		t.Fatal("short API key was accepted")
	}
}

func TestManagementPageConfiguresAnUninitializedServiceFromLoopback(t *testing.T) {
	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	svc := newService(testConfig("https://example.invalid", "model-one"), "")
	handler := svc.handler("ap-client")

	home := httptest.NewRequest(http.MethodGet, "/", nil)
	home.RemoteAddr = "127.0.0.1:49321"
	homeResult := httptest.NewRecorder()
	handler.ServeHTTP(homeResult, home)
	if homeResult.Code != http.StatusOK || !strings.Contains(homeResult.Body.String(), "阿里云模型代理") {
		t.Fatalf("management page status = %d, body = %q", homeResult.Code, homeResult.Body.String())
	}

	statusRequest := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	statusRequest.RemoteAddr = "[::1]:49322"
	statusResult := httptest.NewRecorder()
	handler.ServeHTTP(statusResult, statusRequest)
	if statusResult.Code != http.StatusOK {
		t.Fatalf("management status = %d, body = %s", statusResult.Code, statusResult.Body.String())
	}
	var initial map[string]any
	if err := json.NewDecoder(statusResult.Body).Decode(&initial); err != nil {
		t.Fatal(err)
	}
	if configured, _ := initial["configured"].(bool); configured {
		t.Fatal("uninitialized service unexpectedly reported a configured key")
	}
	if initial["client_key"] != "ap-client" || initial["model_alias"] != modelAlias {
		t.Fatalf("unexpected management connection info: %#v", initial)
	}

	remoteRequest := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	remoteRequest.RemoteAddr = "192.168.1.50:49323"
	remoteResult := httptest.NewRecorder()
	handler.ServeHTTP(remoteResult, remoteRequest)
	if remoteResult.Code != http.StatusForbidden {
		t.Fatalf("remote management status = %d, want 403", remoteResult.Code)
	}

	missingHeader := httptest.NewRequest(
		http.MethodPost,
		"/admin/upstream-key",
		strings.NewReader(`{"api_key":"sk-test-upstream-key"}`),
	)
	missingHeader.RemoteAddr = "127.0.0.1:49324"
	missingHeaderResult := httptest.NewRecorder()
	handler.ServeHTTP(missingHeaderResult, missingHeader)
	if missingHeaderResult.Code != http.StatusForbidden {
		t.Fatalf("missing admin header status = %d, want 403", missingHeaderResult.Code)
	}

	saveRequest := httptest.NewRequest(
		http.MethodPost,
		"/admin/upstream-key",
		strings.NewReader(`{"api_key":"sk-test-upstream-key"}`),
	)
	saveRequest.RemoteAddr = "127.0.0.1:49324"
	saveRequest.Header.Set("Content-Type", "application/json")
	saveRequest.Header.Set("X-Aliyun-Proxy-Admin", "1")
	saveResult := httptest.NewRecorder()
	handler.ServeHTTP(saveResult, saveRequest)
	if saveResult.Code != http.StatusOK {
		t.Fatalf("save key status = %d, body = %s", saveResult.Code, saveResult.Body.String())
	}
	if readSecret("dashscope.key") != "sk-test-upstream-key" {
		t.Fatal("management page did not persist the upstream key")
	}
	var saved map[string]any
	if err := json.NewDecoder(saveResult.Body).Decode(&saved); err != nil {
		t.Fatal(err)
	}
	if configured, _ := saved["configured"].(bool); !configured {
		t.Fatal("configured service was not reported as configured")
	}
}

func TestChatExplainsMissingUpstreamConfiguration(t *testing.T) {
	svc := newService(testConfig("https://example.invalid", "model-one"), "")
	request := httptest.NewRequest(
		http.MethodPost,
		"/v1/chat/completions",
		strings.NewReader(`{"messages":[{"role":"user","content":"hello"}]}`),
	)
	request.Header.Set("Authorization", "Bearer ap-client")
	result := httptest.NewRecorder()
	svc.handler("ap-client").ServeHTTP(result, request)
	if result.Code != http.StatusServiceUnavailable || !strings.Contains(result.Body.String(), "upstream_not_configured") {
		t.Fatalf("missing configuration status = %d, body = %s", result.Code, result.Body.String())
	}
}
