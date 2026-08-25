package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	configVersion = 10
	defaultPort   = 39281
	modelAlias    = "aliyun-translate-auto"
)

type hedgeConfig struct {
	Enabled              bool    `json:"enabled"`
	DelaySeconds         float64 `json:"delay_seconds"`
	MaxConcurrentBackups int     `json:"max_concurrent_backups"`
}

type modelConfig struct {
	ID                       string  `json:"id"`
	Enabled                  bool    `json:"enabled"`
	RPM                      int     `json:"rpm"`
	TPM                      int     `json:"tpm"`
	MinIntervalSeconds       float64 `json:"min_interval_seconds,omitempty"`
	RoutingPriority          int     `json:"routing_priority"`
	RateClass                string  `json:"rate_class,omitempty"`
	Role                     string  `json:"role,omitempty"`
	Adapter                  string  `json:"adapter,omitempty"`
	DefaultTargetLanguage    string  `json:"default_target_language,omitempty"`
	StreamCompatible         *bool   `json:"stream_compatible,omitempty"`
	DisableOnAllocationQuota bool    `json:"disable_on_allocation_quota,omitempty"`
	DisableOnAccessDenied    bool    `json:"disable_on_access_denied,omitempty"`
}

func (m *modelConfig) UnmarshalJSON(data []byte) error {
	type modelConfigAlias modelConfig
	value := modelConfigAlias{Enabled: true}
	if err := json.Unmarshal(data, &value); err != nil {
		return err
	}
	*m = modelConfig(value)
	return nil
}

func boolPointer(value bool) *bool { return &value }

type config struct {
	Version                     int           `json:"version"`
	Host                        string        `json:"host"`
	Port                        int           `json:"port"`
	AllowLANAccess              bool          `json:"allow_lan_access"`
	DashboardEnabled            bool          `json:"dashboard_enabled"`
	UpstreamBaseURL             string        `json:"upstream_base_url"`
	ModelAlias                  string        `json:"model_alias"`
	RequestTimeoutSeconds       float64       `json:"request_timeout_seconds"`
	ModelProbeTimeoutSeconds    float64       `json:"model_probe_timeout_seconds,omitempty"`
	RouteWaitSeconds            float64       `json:"route_wait_seconds"`
	RPMSafetyRatio              float64       `json:"rpm_safety_ratio"`
	DefaultCooldownSeconds      float64       `json:"default_cooldown_seconds"`
	SelectionStrategy           string        `json:"selection_strategy"`
	MetricsFlushIntervalSeconds float64       `json:"metrics_flush_interval_seconds"`
	Hedging                     hedgeConfig   `json:"hedging"`
	Models                      []modelConfig `json:"models"`
}

