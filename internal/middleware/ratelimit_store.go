package middleware

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// CounterStore records a request against a client's quota and reports whether it is allowed.
type CounterStore interface {
	Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error)
}

// redisCounterScript increments a fixed-window counter and applies its TTL in one atomic
// step. A pipeline would not do: INCR and EXPIRE could be separated by a crash, leaving a
// counter with no TTL that locks the client out until someone notices.
//
// The key embeds the window index, so each window gets a fresh counter and expiry only has
// to clean up keys nobody will look at again.
var redisCounterScript = redis.NewScript(`
local count = redis.call('INCR', KEYS[1])
if count == 1 then
  redis.call('PEXPIRE', KEYS[1], ARGV[1])
end
return count
`)

// RedisCounterStore is a fixed-window counter shared by every replica.
//
// The in-memory limiter this replaces counted per process, so N replicas behind a load
// balancer enforced N times the intended limit and a client could evade it entirely by
// spreading requests across them.
type RedisCounterStore struct {
	client *redis.Client
	name   string
}

// NewRedisCounterStore builds a store whose keys are namespaced by name, so limiters with
// different quotas (the strict and general tiers) never share a counter.
func NewRedisCounterStore(client *redis.Client, name string) *RedisCounterStore {
	return &RedisCounterStore{client: client, name: name}
}

func (s *RedisCounterStore) Allow(ctx context.Context, key string, limit int, window time.Duration) (bool, error) {
	windowIndex := time.Now().UnixNano() / int64(window)
	redisKey := fmt.Sprintf("ratelimit:%s:%s:%d", s.name, key, windowIndex)

	// Outlive the window so a counter cannot be dropped early and grant a second allowance,
	// but not so long that abandoned keys accumulate.
	ttl := window * 2

	count, err := redisCounterScript.Run(ctx, s.client, []string{redisKey}, ttl.Milliseconds()).Int64()
	if err != nil {
		return false, err
	}
	return count <= int64(limit), nil
}

// memoryCounterStore is the per-process fallback used when the shared store is unreachable.
//
// It cannot enforce a global limit, but it still caps what any single replica will serve,
// which is strictly better than failing open during a Redis outage - exactly when a service
// is least able to absorb a flood.
type memoryCounterStore struct {
	mu      sync.Mutex
	clients map[string]*client
}

func newMemoryCounterStore(ctx context.Context) *memoryCounterStore {
	s := &memoryCounterStore{clients: make(map[string]*client)}

	// Periodic cleanup of expired clients to prevent unbounded growth.
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.mu.Lock()
				now := time.Now()
				for key, c := range s.clients {
					if now.After(c.resetTime) {
						delete(s.clients, key)
					}
				}
				s.mu.Unlock()
			}
		}
	}()

	return s
}

func (s *memoryCounterStore) Allow(_ context.Context, key string, limit int, window time.Duration) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	c, exists := s.clients[key]
	if !exists || now.After(c.resetTime) {
		s.clients[key] = &client{count: 1, resetTime: now.Add(window)}
		return true, nil
	}

	if c.count >= limit {
		return false, nil
	}
	c.count++
	return true, nil
}
