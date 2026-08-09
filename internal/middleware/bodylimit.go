package middleware

import "net/http"

// DefaultMaxBodyBytes is the cap applied to JSON endpoints. Every request body these
// handlers accept is a small object, so 1 MB is generous.
const DefaultMaxBodyBytes int64 = 1 << 20

// MaxBodyBytes returns middleware that caps how much of a request body a handler can read.
//
// Handlers that call json.NewDecoder(r.Body).Decode read until EOF, so without a cap a
// single request can stream unbounded data into memory. http.MaxBytesReader also aborts
// the connection once the limit is passed, so an oversized upload stops costing bandwidth
// instead of being read to completion and then rejected.
//
// This is not applied to the file upload route, which sets its own, much larger limit.
func MaxBodyBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Body != nil {
				r.Body = http.MaxBytesReader(w, r.Body, limit)
			}
			next.ServeHTTP(w, r)
		})
	}
}
