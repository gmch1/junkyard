package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func serveRegressionRequest(t *testing.T, server http.Handler, method, target, body, authorization string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:50000"
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	result := httptest.NewRecorder()
	server.ServeHTTP(result, request)
	return result
}

func TestModelsExposeConfiguredAndLegacyAliases(t *testing.T) {
	tests := []struct {
		name       string
		configured string
		want       []string
	}{
		{name: "new alias retains legacy discovery", configured: modelAlias, want: []string{modelAlias, "translategemma-4b-it"}},
		{name: "legacy alias is not duplicated", configured: "translategemma-4b-it", want: []string{"translategemma-4b-it"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := testConfig("https://example.invalid", testModel("upstream-model"))
			cfg.ModelAlias = test.configured
			proxy := newTestProxy(t, cfg, "")
			server, err := newAPIServer(proxy)
			if err != nil {
				t.Fatal(err)
			}

			result := serveRegressionRequest(t, server, http.MethodGet, "/v1/models", "", "Bearer ap-client")
			if result.Code != http.StatusOK {
				t.Fatalf("models status=%d body=%s", result.Code, result.Body.String())
			}
			var payload struct {
				Object string `json:"object"`
				Data   []struct {
					ID      string `json:"id"`
					Object  string `json:"object"`
					OwnedBy string `json:"owned_by"`
				} `json:"data"`
			}
			if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if payload.Object != "list" || len(payload.Data) != len(test.want) {
				t.Fatalf("models payload=%s", result.Body.String())
			}
			for index, want := range test.want {
				got := payload.Data[index]
				if got.ID != want || got.Object != "model" || got.OwnedBy != "local-aliyun-proxy" {
					t.Fatalf("model[%d]=%#v want id=%q", index, got, want)
				}
			}
		})
	}
}

func TestDisabledDashboardLeavesOpenAIAPIAvailable(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer upstream.Close()

	cfg := testConfig(upstream.URL, testModel("one"))
	cfg.DashboardEnabled = false
	proxy := newTestProxy(t, cfg, "sk-upstream")
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}

	for _, target := range []string{"/", "/dashboard", "/v1/proxy/dashboard", "/v1/proxy/dashboard-data", "/dashboard-assets/index.js", "/admin/status"} {
		result := serveRegressionRequest(t, server, http.MethodGet, target, "", "")
		if result.Code != http.StatusNotFound {
			t.Errorf("GET %s status=%d body=%s", target, result.Code, result.Body.String())
		}
	}

	health := serveRegressionRequest(t, server, http.MethodGet, "/health", "", "")
	if health.Code != http.StatusOK {
		t.Fatalf("health status=%d body=%s", health.Code, health.Body.String())
	}
	models := serveRegressionRequest(t, server, http.MethodGet, "/v1/models", "", "Bearer ap-client")
	if models.Code != http.StatusOK {
		t.Fatalf("models status=%d body=%s", models.Code, models.Body.String())
	}
	chat := serveRegressionRequest(t, server, http.MethodPost, "/v1/chat/completions", `{"messages":[]}`, "Bearer ap-client")
	if chat.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", chat.Code, chat.Body.String())
	}
}