func defaultConfig() config {
	high := func(id, role string, rpm, tpm int) modelConfig {
		return modelConfig{ID: id, Enabled: true, RPM: rpm, TPM: tpm, RoutingPriority: 10, RateClass: "high-throughput", Role: role}
	}
	low := func(id, role string) modelConfig {
		return modelConfig{ID: id, Enabled: true, RPM: 60, TPM: 1_000_000, MinIntervalSeconds: 30, RoutingPriority: 5, RateClass: "low-frequency", Role: role}
	}
	models := []modelConfig{
		{ID: "deepseek-v4-flash", Enabled: true, RPM: 15_000, TPM: 1_200_000, MinIntervalSeconds: 30, RoutingPriority: 10, Role: "quota-probe", DisableOnAllocationQuota: true, DisableOnAccessDenied: true},
		high("deepseek-v4-pro", "stable-translation", 15_000, 1_200_000),
		high("deepseek-v3.2", "stable-translation", 15_000, 1_000_000),
		high("deepseek-v3.1", "stable-translation", 15_000, 1_200_000),
		high("deepseek-v3", "stable-translation", 15_000, 1_200_000),
		high("kimi-k3", "stable-translation", 15_000, 1_200_000),
		high("Moonshot-Kimi-K2-Instruct", "stable-translation", 500, 1_000_000),
		high("MiniMax-M2.5", "stable-translation", 500, 1_000_000),
		high("MiniMax-M2.1", "stable-translation", 500, 1_000_000),
		high("qwen3.8-max", "stable-quality", 30_000, 5_000_000),
		high("qwen3.7-max", "stable-quality", 30_000, 5_000_000),
		high("qwen3-max", "stable-quality", 30_000, 5_000_000),
		high("qwen3.6-plus", "stable-quality", 30_000, 5_000_000),
		high("qwen3.5-plus", "stable-quality", 30_000, 5_000_000),
		high("qwen-plus", "stable-quality", 30_000, 5_000_000),
		high("qwen-plus-latest", "stable-quality", 15_000, 1_200_000),
		high("qwen-turbo", "stable-fast", 1_200, 5_000_000),
		high("qwen3.6-flash-2026-04-16", "fast", 600, 1_000_000),
		high("qwen3.5-flash-2026-02-23", "fast", 600, 1_000_000),
		high("qwen3.7-plus-2026-05-26", "quality", 600, 1_000_000),
		high("qwen3.6-plus-2026-04-02", "quality", 600, 1_000_000),
		high("qwen3-30b-a3b-instruct-2507", "instruct-fallback", 600, 1_000_000),
		{ID: "qwen-mt-flash", Enabled: true, RPM: 60, TPM: 35_000, MinIntervalSeconds: 30, RoutingPriority: 0, RateClass: "low-frequency", Role: "dedicated-translation", Adapter: "qwen-mt", DefaultTargetLanguage: "Chinese", StreamCompatible: boolPointer(true)},
		{ID: "qwen-mt-lite", Enabled: true, RPM: 60, TPM: 100_000, MinIntervalSeconds: 30, RoutingPriority: 0, RateClass: "low-frequency", Role: "dedicated-translation", Adapter: "qwen-mt", DefaultTargetLanguage: "Chinese", StreamCompatible: boolPointer(true)},
		{ID: "qwen-mt-plus", Enabled: true, RPM: 60, TPM: 25_000, MinIntervalSeconds: 30, RoutingPriority: 0, RateClass: "low-frequency", Role: "dedicated-translation-quality", Adapter: "qwen-mt", DefaultTargetLanguage: "Chinese", StreamCompatible: boolPointer(false)},
		{ID: "qwen-mt-turbo", Enabled: true, RPM: 60, TPM: 35_000, MinIntervalSeconds: 30, RoutingPriority: 0, RateClass: "low-frequency", Role: "dedicated-translation", Adapter: "qwen-mt", DefaultTargetLanguage: "Chinese", StreamCompatible: boolPointer(false)},
		low("qwen-flash-2025-07-28", "low-frequency-fast"),
		low("qwen-plus-2025-09-11", "low-frequency-quality"),
		low("qwen-plus-2025-07-28", "low-frequency-quality"),
	}
	return config{
		Version: configVersion, Host: "127.0.0.1", Port: defaultPort,
		AllowLANAccess: false, DashboardEnabled: true,
		UpstreamBaseURL: "https://dashscope.aliyuncs.com/compatible-mode/v1",
		ModelAlias:      modelAlias, RequestTimeoutSeconds: 120, ModelProbeTimeoutSeconds: 15,
		RouteWaitSeconds: 2, RPMSafetyRatio: 0.90, DefaultCooldownSeconds: 60,
		SelectionStrategy: "random_within_priority", MetricsFlushIntervalSeconds: 5,
		Hedging: hedgeConfig{Enabled: true, DelaySeconds: 5, MaxConcurrentBackups: 4},
		Models:  models,
	}
}

func mergeConfig(current config) (config, bool) {
	defaults := defaultConfig()
	changed := current.Version < configVersion
	if current.Host == "" {
		current.Host = defaults.Host
		changed = true
	}
	if current.Port == 0 {
		current.Port = defaults.Port
		changed = true
	}
	if current.UpstreamBaseURL == "" {
		current.UpstreamBaseURL = defaults.UpstreamBaseURL
		changed = true
	}
	if current.ModelAlias == "" {
		current.ModelAlias = defaults.ModelAlias
		changed = true
	}
	if current.RequestTimeoutSeconds == 0 {
		current.RequestTimeoutSeconds = defaults.RequestTimeoutSeconds
		changed = true
	}
	if current.ModelProbeTimeoutSeconds == 0 {
		current.ModelProbeTimeoutSeconds = defaults.ModelProbeTimeoutSeconds
		changed = true
	}
	if current.RouteWaitSeconds == 0 {
		current.RouteWaitSeconds = defaults.RouteWaitSeconds
		changed = true
	}
	if current.RPMSafetyRatio == 0 {
		current.RPMSafetyRatio = defaults.RPMSafetyRatio
		changed = true
	}
	if current.DefaultCooldownSeconds == 0 {
		current.DefaultCooldownSeconds = defaults.DefaultCooldownSeconds
		changed = true
	}
	if current.SelectionStrategy == "" {
		current.SelectionStrategy = defaults.SelectionStrategy
		changed = true
	}
	if current.MetricsFlushIntervalSeconds == 0 {
		current.MetricsFlushIntervalSeconds = defaults.MetricsFlushIntervalSeconds
		changed = true
	}
	if current.Hedging.DelaySeconds == 0 {
		current.Hedging = defaults.Hedging
		changed = true
	}
	existing := make(map[string]bool, len(current.Models))
	for index := range current.Models {
		model := &current.Models[index]
		existing[model.ID] = true
		if model.RPM == 0 {
			model.RPM = 600
			changed = true
		}
		if model.RoutingPriority == 0 && model.Adapter != "qwen-mt" {
			model.RoutingPriority = 10
			changed = true
		}
	}
	for _, model := range defaults.Models {
		if !existing[model.ID] {
			current.Models = append(current.Models, model)
			changed = true
		}
	}
	current.Version = configVersion
	return current, changed
}

