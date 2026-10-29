package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	goredis "github.com/redis/go-redis/v9"

	"github.com/tralljf/rate-limiter-go-expert/internal/config"
	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
	"github.com/tralljf/rate-limiter-go-expert/internal/middleware"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/memory"
	redisstorage "github.com/tralljf/rate-limiter-go-expert/internal/storage/redis"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("server stopped", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load(".env")
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	storage, closeStorage, err := newStorage(ctx, cfg)
	if err != nil {
		return err
	}
	defer closeStorage()

	l, err := limiter.New(storage, cfg.Limiter)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"message":"ok"}`))
	})

	rateLimit := middleware.RateLimit(l, middleware.Options{
		TrustForwardedFor: cfg.TrustForwardedFor,
		Logger:            logger,
	})
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           rateLimit(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("server listening",
			"addr", srv.Addr,
			"storage", cfg.Storage,
			"ip_limit", cfg.Limiter.IPLimit,
			"tokens", len(cfg.Limiter.TokenLimits),
			"window", cfg.Limiter.Window,
			"block", cfg.Limiter.BlockDuration,
		)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// newStorage selects the persistence strategy configured by RATE_LIMIT_STORAGE.
func newStorage(ctx context.Context, cfg config.Config) (limiter.Storage, func(), error) {
	switch cfg.Storage {
	case config.StorageMemory:
		return memory.New(), func() {}, nil
	default:
		client := goredis.NewClient(&goredis.Options{
			Addr:     cfg.Redis.Addr,
			Password: cfg.Redis.Password,
			DB:       cfg.Redis.DB,
		})
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Ping(pingCtx).Err(); err != nil {
			_ = client.Close()
			return nil, nil, fmt.Errorf("connecting to redis at %s: %w", cfg.Redis.Addr, err)
		}
		return redisstorage.New(client), func() { _ = client.Close() }, nil
	}
}
