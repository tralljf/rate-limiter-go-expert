package limiter_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/memory"
)

type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func newLimiter(t *testing.T, cfg limiter.Config) (*limiter.Limiter, *clock) {
	t.Helper()
	c := &clock{now: time.Unix(1_700_000_000, 0)}
	if cfg.Window == 0 {
		cfg.Window = time.Second
	}
	if cfg.BlockDuration == 0 {
		cfg.BlockDuration = 5 * time.Minute
	}
	l, err := limiter.New(memory.New(memory.WithClock(c.Now)), cfg)
	if err != nil {
		t.Fatalf("limiter.New: %v", err)
	}
	return l, c
}

// send performs n requests and returns how many were allowed.
func send(t *testing.T, l *limiter.Limiter, n int, ip, token string) int {
	t.Helper()
	allowed := 0
	for range n {
		res, err := l.Allow(context.Background(), ip, token)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		if res.Allowed {
			allowed++
		}
	}
	return allowed
}

func TestIPLimit(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 10})

	if got := send(t, l, 15, "192.168.0.1", ""); got != 10 {
		t.Fatalf("allowed %d requests, want 10", got)
	}
	if got := send(t, l, 10, "192.168.0.2", ""); got != 10 {
		t.Fatalf("other IP allowed %d requests, want 10 (limits are per IP)", got)
	}
}

func TestTokenLimit(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 10, TokenLimits: map[string]int{"abc": 3}})

	if got := send(t, l, 5, "192.168.0.1", "abc"); got != 3 {
		t.Fatalf("allowed %d requests with token, want 3", got)
	}
	if got := send(t, l, 1, "192.168.0.2", "abc"); got != 0 {
		t.Fatal("blocked token must stay blocked even from another IP")
	}
	if got := send(t, l, 10, "192.168.0.1", ""); got != 10 {
		t.Fatalf("IP without token allowed %d requests, want 10 (token block must not affect IP)", got)
	}
}

func TestTokenOverridesIP(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 10, TokenLimits: map[string]int{"abc": 100}})

	if got := send(t, l, 100, "192.168.0.1", "abc"); got != 100 {
		t.Fatalf("allowed %d requests with token, want 100", got)
	}
	if got := send(t, l, 1, "192.168.0.1", "abc"); got != 0 {
		t.Fatal("101st request with token should be blocked")
	}
}

func TestTokenWithLowerLimitOverridesIP(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 10, TokenLimits: map[string]int{"low": 2}})

	if got := send(t, l, 5, "192.168.0.1", "low"); got != 2 {
		t.Fatalf("allowed %d requests, want 2 (token rule replaces the IP rule)", got)
	}
}

func TestTokenBypassesBlockedIP(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 2, TokenLimits: map[string]int{"abc": 5}})

	send(t, l, 3, "192.168.0.1", "")
	if got := send(t, l, 1, "192.168.0.1", ""); got != 0 {
		t.Fatal("IP should be blocked")
	}
	if got := send(t, l, 5, "192.168.0.1", "abc"); got != 5 {
		t.Fatalf("allowed %d requests with token from blocked IP, want 5", got)
	}
}

func TestUnknownToken(t *testing.T) {
	t.Run("falls back to IP when no default", func(t *testing.T) {
		l, _ := newLimiter(t, limiter.Config{IPLimit: 2, TokenLimits: map[string]int{"abc": 50}})
		if got := send(t, l, 5, "192.168.0.1", "unknown"); got != 2 {
			t.Fatalf("allowed %d requests, want IP limit 2", got)
		}
		if got := send(t, l, 1, "192.168.0.1", "another-unknown"); got != 0 {
			t.Fatal("rotating unknown tokens must not bypass the IP block")
		}
	})

	t.Run("uses default token limit when set", func(t *testing.T) {
		l, _ := newLimiter(t, limiter.Config{IPLimit: 2, DefaultTokenLimit: 4})
		if got := send(t, l, 6, "192.168.0.1", "unknown"); got != 4 {
			t.Fatalf("allowed %d requests, want default token limit 4", got)
		}
	})
}

func TestBlockDuration(t *testing.T) {
	l, c := newLimiter(t, limiter.Config{IPLimit: 5, Window: time.Second, BlockDuration: 5 * time.Minute})

	start := c.Now()
	if got := send(t, l, 6, "10.0.0.1", ""); got != 5 {
		t.Fatalf("allowed %d requests, want 5", got)
	}

	// The window resets every second, but the block must last 5 minutes.
	for _, elapsed := range []time.Duration{time.Second, time.Minute, 4*time.Minute + 59*time.Second} {
		c.Advance(start.Add(elapsed).Sub(c.Now()))
		if got := send(t, l, 1, "10.0.0.1", ""); got != 0 {
			t.Fatalf("request after %v should be rejected while blocked", elapsed)
		}
	}

	c.Advance(start.Add(5 * time.Minute).Sub(c.Now()))
	if got := send(t, l, 5, "10.0.0.1", ""); got != 5 {
		t.Fatalf("allowed %d requests after the block expired, want 5", got)
	}
}

func TestWindowResets(t *testing.T) {
	l, c := newLimiter(t, limiter.Config{IPLimit: 3, Window: time.Second})

	if got := send(t, l, 3, "10.0.0.1", ""); got != 3 {
		t.Fatalf("allowed %d, want 3", got)
	}
	c.Advance(time.Second)
	if got := send(t, l, 3, "10.0.0.1", ""); got != 3 {
		t.Fatalf("allowed %d in the next window, want 3", got)
	}
}

func TestConcurrentRequests(t *testing.T) {
	l, _ := newLimiter(t, limiter.Config{IPLimit: 10})

	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 100 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res, err := l.Allow(context.Background(), "10.0.0.1", "")
			if err != nil {
				t.Errorf("Allow: %v", err)
				return
			}
			if res.Allowed {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("allowed %d concurrent requests, want 10", allowed.Load())
	}
}

type failingStorage struct{ limiter.Storage }

func (failingStorage) IsBlocked(context.Context, string) (bool, error) {
	return false, errors.New("boom")
}

func TestStorageError(t *testing.T) {
	l, err := limiter.New(failingStorage{memory.New()}, limiter.Config{IPLimit: 1, Window: time.Second, BlockDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := l.Allow(context.Background(), "10.0.0.1", ""); err == nil {
		t.Fatal("expected error from storage")
	}
}

func TestConfigValidation(t *testing.T) {
	valid := limiter.Config{IPLimit: 1, Window: time.Second, BlockDuration: time.Second}
	tests := map[string]func(*limiter.Config){
		"zero ip limit":       func(c *limiter.Config) { c.IPLimit = 0 },
		"negative default":    func(c *limiter.Config) { c.DefaultTokenLimit = -1 },
		"zero token limit":    func(c *limiter.Config) { c.TokenLimits = map[string]int{"abc": 0} },
		"empty token":         func(c *limiter.Config) { c.TokenLimits = map[string]int{"": 1} },
		"zero window":         func(c *limiter.Config) { c.Window = 0 },
		"zero block duration": func(c *limiter.Config) { c.BlockDuration = 0 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			mutate(&cfg)
			if _, err := limiter.New(memory.New(), cfg); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
	if _, err := limiter.New(nil, valid); err == nil {
		t.Fatal("expected error for nil storage")
	}
}
