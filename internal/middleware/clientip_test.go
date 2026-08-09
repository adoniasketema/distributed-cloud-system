package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func requestFrom(remoteAddr, forwardedFor string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remoteAddr
	if forwardedFor != "" {
		r.Header.Set("X-Forwarded-For", forwardedFor)
	}
	return r
}

func TestClientIP_NoTrustedProxies_IgnoresForwardedHeader(t *testing.T) {
	tp, err := NewTrustedProxies(nil)
	require.NoError(t, err)

	// The header is forged; with no proxy configured it must be ignored outright.
	r := requestFrom("203.0.113.9:5555", "1.2.3.4")

	assert.Equal(t, "203.0.113.9", tp.ClientIP(r))
}

func TestClientIP_UntrustedPeer_IgnoresForwardedHeader(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	// Peer is not in the trusted range, so its header carries no weight. This is the case
	// that stops an attacker minting a fresh rate-limit bucket per request.
	r := requestFrom("203.0.113.9:5555", "1.2.3.4")

	assert.Equal(t, "203.0.113.9", tp.ClientIP(r))
}

func TestClientIP_TrustedPeer_UsesForwardedHeader(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	r := requestFrom("10.1.2.3:5555", "198.51.100.7")

	assert.Equal(t, "198.51.100.7", tp.ClientIP(r))
}

func TestClientIP_TrustedPeer_SkipsTrustedHopsRightToLeft(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	// Chain: real client, then two internal hops. Walking from the right past the trusted
	// hops lands on the real client.
	r := requestFrom("10.1.2.3:5555", "198.51.100.7, 10.4.4.4, 10.5.5.5")

	assert.Equal(t, "198.51.100.7", tp.ClientIP(r))
}

func TestClientIP_TrustedPeer_IgnoresClientSuppliedPrefix(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	// The client prepended a spoofed hop. The rightmost untrusted entry is the address the
	// proxy actually observed, so the spoofed value to its left must not win.
	r := requestFrom("10.1.2.3:5555", "1.1.1.1, 198.51.100.7, 10.4.4.4")

	assert.Equal(t, "198.51.100.7", tp.ClientIP(r))
}

func TestClientIP_TrustedPeer_AllHopsTrustedFallsBackToPeer(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	r := requestFrom("10.1.2.3:5555", "10.4.4.4, 10.5.5.5")

	assert.Equal(t, "10.1.2.3", tp.ClientIP(r))
}

func TestClientIP_TrustedPeer_MalformedEntryStopsTheWalk(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	r := requestFrom("10.1.2.3:5555", "198.51.100.7, garbage, 10.4.4.4")

	// The chain is unusable past the malformed hop, so fall back to the peer rather than
	// trusting anything further left.
	assert.Equal(t, "10.1.2.3", tp.ClientIP(r))
}

func TestClientIP_IPv6PeerAndBareIPCIDR(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"2001:db8::1"})
	require.NoError(t, err)

	r := requestFrom("[2001:db8::1]:5555", "198.51.100.7")

	assert.Equal(t, "198.51.100.7", tp.ClientIP(r))
}

func TestClientIP_RemoteAddrWithoutPort(t *testing.T) {
	tp, err := NewTrustedProxies(nil)
	require.NoError(t, err)

	r := requestFrom("203.0.113.9", "")

	assert.Equal(t, "203.0.113.9", tp.ClientIP(r))
}

func TestNewTrustedProxies_RejectsInvalidCIDR(t *testing.T) {
	_, err := NewTrustedProxies([]string{"not-a-cidr"})
	assert.Error(t, err)
}

func TestRateLimiter_SeparateBucketsPerForwardedClient(t *testing.T) {
	tp, err := NewTrustedProxies([]string{"10.0.0.0/8"})
	require.NoError(t, err)

	rl := NewRateLimiter(t.Context(), 1, time.Minute)
	rl.SetTrustedProxies(tp)

	h := rl.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Two different real clients arriving through the same proxy must not share a bucket.
	for _, client := range []string{"198.51.100.7", "198.51.100.8"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, requestFrom("10.1.2.3:5555", client))
		assert.Equal(t, http.StatusOK, rec.Code, "first request from %s", client)
	}

	// The third request repeats a client that has already spent its single allowance.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, requestFrom("10.1.2.3:5555", "198.51.100.7"))
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
}
