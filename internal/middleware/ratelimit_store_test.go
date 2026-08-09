package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSharedStore stands in for Redis: one counter map shared by every RateLimiter that
// holds it, which is what a real shared store gives you across replicas.
type fakeSharedStore struct {
	mu     sync.Mutex
	counts map[string]int
	err    error
	calls  int
}

func newFakeSharedStore() *fakeSharedStore {
	return &fakeSharedStore{counts: make(map[string]int)}
}

func (f *fakeSharedStore) Allow(_ context.Context, key string, limit int, _ time.Duration) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++
	if f.err != nil {
		return false, f.err
	}
	f.counts[key]++
	return f.counts[key] <= limit, nil
}

func newLimiterWithStore(t *testing.T, limit int, store CounterStore) *RateLimiter {
	t.Helper()
	rl := NewRateLimiter(t.Context(), limit, time.Minute)
	rl.SetSharedStore(store)
	return rl
}

func handlerFor(rl *RateLimiter) http.Handler {
	return rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
}

func TestRateLimiter_SharedStoreEnforcesLimitAcrossReplicas(t *testing.T) {
	store := newFakeSharedStore()

	// Two limiters with separate memory, as two API replicas would be.
	replicaA := handlerFor(newLimiterWithStore(t, 2, store))
	replicaB := handlerFor(newLimiterWithStore(t, 2, store))

	req := func() *http.Request { return requestFrom("203.0.113.9:1111", "") }

	recA := httptest.NewRecorder()
	replicaA.ServeHTTP(recA, req())
	assert.Equal(t, http.StatusOK, recA.Code)

	recB := httptest.NewRecorder()
	replicaB.ServeHTTP(recB, req())
	assert.Equal(t, http.StatusOK, recB.Code)

	// Third request, on either replica, exceeds the shared quota of 2. With per-process
	// counting this would have been allowed, because each replica had only seen one.
	recC := httptest.NewRecorder()
	replicaB.ServeHTTP(recC, req())
	assert.Equal(t, http.StatusTooManyRequests, recC.Code)
}

func TestRateLimiter_FallsBackToLocalWhenSharedStoreFails(t *testing.T) {
	store := newFakeSharedStore()
	store.err = errors.New("redis unreachable")

	h := handlerFor(newLimiterWithStore(t, 2, store))
	req := func() *http.Request { return requestFrom("203.0.113.9:1111", "") }

	// The limiter must keep serving rather than failing closed on every request...
	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req())
		assert.Equal(t, http.StatusOK, rec.Code, "request %d", i+1)
	}

	// ...but must still enforce a per-process cap, rather than failing open entirely
	// during exactly the outage when a flood is hardest to absorb.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)

	assert.Positive(t, store.calls, "shared store should still be attempted")
}

func TestRateLimiter_SendsRetryAfterOnRejection(t *testing.T) {
	store := newFakeSharedStore()
	h := handlerFor(newLimiterWithStore(t, 1, store))
	req := func() *http.Request { return requestFrom("203.0.113.9:1111", "") }

	h.ServeHTTP(httptest.NewRecorder(), req())

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req())
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "60", rec.Header().Get("Retry-After"))
}

func TestRateLimiter_DistinctClientsHaveDistinctQuotas(t *testing.T) {
	store := newFakeSharedStore()
	h := handlerFor(newLimiterWithStore(t, 1, store))

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestFrom("203.0.113.9:1111", ""))
	assert.Equal(t, http.StatusOK, rec.Code)

	// A different client must not inherit the first one's spent quota.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, requestFrom("198.51.100.4:1111", ""))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestMemoryCounterStore_ResetsAfterWindow(t *testing.T) {
	s := newMemoryCounterStore(t.Context())
	window := 20 * time.Millisecond

	allowed, err := s.Allow(context.Background(), "k", 1, window)
	require.NoError(t, err)
	assert.True(t, allowed)

	allowed, _ = s.Allow(context.Background(), "k", 1, window)
	assert.False(t, allowed, "quota should be spent within the window")

	time.Sleep(window + 10*time.Millisecond)

	allowed, _ = s.Allow(context.Background(), "k", 1, window)
	assert.True(t, allowed, "quota should refresh once the window has passed")
}
