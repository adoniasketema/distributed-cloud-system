package middleware

import (
	"context"
	"net"
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
}

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

func (rl *RateLimiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			ip = r.RemoteAddr
		}

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
