package api

import (
	"net"
	"net/http"
	"strings"
)

// trustedClientIP replaces chi's middleware.RealIP, which believes any
// X-Forwarded-For or X-Real-IP header from any peer. A reverse proxy appends
// to X-Forwarded-For, so the left-most entry is client-controlled. Here the
// header is honoured only when the TCP peer is a configured trusted proxy,
// and the client is the right-most entry that is not itself trusted. Peers
// outside the list keep their own address, headers ignored.
func trustedClientIP(nets []*net.IPNet) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if peer := peerIP(r.RemoteAddr); peer != nil && containsIP(nets, peer) {
				if client := clientFromForwarded(r.Header.Values("X-Forwarded-For"), nets); client != nil {
					r.RemoteAddr = net.JoinHostPort(client.String(), "0")
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientFromForwarded returns the right-most address outside the trusted
// proxies, or nil when there is none or an entry is malformed (the peer
// address then stands: a malformed chain is never guessed at).
func clientFromForwarded(values []string, nets []*net.IPNet) net.IP {
	var hops []string
	for _, v := range values {
		hops = append(hops, strings.Split(v, ",")...)
	}
	for i := len(hops) - 1; i >= 0; i-- {
		ip := net.ParseIP(strings.TrimSpace(hops[i]))
		if ip == nil {
			return nil
		}
		if !containsIP(nets, ip) {
			return ip
		}
	}
	return nil
}

func peerIP(remoteAddr string) net.IP {
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
		return net.ParseIP(host)
	}
	return net.ParseIP(remoteAddr)
}

func containsIP(nets []*net.IPNet, ip net.IP) bool {
	for _, n := range nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}
