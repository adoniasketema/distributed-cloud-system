package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"nimbus/internal/auth"
)

const testSecret = "test-secret"

// signedToken issues a token with the given subject and expiry using the real claim type.
func signedToken(t *testing.T, subject string, expiresIn time.Duration, secret string) string {
	t.Helper()
	now := time.Now()
	claims := auth.Claims{
		Email: "user@example.com",
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(expiresIn)),
		},
	}
	signed, err := jwt.NewWithClaims(auth.SigningMethod, claims).SignedString([]byte(secret))
	require.NoError(t, err)
	return signed
}

// protected returns the middleware wrapping a handler that records the resolved user ID.
func protected(seen *string) http.Handler {
	return AuthMiddleware(testSecret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v, ok := r.Context().Value(UserIDKey).(string); ok {
			*seen = v
		}
		w.WriteHeader(http.StatusOK)
	}))
}

func serve(h http.Handler, authHeader string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	if authHeader != "" {
		r.Header.Set("Authorization", authHeader)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestAuthMiddleware_AcceptsValidToken(t *testing.T) {
	var seen string
	rec := serve(protected(&seen), "Bearer "+signedToken(t, "user-123", time.Hour, testSecret))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "user-123", seen)
}

func TestAuthMiddleware_RejectsExpiredToken(t *testing.T) {
	var seen string
	rec := serve(protected(&seen), "Bearer "+signedToken(t, "user-123", -time.Minute, testSecret))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, seen)
}

func TestAuthMiddleware_RejectsWrongSecret(t *testing.T) {
	var seen string
	rec := serve(protected(&seen), "Bearer "+signedToken(t, "user-123", time.Hour, "attacker-secret"))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, seen)
}

func TestAuthMiddleware_RejectsAlgNone(t *testing.T) {
	// Algorithm confusion: a token signed with "none" must never be accepted, however
	// well-formed its claims are.
	claims := auth.Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   "user-123",
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		},
	}
	unsigned, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).
		SignedString(jwt.UnsafeAllowNoneSignatureType)
	require.NoError(t, err)

	var seen string
	rec := serve(protected(&seen), "Bearer "+unsigned)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, seen)
}

func TestAuthMiddleware_RejectsEmptySubject(t *testing.T) {
	var seen string
	rec := serve(protected(&seen), "Bearer "+signedToken(t, "", time.Hour, testSecret))

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, seen)
}

func TestAuthMiddleware_RejectsMalformedHeaders(t *testing.T) {
	valid := signedToken(t, "user-123", time.Hour, testSecret)

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"no scheme", valid},
		{"wrong scheme", "Basic " + valid},
		{"lowercase scheme", "bearer " + valid},
		{"too many parts", "Bearer " + valid + " extra"},
		{"empty token", "Bearer "},
		{"garbage token", "Bearer not-a-jwt"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seen string
			rec := serve(protected(&seen), tc.header)
			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Empty(t, seen)
		})
	}
}
