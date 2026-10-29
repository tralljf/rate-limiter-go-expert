// Package memory provides an in-memory limiter.Storage strategy. It is useful
// for tests and single-instance deployments.
package memory

import (
	"context"
	"sync"
	"time"
)

type entry struct {
	value     int64
	expiresAt time.Time
}

// Storage is a thread-safe in-memory implementation of limiter.Storage.
type Storage struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]entry
}

// Option configures a Storage.
type Option func(*Storage)

// WithClock overrides the clock used to compute expirations.
func WithClock(now func() time.Time) Option {
	return func(s *Storage) { s.now = now }
}

// New creates an empty Storage.
func New(opts ...Option) *Storage {
	s := &Storage{now: time.Now, entries: make(map[string]entry)}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Increment implements limiter.Storage.
func (s *Storage) Increment(_ context.Context, key string, ttl time.Duration) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	e, ok := s.get(key, now)
	if !ok {
		e = entry{expiresAt: now.Add(ttl)}
	}
	e.value++
	s.entries[key] = e
	return e.value, nil
}

// Block implements limiter.Storage.
func (s *Storage) Block(_ context.Context, key string, duration time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries[key] = entry{value: 1, expiresAt: s.now().Add(duration)}
	return nil
}

// IsBlocked implements limiter.Storage.
func (s *Storage) IsBlocked(_ context.Context, key string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.get(key, s.now())
	return ok, nil
}

// get returns a live entry, removing it when expired. Callers must hold mu.
func (s *Storage) get(key string, now time.Time) (entry, bool) {
	e, ok := s.entries[key]
	if !ok {
		return entry{}, false
	}
	if !now.Before(e.expiresAt) {
		delete(s.entries, key)
		return entry{}, false
	}
	return e, true
}
