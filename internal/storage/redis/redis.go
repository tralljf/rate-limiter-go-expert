// Package redis provides the Redis limiter.Storage strategy.
package redis

import (
	"context"
	"errors"
	"time"

	goredis "github.com/redis/go-redis/v9"
)

// incrementScript increments the counter and sets its expiration only when the
// key is created, in a single atomic step on the Redis server.
var incrementScript = goredis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("PEXPIRE", KEYS[1], ARGV[1])
end
return count
`)

// Storage is a Redis implementation of limiter.Storage.
type Storage struct {
	client goredis.UniversalClient
}

// New wraps an existing Redis client.
func New(client goredis.UniversalClient) *Storage {
	return &Storage{client: client}
}

// Increment implements limiter.Storage.
func (s *Storage) Increment(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	return incrementScript.Run(ctx, s.client, []string{key}, ttl.Milliseconds()).Int64()
}

// Block implements limiter.Storage.
func (s *Storage) Block(ctx context.Context, key string, duration time.Duration) error {
	return s.client.Set(ctx, key, 1, duration).Err()
}

// IsBlocked implements limiter.Storage.
func (s *Storage) IsBlocked(ctx context.Context, key string) (bool, error) {
	err := s.client.Get(ctx, key).Err()
	if errors.Is(err, goredis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}
