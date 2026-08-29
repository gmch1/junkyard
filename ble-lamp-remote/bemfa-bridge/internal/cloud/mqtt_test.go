package cloud

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	mqtt "github.com/eclipse/paho.mqtt.golang"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/bridge"
)

func TestValidateSubscribeResult(t *testing.T) {
	t.Parallel()

	const topic = "xidingdeng002"
	tests := []struct {
		name   string
		result map[string]byte
		ok     bool
	}{
		{name: "QoS zero", result: map[string]byte{topic: 0}, ok: true},
		{name: "QoS one", result: map[string]byte{topic: 1}, ok: true},
		{name: "rejected", result: map[string]byte{topic: 0x80}},
		{name: "QoS two not allowed", result: map[string]byte{topic: 2}},
		{name: "missing topic", result: map[string]byte{}},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSubscribeResult(topic, tt.result)
			if (err == nil) != tt.ok {
				t.Fatalf("validateSubscribeResult() error = %v, ok = %v", err, tt.ok)
			}
		})
	}
}

func TestEnqueueLatest(t *testing.T) {
	t.Parallel()

	client := &Client{messages: make(chan bridge.Message, 1)}
	first := bridge.Message{Payload: []byte("on")}
	latest := bridge.Message{Payload: []byte("off")}
	if replaced := client.enqueueLatest(first); replaced {
		t.Fatal("first enqueue unexpectedly replaced a message")
	}
	if replaced := client.enqueueLatest(latest); !replaced {
		t.Fatal("second enqueue did not report replacing the queued message")
	}
	if got := string((<-client.messages).Payload); got != "off" {
		t.Fatalf("queued payload = %q, want off", got)
	}
}

func TestEnqueueLatestConcurrent(t *testing.T) {
	t.Parallel()

	client := &Client{messages: make(chan bridge.Message, 1)}
	const senders = 100
	var wait sync.WaitGroup
	wait.Add(senders)
	for i := 0; i < senders; i++ {
		go func(payload byte) {
			defer wait.Done()
			client.enqueueLatest(bridge.Message{Payload: []byte{payload}})
		}(byte(i))
	}
	wait.Wait()
	select {
	case message := <-client.messages:
		if len(message.Payload) != 1 {
			t.Fatalf("payload length = %d, want 1", len(message.Payload))
		}
	default:
		t.Fatal("queue is empty after concurrent enqueue")
	}
}

func TestEnsureSubscriptionRetriesUntilGranted(t *testing.T) {
	t.Parallel()

	const topic = "xidingdeng002"
	client := newTestClient(topic)
	client.retryDelay = time.Millisecond
	client.opTimeout = 100 * time.Millisecond
	subscriber := &fakeSubscriptionClient{
		connected: true,
		tokens: []mqtt.Token{
			completedSubscribeToken(map[string]byte{topic: 0x80}, nil),
			completedSubscribeToken(map[string]byte{topic: 1}, nil),
		},
	}

	done := make(chan struct{})
	go func() {
		client.ensureSubscription(subscriber)
		close(done)
	}()

	select {
	case <-client.initialReady:
	case <-time.After(time.Second):
		t.Fatal("subscription did not become ready")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscription worker did not exit after success")
	}
	if calls := subscriber.callCount(); calls != 2 {
		t.Fatalf("Subscribe call count = %d, want 2", calls)
	}
}

func TestWaitSubscriptionTokenStopsWhenClosed(t *testing.T) {
	t.Parallel()

	token := &fakeSubscribeToken{done: make(chan struct{})}
	closed := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- waitSubscriptionToken(token, time.Hour, closed)
	}()
	close(closed)
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("waitSubscriptionToken() error = nil")
		}
	case <-time.After(time.Second):
		t.Fatal("waitSubscriptionToken() did not stop")
	}
}

