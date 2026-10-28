package stats

import (
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

// TestCollectorAggregates pins §11.4: per-op usage/success/latency,
// DAU/MAU distinct counting and the login funnel — aggregates only.
func TestCollectorAggregates(t *testing.T) {
	now := time.Unix(1_800_000_000, 0) // 2027-01-15 UTC-ish
	c := New()
	c.now = func() time.Time { return now }
	s1 := c.SinkFor("u1")
	s2 := c.SinkFor("u2")

	s1.Record(events.Observation{Kind: events.KindOperation, Operation: "jwc.academic_info", Outcome: "success", DurationMS: 20})
	s1.Record(events.Observation{Kind: events.KindOperation, Operation: "jwc.academic_info", Outcome: "failure:server_error", DurationMS: 60})
	s2.Record(events.Observation{Kind: events.KindOperation, Operation: "jwc.academic_info", Outcome: "success", DurationMS: 40})
	s1.Record(events.Observation{Kind: events.KindOperation, Operation: "ykt.balance", Outcome: "success", DurationMS: 10})
	s1.Record(events.Observation{Kind: events.KindGatewayAuth, Operation: "login", Outcome: "success"})
	s2.Record(events.Observation{Kind: events.KindGatewayAuth, Operation: "login", Outcome: "bad_credentials"})
	s2.Record(events.Observation{Kind: events.KindGatewayAuth, Operation: "login", Outcome: "captcha_required"})

	snap := c.Stats()
	ai := snap.Ops["jwc.academic_info"]
	if ai.Count != 3 || ai.Failures != 1 || ai.SuccessRate != 2.0/3.0 || ai.AvgLatencyMS != 40 {
		t.Fatalf("academic_info agg = %+v", ai)
	}
	if snap.DAU != 2 {
		t.Fatalf("DAU = %d, want 2", snap.DAU)
	}
	if snap.MAU != 2 {
		t.Fatalf("MAU = %d, want 2", snap.MAU)
	}
	if snap.Funnel.Attempts != 3 || snap.Funnel.Success != 1 {
		t.Fatalf("funnel = %+v", snap.Funnel)
	}

	// 31 days later the user window prunes: MAU drops.
	now = now.Add(31 * 24 * time.Hour)
	snap = c.Stats()
	if snap.MAU != 0 || snap.DAU != 0 {
		t.Fatalf("stale window: DAU=%d MAU=%d, want 0/0", snap.DAU, snap.MAU)
	}
}
