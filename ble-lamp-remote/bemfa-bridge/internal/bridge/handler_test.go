package bridge

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/command"
)

type fakeLamp struct {
	commands []command.Command
	err      error
}

func (f *fakeLamp) Execute(_ context.Context, cmd command.Command) error {
	f.commands = append(f.commands, cmd)
	return f.err
}

func TestHandleLiveCommand(t *testing.T) {
	t.Parallel()

	lamp := &fakeLamp{}
	handler, err := NewHandler(lamp, testLogger(), false, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := handler.Handle(context.Background(), Message{Payload: []byte("on")}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(lamp.commands) != 1 || lamp.commands[0].Path != "/v1/light/on" {
		t.Fatalf("lamp commands = %#v", lamp.commands)
	}
}

func TestHandleDryRunNeverCallsDependencies(t *testing.T) {
	t.Parallel()

	lamp := &fakeLamp{}
	handler, err := NewHandler(lamp, testLogger(), true, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}

	if err := handler.Handle(context.Background(), Message{Payload: []byte("off")}); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(lamp.commands) != 0 {
		t.Fatal("dry-run called an external dependency")
	}
}

func TestHandleIgnoresRetainedUnsupportedAndDuplicateMessages(t *testing.T) {
	t.Parallel()

	lamp := &fakeLamp{}
	handler, err := NewHandler(lamp, testLogger(), false, time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	for _, message := range []Message{
		{Payload: []byte("on"), Retained: true},
		{Payload: []byte("on#80")},
		{Payload: []byte("on")},
		{Payload: []byte("on"), Duplicate: true},
	} {
		if err := handler.Handle(context.Background(), message); err != nil {
			t.Fatalf("Handle(%q) error = %v", message.Payload, err)
		}
	}
	if len(lamp.commands) != 1 {
		t.Fatalf("commands = %d, want one", len(lamp.commands))
	}
}

func TestHandleIgnoresBrokerDuplicateWithoutPriorMessage(t *testing.T) {
	t.Parallel()

	lamp := &fakeLamp{}
	handler, err := NewHandler(lamp, testLogger(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(context.Background(), Message{Payload: []byte("on"), Duplicate: true}); err != nil {
		t.Fatal(err)
	}
	if len(lamp.commands) != 0 {
		t.Fatalf("lamp command count = %d, want 0", len(lamp.commands))
	}
}

func TestHandleReturnsLampFailure(t *testing.T) {
	t.Parallel()

	lamp := &fakeLamp{err: errors.New("gateway unavailable")}
	handler, err := NewHandler(lamp, testLogger(), false, 0)
	if err != nil {
		t.Fatal(err)
	}

	if err := handler.Handle(context.Background(), Message{Payload: []byte("off")}); err == nil {
		t.Fatal("Handle() error = nil, want lamp error")
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
}
