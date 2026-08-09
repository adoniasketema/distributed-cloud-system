package middleware

import (
	"net"
	"net/http"
	"strconv"
	"strings"
)

// TrustedProxies decides whether a request's forwarding headers may be believed.
//
// This has to be opt-in. X-Forwarded-For is trivially forged, so trusting it unconditionally
// would let anyone mint a fresh rate-limit bucket per request by varying the header. Trusting
// nothing is equally broken behind a load balancer, where every request appears to come from
// the balancer and all users share one bucket. The only correct answer is to know which peers
// are actually your proxies.
type TrustedProxies struct {
	nets []*net.IPNet
}

// NewTrustedProxies parses a list of CIDR blocks identifying the reverse proxies in front of
// this service. An empty list means no proxy is trusted and the direct peer address is always
// used, which is the safe default for a directly exposed server.
func NewTrustedProxies(cidrs []string) (*TrustedProxies, error) {
	tp := &TrustedProxies{}
	for _, cidr := range cidrs {
		cidr = strings.TrimSpace(cidr)
		if cidr == "" {
			continue
		}
		// Accept a bare IP as a single-host block, since that is how people usually write it.
		if !strings.Contains(cidr, "/") {
			if ip := net.ParseIP(cidr); ip != nil {
				bits := 32
				if ip.To4() == nil {
					bits = 128
				}
				cidr = cidr + "/" + strconv.Itoa(bits)
			}
		}
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			return nil, err
		}
		tp.nets = append(tp.nets, network)
	}
	return tp, nil
}

func (t *TrustedProxies) contains(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP returns the address the rate limiter should key on.
//
// If the direct peer is not a trusted proxy, that peer is the client and any forwarding
// headers it sent are ignored. If it is trusted, X-Forwarded-For is walked from right to
// left - the rightmost entries were appended by infrastructure we control - and the first
// address that is not itself a trusted proxy is the client. Anything further left was
// supplied by the client and cannot be trusted.
func (t *TrustedProxies) ClientIP(r *http.Request) string {
	peer := peerIP(r.RemoteAddr)

	if t == nil || len(t.nets) == 0 || !t.contains(net.ParseIP(peer)) {
		return peer
	}

	forwarded := r.Header.Get("X-Forwarded-For")
	if forwarded == "" {
		return peer
	}

	parts := strings.Split(forwarded, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := strings.TrimSpace(parts[i])
		ip := net.ParseIP(candidate)
		if ip == nil {
			// A malformed entry means the chain cannot be trusted any further left.
			break
		}
		if !t.contains(ip) {
			return ip.String()
		}
	}

	// Every hop in the chain was a trusted proxy; the nearest one is the best answer.
	return peer
}

// peerIP strips the port from a RemoteAddr, tolerating values that have none.
func peerIP(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
