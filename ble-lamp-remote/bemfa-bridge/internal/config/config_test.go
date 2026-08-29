package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	valid := Config{
		BrokerURL:   "ssl://bemfa.com:9503",
		Topic:       "xidingdeng002",
		UID:         strings.Repeat("a", 32),
		LampAPIURL:  "http://192.168.31.129:8791",
		DryRun:      true,
		HTTPTimeout: 3 * time.Second,
		DedupWindow: 5 * time.Second,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "wrong topic suffix", mutate: func(c *Config) { c.Topic = "xidingdeng001" }},
		{name: "topic punctuation", mutate: func(c *Config) { c.Topic = "xiding_deng002" }},
		{name: "plaintext broker", mutate: func(c *Config) { c.BrokerURL = "tcp://bemfa.com:9501" }},
		{name: "public lamp host", mutate: func(c *Config) { c.LampAPIURL = "http://example.com:8791" }},
		{name: "lamp URL path", mutate: func(c *Config) { c.LampAPIURL = "http://192.168.31.129:8791/v1" }},
		{name: "missing live token", mutate: func(c *Config) { c.DryRun = false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			tt.mutate(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() error = nil, want error")
			}
		})
	}
}

func TestLoadReadsProtectedSecretFiles(t *testing.T) {
	t.Setenv("BEMFA_BRIDGE_BROKER_URL", "ssl://bemfa.com:9503")
	t.Setenv("BEMFA_BRIDGE_TOPIC", "xidingdeng002")
	t.Setenv("BEMFA_BRIDGE_LAMP_API_URL", "http://127.0.0.1:8791")
	t.Setenv("BEMFA_BRIDGE_DRY_RUN", "false")

	dir := t.TempDir()
	uidFile := filepath.Join(dir, "uid")
	tokenFile := filepath.Join(dir, "lamp-token")
	writeSecret(t, uidFile, strings.Repeat("b", 32))
	writeSecret(t, tokenFile, strings.Repeat("c", 64))
	t.Setenv("BEMFA_BRIDGE_UID_FILE", uidFile)
	t.Setenv("BEMFA_BRIDGE_LAMP_TOKEN_FILE", tokenFile)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.UID != strings.Repeat("b", 32) || cfg.LampToken != strings.Repeat("c", 64) {
		t.Fatal("Load() did not read expected secrets")
	}
}

func TestReadSecretRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix permission test")
	}

	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSecret(path); err == nil {
		t.Fatal("readSecret() error = nil, want permission error")
	}
}

func writeSecret(t *testing.T, path, value string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}
