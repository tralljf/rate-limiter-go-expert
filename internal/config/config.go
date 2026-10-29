// Package config loads the application settings from environment variables,
// optionally seeded by a .env file.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
)

// Storage strategies supported by the application.
const (
	StorageRedis  = "redis"
	StorageMemory = "memory"
)

// Config is the application configuration.
type Config struct {
	Port              string
	Storage           string
	TrustForwardedFor bool
	Limiter           limiter.Config
	Redis             Redis
}

// Redis holds the Redis connection settings.
type Redis struct {
	Addr     string
	Password string
	DB       int
}

// Load reads the configuration from the environment. Variables already set in
// the environment take precedence over the ones in envFile; a missing envFile
// is not an error.
func Load(envFile string) (Config, error) {
	if err := loadDotEnv(envFile); err != nil {
		return Config{}, err
	}

	var errs []error
	cfg := Config{
		Port:              get("SERVER_PORT", "8080"),
		Storage:           strings.ToLower(get("RATE_LIMIT_STORAGE", StorageRedis)),
		TrustForwardedFor: parse(&errs, "RATE_LIMIT_TRUST_FORWARDED_FOR", "false", strconv.ParseBool),
		Limiter: limiter.Config{
			IPLimit:           parse(&errs, "RATE_LIMIT_IP", "10", strconv.Atoi),
			DefaultTokenLimit: parse(&errs, "RATE_LIMIT_TOKEN_DEFAULT", "0", strconv.Atoi),
			Window:            parse(&errs, "RATE_LIMIT_WINDOW", "1s", time.ParseDuration),
			BlockDuration:     parse(&errs, "RATE_LIMIT_BLOCK_DURATION", "5m", time.ParseDuration),
		},
		Redis: Redis{
			Addr:     get("REDIS_ADDR", "localhost:6379"),
			Password: get("REDIS_PASSWORD", ""),
			DB:       parse(&errs, "REDIS_DB", "0", strconv.Atoi),
		},
	}

	tokens, err := ParseTokenLimits(get("RATE_LIMIT_TOKENS", ""))
	if err != nil {
		errs = append(errs, fmt.Errorf("RATE_LIMIT_TOKENS: %w", err))
	}
	cfg.Limiter.TokenLimits = tokens

	if cfg.Storage != StorageRedis && cfg.Storage != StorageMemory {
		errs = append(errs, fmt.Errorf("RATE_LIMIT_STORAGE: unknown strategy %q", cfg.Storage))
	}
	if err := cfg.Limiter.Validate(); err != nil {
		errs = append(errs, err)
	}
	return cfg, errors.Join(errs...)
}

// ParseTokenLimits parses a list in the form "token1:100,token2:50".
func ParseTokenLimits(raw string) (map[string]int, error) {
	limits := make(map[string]int)
	for item := range strings.SplitSeq(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		token, limit, ok := strings.Cut(item, ":")
		token = strings.TrimSpace(token)
		if !ok || token == "" {
			return nil, fmt.Errorf("invalid entry %q, expected token:limit", item)
		}
		n, err := strconv.Atoi(strings.TrimSpace(limit))
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid limit in entry %q", item)
		}
		limits[token] = n
	}
	return limits, nil
}

func get(key, fallback string) string {
	if v, ok := os.LookupEnv(key); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	return fallback
}

func parse[T any](errs *[]error, key, fallback string, fn func(string) (T, error)) T {
	v, err := fn(get(key, fallback))
	if err != nil {
		*errs = append(*errs, fmt.Errorf("%s: %w", key, err))
	}
	return v
}

// loadDotEnv sets the variables declared in path that are not already present
// in the environment. Lines are KEY=VALUE; blank lines and # comments are ignored.
func loadDotEnv(path string) error {
	if path == "" {
		return nil
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for line := 1; scanner.Scan(); line++ {
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(strings.TrimPrefix(text, "export "), "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return fmt.Errorf("%s:%d: invalid line", path, line)
		}
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	return scanner.Err()
}
