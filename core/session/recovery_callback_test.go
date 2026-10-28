package session

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// A recovery re-logins and starts a fresh JWC callback. The retried read must
// not go out until that callback has finished, or it races the JWC cookies
// the callback is about to set. The callback is delayed so that a missing
// await reliably shows up as a retry that observes only one finished callback.
func TestRecoveryAwaitsJWCCallbackBeforeRetry(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), false).WithDelay(200 * time.Millisecond)
	f := &recoveryFixture{inner: g}
	s := New("20000003", "gw", "school", f)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("initial login: %v", err)
	}
	// Settle the login's own callback so the count below is exactly the
	// recovery's contribution.
	if err := s.AwaitServiceCallback(ctx); err != nil {
		t.Fatalf("await login callback: %v", err)
	}

	retried := false
	f.business = func(call int) (*transport.Response, error) {
		if call == 1 {
			return transport.NewResponse(200, expiredPortal), nil
		}
		retried = true
		if n := g.Callbacks(); n != 2 {
			t.Errorf("retry sent with %d of 2 JWC callbacks finished", n)
		}
		return transport.NewResponse(200, []byte(`{"ok":true}`)), nil
	}
	if _, err := s.SendRead(ctx, &transport.Request{Method: transport.Get, URL: businessURL}); err != nil {
		t.Fatalf("read through recovery: %v", err)
	}
	if !retried {
		t.Fatal("expired read was never retried after recovery")
	}
}
