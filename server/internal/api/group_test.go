package api

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/transport"
	"github.com/Sibuxiangx/loveace-go/server/internal/config"
	"github.com/Sibuxiangx/loveace-go/server/internal/health"
	"github.com/Sibuxiangx/loveace-go/server/internal/school"
	"github.com/Sibuxiangx/loveace-go/server/internal/store/mem"
	"github.com/Sibuxiangx/loveace-go/server/internal/token"
	"github.com/Sibuxiangx/loveace-go/server/internal/vault"
)

// newGroupTestServer builds a server with the group-relevant knobs exposed.
func newGroupTestServer(t *testing.T, tr *transport.MockTransport, adminToken string, opts ...school.Option) (*Server, http.Handler) {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		PasswordMaxLen:  64,
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		KEKVersion:      "v1",
		AdminToken:      adminToken,
	}
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	reg := health.New()
	srv := New(Deps{
		Cfg:    cfg,
		Store:  mem.New(),
		Vault:  v,
		Tokens: token.NewManager(priv, cfg.AccessTokenTTL, cfg.RefreshTokenTTL),
		School: school.NewManager(func() transport.Transport { return tr }, append(opts, school.WithSinkFactory(reg.SinkFor), school.WithoutLoginGate())...),
		Health: reg,
		Pepper: bytes.Repeat([]byte{2}, 32),
	})
	return srv, srv.Router()
}

func loginAs(t *testing.T, h http.Handler, account, password, deviceName string) (*http.Response, any) {
	t.Helper()
	return doJSON(t, h, http.MethodPost, "/v1/auth/sessions", "", map[string]any{
		"account": account, "password": password,
		"device_name": deviceName, "platform": "web",
	})
}

// mustLogin asserts a 200 login and returns the access token.
func mustLogin(t *testing.T, h http.Handler, account, password, deviceName string) string {
	t.Helper()
	res, rawOut := loginAs(t, h, account, password, deviceName)
	out := asMap(t, rawOut)
	if res.StatusCode != 200 {
		t.Fatalf("login %s = %d %v", deviceName, res.StatusCode, out)
	}
	access, _ := out["access_token"].(string)
	if access == "" {
		t.Fatalf("login %s: no access token in %v", deviceName, out)
	}
	return access
}

// TestLoginSharingReusesSchoolSession pins the §7.5 revised rule: the
// UAAP layer has no ban budget, so a login whose password matches the
// vault BINDS the device to the user's one live school session — zero
// school calls, zero extra UAAP sessions, no session-limit for shares.
func TestLoginSharingReusesSchoolSession(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "")

	mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	baseLogins, basePosts := countLogins(tr)

	// Six more devices, same password: all bind to the shared session.
	for _, d := range []string{"d2", "d3", "d4", "d5", "d6", "d7"} {
		mustLogin(t, h, "20000003", "Passw0rd!123", d)
	}
	logins, posts := countLogins(tr)
	if logins != baseLogins || posts != basePosts {
		t.Fatalf("shared logins touched the school: logins %d→%d posts %d→%d",
			baseLogins, logins, basePosts, posts)
	}
}

// TestSessionGroupLimitRejectsSixthFullLogin: logins that CANNOT share
// (the vault does not know the password — e.g. it was just changed) take
// the full-login path, and THAT path is still capped at five concurrent
// school sessions with the active list in the problem.
func TestSessionGroupLimitRejectsSixthFullLogin(t *testing.T) {
	_, h := newGroupTestServer(t, successTransport(t), "")

	mustLogin(t, h, "20000003", "Passw0rd!123", "seed") // seeds the vault

	for _, d := range []string{"d1", "d2", "d3", "d4"} {
		mustLogin(t, h, "20000003", "Fresh-pw-"+d, d) // never matches the vault
	}
	res, rawOut := loginAs(t, h, "20000003", "Fresh-pw-d5", "d5")
	out := asMap(t, rawOut)
	if res.StatusCode != http.StatusConflict || out["code"] != "auth.session_limit" {
		t.Fatalf("6th full login = %d %v", res.StatusCode, out)
	}
	sessions, _ := out["sessions"].([]any)
	if len(sessions) != 5 {
		t.Fatalf("expected the 5 active sessions in the problem, got %v", sessions)
	}

	// Same-device re-login replaces, never grows.
	for _, d := range []string{"d1", "d2", "d3"} {
		mustLogin(t, h, "20000003", "Fresh-pw-"+d, d)
	}
}

