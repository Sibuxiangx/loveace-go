package health

import (
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

func newTestRegistry(now func() time.Time) *Registry {
	r := New()
	r.now = now
	return r
}

func at(t *testing.T, r *Registry, user string, kind events.Kind, operation, outcome string) {
	t.Helper()
	r.ingest(user, events.Observation{Kind: kind, Operation: operation, Outcome: outcome, DurationMS: 5})
}

// TestMinSamplesAndAccountsRule: five failures from ONE account must not
// degrade (single noisy user); a second account pushes it over.
func TestMinSamplesAndAccountsRule(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })

	for i := 0; i < 6; i++ {
		now = now.Add(time.Minute)
		at(t, r, "u1", events.KindOperation, "jwc.op", "failure:server_error")
	}
	if st := r.Status().Layers["jwc"]; st.State != StateUnknown {
		t.Fatalf("one noisy account produced state %q, want unknown", st.State)
	}
	now = now.Add(time.Minute)
	at(t, r, "u2", events.KindOperation, "jwc.op", "failure:server_error")
	if st := r.Status().Layers["jwc"]; st.State != StateDegraded {
		t.Fatalf("state = %q, want degraded with 2 accounts", st.State)
	}
}

// TestPasswordErrorsAreNotOutages: invalid credentials and cancelled ops
// never count as school failures.
func TestPasswordErrorsAreNotOutages(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	for i := 0; i < 6; i++ {
		now = now.Add(time.Minute)
		at(t, r, "u1", events.KindGatewayAuth, "login", "bad_credentials")
		at(t, r, "u2", events.KindOperation, "jwc.op", "failure:invalid_credentials")
	}
	if st := r.Status().Layers["gateway"]; st.State != StateUnknown {
		t.Fatalf("gateway state = %q, want unknown", st.State)
	}
	if st := r.Status().Layers["jwc"]; st.State != StateUnknown {
		t.Fatalf("jwc state = %q, want unknown", st.State)
	}
}

// TestL0BlocksL1: a degraded gateway marks business layers
// blocked_by_gateway instead of reporting independent outages.
func TestL0BlocksL1(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	// Degraded jwc from two accounts.
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		at(t, r, "u1", events.KindOperation, "jwc.op", "failure:server_error")
		at(t, r, "u2", events.KindOperation, "jwc.op", "failure:server_error")
	}
	if st := r.Status().Layers["jwc"]; st.State != StateDegraded {
		t.Fatalf("precondition: jwc = %q", st.State)
	}
	// Now the gateway degrades (2 accounts, recent failures).
	for i := 0; i < 4; i++ {
		now = now.Add(30 * time.Second)
		at(t, r, "u1", events.KindGatewayAuth, "login", "unavailable")
		at(t, r, "u2", events.KindGatewayAuth, "login", "unavailable")
	}
	if st := r.Status().Layers["jwc"]; st.State != StateBlockedByGateway {
		t.Fatalf("jwc = %q, want blocked_by_gateway", st.State)
	}
}

// TestHysteresisAndDecay: recovery needs a success streak; idleness decays
// to unknown while keeping the last known state.
func TestHysteresisAndDecay(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	for i := 0; i < 5; i++ {
		now = now.Add(time.Minute)
		at(t, r, "u1", events.KindGatewayAuth, "login", "unavailable")
		at(t, r, "u2", events.KindGatewayAuth, "login", "unavailable")
	}
	if st := r.Status().Layers["gateway"]; st.State != StateDegraded {
		t.Fatalf("precondition: %q", st.State)
	}
	// Two successes — below the streak: still degraded.
	for i := 0; i < 2; i++ {
		now = now.Add(30 * time.Second)
		at(t, r, "u1", events.KindGatewayAuth, "login", "success")
	}
	if st := r.Status().Layers["gateway"]; st.State != StateDegraded {
		t.Fatalf("hysteresis failed: %q", st.State)
	}
	// Third consecutive success: healthy.
	now = now.Add(30 * time.Second)
	at(t, r, "u1", events.KindGatewayAuth, "login", "success")
	if st := r.Status().Layers["gateway"]; st.State != StateHealthy {
		t.Fatalf("state = %q, want healthy after streak", st.State)
	}
	// Idle past the decay threshold: unknown, last known retained.
	now = now.Add(2 * IdleDecay)
	st := r.Status().Layers["gateway"]
	if st.State != StateUnknown {
		t.Fatalf("idle state = %q, want unknown", st.State)
	}
	if st.LastKnown != StateHealthy {
		t.Fatalf("last known = %q, want healthy", st.LastKnown)
	}
}

// TestMaintenanceOverride: the manual override wins and expires.
func TestMaintenanceOverride(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	r.SetMaintenance("jwc", 30)
	at(t, r, "u1", events.KindOperation, "jwc.op", "failure:server_error")
	if st := r.Status().Layers["jwc"]; st.State != StateMaintenance {
		t.Fatalf("state = %q, want maintenance", st.State)
	}
	now = now.Add(31 * time.Minute)
	at(t, r, "u1", events.KindOperation, "jwc.op", "success")
	if st := r.Status().Layers["jwc"]; st.State == StateMaintenance {
		t.Fatal("override did not expire")
	}
}

// TestCountersAndCaptchaTimeline: captcha events and recovery latency land
// in the §10 counters; the public view hides them.
func TestCountersAndCaptchaTimeline(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })
	at(t, r, "u1", events.KindGatewayAuth, "login", "captcha_required")
	at(t, r, "u1", events.KindGatewayAuth, "login", "captcha_invalid")
	at(t, r, "u1", events.KindSessionExpiry, "send", "expired")
	at(t, r, "u1", events.KindReconnect, "recover", "success")

	full := r.Status()
	if full.Counters.CaptchaRejections != 2 || len(full.Counters.CaptchaTimeline) != 2 {
		t.Fatalf("captcha counters = %+v", full.Counters)
	}
	if full.Counters.SessionExpiries != 1 {
		t.Fatalf("expiry counter = %d", full.Counters.SessionExpiries)
	}
	if full.Counters.Recoveries.OK != 1 || full.Counters.Recoveries.AvgLatencyMS != 5 {
		t.Fatalf("recovery counters = %+v", full.Counters.Recoveries)
	}
	// Public view: layers only, no counter data.
	pub := r.Public()
	if len(pub.Layers) == 0 {
		t.Fatal("public view lost layers")
	}
}
