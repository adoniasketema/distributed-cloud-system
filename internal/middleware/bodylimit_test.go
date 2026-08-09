package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaxBodyBytes_AllowsBodyWithinLimit(t *testing.T) {
	var read int
	h := MaxBodyBytes(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		read = len(b)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 64)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Equal(t, 64, read)
}

func TestMaxBodyBytes_RejectsOversizedBody(t *testing.T) {
	var readErr error
	h := MaxBodyBytes(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, readErr = io.ReadAll(r.Body)
	}))

	// Far larger than the limit: without the cap this would be read into memory in full.
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", 1<<20)))
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.Error(t, readErr, "reading past the limit must fail rather than return the whole body")
	var maxErr *http.MaxBytesError
	assert.ErrorAs(t, readErr, &maxErr)
}

func TestMaxBodyBytes_HandlesNilBody(t *testing.T) {
	called := false
	h := MaxBodyBytes(64)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Body = nil
	h.ServeHTTP(httptest.NewRecorder(), req)

	assert.True(t, called)
}