func loadConfig() (config, error) {
	if err := ensureStateDirectory(); err != nil {
		return config{}, err
	}
	path := statePath("proxy.json")
	resolved := defaultConfig()
	data, err := os.ReadFile(path)
	if err == nil {
		if err := json.Unmarshal(data, &resolved); err != nil {
			return config{}, fmt.Errorf("invalid proxy.json: %w", err)
		}
		var changed bool
		resolved, changed = mergeConfig(resolved)
		if changed {
			if err := saveConfig(resolved); err != nil {
				return config{}, err
			}
		}
	} else if os.IsNotExist(err) {
		if err := saveConfig(resolved); err != nil {
			return config{}, err
		}
	} else {
		return config{}, err
	}

	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_HOST")); value != "" {
		resolved.Host = value
	}
	if value := strings.TrimSpace(os.Getenv("ALIYUN_PROXY_PORT")); value != "" {
		port, parseErr := strconv.Atoi(value)
		if parseErr != nil {
			return config{}, errors.New("ALIYUN_PROXY_PORT must be an integer")
		}
		resolved.Port = port
	}
	var parseErr error
	resolved.AllowLANAccess, parseErr = environmentBoolean("ALIYUN_PROXY_ALLOW_LAN", resolved.AllowLANAccess)
	if parseErr != nil {
		return config{}, parseErr
	}
	resolved.DashboardEnabled, parseErr = environmentBoolean("ALIYUN_PROXY_DASHBOARD_ENABLED", resolved.DashboardEnabled)
	if parseErr != nil {
		return config{}, parseErr
	}
	if err := validateConfig(resolved); err != nil {
		return config{}, err
	}
	return resolved, nil
}

func environmentBoolean(name string, fallback bool) (bool, error) {
	raw, exists := os.LookupEnv(name)
	if !exists {
		return fallback, nil
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, nil
	case "0", "false", "no", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%s must be a boolean value", name)
	}
}

func validateConfig(cfg config) error {
	if strings.TrimSpace(cfg.Host) == "" {
		return errors.New("proxy host cannot be empty")
	}
	if !isLoopbackHost(cfg.Host) && !cfg.AllowLANAccess {
		return errors.New("non-loopback hosts require allow_lan_access=true or ALIYUN_PROXY_ALLOW_LAN=1")
	}
	if cfg.Port < 1 || cfg.Port > 65535 || cfg.Port == 8080 {
		return errors.New("proxy port must be 1-65535 and cannot be 8080")
	}
	if cfg.RequestTimeoutSeconds <= 0 || cfg.RouteWaitSeconds <= 0 || cfg.MetricsFlushIntervalSeconds <= 0 {
		return errors.New("timeout and metrics intervals must be greater than zero")
	}
	if cfg.Hedging.DelaySeconds <= 0 || cfg.Hedging.MaxConcurrentBackups < 1 {
		return errors.New("invalid hedging configuration")
	}
	if cfg.SelectionStrategy != "round_robin" && cfg.SelectionStrategy != "random_within_priority" {
		return errors.New("selection_strategy must be round_robin or random_within_priority")
	}
	seen, enabled := map[string]bool{}, 0
	for _, model := range cfg.Models {
		id := strings.TrimSpace(model.ID)
		if id == "" || seen[id] {
			return errors.New("model IDs must be non-empty and unique")
		}
		seen[id] = true
		if model.Enabled {
			enabled++
		}
	}
	if enabled == 0 {
		return errors.New("at least one model must be enabled")
	}
	return nil
}

func saveConfig(cfg config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(statePath("proxy.json"), append(data, '\n'), 0o600)
}
