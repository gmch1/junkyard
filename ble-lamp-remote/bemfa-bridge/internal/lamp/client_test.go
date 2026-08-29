package lamp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/command"
)

func TestExecute(t *testing.T) {
	t.Parallel()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	transport := roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/v1/light/on" {
			t.Errorf("path = %s, want /v1/light/on", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		if r.ContentLength > 0 {
			t.Errorf("ContentLength = %d, want empty body", r.ContentLength)
		}
		return response(http.StatusAccepted, "accepted"), nil
	})

	client := newClient("http://127.0.0.1:8791", token, &http.Client{Transport: transport, Timeout: time.Second})
	err := client.Execute(context.Background(), command.Command{Payload: "on", Path: "/v1/light/on"})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecuteRejectsNonAcceptedResponse(t *testing.T) {
	t.Parallel()

	transport := roundTripperFunc(func(_ *http.Request) (*http.Response, error) {
		return response(http.StatusUnauthorized, strings.Repeat("x", 8192)), nil
	})
	client := newClient("http://127.0.0.1:8791", strings.Repeat("a", 64), &http.Client{Transport: transport, Timeout: time.Second})
	err := client.Execute(context.Background(), command.Command{Payload: "off", Path: "/v1/light/off"})
	if err == nil || !strings.Contains(err.Error(), "401 Unauthorized") {
		t.Fatalf("Execute() error = %v, want status error", err)
	}
}

func TestNewClientDisablesProxyAndRedirects(t *testing.T) {
	t.Parallel()

	const token = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	client := NewClient("http://127.0.0.1:8791", token, time.Second)
	transport, ok := client.http.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", client.http.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("Transport.Proxy must be nil")
	}

	requests := 0
	client.http.Transport = roundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("Authorization = %q", got)
		}
		resp := response(http.StatusTemporaryRedirect, "redirect")
		resp.Header.Set("Location", "http://203.0.113.10/collect")
		resp.Request = r
		return resp, nil
	})

	err := client.Execute(context.Background(), command.Command{Payload: "on", Path: "/v1/light/on"})
	if err == nil || !strings.Contains(err.Error(), "307 Temporary Redirect") {
		t.Fatalf("Execute() error = %v, want redirect status error", err)
	}
	if requests != 1 {
		t.Fatalf("request count = %d, want 1", requests)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
