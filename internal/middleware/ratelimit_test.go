package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimiter(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	rl := NewRateLimiter(ctx, 2, time.Minute)

	handler := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("success"))
	}))

	req1 := httptest.NewRequest("GET", "/test", nil)
	req1.RemoteAddr = "192.168.1.1:12345"
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)
	assert.Equal(t, http.StatusOK, w1.Code)

	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.RemoteAddr = "192.168.1.1:12346" // same IP, different port
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code)

	// 3rd request should hit rate limit (limit is 2)
	req3 := httptest.NewRequest("GET", "/test", nil)
	req3.RemoteAddr = "192.168.1.1:12347"
	w3 := httptest.NewRecorder()
	handler.ServeHTTP(w3, req3)
	assert.Equal(t, http.StatusTooManyRequests, w3.Code)

	// Different IP should succeed
	req4 := httptest.NewRequest("GET", "/test", nil)
	req4.RemoteAddr = "10.0.0.1:12345"
	w4 := httptest.NewRecorder()
	handler.ServeHTTP(w4, req4)
	assert.Equal(t, http.StatusOK, w4.Code)
}