func TestChatRequiresClientKeyAndForwardsUpstreamCredentials(t *testing.T) {
	type receivedRequest struct {
		authorization string
		model         string
	}
	received := make(chan receivedRequest, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream body: %v", err)
		}
		received <- receivedRequest{authorization: r.Header.Get("Authorization"), model: body["model"].(string)}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, testConfig(upstream.URL, testModel("routed-model")), "sk-upstream-secret")
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"model":"caller-alias","messages":[{"role":"user","content":"hello"}]}`
	unauthorized := serveRegressionRequest(t, server, http.MethodPost, "/v1/chat/completions", body, "Bearer wrong")
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}
	select {
	case request := <-received:
		t.Fatalf("unauthorized request reached upstream: %#v", request)
	default:
	}

	result := serveRegressionRequest(t, server, http.MethodPost, "/v1/chat/completions", body, "Bearer ap-client")
	if result.Code != http.StatusOK {
		t.Fatalf("chat status=%d body=%s", result.Code, result.Body.String())
	}
	if got := result.Header().Get("X-Proxy-Model"); got != "routed-model" {
		t.Fatalf("X-Proxy-Model=%q", got)
	}
	if got := result.Header().Get("X-Proxy-Attempts"); got != "routed-model" {
		t.Fatalf("X-Proxy-Attempts=%q", got)
	}
	request := <-received
	if request.authorization != "Bearer sk-upstream-secret" || request.model != "routed-model" {
		t.Fatalf("upstream request=%#v", request)
	}
}

func TestChatWithoutUpstreamKeyReturnsServiceUnavailable(t *testing.T) {
	proxy := newTestProxy(t, testConfig("https://example.invalid", testModel("one")), "")
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}
	result := serveRegressionRequest(t, server, http.MethodPost, "/v1/chat/completions", `{"messages":[]}`, "Bearer ap-client")
	if result.Code != http.StatusServiceUnavailable {
		t.Fatalf("chat status=%d body=%s", result.Code, result.Body.String())
	}
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(result.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Error.Code != "upstream_not_configured" || result.Header().Get("X-Proxy-Model") != "" {
		t.Fatalf("chat headers=%v body=%s", result.Header(), result.Body.String())
	}
}

func TestUnavailableModelProbeMustSucceedBeforeReenable(t *testing.T) {
	var failProbe atomic.Bool
	failProbe.Store(true)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failProbe.Load() {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"error":{"code":"ModelNotFound","message":"model unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	t.Setenv("ALIYUN_PROXY_STATE_DIR", t.TempDir())
	proxy := newTestProxy(t, testConfig(upstream.URL, testModel("recovering"), testModel("fallback")), "sk-upstream")
	proxy.pool.disable(proxy.pool.states[0], "ModelNotFound")
	if err := proxy.unavailable.mark("recovering", http.StatusForbidden, "ModelNotFound", "model unavailable"); err != nil {
		t.Fatal(err)
	}
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}

	reenable := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/proxy/models/enabled", strings.NewReader(`{"model":"recovering","enabled":true}`))
		request.RemoteAddr = "127.0.0.1:50000"
		request.Header.Set("X-Proxy-Dashboard", "1")
		result := httptest.NewRecorder()
		server.ServeHTTP(result, request)
		return result
	}

	failed := reenable()
	if failed.Code != http.StatusConflict || !strings.Contains(failed.Body.String(), `"code":"model_probe_failed"`) {
		t.Fatalf("failed probe status=%d body=%s", failed.Code, failed.Body.String())
	}
	if unavailable, exists := proxy.pool.unavailableStatus("recovering"); !exists || !unavailable {
		t.Fatalf("failed probe changed unavailable state: exists=%t unavailable=%t", exists, unavailable)
	}
	if _, exists := proxy.unavailable.snapshot()["recovering"]; !exists {
		t.Fatal("failed probe cleared persisted unavailable state")
	}

	failProbe.Store(false)
	succeeded := reenable()
	if succeeded.Code != http.StatusOK || !strings.Contains(succeeded.Body.String(), `"probed":true`) {
		t.Fatalf("successful probe status=%d body=%s", succeeded.Code, succeeded.Body.String())
	}
	if unavailable, exists := proxy.pool.unavailableStatus("recovering"); !exists || unavailable {
		t.Fatalf("successful probe did not clear unavailable state: exists=%t unavailable=%t", exists, unavailable)
	}
	if _, exists := proxy.unavailable.snapshot()["recovering"]; exists {
		t.Fatal("successful probe did not clear persisted unavailable state")
	}
}

func TestManagementIsLocalAssetsRejectTraversalAndPublicStatusHidesKeys(t *testing.T) {
	proxy := newTestProxy(t, testConfig("https://example.invalid", testModel("one")), "sk-upstream-secret")
	server, err := newAPIServer(proxy)
	if err != nil {
		t.Fatal(err)
	}

	remote := httptest.NewRequest(http.MethodGet, "/admin/status", nil)
	remote.RemoteAddr = "192.0.2.10:50000"
	remoteResult := httptest.NewRecorder()
	server.ServeHTTP(remoteResult, remote)
	if remoteResult.Code != http.StatusForbidden {
		t.Fatalf("remote management status=%d body=%s", remoteResult.Code, remoteResult.Body.String())
	}

	traversal := serveRegressionRequest(t, server, http.MethodGet, "/dashboard-assets/%2e%2e%2findex.html", "", "")
	if traversal.Code != http.StatusNotFound {
		t.Fatalf("traversal status=%d body=%s", traversal.Code, traversal.Body.String())
	}

	status := serveRegressionRequest(t, server, http.MethodGet, "/v1/proxy/status", "", "Bearer ap-client")
	if status.Code != http.StatusOK {
		t.Fatalf("public status=%d body=%s", status.Code, status.Body.String())
	}
	for _, secret := range []string{"ap-client", "sk-upstream-secret", "client_key", "upstream_key"} {
		if strings.Contains(status.Body.String(), secret) {
			t.Fatalf("public status leaked %q: %s", secret, status.Body.String())
		}
	}
}
