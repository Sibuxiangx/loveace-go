package school

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// TestAccountCoolingLadder pins §5.2: repeated invalid-credential
// failures cool the account down (30s → 2m → …), a successful login
// resets the ledger, and the clock drives recovery.
func TestAccountCoolingLadder(t *testing.T) {
	now := time.Unix(1700000000, 0)
	gw := newChaosGateway(t)
	gw.setBadCreds(true)
	m := NewManager(func() transport.Transport { return gw },
		WithoutLoginGate(), WithClock(func() time.Time { return now }))

	// First failure: no cooldown (ladder step 0) — the retry goes back
	// to the gateway immediately.
	if _, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t"); errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("first failure = %v, want invalid_credentials", err)
	}
	if _, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t"); errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("second failure = %v, want invalid_credentials", err)
	}
	// Third attempt: cooling (30s) blocks the gateway touch.
	_, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t")
	var cool *AccountCoolingError
	if !errorsAsCooling(err, &cool) || cool.RetryAfter <= 0 {
		t.Fatalf("third attempt = %v, want account cooling", err)
	}
	if auths, _ := gw.counts(); auths != 2 {
		t.Fatalf("gateway touched %d times, want 2 (the cooled attempt must not touch)", auths)
	}

	// Repair the credentials, but the clock still gates: cooling holds.
	gw.setBadCreds(false)
	if _, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "d", "t"); !errorsAsCooling(err, &cool) {
		t.Fatalf("login during cooling = %v, want cooling", err)
	}
	// Advance past the 30s step: success clears the ledger.
	now = now.Add(31 * time.Second)
	if _, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "d", "t"); err != nil {
		t.Fatalf("login after cooling: %v", err)
	}
	// The ledger is clean: a fresh failure starts at step 0 again (the
	// first mistake cools nothing; the SECOND failure starts the ladder).
	gw.setBadCreds(true)
	if _, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t"); errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("post-reset failure = %v", err)
	}
	if _, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t"); errs.CodeOf(err) != errs.InvalidCredentials {
		t.Fatalf("second post-reset failure = %v", err)
	}
	if _, err := m.Login(context.Background(), "u1", "20000003", "wrong", "", "d", "t"); !errorsAsCooling(err, &cool) {
		t.Fatalf("third post-reset attempt = %v, want cooling", err)
	}
}

func errorsAsCooling(err error, target **AccountCoolingError) bool {
	for err != nil {
		if c, ok := err.(*AccountCoolingError); ok {
			*target = c
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
