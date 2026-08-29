package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBrokerURL   = "ssl://bemfa.com:9503"
	defaultHTTPTimeout = 3 * time.Second
	defaultDedupWindow = 5 * time.Second
)

var (
	topicPattern = regexp.MustCompile(`^[A-Za-z0-9]{1,64}$`)
	uidPattern   = regexp.MustCompile(`^[A-Za-z0-9]{16,64}$`)
	tokenPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type Config struct {
	BrokerURL   string
	Topic       string
	UID         string
	LampAPIURL  string
	LampToken   string
	DryRun      bool
	HTTPTimeout time.Duration
	DedupWindow time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		BrokerURL:   envOrDefault("BEMFA_BRIDGE_BROKER_URL", defaultBrokerURL),
		Topic:       strings.TrimSpace(os.Getenv("BEMFA_BRIDGE_TOPIC")),
		LampAPIURL:  strings.TrimSpace(os.Getenv("BEMFA_BRIDGE_LAMP_API_URL")),
		HTTPTimeout: defaultHTTPTimeout,
		DedupWindow: defaultDedupWindow,
		DryRun:      true,
	}

	var err error
	if cfg.DryRun, err = boolEnv("BEMFA_BRIDGE_DRY_RUN", true); err != nil {
		return Config{}, err
	}
	if cfg.HTTPTimeout, err = durationEnv("BEMFA_BRIDGE_HTTP_TIMEOUT", defaultHTTPTimeout); err != nil {
		return Config{}, err
	}
	if cfg.DedupWindow, err = durationEnv("BEMFA_BRIDGE_DEDUP_WINDOW", defaultDedupWindow); err != nil {
		return Config{}, err
	}

	uidFile := strings.TrimSpace(os.Getenv("BEMFA_BRIDGE_UID_FILE"))
	if uidFile == "" {
		return Config{}, errors.New("BEMFA_BRIDGE_UID_FILE is required")
	}
	if cfg.UID, err = readSecret(uidFile); err != nil {
		return Config{}, fmt.Errorf("read Bemfa UID: %w", err)
	}

	tokenFile := strings.TrimSpace(os.Getenv("BEMFA_BRIDGE_LAMP_TOKEN_FILE"))
	if tokenFile != "" {
		if cfg.LampToken, err = readSecret(tokenFile); err != nil {
			return Config{}, fmt.Errorf("read lamp API token: %w", err)
		}
	}

	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) Validate() error {
	if !topicPattern.MatchString(c.Topic) || !strings.HasSuffix(c.Topic, "002") {
		return errors.New("BEMFA_BRIDGE_TOPIC must be 1-64 letters/digits and end in 002")
	}
	if !uidPattern.MatchString(c.UID) {
		return errors.New("Bemfa UID has an invalid format")
	}
	if err := validateBrokerURL(c.BrokerURL); err != nil {
		return err
	}
	if err := validateLampAPIURL(c.LampAPIURL); err != nil {
		return err
	}
	if c.HTTPTimeout <= 0 || c.HTTPTimeout > 30*time.Second {
		return errors.New("BEMFA_BRIDGE_HTTP_TIMEOUT must be between 1ns and 30s")
	}
	if c.DedupWindow < 0 || c.DedupWindow > time.Minute {
		return errors.New("BEMFA_BRIDGE_DEDUP_WINDOW must be between 0 and 1m")
	}
	if !c.DryRun && !tokenPattern.MatchString(c.LampToken) {
		return errors.New("a valid 64-character lowercase lamp API token is required when dry-run is disabled")
	}
	if c.LampToken != "" && !tokenPattern.MatchString(c.LampToken) {
		return errors.New("lamp API token has an invalid format")
	}
	return nil
}

func validateBrokerURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("BEMFA_BRIDGE_BROKER_URL could not be parsed")
	}
	if parsed.Scheme != "ssl" || parsed.Hostname() == "" || parsed.Port() == "" {
		return errors.New("BEMFA_BRIDGE_BROKER_URL must use ssl:// with an explicit port")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("BEMFA_BRIDGE_BROKER_URL must not contain credentials, a path, query, or fragment")
	}
	return nil
}

func validateLampAPIURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return errors.New("BEMFA_BRIDGE_LAMP_API_URL could not be parsed")
	}
	if parsed.Scheme != "http" || parsed.Hostname() == "" || parsed.Port() == "" {
		return errors.New("BEMFA_BRIDGE_LAMP_API_URL must use http:// with an explicit port")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errors.New("BEMFA_BRIDGE_LAMP_API_URL must be an origin without credentials, path, query, or fragment")
	}
	ip := net.ParseIP(parsed.Hostname())
	if ip == nil || (!ip.IsPrivate() && !ip.IsLoopback()) {
		return errors.New("BEMFA_BRIDGE_LAMP_API_URL host must be a private or loopback IP address")
	}
	return nil
}

func readSecret(rawPath string) (string, error) {
	path, err := expandHome(rawPath)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("secret path is not a regular file")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("secret file %s must not be readable or writable by group/other", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", errors.New("secret file is empty")
	}
	return value, nil
}

func expandHome(path string) (string, error) {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		if path == "~" {
			return home, nil
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~/")), nil
	}
	return path, nil
}

func envOrDefault(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func boolEnv(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func durationEnv(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a Go duration: %w", name, err)
	}
	return parsed, nil
}
