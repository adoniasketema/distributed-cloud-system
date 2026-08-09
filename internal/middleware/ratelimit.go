package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"time"
)

type client struct {
	count     int
	resetTime time.Time
}

type RateLimiter struct {
	limit   int
	window  time.Duration
	proxies *TrustedProxies

	// shared enforces the limit across every replica. Nil means this limiter is
	// process-local, which is only correct for a single-instance deployment.
	shared CounterStore
	// local backs up the shared store when it is unreachable.
	local *memoryCounterStore
}

// NewRateLimiter builds a fixed-window limiter keyed on client address.
//
// Without SetSharedStore the limiter counts per process, so running N replicas enforces N
// times the intended limit. Callers that scale horizontally must supply a shared store.
// Callers behind a reverse proxy must also call SetTrustedProxies, otherwise every request
// appears to originate from the proxy and all users share a single bucket.
func NewRateLimiter(ctx context.Context, limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:  limit,
		window: window,
		local:  newMemoryCounterStore(ctx),
	}
}

// SetTrustedProxies configures which peers may have their forwarding headers believed when
// determining the client address. Intended to be called at wiring time.
func (rl *RateLimiter) SetTrustedProxies(proxies *TrustedProxies) {
	rl.proxies = proxies
}

// SetSharedStore installs the cross-replica counter. Intended to be called at wiring time.
func (rl *RateLimiter) SetSharedStore(store CounterStore) {
	rl.shared = store
}

// allow consults the shared store, falling back to the process-local one if it fails.
func (rl *RateLimiter) allow(ctx context.Context, key string) bool {
	if rl.shared != nil {
		allowed, err := rl.shared.Allow(ctx, key, rl.limit, rl.window)
		if err == nil {
			return allowed
		}
		// Degrade rather than fail open: a Redis outage is precisely when an unprotected
		// service is least able to absorb a flood. The local counter still caps what this
		// replica will serve.
		slog.Warn("rate limiter shared store unavailable, falling back to per-process limit", "error", err)
	}

	allowed, _ := rl.local.Allow(ctx, key, rl.limit, rl.window)
	return allowed
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := rl.proxies.ClientIP(r)

		if !rl.allow(r.Context(), key) {
			w.Header().Set("Retry-After", strconv.Itoa(int(rl.window.Seconds())))
			http.Error(w, "Too Many Requests - rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		next.ServeHTTP(w, r)
	})
}
