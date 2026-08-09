package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"
)

type client struct {
	count     int
	resetTime time.Time
}

type RateLimiter struct {
	mu      sync.Mutex
	clients map[string]*client
	limit   int
	window  time.Duration
	proxies *TrustedProxies
}

// NewRateLimiter builds a fixed-window limiter keyed on client address. Callers that sit
// behind a reverse proxy must pass the proxy CIDRs via SetTrustedProxies, otherwise every
// request appears to originate from the proxy and all users share a single bucket.
func NewRateLimiter(ctx context.Context, limit int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		clients: make(map[string]*client),
		limit:   limit,
		window:  window,
	}

	// Periodic cleanup of expired clients to prevent memory leaks, respecting context cancellation
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.mu.Lock()
				now := time.Now()
				for ip, c := range rl.clients {
					if now.After(c.resetTime) {
						delete(rl.clients, ip)
					}
				}
				rl.mu.Unlock()
			}
		}
	}()

	return rl
}

// SetTrustedProxies configures which peers may have their forwarding headers believed when
// determining the client address. It is intended to be called at wiring time, before the
// limiter starts serving.
func (rl *RateLimiter) SetTrustedProxies(proxies *TrustedProxies) {
	rl.proxies = proxies
}

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := rl.proxies.ClientIP(r)

		rl.mu.Lock()
		now := time.Now()
		c, exists := rl.clients[ip]
		if !exists || now.After(c.resetTime) {
			rl.clients[ip] = &client{
				count:     1,
				resetTime: now.Add(rl.window),
			}
			rl.mu.Unlock()
			next.ServeHTTP(w, r)
			return
		}

		if c.count >= rl.limit {
			rl.mu.Unlock()
			http.Error(w, "Too Many Requests - rate limit exceeded", http.StatusTooManyRequests)
			return
		}

		c.count++
		rl.mu.Unlock()
		next.ServeHTTP(w, r)
	})
}
