package redis_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
	redisstorage "github.com/tralljf/rate-limiter-go-expert/internal/storage/redis"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/storagetest"
)

// newClient connects to the Redis pointed by REDIS_TEST_ADDR and isolates each
// test with a unique key prefix. The test is skipped when the variable is unset.
func newClient(t *testing.T) goredis.UniversalClient {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; run `docker compose run --rm test` to include the Redis tests")
	}
	client := goredis.NewClient(&goredis.Options{Addr: addr, Password: os.Getenv("REDIS_TEST_PASSWORD")})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("connecting to redis at %s: %v", addr, err)
	}
	return client
}

// prefixed namespaces every key so tests do not interfere with each other.
type prefixed struct {
	limiter.Storage
	prefix string
}

func (p prefixed) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return p.Storage.Increment(ctx, p.prefix+key, ttl)
}

func (p prefixed) Block(ctx context.Context, key string, d time.Duration) error {
	return p.Storage.Block(ctx, p.prefix+key, d)
}

func (p prefixed) IsBlocked(ctx context.Context, key string) (bool, error) {
	return p.Storage.IsBlocked(ctx, p.prefix+key)
}

func newStorage(t *testing.T) limiter.Storage {
	prefix := fmt.Sprintf("test:%s:%d:", strings.ReplaceAll(t.Name(), " ", "_"), time.Now().UnixNano())
	return prefixed{Storage: redisstorage.New(newClient(t)), prefix: prefix}
}

func TestStorageContract(t *testing.T) {
	storagetest.Run(t, newStorage, time.Sleep)
}

func TestIncrementSetsTTLOnlyOnCreation(t *testing.T) {
	client := newClient(t)
	s := redisstorage.New(client)
	ctx := context.Background()
	key := fmt.Sprintf("test:ttl:%d", time.Now().UnixNano())
	t.Cleanup(func() { client.Del(ctx, key) })

	if _, err := s.Increment(ctx, key, time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Increment(ctx, key, time.Hour); err != nil {
		t.Fatal(err)
	}
	ttl, err := client.PTTL(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if ttl <= 0 || ttl > time.Minute {
		t.Fatalf("TTL = %v, want within (0, 1m]", ttl)
	}
}

func TestLimiterWithRedis(t *testing.T) {
	ctx := context.Background()
	l, err := limiter.New(newStorage(t), limiter.Config{
		IPLimit:       2,
		TokenLimits:   map[string]int{"premium": 5},
		Window:        time.Second,
		BlockDuration: 1500 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	allow := func(ip, token string) bool {
		t.Helper()
		res, err := l.Allow(ctx, ip, token)
		if err != nil {
			t.Fatalf("Allow: %v", err)
		}
		return res.Allowed
	}

	for i := range 2 {
		if !allow("10.0.0.1", "") {
			t.Fatalf("request %d by IP should be allowed", i+1)
		}
	}
	if allow("10.0.0.1", "") {
		t.Fatal("3rd request by IP should be blocked")
	}

	for i := range 5 {
		if !allow("10.0.0.1", "premium") {
			t.Fatalf("request %d with token should be allowed despite the IP block", i+1)
		}
	}
	if allow("10.0.0.1", "premium") {
		t.Fatal("6th request with token should be blocked")
	}

	time.Sleep(1100 * time.Millisecond)
	if allow("10.0.0.1", "") {
		t.Fatal("IP must remain blocked after the window resets")
	}

	time.Sleep(600 * time.Millisecond)
	if !allow("10.0.0.1", "") {
		t.Fatal("IP should be allowed after the block expires")
	}
}
