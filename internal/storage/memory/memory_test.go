package memory_test

import (
	"sync"
	"testing"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/memory"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/storagetest"
)

func TestStorageContract(t *testing.T) {
	var (
		mu  sync.Mutex
		now = time.Unix(0, 0)
	)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	advance := func(d time.Duration) {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(d)
	}
	storagetest.Run(t, func(*testing.T) limiter.Storage {
		return memory.New(memory.WithClock(clock))
	}, advance)
}
