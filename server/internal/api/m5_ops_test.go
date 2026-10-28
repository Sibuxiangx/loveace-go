package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// opsTransport extends the standard school mock with the ykt routes and
// an error-injection point for write-outcome testing.
type opsTransport struct {
	inner    *transport.MockTransport
	failURL  atomic.Pointer[string] // POSTs whose URL contains this fail at the transport level
	failHits atomic.Int64
}

// setFailURL is synchronized: login starts a JWC callback goroutine that
// shares this transport, so the field must not be written unguarded.
func (t *opsTransport) setFailURL(u string) { t.failURL.Store(&u) }

func (t *opsTransport) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	if p := t.failURL.Load(); p != nil && *p != "" && strings.Contains(r.URL, *p) {
		t.failHits.Add(1)
		// Match the real HTTPTransport's convention: transport-level
		// failures are errs.Network (which a write maps to
		// outcome_unknown).
		return nil, errs.NewNetwork("transport blew up mid-write")
	}
	return t.inner.Execute(ctx, r)
}

func newOpsServer(t *testing.T) (*opsTransport, http.Handler, string) {
	return newOpsServerWithCfg(t, nil)
}

func newOpsServerWithCfg(t *testing.T, tweak func(*config.Config)) (*opsTransport, http.Handler, string) {
	t.Helper()
	mock := successTransport(t)
	mock.OnRequest(transport.Get, "/casLogin.jsp", transport.NewResponse(200, []byte("<html>cas ok</html>"))).
		OnRequest(transport.Get, "/queryUserBalances.action", transport.NewResponse(200, []byte(
			`<div><label>校园卡余额：</label><label> 88.50 元</label></div>`))).
		// The labor CAS hop must mint a labor ticket: the redirect chain
		// must contain "register?ticket=…" (labor.go Ticket()). The
		// Location uses the vpn2-rewritten form — a raw internal host
		// would be read as a stale gateway session (send.go namespace
		// check) and trigger recovery instead.
		OnRequest(transport.Get, "uaap-aufe-edu-cn.vpn2.aufe.edu.cn:8118/cas/login",
			transport.NewResponse(302, []byte("moved")).
				WithHeader("Location", "http://api-ldjlb-ac-acxk-net.vpn2.aufe.edu.cn:8118/register?ticket=LAB-1"))
	wrapped := &opsTransport{inner: mock}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		PasswordMaxLen:  64,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		KEKVersion:      "v1",
	}
	if tweak != nil {
		tweak(&cfg)
	}
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Cfg:    cfg,
		Store:  mem.New(),
		Vault:  v,
		Tokens: token.NewManager(priv, cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		School: school.NewManager(func() transport.Transport { return wrapped }, school.WithoutLoginGate()),
		Pepper: bytes.Repeat([]byte{2}, 32),
	})
	h := srv.Router()
	_, rawOut := login(t, h, "20000003", "Passw0rd!123")
	access := asMap(t, rawOut)["access_token"].(string)
	return wrapped, h, access
}

func runOps(t *testing.T, h http.Handler, access, key, op string, args map[string]any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"op": op, "args": args})
	req := httptest.NewRequest(http.MethodPost, "/v1/me/ops", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+access)
	if key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	var out any
	_ = json.Unmarshal(res.Body.Bytes(), &out)
	m, _ := out.(map[string]any)
	return res, m
}

// TestOpsReadDispatch runs a read op through the generic surface.
func TestOpsReadDispatch(t *testing.T) {
	_, h, access := newOpsServer(t)

	res, out := runOps(t, h, access, "", "ykt.balance", nil)
	if res.Code != 200 {
		t.Fatalf("ykt.balance = %d %v", res.Code, out)
	}
	if !strings.Contains(res.Body.String(), "88.50") {
		t.Fatalf("balance result missing parsed value: %s", res.Body.String())
	}

	// Unknown op and auth ops are both refused.
	res, _ = runOps(t, h, access, "", "ykt.nonexistent", nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("unknown op = %d, want 404", res.Code)
	}
	res, _ = runOps(t, h, access, "", "auth.ec_login", nil)
	if res.Code != http.StatusNotFound {
		t.Fatalf("auth op through /v1/me/ops = %d, want 404", res.Code)
	}
}

