package tracing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

type traceIDKey struct{}

// WithTraceID attaches a trace ID to the context.
func WithTraceID(ctx context.Context, traceID string) context.Context {
	return context.WithValue(ctx, traceIDKey{}, traceID)
}

// TraceIDFromContext retrieves the trace ID from the context. If absent, returns an empty string.
func TraceIDFromContext(ctx context.Context) string {
	if val, ok := ctx.Value(traceIDKey{}).(string); ok {
		return val
	}
	return ""
}

// GenerateTraceID creates a clean 16-character hex trace string using crypto/rand.
func GenerateTraceID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("trace-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Middleware creates an HTTP middleware that extracts or generates an X-Request-ID header,
// sets it on the response, and injects it into the request context.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		traceID := r.Header.Get("X-Request-ID")
		if traceID == "" {
			traceID = GenerateTraceID()
		}
		w.Header().Set("X-Request-ID", traceID)
		ctx := WithTraceID(r.Context(), traceID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Logger returns an slog.Logger instance enriched with the current trace_id if available.
func Logger(ctx context.Context) *slog.Logger {
	traceID := TraceIDFromContext(ctx)
	if traceID != "" {
		return slog.Default().With("trace_id", traceID)
	}
	return slog.Default()
}
