package bridge

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/command"
)

type Lamp interface {
	Execute(context.Context, command.Command) error
}

type Message struct {
	Payload   []byte
	Retained  bool
	Duplicate bool
}

type Handler struct {
	lamp        Lamp
	logger      *slog.Logger
	dryRun      bool
	dedupWindow time.Duration
	now         func() time.Time

	mu          sync.Mutex
	lastPayload string
	lastHandled time.Time
}

func NewHandler(lamp Lamp, logger *slog.Logger, dryRun bool, dedupWindow time.Duration) (*Handler, error) {
	if logger == nil {
		return nil, errors.New("logger is required")
	}
	if !dryRun && lamp == nil {
		return nil, errors.New("lamp client is required when dry-run is disabled")
	}
	return &Handler{
		lamp:        lamp,
		logger:      logger,
		dryRun:      dryRun,
		dedupWindow: dedupWindow,
		now:         time.Now,
	}, nil
}

func (h *Handler) Handle(ctx context.Context, message Message) error {
	if message.Retained {
		h.logger.Info("ignored retained Bemfa command")
		return nil
	}
	if message.Duplicate {
		h.logger.Info("ignored MQTT duplicate Bemfa command")
		return nil
	}

	cmd, err := command.Parse(message.Payload)
	if errors.Is(err, command.ErrUnsupported) {
		h.logger.Info("ignored unsupported Bemfa command")
		return nil
	}
	if err != nil {
		return fmt.Errorf("parse command: %w", err)
	}

	if h.isDuplicate(cmd.Payload) {
		h.logger.Info("ignored duplicate Bemfa command", "command", cmd.Payload)
		return nil
	}

	if h.dryRun {
		h.remember(cmd.Payload)
		h.logger.Info("dry-run: lamp command not executed", "command", cmd.Payload)
		return nil
	}

	if err := h.lamp.Execute(ctx, cmd); err != nil {
		return err
	}
	h.remember(cmd.Payload)
	h.logger.Info("lamp gateway accepted command", "command", cmd.Payload)
	return nil
}

func (h *Handler) isDuplicate(payload string) bool {
	if h.dedupWindow == 0 {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return payload == h.lastPayload && h.now().Sub(h.lastHandled) < h.dedupWindow
}

func (h *Handler) remember(payload string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastPayload = payload
	h.lastHandled = h.now()
}
