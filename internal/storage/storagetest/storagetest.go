// Package storagetest provides a contract test suite that every
// limiter.Storage strategy must pass.
package storagetest

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
)

// Run executes the contract tests. newStorage must return an empty storage;
// wait must advance time by d (sleeping for real storages).
func Run(t *testing.T, newStorage func(t *testing.T) limiter.Storage, wait func(d time.Duration)) {
	t.Helper()
	ctx := context.Background()

	t.Run("increment counts and expires", func(t *testing.T) {
		s := newStorage(t)
		for want := int64(1); want <= 3; want++ {
			got, err := s.Increment(ctx, "counter", 200*time.Millisecond)
			if err != nil {
				t.Fatalf("Increment: %v", err)
			}
			if got != want {
				t.Fatalf("Increment = %d, want %d", got, want)
			}
		}
		wait(300 * time.Millisecond)
		got, err := s.Increment(ctx, "counter", 200*time.Millisecond)
		if err != nil {
			t.Fatalf("Increment: %v", err)
		}
		if got != 1 {
			t.Fatalf("Increment after expiration = %d, want 1", got)
		}
	})

	t.Run("block expires", func(t *testing.T) {
		s := newStorage(t)
		assertBlocked(t, s, "blocked", false)
		if err := s.Block(ctx, "blocked", 200*time.Millisecond); err != nil {
			t.Fatalf("Block: %v", err)
		}
		assertBlocked(t, s, "blocked", true)
		assertBlocked(t, s, "other", false)
		wait(300 * time.Millisecond)
		assertBlocked(t, s, "blocked", false)
	})

	t.Run("increment is atomic under concurrency", func(t *testing.T) {
		s := newStorage(t)
		const workers = 50
		var wg sync.WaitGroup
		seen := make(chan int64, workers)
		for range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				v, err := s.Increment(ctx, "concurrent", time.Minute)
				if err != nil {
					t.Errorf("Increment: %v", err)
					return
				}
				seen <- v
			}()
		}
		wg.Wait()
		close(seen)
		unique := make(map[int64]bool)
		for v := range seen {
			if unique[v] {
				t.Fatalf("value %d returned twice", v)
			}
			unique[v] = true
		}
		if len(unique) != workers {
			t.Fatalf("got %d distinct values, want %d", len(unique), workers)
		}
	})
}

func assertBlocked(t *testing.T, s limiter.Storage, key string, want bool) {
	t.Helper()
	got, err := s.IsBlocked(context.Background(), key)
	if err != nil {
		t.Fatalf("IsBlocked: %v", err)
	}
	if got != want {
		t.Fatalf("IsBlocked(%q) = %v, want %v", key, got, want)
	}
}