func TestSubscriptionCallbackFiltersUnsafeMessages(t *testing.T) {
	t.Parallel()

	const topic = "xidingdeng002"
	client := newTestClient(topic)
	subscriber := &fakeSubscriptionClient{
		connected: true,
		tokens:    []mqtt.Token{completedSubscribeToken(map[string]byte{topic: 1}, nil)},
	}
	if err := client.subscribe(subscriber); err != nil {
		t.Fatal(err)
	}
	handler := subscriber.messageHandler()
	for _, message := range []mqtt.Message{
		fakeMessage{payload: []byte("on"), retained: true},
		fakeMessage{payload: []byte("on"), duplicate: true},
		fakeMessage{payload: nil},
		fakeMessage{payload: make([]byte, maxPayloadBytes+1)},
	} {
		handler(nil, message)
	}
	select {
	case message := <-client.messages:
		t.Fatalf("unsafe message reached queue: %q", message.Payload)
	default:
	}

	handler(nil, fakeMessage{payload: []byte("on")})
	select {
	case message := <-client.messages:
		if got := string(message.Payload); got != "on" {
			t.Fatalf("queued payload = %q, want on", got)
		}
	default:
		t.Fatal("valid message did not reach queue")
	}
}

func TestUnsupportedMessageCannotDisplaceQueuedCommand(t *testing.T) {
	t.Parallel()

	const topic = "xidingdeng002"
	client := newTestClient(topic)
	subscriber := &fakeSubscriptionClient{
		connected: true,
		tokens:    []mqtt.Token{completedSubscribeToken(map[string]byte{topic: 1}, nil)},
	}
	if err := client.subscribe(subscriber); err != nil {
		t.Fatal(err)
	}
	handler := subscriber.messageHandler()
	handler(nil, fakeMessage{payload: []byte("off")})
	for _, payload := range [][]byte{[]byte("on#80"), []byte("junk"), []byte(" on ")} {
		handler(nil, fakeMessage{payload: payload})
	}

	select {
	case message := <-client.messages:
		if got := string(message.Payload); got != "off" {
			t.Fatalf("queued payload = %q, want off", got)
		}
	default:
		t.Fatal("valid queued command was displaced")
	}
}

func newTestClient(topic string) *Client {
	return &Client{
		topic:        topic,
		messages:     make(chan bridge.Message, 1),
		logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
		opTimeout:    100 * time.Millisecond,
		retryDelay:   time.Millisecond,
		initialReady: make(chan struct{}),
		closed:       make(chan struct{}),
	}
}

type fakeSubscriptionClient struct {
	mu        sync.Mutex
	connected bool
	tokens    []mqtt.Token
	calls     int
	handler   mqtt.MessageHandler
}

func (f *fakeSubscriptionClient) IsConnected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connected
}

func (f *fakeSubscriptionClient) Subscribe(_ string, _ byte, handler mqtt.MessageHandler) mqtt.Token {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.handler = handler
	index := f.calls
	f.calls++
	if index >= len(f.tokens) {
		index = len(f.tokens) - 1
	}
	return f.tokens[index]
}

func (f *fakeSubscriptionClient) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeSubscriptionClient) messageHandler() mqtt.MessageHandler {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.handler
}

type fakeSubscribeToken struct {
	done   chan struct{}
	err    error
	result map[string]byte
}

func completedSubscribeToken(result map[string]byte, err error) *fakeSubscribeToken {
	done := make(chan struct{})
	close(done)
	return &fakeSubscribeToken{done: done, err: err, result: result}
}

func (f *fakeSubscribeToken) Wait() bool {
	<-f.done
	return true
}

func (f *fakeSubscribeToken) WaitTimeout(timeout time.Duration) bool {
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-f.done:
		return true
	case <-timer.C:
		return false
	}
}

func (f *fakeSubscribeToken) Done() <-chan struct{} { return f.done }
func (f *fakeSubscribeToken) Error() error          { return f.err }
func (f *fakeSubscribeToken) Result() map[string]byte {
	return f.result
}

type fakeMessage struct {
	payload   []byte
	retained  bool
	duplicate bool
}

func (f fakeMessage) Duplicate() bool   { return f.duplicate }
func (f fakeMessage) Qos() byte         { return 1 }
func (f fakeMessage) Retained() bool    { return f.retained }
func (f fakeMessage) Topic() string     { return "xidingdeng002" }
func (f fakeMessage) MessageID() uint16 { return 1 }
func (f fakeMessage) Payload() []byte   { return f.payload }
func (f fakeMessage) Ack()              {}

var _ mqtt.Token = (*fakeSubscribeToken)(nil)
var _ mqtt.Message = fakeMessage{}
