// Package api wires the HTTP surface of loveaced: Huma v2 on chi, our own
// data envelopes, RFC 9457 problem+json errors and school-side error
// mapping (DESIGN.md §3).
package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humachi"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/Sibuxiangx/loveace-go/server/internal/cache"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/stats"
	"github.com/Sibuxiangx/loveace-go/server/internal/store"
	"github.com/Sibuxiangx/loveace-go/server/internal/teachereval"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// Deps are the collaborators the API needs.
type Deps struct {
	Cfg    config.Config
	Store  store.Store
	Vault  *vault.Vault
	Tokens *token.Manager
	School *school.Manager
	Health *health.Registry
	Stats  *stats.Collector
	// Teach runs the background auto-evaluation pool; nil disables it.
	Teach  *teachereval.Pool
	Pepper []byte
	// LoginPerMinute caps login attempts per client IP (§5.2); 0 uses
	// the default (20).
	LoginPerMinute int
}

// Server holds the API's collaborators.
type Server struct {
	deps       Deps
	cache      *cache.Cache
	rejections *rejectionCache
	logins     *loginRateLimiter
	now        func() time.Time
}

// New builds the API server.
func New(d Deps) *Server {
	if d.Health == nil {
		d.Health = health.New()
	}
	if d.Stats == nil {
		d.Stats = stats.New()
	}
	if d.Teach == nil {
		d.Teach = teachereval.New(teachereval.Config{}, d.Store, nil, nil)
	}
	perMinute := d.LoginPerMinute
	if perMinute <= 0 {
		perMinute = 20
	}
	return &Server{
		deps:       d,
		cache:      cache.New(),
		rejections: newRejectionCache(15*time.Minute, time.Now),
		logins:     newLoginRateLimiter(perMinute, time.Now),
		now:        time.Now,
	}
}

// Router builds the chi router with all routes registered.
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, trustedClientIP(s.deps.Cfg.TrustedProxies), middleware.Recoverer, middleware.Logger)
	// huma echoes request bodies into validation errors; on the auth paths
	// that must never leak a password. See redactSensitive below.
	r.Use(sensitivePathGuard)
	r.Use(publicCacheHeaders)
	r.Use(s.loginRateGuard())
	r.Use(s.corsGuard())
	// §9: any request is user activity — drives the L0 prober's
	// active/idle discipline (idle servers are never probed).
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.deps.Health != nil {
				s.deps.Health.TouchActivity()
			}
			next.ServeHTTP(w, r)
		})
	})

	api := humachi.New(r, huma.DefaultConfig("LoveACE API", "0.1.0"))
	s.registerAuth(api)
	s.registerMe(api)
	s.registerAcademic(api)
	s.registerOps(api)
	s.registerTeacherEval(api)
	s.registerPublicContent(api)
	s.registerMeContent(api)
	s.registerMergedTimetable(api)
	s.registerPublicCalendar(r)
	s.registerAdminContent(api)
	s.registerAdmin(r)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	// §9: the public status page shows only layer states (no counters).
	r.Get("/v1/public/status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.deps.Health.Public())
	})
	return r
}

// Output is the standard huma output wrapper. Huma serializes only the
// Body field as the response; without a field named Body it would default
// to 204-with-no-body (processOutputType). Status overrides the code when
// non-zero... it is always set by handlers here for clarity.
type Output[T any] struct {
	Status int
	Body   T
}

const bearerPrefix = "Bearer "

// authSensitiveFields are the request-body fields whose values must never
// appear in any response, including framework validation errors.
var authSensitiveFields = []string{"password", "gateway_password", "refresh_token", "code"}

// sensitivePathGuard wraps the /v1/auth paths with response sanitization:
// when a request carried sensitive values, any error response containing
// one of them has the occurrence replaced. huma echoes request bodies into
// validation errors (error.go validateBody), so this filter is load-bearing.
func sensitivePathGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/auth") {
			next.ServeHTTP(w, r)
			return
		}
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		secrets := extractSecrets(raw)
		if len(secrets) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		rec := &redactingWriter{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		rec.flushWith(func(body []byte) []byte {
			for _, s := range secrets {
				body = bytes.ReplaceAll(body, []byte(s), []byte("[redacted]"))
			}
			return body
		})
	})
}

