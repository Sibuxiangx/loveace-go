package school

import (
	"context"
	"errors"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// The password-change intercept (2026-10-17 production truth): when the
// school demands a password change, the CAS mints a TGT but 302s the
// identity login to /mng/acp/completeInfo/password instead of issuing a
// ticket. The credentials are CORRECT — the outcome is a distinct fatal
// code, never invalid_credentials, and the invalid-credential hook must
// NOT fire (the vault must not be marked).

// TestPasswordChangeRequiredFromLogin: the demand surfaces after the EC
// login — the demand is fatal (never invalid_credentials), registers
// nothing, and must NOT fire the invalid-credential hook (the vault must
// not be marked: the credentials are CORRECT).
func TestPasswordChangeRequiredFromLogin(t *testing.T) {
	gw := newChaosGateway(t)
	gw.setPasswordChange(true)
	m := NewManager(func() transport.Transport { return gw }, WithoutLoginGate(),
		WithInvalidCredentialsHook(func(context.Context, string) {
			t.Fatal("the credentials are CORRECT: the invalid hook must not fire")
		}))

	s, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "d1", "test")
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != errs.PasswordChangeRequired {
		t.Fatalf("login error = %v, want password_change_required", err)
	}
	if s != nil {
		t.Fatal("login must not return a session")
	}
	auths, _ := gw.counts()
	if auths != 1 { // the EC login's auth-page GET; the identity phase rides the CAS
		t.Fatalf("gateway auth touches = %d, want 1", auths)
	}
}
