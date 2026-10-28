package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginRateLimiter is the per-IP throttle on the login endpoint (§5.2).
// Login is the expensive, captcha-prone path; one IP must not open more
// than a bounded number of login attempts per minute. Fixed-window,
// in-memory, lazy-allocated — enough for a single instance.
type loginRateLimiter struct {
	mu        sync.Mutex
	windows   map[string]*ipWindow
	perMinute int
	now       func() time.Time
}

type ipWindow struct {
	start time.Time
	count int
}

func newLoginRateLimiter(perMinute int, now func() time.Time) *loginRateLimiter {
	return &loginRateLimiter{windows: map[string]*ipWindow{}, perMinute: perMinute, now: now}
}

// allow reports whether one more login from ip is admissible, and how
// long until a refused IP may retry.
func (l *loginRateLimiter) allow(ip string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	w := l.windows[ip]
	if w == nil || now.Sub(w.start) >= time.Minute {
		if len(l.windows) > 10000 { // bound the map under address-spoofing
			l.windows = map[string]*ipWindow{}
		}
		l.windows[ip] = &ipWindow{start: now, count: 1}
		return true, 0
	}
	w.count++
	if w.count > l.perMinute {
		return false, time.Minute - now.Sub(w.start)
	}
	return true, 0
}

// loginRateGuard returns the middleware throttling POST /v1/auth/sessions.
func (s *Server) loginRateGuard() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && r.URL.Path == "/v1/auth/sessions" {
				ip := clientIP(r)
				if ok, retryIn := s.logins.allow(ip); !ok {
					w.Header().Set("Content-Type", "application/problem+json")
					w.Header().Set("Retry-After", retryAfterSeconds(retryIn))
					w.WriteHeader(http.StatusTooManyRequests)
					_ = json.NewEncoder(w).Encode(map[string]any{
						"title": "Too Many Requests", "status": 429,
						"code": "rate_limited", "retry_after": int(retryIn.Seconds()) + 1,
						"detail": "登录尝试过于频繁，请稍后再试",
					})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// corsGuard implements the CORS allow-list (config.LOVEACED_CORS_ORIGINS,
// exact origins only). With no origins configured it is a no-op: same-
// origin deployments get zero CORS surface. Token auth lives in headers,
// so no credentials/cookies are ever allowed.
func (s *Server) corsGuard() func(http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.deps.Cfg.CORSOrigins {
		allowed[strings.TrimSpace(o)] = true
	}
	setHeaders := func(w http.ResponseWriter, origin string) {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Vary", "Origin")
	}
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !allowed[origin] {
				next.ServeHTTP(w, r)
				return
			}
			if r.Method == http.MethodOptions &&
				r.Header.Get("Access-Control-Request-Method") != "" {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin)
				h.Set("Vary", "Origin")
				h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, Idempotency-Key")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			setHeaders(w, origin)
			next.ServeHTTP(w, r)
		})
	}
}

func retryAfterSeconds(d time.Duration) string {
	if d < time.Second {
		d = time.Second
	}
	return strconv.Itoa(int(d.Seconds()) + 1)
}
