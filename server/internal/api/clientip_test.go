package api

import (
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func mustCIDRs(t *testing.T, cidrs ...string) []*net.IPNet {
	t.Helper()
	var out []*net.IPNet
	for _, c := range cidrs {
		_, n, err := net.ParseCIDR(c)
		if err != nil {
			t.Fatalf("parse %q: %v", c, err)
		}
		out = append(out, n)
	}
	return out
}

// seenRemote runs one request through the trusted-proxy middleware and
// returns the RemoteAddr the handler observed.
func seenRemote(t *testing.T, nets []*net.IPNet, peer string, xff ...string) string {
	t.Helper()
	var seen string
	h := trustedClientIP(nets)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.RemoteAddr
	}))
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/sessions", nil)
	req.RemoteAddr = peer
	for _, v := range xff {
		req.Header.Add("X-Forwarded-For", v)
	}
	h.ServeHTTP(httptest.NewRecorder(), req)
	return seen
}

func TestUntrustedPeerCannotSpoofForwardedFor(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.1/32")
	got := seenRemote(t, nets, "203.0.113.9:5555", "1.2.3.4")
	if got != "203.0.113.9:5555" {
		t.Fatalf("untrusted peer's header was honoured: RemoteAddr = %q", got)
	}
}

func TestTrustedProxyResolvesRightmostUntrustedHop(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.1/32", "10.0.0.0/8")
	// The client may prepend anything; the proxy appends the real address.
	got := seenRemote(t, nets, "127.0.0.1:4000", "9.9.9.9, 198.51.100.7", "10.0.0.2")
	host, _, err := net.SplitHostPort(got)
	if err != nil || host != "198.51.100.7" {
		t.Fatalf("client = %q (err %v), want 198.51.100.7", got, err)
	}
}

func TestTrustedProxyWithoutUsableHeaderKeepsPeer(t *testing.T) {
	nets := mustCIDRs(t, "127.0.0.1/32")
	for _, xff := range []string{"", "not-an-address", "1.2.3.4, junk"} {
		var got string
		if xff == "" {
			got = seenRemote(t, nets, "127.0.0.1:4000")
		} else {
			got = seenRemote(t, nets, "127.0.0.1:4000", xff)
		}
		if got != "127.0.0.1:4000" {
			t.Fatalf("header %q: RemoteAddr = %q, want the peer unchanged", xff, got)
		}
	}
}
