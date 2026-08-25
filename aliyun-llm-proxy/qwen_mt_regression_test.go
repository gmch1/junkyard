package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type qwenMTRecordedUpstream struct {
	mu       sync.Mutex
	requests []map[string]any
}

func (r *qwenMTRecordedUpstream) record(body map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = append(r.requests, body)
}

func (r *qwenMTRecordedUpstream) snapshot() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.requests...)
}

func qwenMTRegressionModel(id string) modelConfig {
	return modelConfig{
		ID:                    id,
		Enabled:               true,
		RPM:                   60_000,
		TPM:                   1_000_000,
		RoutingPriority:       0,
		Adapter:               "qwen-mt",
		DefaultTargetLanguage: "Chinese",
	}
}

func jsonRequestClone(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	var cloned map[string]any
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		t.Fatal(err)
	}
	return cloned
}

func TestQwenMTLiteUnsupportedLanguageFallsThroughToFullModel(t *testing.T) {
	recorder := &qwenMTRecordedUpstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		recorder.record(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"你好"}}]}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, testConfig(upstream.URL,
		qwenMTRegressionModel("qwen-mt-lite"),
		qwenMTRegressionModel("qwen-mt-plus"),
		testModel("general"),
	), "sk-upstream")
	body := map[string]any{
		"model": "anything",
		"messages": []any{
			map[string]any{"role": "user", "content": "Translate to Cantonese:\n\nHello"},
		},
	}

	response, state, attempts := proxy.route(body, false)
	if response == nil || response.Status != http.StatusOK || state == nil || state.Config.ID != "qwen-mt-plus" {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	if got := strings.Join(attempts, ","); got != "qwen-mt-lite,qwen-mt-plus" {
		t.Fatalf("attempts = %q", got)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 || requests[0]["model"] != "qwen-mt-plus" {
		t.Fatalf("upstream requests = %#v", requests)
	}
	options, _ := requests[0]["translation_options"].(map[string]any)
	if options["source_lang"] != "auto" || options["target_lang"] != "yue" {
		t.Fatalf("translation options = %#v", options)
	}
	messages, _ := requests[0]["messages"].([]any)
	if len(messages) != 1 || messages[0].(map[string]any)["content"] != "Hello" {
		t.Fatalf("adapted messages = %#v", messages)
	}
}

func TestQwenMTUnknownLanguageSkipsRemainingMTModelsAndUsesGeneral(t *testing.T) {
	recorder := &qwenMTRecordedUpstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		recorder.record(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, testConfig(upstream.URL,
		qwenMTRegressionModel("qwen-mt-flash"),
		qwenMTRegressionModel("qwen-mt-plus"),
		testModel("general"),
	), "sk-upstream")
	body := map[string]any{
		"model":       "anything",
		"temperature": 0.25,
		"messages": []any{
			map[string]any{"role": "user", "content": "Translate to Hawaiian:\n\nHello"},
		},
	}

	response, state, attempts := proxy.route(body, false)
	if response == nil || response.Status != http.StatusOK || state == nil || state.Config.ID != "general" {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	if got := strings.Join(attempts, ","); got != "qwen-mt-flash,general" {
		t.Fatalf("attempts = %q", got)
	}
	requests := recorder.snapshot()
	if len(requests) != 1 || requests[0]["model"] != "general" {
		t.Fatalf("upstream requests = %#v", requests)
	}
	want := jsonRequestClone(t, body)
	want["model"] = "general"
	if !reflect.DeepEqual(requests[0], want) {
		t.Fatalf("general request = %#v, want %#v", requests[0], want)
	}
}

func TestQwenMTUpstreamLanguage400FallsBackWithOriginalRequest(t *testing.T) {
	recorder := &qwenMTRecordedUpstream{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode upstream request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		recorder.record(body)
		w.Header().Set("Content-Type", "application/json")
		if body["model"] == "qwen-mt-flash" {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":"invalid_parameter_error","message":"暂时不支持当前设置的语种！"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	proxy := newTestProxy(t, testConfig(upstream.URL,
		qwenMTRegressionModel("qwen-mt-flash"),
		qwenMTRegressionModel("qwen-mt-plus"),
		testModel("general"),
	), "sk-upstream")
	body := map[string]any{
		"model":       "anything",
		"temperature": 0.35,
		"max_tokens":  float64(321),
		"top_p":       0.9,
		"metadata":    map[string]any{"request_id": "mt-regression"},
		"messages": []any{
			map[string]any{"role": "system", "content": "Return only the translation."},
			map[string]any{"role": "user", "content": "Translate to Simplified Mandarin Chinese:\n\nHello"},
		},
	}
	original := jsonRequestClone(t, body)

	response, state, attempts := proxy.route(body, false)
	if response == nil || response.Status != http.StatusOK || state == nil || state.Config.ID != "general" {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	if got := strings.Join(attempts, ","); got != "qwen-mt-flash,general" {
		t.Fatalf("attempts = %q", got)
	}
	requests := recorder.snapshot()
	if len(requests) != 2 || requests[0]["model"] != "qwen-mt-flash" || requests[1]["model"] != "general" {
		t.Fatalf("upstream requests = %#v", requests)
	}
	if _, exists := requests[0]["temperature"]; exists {
		t.Fatalf("MT adapter leaked caller options: %#v", requests[0])
	}
	wantGeneral := jsonRequestClone(t, body)
	wantGeneral["model"] = "general"
	if !reflect.DeepEqual(requests[1], wantGeneral) {
		t.Fatalf("general fallback request = %#v, want %#v", requests[1], wantGeneral)
	}
	if !reflect.DeepEqual(jsonRequestClone(t, body), original) {
		t.Fatalf("route mutated original body: %#v, want %#v", body, original)
	}
}

func TestQwenMTLanguageAliasesCapabilitiesAndErrorBoundary(t *testing.T) {
	if len(qwenMTLanguages) != 92 {
		t.Fatalf("full Qwen-MT language count = %d", len(qwenMTLanguages))
	}
	if len(qwenMTLiteLanguages) != 31 {
		t.Fatalf("Qwen-MT Lite language count = %d", len(qwenMTLiteLanguages))
	}

	aliases := map[string]string{
		"  Simplified   Mandarin Chinese ": "zh",
		"Traditional Mandarin Chinese":     "zh_tw",
		"zh-TW":                            "zh_tw",
		"Standard Arabic":                  "ar",
		"Iranian Persian":                  "fa",
		"繁体中文":                             "zh_tw",
	}
	for input, want := range aliases {
		got, ok := qwenMTLanguageCode(input)
		if !ok || got != want {
			t.Errorf("qwenMTLanguageCode(%q) = %q, %t; want %q, true", input, got, ok, want)
		}
	}
	if _, ok := qwenMTLanguageCode("Hawaiian"); ok {
		t.Fatal("unsupported Hawaiian language was mapped")
	}
	if qwenMTModelSupports("qwen-mt-lite", "yue") || qwenMTModelSupports("qwen-mt-lite", "ro") {
		t.Fatal("Qwen-MT Lite accepted a full-model-only language")
	}
	if !qwenMTModelSupports("qwen-mt-flash", "yue") || !qwenMTModelSupports("qwen-mt-lite", "fa") {
		t.Fatal("Qwen-MT capability table rejected a supported language")
	}

	model := qwenMTRegressionModel("qwen-mt-flash")
	for _, test := range []struct {
		name    string
		status  int
		code    string
		message string
		want    bool
	}{
		{name: "Aliyun Chinese message", status: 400, code: "invalid_parameter_error", message: "暂时不支持当前设置的语种！", want: true},
		{name: "English message", status: 400, code: "InvalidParameter", message: "Target language is not supported", want: true},
		{name: "unrelated invalid parameter", status: 400, code: "invalid_parameter_error", message: "temperature is invalid", want: false},
		{name: "wrong status", status: 422, code: "invalid_parameter_error", message: "unsupported language", want: false},
		{name: "wrong code", status: 400, code: "BadRequest", message: "unsupported language", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := isQwenMTLanguageError(test.status, test.code, test.message, model); got != test.want {
				t.Fatalf("isQwenMTLanguageError(...) = %t, want %t", got, test.want)
			}
		})
	}
}
