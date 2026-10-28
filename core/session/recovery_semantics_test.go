package session

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// The three concurrency-recovery semantics that AGENTS.md 缺陷 2 called
// out as untested: Logout wins over a completing recovery, writes are
// never replayed on expiry, and a network-unavailable recovery defers
// without losing the stored credentials.

// recoveryFixture wraps the standard login fixture with hooks: business
// requests (anything off the login chain) are counted and scriptable; the
// first reauth hop (/por/login_auth.csp) can be intercepted.
type recoveryFixture struct {
	inner         transport.Transport
	mu            sync.Mutex
	business      func(call int) (*transport.Response, error)
	authHook      func(ctx context.Context, r *transport.Request) (*transport.Response, error)
	pswHook       func(ctx context.Context, r *transport.Request) (*transport.Response, error)
	businessCalls int
}

func (f *recoveryFixture) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	onChain := strings.Contains(r.URL, "/por/") ||
		strings.Contains(r.URL, "cas/login") ||
		strings.Contains(r.URL, "j_spring_cas_security_check")
	if strings.Contains(r.URL, "/por/login_auth.csp") && f.authHook != nil {
		return f.authHook(ctx, r)
	}
	if strings.Contains(r.URL, "/por/login_psw.csp") && f.pswHook != nil {
		return f.pswHook(ctx, r)
	}
	if onChain {
		return f.inner.Execute(ctx, r)
	}
	f.mu.Lock()
	f.businessCalls++
	call := f.businessCalls
	hook := f.business
	f.mu.Unlock()
	if hook != nil {
		return hook(call)
	}
	return f.inner.Execute(ctx, r)
}

func (f *recoveryFixture) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.businessCalls
}

// The SangFor portal page the detector classifies as vpn_session_expired.
var expiredPortal = []byte(`<html><body class="sangfor-body"><div class="sangfor-main"></div>
<script src="/jssdk/business/session.js"></script>
<script>var g_lines=[];var gotoLines=[];win_location=1;var x='sf_ssl_ms_1';</script>
<a href="#!/login">login</a></body></html>`)

const businessURL = "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/x/planCourseList"

// TestRecoveryCaptchaRequiresRelogin: when the recovery itself hits a gateway
// CAPTCHA (the auth page comes back with RndImg=1), a READ cannot answer it,
// so it fails as AuthRequired and the client signs in again, where the captcha
// flow lives. It must NOT surface outcome_unknown.
func TestRecoveryCaptchaRequiresRelogin(t *testing.T) {
	f := &recoveryFixture{inner: testutil.LoginTransport(t)}
	s := New("20000003", "gw", "school", f)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, expiredPortal), nil
	}
	modulus, exponent := testutil.RSAPublicNumbers(t)
	f.authHook = func(context.Context, *transport.Request) (*transport.Response, error) {
		// The polluted gateway: a valid auth page that demands a CAPTCHA.
		body := "<auth><TwfID>TWFID123</TwfID>" +
			"<RSA_ENCRYPT_KEY>" + modulus + "</RSA_ENCRYPT_KEY>" +
			"<RSA_ENCRYPT_EXP>" + exponent + "</RSA_ENCRYPT_EXP>" +
			"<CSRF_RAND_CODE>c1</CSRF_RAND_CODE><RndImg>1</RndImg></auth>"
		return transport.NewResponse(200, []byte(body)), nil
	}

	_, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL})
	if errs.CodeOf(err) != errs.AuthRequired {
		t.Fatalf("recovery captcha error = %v, want auth_required", err)
	}

	// Once the gateway stops demanding a CAPTCHA, a fresh read re-logins and succeeds.
	f.authHook = nil
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, []byte(`{"ok":true}`)), nil
	}
	if _, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL}); err != nil {
		t.Fatalf("read after pollution healed: %v", err)
	}
}

