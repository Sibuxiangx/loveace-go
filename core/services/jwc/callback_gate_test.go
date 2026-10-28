package jwc

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// Login returns before the JWC callback finishes, so the first JWC read after
// login must be held by the service helper: no JWC request may reach the
// gateway until the callback has been redeemed.
func TestJWCReadWaitsForServiceCallbackAfterLogin(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), true)
	s := session.New("20000003", "gw", "school", g)
	t.Cleanup(func() { g.Release(); s.Logout() })
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("login: %v", err)
	}

	read := make(chan struct{})
	go func() {
		// The fixture has no route for this page; only the ordering matters.
		_, _ = New(s).Terms(ctx)
		close(read)
	}()

	select {
	case <-read:
		t.Fatal("JWC read completed before the callback was released")
	case <-time.After(150 * time.Millisecond):
	}
	if got := g.Order(); len(got) != 0 {
		t.Fatalf("JWC request reached the gateway while the callback was pending: %v", got)
	}

	g.Release()
	select {
	case <-read:
	case <-time.After(5 * time.Second):
		t.Fatal("JWC read did not complete after the callback was released")
	}
	if got := g.Order(); len(got) != 2 || got[0] != "callback" || got[1] != "jwc" {
		t.Fatalf("gateway order = %v, want [callback jwc]", got)
	}
}
