package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/bridge"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/cloud"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/config"
	"github.com/gmch1/junkyard/ble-lamp-remote/bemfa-bridge/internal/lamp"
)

var version = "dev"

func main() {
	checkConfig := flag.Bool("check-config", false, "validate configuration without connecting")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("invalid configuration", "error", err)
		os.Exit(2)
	}
	if *checkConfig {
		logger.Info("configuration is valid", "dry_run", cfg.DryRun, "topic", cfg.Topic)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cloudClient, err := cloud.NewClient(cfg.BrokerURL, cfg.Topic, cfg.UID, logger)
	if err != nil {
		logger.Error("create Bemfa MQTT client", "error", err)
		os.Exit(1)
	}
	defer cloudClient.Close()

	if err := cloudClient.Connect(ctx); err != nil {
		logger.Error("Bemfa MQTT startup failed", "error", err)
		os.Exit(1)
	}

	var lampClient bridge.Lamp
	if !cfg.DryRun {
		lampClient = lamp.NewClient(cfg.LampAPIURL, cfg.LampToken, cfg.HTTPTimeout)
	}
	handler, err := bridge.NewHandler(lampClient, logger, cfg.DryRun, cfg.DedupWindow)
	if err != nil {
		logger.Error("create bridge handler", "error", err)
		os.Exit(1)
	}

	logger.Info("Bemfa lamp bridge started", "version", version, "dry_run", cfg.DryRun, "topic", cfg.Topic)
	for {
		select {
		case <-ctx.Done():
			logger.Info("Bemfa lamp bridge stopped")
			return
		case message := <-cloudClient.Messages():
			if err := handler.Handle(ctx, message); err != nil && !errors.Is(err, context.Canceled) {
				logger.Error("lamp command failed", "error", err)
			}
		}
	}
}
