// Package limiter contains the rate limiting domain logic. It knows nothing
// about HTTP: callers provide the client IP and the (optional) access token and
// receive a decision. Persistence is delegated to a Storage strategy.
package limiter

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Storage is the persistence strategy used by the Limiter. Implementations
// must be safe for concurrent use and Increment must be atomic.
type Storage interface {
	// Increment atomically increments the counter stored at key and returns
	// the new value. When the key is created, it must expire after ttl.
	Increment(ctx context.Context, key string, ttl time.Duration) (int64, error)
	// Block marks key as blocked for the given duration.
	Block(ctx context.Context, key string, duration time.Duration) error
	// IsBlocked reports whether key is currently blocked.
	IsBlocked(ctx context.Context, key string) (bool, error)
}

// Config holds the limiter rules.
type Config struct {
	// IPLimit is the maximum number of requests per Window for a single IP.
	IPLimit int
	// TokenLimits maps an access token to its maximum number of requests per Window.
	TokenLimits map[string]int
	// DefaultTokenLimit is applied to tokens absent from TokenLimits. When it is
	// zero, unknown tokens are ignored and the request is limited by IP.
	DefaultTokenLimit int
	// Window is the length of the counting window (1s means requests per second).
	Window time.Duration
	// BlockDuration is how long an IP or token stays blocked after exceeding its limit.
	BlockDuration time.Duration
}

// Validate checks that the configuration is usable.
func (c Config) Validate() error {
	if c.IPLimit <= 0 {
		return errors.New("IP limit must be greater than zero")
	}
	if c.DefaultTokenLimit < 0 {
		return errors.New("default token limit must not be negative")
	}
	for token, limit := range c.TokenLimits {
		if token == "" {
			return errors.New("token must not be empty")
		}
		if limit <= 0 {
			return fmt.Errorf("limit for token %q must be greater than zero", token)
		}
	}
	if c.Window <= 0 {
		return errors.New("window must be greater than zero")
	}
	if c.BlockDuration <= 0 {
		return errors.New("block duration must be greater than zero")
	}
	return nil
}

// Kind identifies which criterion was used to limit a request.
type Kind string

const (
	KindIP    Kind = "ip"
	KindToken Kind = "token"
)

// Result describes the decision taken for a request.
type Result struct {
	Allowed bool
	Kind    Kind
	Limit   int
	Count   int64
}

// Limiter applies the IP and token rules using a Storage strategy.
type Limiter struct {
	storage Storage
	cfg     Config
}

// New creates a Limiter. It returns an error when the configuration is invalid.
func New(storage Storage, cfg Config) (*Limiter, error) {
	if storage == nil {
		return nil, errors.New("storage must not be nil")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Limiter{storage: storage, cfg: cfg}, nil
}

// Allow decides whether a request coming from ip with the given token may
// proceed. A recognised token takes precedence over the IP: only the token
// counter and block are evaluated in that case.
func (l *Limiter) Allow(ctx context.Context, ip, token string) (Result, error) {
	kind, id, limit := l.rule(ip, token)
	res := Result{Kind: kind, Limit: limit}

	blockKey := "ratelimit:block:" + string(kind) + ":" + id
	blocked, err := l.storage.IsBlocked(ctx, blockKey)
	if err != nil {
		return res, fmt.Errorf("checking block: %w", err)
	}
	if blocked {
		return res, nil
	}

	count, err := l.storage.Increment(ctx, "ratelimit:count:"+string(kind)+":"+id, l.cfg.Window)
	if err != nil {
		return res, fmt.Errorf("incrementing counter: %w", err)
	}
	res.Count = count
	if count > int64(limit) {
		if err := l.storage.Block(ctx, blockKey, l.cfg.BlockDuration); err != nil {
			return res, fmt.Errorf("blocking: %w", err)
		}
		return res, nil
	}
	res.Allowed = true
	return res, nil
}

func (l *Limiter) rule(ip, token string) (Kind, string, int) {
	if token != "" {
		if limit, ok := l.cfg.TokenLimits[token]; ok {
			return KindToken, token, limit
		}
		if l.cfg.DefaultTokenLimit > 0 {
			return KindToken, token, l.cfg.DefaultTokenLimit
		}
	}
	return KindIP, ip, l.cfg.IPLimit
}
