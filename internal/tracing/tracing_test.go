package tracing

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTracingContext(t *testing.T) {
	ctx := context.Background()
	assert.Empty(t, TraceIDFromContext(ctx))

	traceID := "test-trace-1234"
	ctx = WithTraceID(ctx, traceID)
	assert.Equal(t, traceID, TraceIDFromContext(ctx))
}

func TestGenerateTraceID(t *testing.T) {
	id1 := GenerateTraceID()
	id2 := GenerateTraceID()
	assert.NotEmpty(t, id1)
	assert.Len(t, id1, 16)
	assert.NotEqual(t, id1, id2)
}

func TestMiddleware(t *testing.T) {
	handler := Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := TraceIDFromContext(r.Context())
		assert.NotEmpty(t, id)
		w.Write([]byte("OK"))
	}))

	// Case 1: Without X-Request-ID header
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	genID := rec.Header().Get("X-Request-ID")
	assert.NotEmpty(t, genID)

	// Case 2: With explicit X-Request-ID header
	customID := "custom-trace-8888"
	req2 := httptest.NewRequest("GET", "/test", nil)
	req2.Header.Set("X-Request-ID", customID)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, customID, rec2.Header().Get("X-Request-ID"))
}
