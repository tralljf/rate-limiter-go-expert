// Package middleware adapts the limiter domain to net/http.
package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/tralljf/rate-limiter-go-expert/internal/limiter"
)

// TokenHeader is the header that carries the access token.
const TokenHeader = "API_KEY"

// LimitExceededMessage is the body returned with HTTP 429.
const LimitExceededMessage = "you have reached the maximum number of requests or actions allowed within a certain time frame"

// Allower is the subset of the limiter used by the middleware.
type Allower interface {
	Allow(ctx context.Context, ip, token string) (limiter.Result, error)
}

// Options configures the middleware.
type Options struct {
	// TrustForwardedFor makes the middleware use the first address of the
	// X-Forwarded-For header (or X-Real-IP) as the client IP. Enable it only
	// behind a trusted reverse proxy.
	TrustForwardedFor bool
	Logger            *slog.Logger
}

// RateLimit returns a middleware that rejects requests exceeding the limits
// with HTTP 429.
func RateLimit(l Allower, opts Options) func(http.Handler) http.Handler {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := ClientIP(r, opts.TrustForwardedFor)
			token := strings.TrimSpace(r.Header.Get(TokenHeader))

			res, err := l.Allow(r.Context(), ip, token)
			if err != nil {
				logger.Error("rate limiter unavailable", "error", err)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
				return
			}
			if !res.Allowed {
				w.Header().Set("Content-Type", "text/plain; charset=utf-8")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(LimitExceededMessage))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ClientIP extracts the client IP from the request.
func ClientIP(r *http.Request, trustForwardedFor bool) string {
	if trustForwardedFor {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			first, _, _ := strings.Cut(xff, ",")
			if ip := strings.TrimSpace(first); ip != "" {
				return ip
			}
		}
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