// TestOpsWriteIdempotency pins the M5 write semantics: key required,
// first execution stored, retry replays (never re-executes), hash
// mismatch refused, and a transport-level failure stores a stable
// outcome_unknown that replays identically without re-executing.
func TestOpsWriteIdempotency(t *testing.T) {
	tr, h, access := newOpsServer(t)

	// No key → 428.
	res, out := runOps(t, h, access, "", "aac.reset_ticket", nil)
	if res.Code != http.StatusPreconditionRequired || out["code"] != "idempotency_key_required" {
		t.Fatalf("missing key = %d %v", res.Code, out)
	}

	// First run with a key → 200; replay → same 200 body.
	res1, out1 := runOps(t, h, access, "k-1", "aac.reset_ticket", nil)
	if res1.Code != 200 {
		t.Fatalf("write with key = %d %v", res1.Code, out1)
	}
	res2, out2 := runOps(t, h, access, "k-1", "aac.reset_ticket", nil)
	if res2.Code != 200 || res2.Body.String() != res1.Body.String() {
		t.Fatalf("replay = %d %v, want replay of %v", res2.Code, out2, out1)
	}

	// Same key, different args → 422.
	res3, _ := runOps(t, h, access, "k-1", "aac.reset_ticket", map[string]any{"x": 1})
	if res3.Code != http.StatusUnprocessableEntity {
		t.Fatalf("key reuse with different args = %d, want 422", res3.Code)
	}

	// Transport-level failure mid-write → outcome_unknown, stored and
	// replayed WITHOUT re-executing.
	tr.setFailURL("/User/Center/DoScanSignQRImage")
	res4, out4 := runOps(t, h, access, "k-2", "labor.scan_sign_in",
		map[string]any{"content": "QR", "location": "here"})
	if res4.Code != http.StatusConflict || out4["code"] != "school.outcome_unknown" {
		t.Fatalf("failed write = %d %v, want 409 outcome_unknown", res4.Code, out4)
	}
	res5, out5 := runOps(t, h, access, "k-2", "labor.scan_sign_in",
		map[string]any{"content": "QR", "location": "here"})
	if res5.Code != http.StatusConflict || out5["code"] != "school.outcome_unknown" {
		t.Fatalf("unknown replay = %d %v", res5.Code, out5)
	}
	if n := tr.failHits.Load(); n != 1 {
		t.Fatalf("write executed %d times, want exactly 1 (replay must not re-execute)", n)
	}
}

// TestCorsAllowList pins the CORS guard: allowed origins get preflight +
// actual-response headers; anything else gets none.
func TestCorsAllowList(t *testing.T) {
	_, h, _ := newOpsServerWithCfg(t, func(c *config.Config) {
		c.CORSOrigins = []string{"http://localhost:5173"}
	})

	preflight := func(origin string) http.Header {
		req, _ := http.NewRequest(http.MethodOptions, "/v1/me", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodPost)
		req.Header.Set("Access-Control-Request-Headers", "Authorization, Idempotency-Key")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		return res.Header()
	}
	h1 := preflight("http://localhost:5173")
	if h1.Get("Access-Control-Allow-Origin") != "http://localhost:5173" ||
		h1.Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("preflight headers = %v", h1)
	}
	if h2 := preflight("https://evil.example"); h2.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin got CORS headers: %v", h2)
	}

	// Actual (non-preflight) response carries the header for allowed origins.
	req, _ := http.NewRequest(http.MethodGet, "/v1/me", nil)
	req.Header.Set("Origin", "http://localhost:5173")
	req.Header.Set("Authorization", "Bearer bogus")
	res := httptest.NewRecorder()
	h.ServeHTTP(res, req)
	if res.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("actual response missing ACAO: %v", res.Header())
	}
}

// TestLoginIPLimit pins the per-IP login throttle (§5.2): after the
// per-minute budget, the endpoint answers 429 rate_limited without
// touching the school.
func TestLoginIPLimit(t *testing.T) {
	_, h, _ := newOpsServer(t)
	for i := 0; i < 20; i++ {
		req, _ := http.NewRequest(http.MethodPost, "/v1/auth/sessions",
			strings.NewReader(`{"account":"20000003","password":"Passw0rd!123","device_name":"d","platform":"t"}`))
		req.Header.Set("Content-Type", "application/json")
		res := httptest.NewRecorder()
		h.ServeHTTP(res, req)
		if res.Code != 200 {
			t.Fatalf("login %d = %d, want 200 within budget", i, res.Code)
		}
	}
	res := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/v1/auth/sessions",
		strings.NewReader(`{"account":"20000003","password":"Passw0rd!123","device_name":"d","platform":"t"}`))
	req.Header.Set("Content-Type", "application/json")
	h.ServeHTTP(res, req)
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("over-budget login = %d, want 429", res.Code)
	}
	if !strings.Contains(res.Body.String(), "rate_limited") {
		t.Fatalf("body = %s, want rate_limited problem", res.Body.String())
	}
}
