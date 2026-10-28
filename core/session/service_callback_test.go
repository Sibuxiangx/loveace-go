package session

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/internal/testutil"
)

// waitClosed fails the test unless ch closes within d.
func waitClosed(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("%s did not finish within %s", what, d)
	}
}

// callbackObservations returns the jwc_callback outcomes seen by the sink.
func callbackObservations(mem *events.Memory) []string {
	var out []string
	for _, o := range mem.Snapshot() {
		if o.Operation == "jwc_callback" {
			out = append(out, o.Outcome)
		}
	}
	return out
}

// Login must return while the JWC callback is still in flight: the callback
// is ~1-2s of the login critical path, and login does not need its result.
func TestLoginDoesNotWaitForJWCCallback(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), true)
	s := New("20000003", "gw", "school", g)
	t.Cleanup(func() { g.Release(); s.Logout() })

	done := make(chan error, 1)
	go func() { done <- s.Login(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("login: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login blocked on the JWC callback")
	}
	waitClosed(t, g.Started(), 5*time.Second, "JWC callback request")
	if s.State() != StateAuthenticated {
		t.Fatalf("state after login = %s, want authenticated", s.State())
	}
}

// A JWC request awaits the callback: AwaitServiceCallback holds until the
// callback has finished, and no JWC request reaches the gateway before it.
func TestAwaitServiceCallbackHoldsUntilCallbackFinishes(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), true)
	s := New("20000003", "gw", "school", g)
	t.Cleanup(func() { g.Release(); s.Logout() })
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitClosed(t, g.Started(), 5*time.Second, "JWC callback request")

	awaited := make(chan error, 1)
	go func() { awaited <- s.AwaitServiceCallback(ctx) }()
	select {
	case <-awaited:
		t.Fatal("await returned while the callback was still pending")
	case <-time.After(100 * time.Millisecond):
	}
	g.Release()
	select {
	case err := <-awaited:
		if err != nil {
			t.Fatalf("await: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("await did not return after the callback finished")
	}
	if got := g.Order(); len(got) != 1 || got[0] != "callback" {
		t.Fatalf("gateway order = %v, want [callback]", got)
	}
}

// Logout joins a pending callback: it returns promptly, and the abandoned
// callback records no JWC availability observation for the account.
func TestLogoutJoinsPendingCallbackWithoutObservation(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), true)
	s := New("20000003", "gw", "school", g)
	mem := events.NewMemory()
	s.SetEventSink(mem)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("login: %v", err)
	}
	waitClosed(t, g.Started(), 5*time.Second, "JWC callback request")

	logout := make(chan struct{})
	go func() { s.Logout(); close(logout) }()
	waitClosed(t, logout, 5*time.Second, "logout with a pending callback")

	if err := s.AwaitServiceCallback(ctx); err != nil {
		t.Fatalf("await after logout: %v", err)
	}
	if got := callbackObservations(mem); len(got) != 0 {
		t.Fatalf("abandoned callback emitted observations: %v", got)
	}
}

// A failed JWC callback never fails login. The request path proceeds as it
// did before the callback was asynchronous, and the failure is observed.
func TestCallbackFailureDoesNotFailLogin(t *testing.T) {
	g := testutil.NewCallbackGate(testutil.LoginTransport(t), false).WithStatus(500)
	s := New("20000003", "gw", "school", g)
	t.Cleanup(s.Logout)
	mem := events.NewMemory()
	s.SetEventSink(mem)
	ctx := context.Background()
	if err := s.Login(ctx); err != nil {
		t.Fatalf("login must survive a JWC callback failure: %v", err)
	}
	if err := s.AwaitServiceCallback(ctx); err != nil {
		t.Fatalf("await: %v", err)
	}
	if got := callbackObservations(mem); len(got) != 1 || got[0] != "failed" {
		t.Fatalf("callback observations = %v, want [failed]", got)
	}
	if s.State() != StateAuthenticated {
		t.Fatalf("state = %s, want authenticated", s.State())
	}
}