// countLogins counts the school-side login attempts (auth page GETs) and
// credential submissions (login_psw POSTs) the mock has seen.
func countLogins(tr *transport.MockTransport) (auths, posts int) {
	for _, r := range tr.Recorded() {
		switch {
		case strings.Contains(r.URL, "/por/login_auth.csp"):
			auths++
		case strings.Contains(r.URL, "/por/login_psw.csp"):
			posts++
		}
	}
	return
}

// TestLogoutDeviceRefcountSharedSession: with several devices sharing one
// school session, revoking ONE device keeps the shared session alive for
// the rest; the last device out tears it down.
func TestLogoutDeviceRefcountSharedSession(t *testing.T) {
	srv, h := newGroupTestServer(t, successTransport(t), "")

	tok1 := mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	mustLogin(t, h, "20000003", "Passw0rd!123", "d2") // shared bind

	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/me/devices", tok1, nil)
	if res.StatusCode != 200 {
		t.Fatalf("devices = %d %v", res.StatusCode, rawOut)
	}
	devices, ok := rawOut.([]any)
	if !ok || len(devices) != 2 {
		t.Fatalf("devices = %v, want 2", rawOut)
	}

	// Revoke d2: d1's shared session must stay alive.
	d2 := asMap(t, devices[1])["id"].(string)
	if res, _ := doJSON(t, h, http.MethodDelete, "/v1/me/devices/"+d2, tok1, nil); res.StatusCode != 200 {
		t.Fatalf("revoke d2 = %d", res.StatusCode)
	}
	if stats := srv.deps.School.GroupStats(); len(stats) != 1 || stats[0].Active != 1 {
		t.Fatalf("group stats after one revoke = %+v, want 1 active", stats)
	}
	if _, err := srv.deps.School.SessionFor(context.Background(), mustUserID(t, srv, tok1), ""); err != nil {
		t.Fatalf("shared session died with a revoked device: %v", err)
	}

	// Revoke d1 (the last binding): the shared session tears down.
	d1 := asMap(t, devices[0])["id"].(string)
	if res, _ := doJSON(t, h, http.MethodDelete, "/v1/me/devices/"+d1, tok1, nil); res.StatusCode != 200 {
		t.Fatalf("revoke d1 = %d", res.StatusCode)
	}
	if stats := srv.deps.School.GroupStats(); len(stats) != 0 {
		t.Fatalf("group stats after the last revoke = %+v, want empty", stats)
	}
}

// mustUserID resolves the user id behind an access token (test helper).
func mustUserID(t *testing.T, srv *Server, token string) string {
	t.Helper()
	claims, err := srv.deps.Tokens.VerifyAccessToken(token)
	if err != nil {
		t.Fatal(err)
	}
	return claims.UserID
}

// TestSessionGroupZombieRecycle pins the §7.5 zombie rule: a group whose
// every member has been idle beyond the threshold recycles its oldest
// member to admit a new login; one fresh member keeps the group closed.
func TestSessionGroupZombieRecycle(t *testing.T) {
	tr := successTransport(t)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New(bytes.Repeat([]byte{1}, 32), "v1")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Deps{
		Cfg: config.Config{
			PasswordMaxLen: 64, AccessTokenTTL: 15 * time.Minute,
			RefreshTokenTTL: 30 * 24 * time.Hour, KEKVersion: "v1",
		},
		Store: mem.New(), Vault: v,
		Tokens: token.NewManager(priv, 15*time.Minute, 30*24*time.Hour),
		School: school.NewManager(func() transport.Transport { return tr }, school.WithClock(clock), school.WithoutLoginGate()),
		Pepper: bytes.Repeat([]byte{2}, 32),
	})
	h := srv.Router()

	// d1 first, then the rest a minute later — a deterministic oldest.
	// Distinct passwords force the FULL-login path (a matching vault
	// password would take the sharing fast path and never hit the cap).
	now = now.Add(time.Minute)
	mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	for _, d := range []string{"d2", "d3", "d4", "d5"} {
		now = now.Add(time.Minute)
		mustLogin(t, h, "20000003", "Fresh-pw-"+d, d)
	}

	// +12min past d1's login (+11 past the rest): everything is a zombie,
	// so the 6th login recycles the oldest instead of being rejected.
	now = now.Add(12 * time.Minute)
	res, rawOut := loginAs(t, h, "20000003", "Fresh-pw-d6", "d6")
	if res.StatusCode != 200 {
		t.Fatalf("zombie-recycled 6th login = %d %v", res.StatusCode, rawOut)
	}

	// d6 was just registered (idle 0) while d2..d5 remain zombies: the
	// group is full but NOT all-zombie → the next login is rejected.
	res, rawOut = loginAs(t, h, "20000003", "Fresh-pw-d7", "d7")
	out := asMap(t, rawOut)
	if res.StatusCode != http.StatusConflict || out["code"] != "auth.session_limit" {
		t.Fatalf("login with a fresh member present = %d %v", res.StatusCode, out)
	}
}