// TestRecoveryInvalidCredentialsSurfaces: a recovery that fails because the
// school rejected the stored password surfaces credentials_invalid so the
// client knows a re-login is required.
func TestRecoveryInvalidCredentialsSurfaces(t *testing.T) {
	f := &recoveryFixture{inner: testutil.LoginTransport(t)}
	s := New("20000003", "gw", "school", f)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, expiredPortal), nil
	}
	f.pswHook = func(context.Context, *transport.Request) (*transport.Response, error) {
		// The gateway rejecting the password (Result 20004) is how a real
		// credentials failure surfaces.
		return transport.NewResponse(200, []byte("<Result>20004</Result>")), nil
	}

	_, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL})
	if errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("recovery credentials error = %v, want invalid_credentials", err)
	}
}

// TestLogoutDuringRecoveryStaysLoggedOut: a Logout racing a blocked
// recovery must win — the finishing login may not publish authenticated
// (authenticate re-checks cancellation and generation before publishing).
func TestLogoutDuringRecoveryStaysLoggedOut(t *testing.T) {
	f := &recoveryFixture{inner: testutil.LoginTransport(t)}
	s := New("20000003", "gw", "school", f)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}

	block := make(chan struct{})
	reached := make(chan struct{}, 1)
	f.authHook = func(hctx context.Context, r *transport.Request) (*transport.Response, error) {
		select {
		case reached <- struct{}{}:
		default:
		}
		select {
		case <-block:
			return f.inner.Execute(hctx, r)
		case <-hctx.Done():
			return nil, hctx.Err()
		}
	}
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, expiredPortal), nil
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL})
		errCh <- err
	}()
	select {
	case <-reached:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery never reached the gateway auth page")
	}

	s.Logout()
	close(block)
	err := <-errCh
	if err == nil {
		t.Fatal("SendRead after Logout returned success; the recovery must be cancelled")
	}
	if s.State() == StateAuthenticated {
		t.Fatal("a login that completed after Logout published authenticated state")
	}
	if s.State() != StateSignedOut {
		t.Fatalf("state = %s, want signed_out", s.State())
	}
}

// TestWriteExpiryIsNotReplayed: a write whose response says the session
// expired returns outcome_unknown and never resends the request.
func TestWriteExpiryIsNotReplayed(t *testing.T) {
	f := &recoveryFixture{inner: testutil.LoginTransport(t)}
	cfg := DefaultConfig()
	cfg.AllowWrites = true
	s := NewWithConfig("20000003", "gw", "school", f, cfg)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, expiredPortal), nil
	}

	_, err := s.SendWrite(ctx, &transport.Request{Method: transport.Post, URL: businessURL})
	if errs.CodeOf(err) != errs.OutcomeUnknown {
		t.Fatalf("write expiry error = %v, want outcome_unknown", err)
	}
	if got := f.calls(); got != 1 {
		t.Fatalf("write was dispatched %d times, want exactly 1 (no replay)", got)
	}
}

// TestRecoveryUnavailableDefersAndKeepsCredentials: when the recovery
// itself hits a network failure the read is deferred (temporarily
// unavailable, NOT outcome_unknown), the stored credentials survive, and
// a later attempt performs a full reauthentication and succeeds.
func TestRecoveryUnavailableDefersAndKeepsCredentials(t *testing.T) {
	f := &recoveryFixture{inner: testutil.LoginTransport(t)}
	s := New("20000003", "gw", "school", f)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, expiredPortal), nil
	}
	f.authHook = func(context.Context, *transport.Request) (*transport.Response, error) {
		return nil, errs.NewNetwork("hook: network down")
	}

	_, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL})
	if errs.CodeOf(err) != errs.TemporarilyUnavailable {
		t.Fatalf("deferred recovery error = %v, want temporarily_unavailable", err)
	}
	// Credentials are preserved: the session can still reauthenticate.

	// Heal the network; the next read must fully reauthenticate and serve.
	f.authHook = nil
	f.business = func(int) (*transport.Response, error) {
		return transport.NewResponse(200, []byte(`{"ok":true}`)), nil
	}
	res, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL})
	if err != nil {
		t.Fatalf("read after recovery: %v", err)
	}
	if string(res.Body) != `{"ok":true}` {
		t.Fatalf("body = %q", res.Body)
	}
}
