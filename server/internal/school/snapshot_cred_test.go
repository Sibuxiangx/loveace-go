package school

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// The snapshot restore must rebuild sessions WITH real credentials: the
// cookies expire school-side, and the recovery re-logins with whatever the
// session holds. An empty-credential restore was a zombie — its reads 409'd
// outcome_unknown forever (2026-10-16 production incident).

// portalPage is the SangFor portal body the detector classifies as an
// expired session.
var portalPage = []byte(`<html><body class="sangfor-body"><div class="sangfor-main"></div>
<script src="/jssdk/business/session.js"></script>
<script>var g_lines=[];var gotoLines=[];win_location=1;var x='sf_ssl_ms_1';</script>
<a href="#!/login">login</a></body></html>`)

// bizWrap scripts the business (non-login) responses: the first N reads
// see an expired-session portal, then the endpoint recovers.
type bizWrap struct {
	inner    *chaosGateway
	expired  atomic.Int64 // remaining expired responses
	down     atomic.Bool  // business endpoint hard-down
	expiries int
}

func (w *bizWrap) Execute(ctx context.Context, r *transport.Request) (*transport.Response, error) {
	if !strings.Contains(r.URL, "/por/") && !strings.Contains(r.URL, "cas/") {
		if w.down.Load() {
			return nil, errs.NewNetwork("business endpoint unreachable")
		}
		if w.expired.Add(-1) >= 0 {
			return transport.NewResponse(200, portalPage), nil
		}
		return transport.NewResponse(200, []byte(`{"ok":true}`)), nil
	}
	return w.inner.Execute(ctx, r)
}

func snapshotWithCookies() UserSnapshot {
	return UserSnapshot{
		UserID:   "u1",
		DeviceID: "d1",
		SavedAt:  time.Now(),
		Cookies: []*cookies.Cookie{
			{Name: "TWFID", Value: "v1", Domain: ".vpn2.aufe.edu.cn", Path: "/"},
		},
	}
}

// TestSnapshotRestoreCarriesCredentials: a restored session re-logins with
// the resolver's credentials when its cookies expire, and the read
// recovers instead of 409ing outcome_unknown.
func TestSnapshotRestoreCarriesCredentials(t *testing.T) {
	gw := newChaosGateway(t)
	biz := &bizWrap{inner: gw}
	biz.expired.Store(1)
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{"u1": snapshotWithCookies()}}
	m := NewManager(func() transport.Transport { return biz },
		WithSnapshotStore(st),
		WithCredentialResolver(func(context.Context, string) (string, string, string, error) {
			return "20000003", "gw", "school-pw", nil
		}))

	sess, err := m.SessionFor(context.Background(), "u1", "d1")
	if err != nil {
		t.Fatalf("snapshot restore: %v", err)
	}
	res, err := sess.SendRead(context.Background(),
		&transport.Request{Method: transport.Get, URL: "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/x/plan"})
	if err != nil {
		t.Fatalf("read after restore+expiry: %v", err)
	}
	if string(res.Body) != `{"ok":true}` {
		t.Fatalf("body = %q", res.Body)
	}
	if _, posts := gw.counts(); posts == 0 {
		t.Fatal("recovery never submitted credentials; the session has none")
	}
}

// TestSnapshotRestoreResolverMissSkipsRestore: a resolver miss degrades to
// no restore (the next request takes the vault-relogin path) instead of
// building a credential-less zombie.
func TestSnapshotRestoreResolverMissSkipsRestore(t *testing.T) {
	gw := newChaosGateway(t)
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{"u1": snapshotWithCookies()}}
	m := NewManager(func() transport.Transport { return gw },
		WithSnapshotStore(st),
		WithCredentialResolver(func(context.Context, string) (string, string, string, error) {
			return "", "", "", errs.NewNotFound("credential")
		}))

	if _, err := m.SessionFor(context.Background(), "u1", "d1"); err != ErrNoSession {
		t.Fatalf("resolver miss: %v, want ErrNoSession", err)
	}
	if cs := m.CoolingStats(); cs.RestoreMisses != 1 {
		t.Fatalf("restore misses = %d, want 1", cs.RestoreMisses)
	}
}

// TestSnapshotRecoveryBadPasswordSurfaces: when the resolver's credentials
// are rejected school-side, the read surfaces invalid_credentials (the
// client must re-enter the password) — never outcome_unknown.
func TestSnapshotRecoveryBadPasswordSurfaces(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setBadCreds(true)
	biz := &bizWrap{inner: gw}
	biz.expired.Store(1)
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{"u1": snapshotWithCookies()}}
	m := NewManager(func() transport.Transport { return biz },
		WithSnapshotStore(st),
		WithCredentialResolver(func(context.Context, string) (string, string, string, error) {
			return "20000003", "gw", "wrong-pw", nil
		}))

	sess, err := m.SessionFor(context.Background(), "u1", "d1")
	if err != nil {
		t.Fatalf("snapshot restore: %v", err)
	}
	_, err = sess.SendRead(context.Background(),
		&transport.Request{Method: transport.Get, URL: "http://jwcxk2-aufe-edu-cn.vpn2.aufe.edu.cn:8118/x/plan"})
	if errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("bad-password recovery = %v, want invalid_credentials", err)
	}
}