// TestDeviceRevokeFreesQuota pins §7.5: deleting a device releases its
// UAAP quota slot; the next login is admitted immediately.
func TestDeviceRevokeFreesQuota(t *testing.T) {
	_, h := newGroupTestServer(t, successTransport(t), "")

	access := make([]string, 0, 5)
	for i, d := range []string{"d1", "d2", "d3", "d4", "d5"} {
		access = append(access, mustLogin(t, h, "20000003", "Full-pw-"+d, d))
		_ = i
	}
	if res, _ := loginAs(t, h, "20000003", "Full-pw-d6", "d6"); res.StatusCode != http.StatusConflict {
		t.Fatalf("pre-revoke 6th login = %d, want 409", res.StatusCode)
	}
	// Find d1's device id through the device list.
	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/me/devices", access[0], nil)
	if res.StatusCode != 200 {
		t.Fatalf("devices = %d", res.StatusCode)
	}
	devices, ok := rawOut.([]any)
	if !ok || len(devices) == 0 {
		t.Fatalf("no devices: %v", rawOut)
	}
	devID, _ := asMap(t, devices[0])["id"].(string)

	res, _ = doJSON(t, h, http.MethodDelete, "/v1/me/devices/"+devID, access[0], nil)
	if res.StatusCode != 200 {
		t.Fatalf("device revoke = %d", res.StatusCode)
	}
	res, rawOut = loginAs(t, h, "20000003", "Full-pw-d6", "d6")
	if res.StatusCode != 200 {
		t.Fatalf("post-revoke 6th login = %d %v", res.StatusCode, rawOut)
	}
}

// TestAdminPoolEndpoint pins §10: pool observability is opt-in via
// LOVEACED_ADMIN_TOKEN; unset means the route does not exist.
func TestAdminSessionsEndpoint(t *testing.T) {
	tr := successTransport(t)
	_, h := newGroupTestServer(t, tr, "adm-secret")

	// No token configured → the route does not exist at all.
	_, hNone := newGroupTestServer(t, tr, "")
	res, _ := doJSON(t, hNone, http.MethodGet, "/v1/admin/sessions", "", nil)
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("admin route without token configured = %d, want 404", res.StatusCode)
	}

	res, _ = doJSON(t, h, http.MethodGet, "/v1/admin/sessions", "", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin without credentials = %d, want 401", res.StatusCode)
	}
	res, _ = doJSON(t, h, http.MethodGet, "/v1/admin/sessions", "wrong", nil)
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("admin with wrong token = %d, want 401", res.StatusCode)
	}

	mustLogin(t, h, "20000003", "Passw0rd!123", "d1")
	res, rawOut := doJSON(t, h, http.MethodGet, "/v1/admin/sessions", "adm-secret", nil)
	out := asMap(t, rawOut)
	if res.StatusCode != 200 {
		t.Fatalf("admin sessions = %d %v", res.StatusCode, out)
	}
	if _, ok := out["captcha"].(map[string]any); !ok {
		t.Fatalf("captcha stats missing/misshaped: %v", out["captcha"])
	}
	groups, _ := out["session_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("expected 1 session group, got %v", out["session_groups"])
	}
	g := asMap(t, groups[0])
	if g["active"] != float64(1) || g["capacity"] != float64(5) {
		t.Fatalf("group stat = %v", g)
	}
}
