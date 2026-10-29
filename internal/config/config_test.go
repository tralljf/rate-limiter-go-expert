package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/config"
)

var envKeys = []string{
	"SERVER_PORT", "RATE_LIMIT_STORAGE", "RATE_LIMIT_TRUST_FORWARDED_FOR", "RATE_LIMIT_IP",
	"RATE_LIMIT_TOKENS", "RATE_LIMIT_TOKEN_DEFAULT", "RATE_LIMIT_WINDOW", "RATE_LIMIT_BLOCK_DURATION",
	"REDIS_ADDR", "REDIS_PASSWORD", "REDIS_DB",
}

// clearEnv unsets the config variables for the duration of the test.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range envKeys {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
}

func TestLoadFromDotEnvAndEnvironment(t *testing.T) {
	clearEnv(t)
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\nRATE_LIMIT_IP=7\nRATE_LIMIT_TOKENS=abc:100, def:50\nRATE_LIMIT_BLOCK_DURATION=2m\nREDIS_ADDR=redis:6379\nREDIS_DB=2\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RATE_LIMIT_IP", "20")

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Limiter.IPLimit != 20 {
		t.Errorf("IPLimit = %d, want 20 (environment wins over .env)", cfg.Limiter.IPLimit)
	}
	if cfg.Limiter.TokenLimits["abc"] != 100 || cfg.Limiter.TokenLimits["def"] != 50 {
		t.Errorf("TokenLimits = %v", cfg.Limiter.TokenLimits)
	}
	if cfg.Limiter.BlockDuration != 2*time.Minute {
		t.Errorf("BlockDuration = %v, want 2m", cfg.Limiter.BlockDuration)
	}
	if cfg.Limiter.Window != time.Second {
		t.Errorf("Window = %v, want default 1s", cfg.Limiter.Window)
	}
	if cfg.Redis.Addr != "redis:6379" || cfg.Redis.DB != 2 {
		t.Errorf("Redis = %+v", cfg.Redis)
	}
	if cfg.Storage != config.StorageRedis || cfg.Port != "8080" {
		t.Errorf("Storage = %q, Port = %q", cfg.Storage, cfg.Port)
	}
}

func TestLoadInvalid(t *testing.T) {
	tests := map[string][2]string{
		"bad ip limit":  {"RATE_LIMIT_IP", "abc"},
		"zero ip limit": {"RATE_LIMIT_IP", "0"},
		"bad tokens":    {"RATE_LIMIT_TOKENS", "abc"},
		"bad duration":  {"RATE_LIMIT_BLOCK_DURATION", "5"},
		"bad storage":   {"RATE_LIMIT_STORAGE", "mongo"},
	}
	for name, kv := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv(kv[0], kv[1])
			if _, err := config.Load(""); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestParseTokenLimits(t *testing.T) {
	got, err := config.ParseTokenLimits(" abc:100 ,def:50,")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["abc"] != 100 || got["def"] != 50 {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"abc", ":10", "abc:x", "abc:-1"} {
		if _, err := config.ParseTokenLimits(bad); err == nil {
			t.Errorf("ParseTokenLimits(%q): expected error", bad)
		}
	}
}
