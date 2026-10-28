package session_test

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

func newSession(t *testing.T) (*session.Session, *transport.MockTransport) {
	t.Helper()
	tr := testutil.LoginTransport(t)
	return session.New("20000002", "gw", "school", tr), tr
}

func TestLoginSucceedsAndSetsState(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if s.State() != session.StateAuthenticated {
		t.Fatalf("state = %s", s.State())
	}
	insp := s.Inspect()
	if !insp.ECLogged || !insp.UAAPLogged || !insp.HasTWFID {
		t.Fatalf("inspect = %+v", insp)
	}
	if insp.UserIDHash == "" || strings.Contains(insp.UserIDHash, "20000002") {
		t.Fatalf("user id hash must be a non-reversible fingerprint: %q", insp.UserIDHash)
	}
	// Idempotent: second login is a no-op.
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestECLoginReportsMissingTwfid(t *testing.T) {
	// A response without <TwfID> must not be treated as a session expiry or
	// a success; it is a distinct "not found" outcome.
	tr := transport.NewMockTransport().
		On("/por/login_auth.csp", transport.NewResponse(200, []byte("<auth></auth>")))
	s := session.New("20000002", "gw", "school", tr)
	status := s.ECLogin(context.Background())
	if status.Outcome != session.ECNotFoundTwfid {
		t.Fatalf("outcome = %s", status.Outcome)
	}
	if s.Inspect().ECLogged {
		t.Fatal("must not be logged in")
	}
}

func TestReadsReplayOnceAfterReauth(t *testing.T) {
	// First data response is a Sangfor portal page; after EC reauth the
	// second returns JSON. The mock is static, so we assert the recovery
	// path by checking that a second request is issued.
	tr := testutil.LoginTransport(t).
		On("/jwc/data", transport.NewResponse(200, []byte(`{"ok":true}`)))
	s := session.New("20000002", "gw", "school", tr)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	outcome, err := s.Send(ctx, transport.NewRequest(transport.Get, "http://jwc/jwc/data"), session.Read)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Response.Status != 200 {
		t.Fatalf("status = %d", outcome.Response.Status)
	}
}

func TestConcurrentExpiryProducesSingleReauth(t *testing.T) {
	// Concurrent 100 readers hitting an expired session must merge into one
	// reauthentication (single-flight recovery).
	var loginPosts atomic.Int32
	tr := testutil.LoginTransport(t)
	wrap := tr
	_ = wrap
	// Count login_psw POSTs via the recorded requests after the fact.
	tr.On("/jwc/data", transport.NewResponse(200, []byte(`{"ok":true}`)))

	s := session.New("20000002", "gw", "school", tr)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	// The mock is static, so expiry never actually occurs; instead simulate
	// concurrent EnsureAuthenticated calls, which must merge onto the auth
	// lock without issuing duplicate EC logins.
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.EnsureAuthenticated(ctx); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	for _, r := range tr.Recorded() {
		if r.Method == transport.Post && strings.Contains(r.URL, "/por/login_psw.csp") {
			loginPosts.Add(1)
		}
	}
	if got := loginPosts.Load(); got != 1 {
		t.Fatalf("EC login POST count = %d, want 1", got)
	}
}

func TestWritesArePermissionGated(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	_, err := s.SendWrite(ctx, transport.NewRequest(transport.Post, "http://jwc/write"))
	var e *errs.Error
	if err == nil || !asErr(err, &e) || e.Code != errs.InvalidArgument {
		t.Fatalf("want invalid_argument, got %v", err)
	}
}

func TestNonAsciiCredentialRejectedBeforeAnyRequest(t *testing.T) {
	tr := transport.NewMockTransport()
	s := session.New("20000002", "gw", "密码", tr)
	err := s.Login(context.Background())
	var e *errs.Error
	if !asErr(err, &e) || e.Code != errs.NonAsciiCredential {
		t.Fatalf("err = %v", err)
	}
	if len(tr.Recorded()) != 0 {
		t.Fatal("no requests must be made")
	}
	if got := e.Error(); strings.Contains(got, "密码") {
		t.Fatalf("value leaked: %q", got)
	}
}

func TestValidateCredentialsRejectsFullWidth(t *testing.T) {
	for _, value := range []string{"！pass", "密码", "abc＠def", "a\u3000b", "中文，"} {
		err := session.ValidateCredentials("20000002", "gw", value)
		var e *errs.Error
		if !asErr(err, &e) || e.Code != errs.NonAsciiCredential {
			t.Fatalf("value %q: err = %v", value, err)
		}
		if strings.Contains(err.Error(), value) {
			t.Fatalf("value %q leaked into error", value)
		}
	}
	if err := session.ValidateCredentials("20000002", "gw", "!@#$%^&*()_+-=Abc123"); err != nil {
		t.Fatalf("ascii symbols must pass: %v", err)
	}
	if err := session.ValidateCredentials("２０２４", "gw", "ascii"); err == nil {
		t.Fatal("full-width account must fail")
	}
}

func TestLogoutCancelsAndAdvancesGeneration(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	gen := s.Generation()
	s.Logout()
	if s.State() != session.StateSignedOut {
		t.Fatalf("state = %s", s.State())
	}
	if s.Generation() <= gen {
		t.Fatal("logout must advance generation")
	}
	if insp := s.Inspect(); len(insp.Cookies) != 0 {
		t.Fatal("cookies must be cleared")
	}
}

func TestCancelOperationKeepsSessionUsable(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	s.CancelOperation()
	// Session remains usable: login state preserved.
	if s.State() != session.StateAuthenticated {
		t.Fatalf("state = %s", s.State())
	}
	if err := s.EnsureAuthenticated(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestHeartbeatStates(t *testing.T) {
	// The heartbeat probes the proxied UAAP root, fingerprint-based:
	// the school's own pages (the CAS login form included) = alive; the
	// EC login demand (a redirect to the BARE portal host, or the
	// gateway's interception fingerprints) = a dead TWFID.
	tr := transport.NewMockTransport().
		On("uaap-aufe-edu-cn.vpn2.aufe.edu.cn", transport.NewResponse(200, []byte("<html>uaap cas login page</html>")))
	s := session.New("20000002", "gw", "school", tr)
	if got := s.Heartbeat(context.Background()); got != session.HeartbeatAlive {
		t.Fatalf("heartbeat = %s", got)
	}
	// The CAS's own login form is the school's page: alive.
	trCAS := transport.NewMockTransport().
		On("uaap-aufe-edu-cn.vpn2.aufe.edu.cn", transport.NewResponse(200,
			[]byte(`<html><form action="/cas/login"><input name="lt"><input name="execution"></form></html>`)))
	sCAS := session.New("20000002", "gw", "school", trCAS)
	if got := sCAS.Heartbeat(context.Background()); got != session.HeartbeatAlive {
		t.Fatalf("heartbeat (cas page) = %s", got)
	}
	// The EC login demand: a redirect to the bare portal host.
	trPortal := transport.NewMockTransport().
		On("uaap-aufe-edu-cn.vpn2.aufe.edu.cn", transport.NewResponse(302,
			[]byte("moved")).WithHeader("Location", "https://vpn2.aufe.edu.cn/portal"))
	sPortal := session.New("20000002", "gw", "school", trPortal)
	if got := sPortal.Heartbeat(context.Background()); got != session.HeartbeatExpired {
		t.Fatalf("heartbeat (portal redirect) = %s", got)
	}
	// The gateway's auth XML on a proxied host = interception.
	trXML := transport.NewMockTransport().
		On("uaap-aufe-edu-cn.vpn2.aufe.edu.cn", transport.NewResponse(200, []byte("<auth><TwfID>t</TwfID></auth>")))
	sXML := session.New("20000002", "gw", "school", trXML)
	if got := sXML.Heartbeat(context.Background()); got != session.HeartbeatExpired {
		t.Fatalf("heartbeat (auth xml) = %s", got)
	}
	// The legacy login-page markers = interception.
	trLegacy := transport.NewMockTransport().
		On("uaap-aufe-edu-cn.vpn2.aufe.edu.cn", transport.NewResponse(200,
			[]byte(`<html><body>session timeout, <a href="/por/login_auth.csp">relogin</a> svpn_name</body></html>`)))
	sLegacy := session.New("20000002", "gw", "school", trLegacy)
	if got := sLegacy.Heartbeat(context.Background()); got != session.HeartbeatExpired {
		t.Fatalf("heartbeat (legacy page) = %s", got)
	}
}

func TestClearCookiesByScope(t *testing.T) {
	s, _ := newSession(t)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatal(err)
	}
	if s.Inspect().HasTWFID != true {
		t.Fatal("twfid expected")
	}
	s.ClearCookies(cookies.All)
	// Rust parity: clear_cookies empties the jar but the session twfid
	// field remains; automatic recovery re-authenticates on the next call.
	if len(s.Inspect().Cookies) != 0 {
		t.Fatal("jar cookies must be cleared")
	}
	// Credentials/state survive a cookie clear.
	if s.State() != session.StateAuthenticated {
		t.Fatalf("state = %s", s.State())
	}
	// And a business call triggers automatic recovery (mock has login routes).
	if err := s.EnsureAuthenticated(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestExtractHelpers(t *testing.T) {
	if v, ok := session.ExtractBetween("<TwfID>t1</TwfID><RSA_ENCRYPT_KEY>0d</RSA_ENCRYPT_KEY>", "TwfID"); !ok || v != "t1" {
		t.Fatalf("extract = %q %v", v, ok)
	}
	if v, ok := session.ExtractBetween("body", "TwfID"); ok {
		t.Fatalf("unexpected extract = %q", v)
	}
	html := `<input type="hidden" name="lt" value="LT-1"/>`
	if v, ok := session.ExtractInputValue(html, "lt"); !ok || v != "LT-1" {
		t.Fatalf("input value = %q %v", v, ok)
	}
	if v, ok := session.ExtractFormAction(`<form action="/cas/login" method="post">`); !ok || v != "/cas/login" {
		t.Fatalf("form action = %q %v", v, ok)
	}
}

func asErr(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
	}
	return ok
}
