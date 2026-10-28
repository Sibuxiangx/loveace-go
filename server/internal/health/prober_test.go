package health

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

// TestProberActivityDiscipline pins §9: probes run only while users are
// active; an idle registry is never probed.
func TestProberActivityDiscipline(t *testing.T) {
	var clock atomic.Int64 // unix seconds; read by the registry's clock
	clock.Store(1_800_000_000)
	r := newTestRegistry(func() time.Time { return time.Unix(clock.Load(), 0) })

	var probes atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.RunProber(ctx, func(context.Context) string {
		probes.Add(1)
		return ProbeAlive
	}, 20*time.Millisecond, 100*time.Millisecond)

	// Active phase: touches keep the prober running.
	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		r.TouchActivity()
		time.Sleep(20 * time.Millisecond)
	}
	if probes.Load() == 0 {
		t.Fatal("prober never ran while active")
	}
	// Idle phase: advance the clock far past idleAfter — the loop must
	// stop probing entirely.
	clock.Store(clock.Load() + 3600)
	quiet := probes.Load()
	time.Sleep(150 * time.Millisecond)
	if still := probes.Load(); still != quiet {
		t.Fatalf("probes continued while idle: %d → %d", quiet, still)
	}
}

// TestProberPollutionTimeline: a polluted probe lands on the captcha
// timeline and the public view exposes the latest probe plus minute
// history built from real traffic.
func TestProberPollutionTimeline(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	r := newTestRegistry(func() time.Time { return now })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go r.RunProber(ctx, func(context.Context) string { return ProbePolluted },
		20*time.Millisecond, time.Hour)
	r.TouchActivity()
	time.Sleep(80 * time.Millisecond)

	if p := r.LatestProbe(); p.Result != ProbePolluted {
		t.Fatalf("latest probe = %q, want polluted", p.Result)
	}
	full := r.Status()
	if full.Counters.CaptchaRejections == 0 {
		t.Fatal("polluted probe did not reach the captcha timeline")
	}

	// Real traffic feeds the minute history.
	r.ingest("u1", events.Observation{Kind: events.KindOperation, Operation: "jwc.op", Outcome: "success", DurationMS: 5})
	pub := r.Public()
	if len(pub.History) == 0 || pub.History[0].Ops == 0 {
		t.Fatalf("public history = %+v, want at least one bucket with ops", pub.History)
	}
	if pub.Gateway == nil || pub.Gateway.Result != ProbePolluted {
		t.Fatalf("public gateway probe = %+v, want polluted", pub.Gateway)
	}
}
