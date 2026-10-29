package middleware_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
	"github.com/tralljf/rate-limiter-go-expert/internal/middleware"
	"github.com/tralljf/rate-limiter-go-expert/internal/storage/memory"
)

const wantMessage = "you have reached the maximum number of requests or actions allowed within a certain time frame"

func newHandler(t *testing.T, cfg limiter.Config) http.Handler {
	t.Helper()
	cfg.Window = time.Second
	cfg.BlockDuration = 5 * time.Minute
	l, err := limiter.New(memory.New(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	})
	return middleware.RateLimit(l, middleware.Options{})(next)
}

func do(h http.Handler, ip, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ip + ":12345"
	if token != "" {
		req.Header.Set(middleware.TokenHeader, token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestReturns429WithExactMessage(t *testing.T) {
	h := newHandler(t, limiter.Config{IPLimit: 2})

	for i := range 2 {
		if rec := do(h, "1.1.1.1", ""); rec.Code != http.StatusOK {
			t.Fatalf("request %d: status %d, want 200", i+1, rec.Code)
		}
	}
	rec := do(h, "1.1.1.1", "")
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if body := rec.Body.String(); body != wantMessage {
		t.Fatalf("body %q, want %q", body, wantMessage)
	}
	if middleware.LimitExceededMessage != wantMessage {
		t.Fatal("exported message differs from the specification")
	}
}

func TestTokenHeaderOverridesIP(t *testing.T) {
	h := newHandler(t, limiter.Config{IPLimit: 10, TokenLimits: map[string]int{"abc": 100}})

	for i := range 100 {
		if rec := do(h, "2.2.2.2", "abc"); rec.Code != http.StatusOK {
			t.Fatalf("request %d with token: status %d, want 200", i+1, rec.Code)
		}
	}
	if rec := do(h, "2.2.2.2", "abc"); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("101st request with token: status %d, want 429", rec.Code)
	}

	allowed := 0
	for range 11 {
		if do(h, "2.2.2.2", "").Code == http.StatusOK {
			allowed++
		}
	}
	if allowed != 10 {
		t.Fatalf("requests without token allowed: %d, want 10", allowed)
	}
}

func TestBlockedClientIsRejected(t *testing.T) {
	h := newHandler(t, limiter.Config{IPLimit: 1})
	do(h, "3.3.3.3", "")
	for range 5 {
		if rec := do(h, "3.3.3.3", ""); rec.Code != http.StatusTooManyRequests || rec.Body.String() != wantMessage {
			t.Fatalf("got %d %q, want 429 with message", rec.Code, rec.Body.String())
		}
	}
	if rec := do(h, "4.4.4.4", ""); rec.Code != http.StatusOK {
		t.Fatalf("another IP: status %d, want 200", rec.Code)
	}
}

type errAllower struct{}

func (errAllower) Allow(context.Context, string, string) (limiter.Result, error) {
	return limiter.Result{}, errors.New("redis down")
}

func TestStorageFailureReturns500(t *testing.T) {
	h := middleware.RateLimit(errAllower{}, middleware.Options{})(http.NotFoundHandler())
	if rec := do(h, "5.5.5.5", ""); rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d, want 500", rec.Code)
	}
}

func TestClientIP(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = "10.0.0.1:5000"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.2")

	if got := middleware.ClientIP(req, false); got != "10.0.0.1" {
		t.Fatalf("untrusted: got %q, want 10.0.0.1", got)
	}
	if got := middleware.ClientIP(req, true); got != "203.0.113.7" {
		t.Fatalf("trusted: got %q, want 203.0.113.7", got)
	}
}
