package session

import (
	"context"
	"testing"

	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
)

// TestCompleteCaptchaLoginFinalizesAuthenticatedState covers the server's
// CAPTCHA continuation: after a successful gateway step (here the mock's
// normal EC login), CompleteCaptchaLogin must run the identity step and
// publish StateAuthenticated exactly like a normal Login.
func TestCompleteCaptchaLoginFinalizesAuthenticatedState(t *testing.T) {
	tr := testutil.LoginTransport(t)
	s := New("20000003", "gw", "school", tr)
	ctx := context.Background()

	if st := s.ECLogin(ctx); st.Outcome != ECSuccess {
		t.Fatalf("EC login outcome = %s", st.Outcome)
	}
	if s.State() == StateAuthenticated {
		t.Fatal("session must not be authenticated before the identity step")
	}
	if err := s.CompleteCaptchaLogin(ctx); err != nil {
		t.Fatalf("CompleteCaptchaLogin: %v", err)
	}
	if s.State() != StateAuthenticated {
		t.Fatalf("state = %s, want authenticated", s.State())
	}
	// Idempotent: a second call is a no-op.
	if err := s.CompleteCaptchaLogin(ctx); err != nil {
		t.Fatalf("second call: %v", err)
	}
}
