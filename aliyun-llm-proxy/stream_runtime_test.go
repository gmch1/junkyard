package main

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStreamingTimeoutDoesNotCapTotalResponseDuration(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("upstream response does not support flushing")
			return
		}
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		for index := 0; index < 5; index++ {
			time.Sleep(30 * time.Millisecond)
			_, _ = io.WriteString(w, "data: next\n\n")
			flusher.Flush()
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := testConfig(upstream.URL, testModel("streaming"))
	cfg.RequestTimeoutSeconds = .1
	proxy := newTestProxy(t, cfg, "sk-upstream")
	response, state, attempts := proxy.route(map[string]any{"messages": []any{}, "stream": true}, true)
	if response == nil || state == nil || state.Config.ID != "streaming" || strings.Join(attempts, ",") != "streaming" {
		t.Fatalf("response=%#v state=%#v attempts=%v", response, state, attempts)
	}
	remainder, err := io.ReadAll(response.Stream)
	if err != nil {
		t.Fatalf("stream was cut off by the total request timeout: %v", err)
	}
	if err := response.Stream.Close(); err != nil {
		t.Fatal(err)
	}
	content := string(response.Prefetched) + string(remainder)
	if strings.Count(content, "data: next") != 5 || !strings.HasSuffix(content, "data: [DONE]\n\n") {
		t.Fatalf("stream content=%q", content)
	}
}

func TestStreamingTimeoutStillLimitsIdleGaps(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: first\n\n")
		flusher.Flush()
		time.Sleep(150 * time.Millisecond)
		_, _ = io.WriteString(w, "data: too-late\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	cfg := testConfig(upstream.URL, testModel("streaming"))
	cfg.RequestTimeoutSeconds = .05
	proxy := newTestProxy(t, cfg, "sk-upstream")
	response, state, _ := proxy.route(map[string]any{"messages": []any{}, "stream": true}, true)
	if response == nil || state == nil {
		t.Fatalf("response=%#v state=%#v", response, state)
	}
	_, err := io.ReadAll(response.Stream)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("idle stream error=%v, want context deadline exceeded", err)
	}
	_ = response.Stream.Close()
}

func TestRouteContextCancellationStopsUpstreamRequest(t *testing.T) {
	started := make(chan struct{})
	releaseUpstream := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseUpstream) }) }
	t.Cleanup(release)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-releaseUpstream
		_, _ = io.WriteString(w, `{"choices":[]}`)
	}))
	defer upstream.Close()

	cfg := testConfig(upstream.URL, testModel("one"), testModel("two"))
	cfg.RequestTimeoutSeconds = 5
	proxy := newTestProxy(t, cfg, "sk-upstream")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	done := make(chan struct{})
	var routeAttempts []string
	go func() {
		defer close(done)
		response, state, attempts := proxy.routeContext(ctx, map[string]any{"messages": []any{}}, false)
		routeAttempts = attempts
		if response == nil || response.Status != 499 || state != nil {
			t.Errorf("canceled route response=%#v state=%#v", response, state)
		}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("upstream request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("route did not finish after cancellation")
	}
	if got := strings.Join(routeAttempts, ","); got != "one" {
		t.Fatalf("canceled route attempts=%q", got)
	}
	release()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error=%v", ctx.Err())
	}
	snapshots := regressionModelSnapshots(proxy)
	for _, model := range []string{"one", "two"} {
		snapshot := snapshots[model]
		if snapshot.InFlight != 0 || snapshot.Failures != 0 || snapshot.CooldownSeconds != 0 || snapshot.Attempts != 0 {
			t.Fatalf("cancellation changed %s metrics: %#v", model, snapshot)
		}
	}
}

func TestStreamReadErrorClassificationDistinguishesDisconnects(t *testing.T) {
	tests := []struct {
		name                         string
		requestErr, readErr          error
		wantFailed, wantDisconnected bool
	}{
		{name: "client canceled", requestErr: context.Canceled, readErr: context.Canceled, wantDisconnected: true},
		{name: "upstream idle timeout", readErr: context.DeadlineExceeded, wantFailed: true},
		{name: "normal end", readErr: io.EOF},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			failed, disconnected := classifyStreamReadError(test.requestErr, test.readErr)
			if failed != test.wantFailed || disconnected != test.wantDisconnected {
				t.Fatalf("failed=%t disconnected=%t, want failed=%t disconnected=%t", failed, disconnected, test.wantFailed, test.wantDisconnected)
			}
		})
	}
}