// sensitiveFieldRe extracts "key":"value" pairs from a request body. It
// deliberately does NOT require the body to be valid JSON: the malformed-
// JSON case is exactly when huma echoes the raw body into the error, so
// the scan must work on broken input too.
var sensitiveFieldRe = regexp.MustCompile(`"([^"]+)"\s*:\s*"([^"]*)"`)

func extractSecrets(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	sensitive := map[string]bool{}
	for _, f := range authSensitiveFields {
		sensitive[f] = true
	}
	var secrets []string
	for _, m := range sensitiveFieldRe.FindAllStringSubmatch(string(raw), -1) {
		if sensitive[m[1]] && len(m[2]) >= 4 {
			secrets = append(secrets, m[2])
		}
	}
	return secrets
}

// redactingWriter buffers the response so it can be filtered before being
// sent. Used only on /v1/auth paths whose requests carried secrets.
type redactingWriter struct {
	http.ResponseWriter
	code    int
	buf     bytes.Buffer
	written bool
}

func (r *redactingWriter) WriteHeader(code int) {
	if !r.written {
		r.code = code
	}
}

func (r *redactingWriter) Write(b []byte) (int, error) {
	r.written = true
	return r.buf.Write(b)
}

func (r *redactingWriter) flushWith(f func([]byte) []byte) {
	if r.code == 0 {
		r.code = http.StatusOK
	}
	target := r.ResponseWriter
	for k, vv := range r.Header() {
		target.Header()[k] = vv
	}
	target.Header().Set("Content-Length", strconv.Itoa(r.buf.Len()))
	target.WriteHeader(r.code)
	_, _ = target.Write(f(r.buf.Bytes()))
}

// publicCacheTTLs are the per-path cache lifetimes for public content
// (DESIGN §11.2): the server serves users directly — no CDN in front —
// so these are plain HTTP headers (hints for clients/browsers, and for
// any reverse proxy if one is ever added). /v1/me/* is never cached
// (Authorization-bound).
var publicCacheTTLs = map[string]string{
	"/v1/public/status":        "public, max-age=30",
	"/v1/public/announcements": "public, max-age=60, stale-while-revalidate=300",
	"/v1/public/calendar":      "public, max-age=300, stale-while-revalidate=600",
}

// publicCacheHeaders stamps the matching Cache-Control header before the
// handlers run.
func publicCacheHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cc, ok := publicCacheTTLs[r.URL.Path]; ok {
			w.Header().Set("Cache-Control", cc)
		}
		next.ServeHTTP(w, r)
	})
}

// authenticate verifies the Authorization header of a protected endpoint.
// The JWT alone is not enough: a logged-out device keeps a valid token until
// it expires, so the device row is consulted on every request.
func (s *Server) authenticate(ctx context.Context, authz string) (token.AccessClaims, error) {
	if !strings.HasPrefix(authz, bearerPrefix) {
		return token.AccessClaims{}, newProblem(http.StatusUnauthorized, "auth.invalid_token", "缺少访问令牌")
	}
	claims, err := s.deps.Tokens.VerifyAccessToken(strings.TrimPrefix(authz, bearerPrefix))
	if err != nil {
		return token.AccessClaims{}, newProblem(http.StatusUnauthorized, "auth.token_expired", "访问令牌无效或已过期")
	}
	dev, err := s.deps.Store.GetDevice(ctx, claims.UserID, claims.DeviceID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && dev.RevokedAt != nil) {
		return token.AccessClaims{}, newProblem(http.StatusUnauthorized, "auth.invalid_token", "设备已登出，请重新登录")
	}
	if err != nil {
		return token.AccessClaims{}, mapErr(err)
	}
	return claims, nil
}
