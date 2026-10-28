package session

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/config"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// The CAS ticket mint walks raw hops through RedirectLocations, which
// bypasses sendFollowingRedirects' expiry handling. While the gateway
// session is valid the vpn2 proxy rewrites service redirects into the vpn2
// namespace; once it stops honouring the session, the CAS Location comes
// back unrewritten and following it can only time out on an unreachable
// internal address. RedirectLocations must detect the escape, recover
// (single-flight reauth, exactly like reads) and retry the walk once.

type mintFixture struct {
	inner    transport.Transport
	mu       sync.Mutex
	stale    bool
	authHits int // /por/login_auth.csp executions (login + recoveries)
	followed []string
}

func (f *mintFixture) Execute(_ context.Context, r *transport.Request) (*transport.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if strings.Contains(r.URL, "/por/login_auth.csp") {
		f.authHits++
		f.stale = false // a fresh TWFID re-arms the gateway session
		return f.inner.Execute(context.Background(), r)
	}
	if strings.Contains(r.URL, "cas/login") && strings.Contains(r.URL, "CoreLoginCallback") {
		if f.stale {
			// The gateway proxies the CAS hop but no longer rewrites the
			// service redirect into the vpn2 namespace.
			return transport.NewResponse(302, []byte("moved")).
				WithHeader("Location", "http://api.dekt.ac.acxk.net/User/Index/CoreLoginCallback?ticket=ST-stale"), nil
		}
		return transport.NewResponse(302, []byte("moved")).
			WithHeader("Location", "http://api-dekt-ac-acxk-net.vpn2.aufe.edu.cn:8118/User/Index/CoreLoginCallback?ticket=ST-ok"), nil
	}
	if strings.Contains(r.URL, "CoreLoginCallback") {
		f.followed = append(f.followed, r.URL)
		return transport.NewResponse(200, []byte(`<html>dekt</html>`)), nil
	}
	return f.inner.Execute(context.Background(), r)
}

func (f *mintFixture) authCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authHits
}

func newMintSession(t *testing.T, f *mintFixture) *Session {
	t.Helper()
	s := New("20000003", "gw", "school", f)
	if err := s.Login(context.Background()); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	return s
}

// TestRedirectLocationsRecoversStaleGatewaySession: an unrewritten CAS
// redirect triggers one recovery and the retried walk finds the ticket.
func TestRedirectLocationsRecoversStaleGatewaySession(t *testing.T) {
	f := &mintFixture{inner: testutil.LoginTransport(t)}
	s := newMintSession(t, f)
	if got := f.authCount(); got != 1 {
		t.Fatalf("auth page hits after login = %d, want 1", got)
	}
	f.mu.Lock()
	f.stale = true // the gateway session dies while the session idles
	f.mu.Unlock()

	locations, err := s.RedirectLocations(context.Background(), config.AACLoginService, 10)
	if err != nil {
		t.Fatalf("RedirectLocations: %v", err)
	}
	joined := strings.Join(locations, " ")
	if !strings.Contains(joined, "ticket=ST-ok") {
		t.Fatalf("locations after recovery = %q, want the rewritten ticket callback", joined)
	}
	if got := f.authCount(); got != 2 {
		t.Fatalf("auth page hits = %d, want 2 (initial login + one recovery)", got)
	}
	for _, u := range f.followed {
		if !strings.Contains(u, config.VPNHostSuffix) {
			t.Fatalf("walk followed the unrewritten internal URL %q", u)
		}
	}
}

// TestRedirectLocationsFreshSessionMakesNoRecovery: the mint chain starts at
// cas/login by design, so the bare cas_login_url detection signal must not
// fire and a fresh session mints without any reauthentication.
func TestRedirectLocationsFreshSessionMakesNoRecovery(t *testing.T) {
	f := &mintFixture{inner: testutil.LoginTransport(t)}
	s := newMintSession(t, f)

	locations, err := s.RedirectLocations(context.Background(), config.AACLoginService, 10)
	if err != nil {
		t.Fatalf("RedirectLocations: %v", err)
	}
	if !strings.Contains(strings.Join(locations, " "), "ticket=ST-ok") {
		t.Fatalf("locations = %q, want the ticket callback", strings.Join(locations, " "))
	}
	if got := f.authCount(); got != 1 {
		t.Fatalf("auth page hits = %d, want 1 (no recovery on the fresh walk)", got)
	}
}
