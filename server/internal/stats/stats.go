// Package stats aggregates usage statistics (DESIGN.md §11.4): per-
// operation call volumes, success rates and latency; DAU/MAU; the login
// funnel. Aggregate-only — no raw events, no content, no identifiers
// beyond the opaque server user id used for distinct counting. In-memory
// with a 30-day user window; persistent rollups land with the jobs
// infrastructure (M-later).
package stats

import (
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

// Collector consumes observations.
type Collector struct {
	mu  sync.Mutex
	now func() time.Time

	ops      map[string]*opAgg
	lastSeen map[string]time.Time       // user -> last active (MAU window)
	days     map[string]map[string]bool // day -> distinct users (DAU)

	loginAttempts int64
	loginSuccess  int64
}

type opAgg struct {
	count     int64
	failures  int64
	latencyMS int64
}

// New builds an empty collector.
func New() *Collector {
	return &Collector{
		now:      time.Now,
		ops:      map[string]*opAgg{},
		lastSeen: map[string]time.Time{},
		days:     map[string]map[string]bool{},
	}
}

type boundSink struct {
	c    *Collector
	user string
}

func (b *boundSink) Record(o events.Observation) { b.c.ingest(b.user, o) }

// SinkFor returns a user-tagged sink.
func (c *Collector) SinkFor(user string) events.Sink { return &boundSink{c: c, user: user} }

func (c *Collector) ingest(user string, o events.Observation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()

	switch o.Kind {
	case events.KindOperation:
		agg := c.ops[o.Operation]
		if agg == nil {
			agg = &opAgg{}
			c.ops[o.Operation] = agg
		}
		agg.count++
		agg.latencyMS += o.DurationMS
		if o.Outcome != "success" {
			agg.failures++
		}
		if user != "" {
			c.lastSeen[user] = now
			day := now.Format("2006-01-02")
			set, ok := c.days[day]
			if !ok {
				set = map[string]bool{}
				c.days[day] = set
			}
			set[user] = true
			c.pruneLocked(now)
		}
	case events.KindGatewayAuth, events.KindLogin:
		if o.Operation != "login" {
			break
		}
		switch o.Outcome {
		case "success", "bad_credentials", "unavailable", "failed",
			"throttled", "captcha_required", "captcha_invalid":
			c.loginAttempts++
		}
		if o.Outcome == "success" {
			c.loginSuccess++
		}
	}
}

func (c *Collector) pruneLocked(now time.Time) {
	if len(c.days) <= 35 {
		return
	}
	cutoff := now.AddDate(0, 0, -30).Format("2006-01-02")
	for day := range c.days {
		if day < cutoff {
			delete(c.days, day)
		}
	}
	for u, at := range c.lastSeen {
		if now.Sub(at) > 30*24*time.Hour {
			delete(c.lastSeen, u)
		}
	}
}

// OpStat is one operation's usage aggregate.
type OpStat struct {
	Count        int64   `json:"count"`
	Failures     int64   `json:"failures"`
	SuccessRate  float64 `json:"success_rate"`
	AvgLatencyMS int64   `json:"avg_latency_ms"`
}

// Snapshot renders the current aggregates.
type Snapshot struct {
	AsOf   time.Time         `json:"as_of"`
	Ops    map[string]OpStat `json:"ops"`
	DAU    int               `json:"dau_today"`
	MAU    int               `json:"mau_30d"`
	Funnel struct {
		Attempts int64   `json:"attempts"`
		Success  int64   `json:"success"`
		Rate     float64 `json:"rate"`
	} `json:"login_funnel"`
}

// Stats returns the aggregates.
func (c *Collector) Stats() Snapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	out := Snapshot{AsOf: now, Ops: map[string]OpStat{}}
	for op, agg := range c.ops {
		st := OpStat{Count: agg.count, Failures: agg.failures}
		if agg.count > 0 {
			st.SuccessRate = float64(agg.count-agg.failures) / float64(agg.count)
			st.AvgLatencyMS = agg.latencyMS / agg.count
		}
		out.Ops[op] = st
	}
	today := now.Format("2006-01-02")
	out.DAU = len(c.days[today])
	mau := map[string]bool{}
	cutoff := now.AddDate(0, 0, -30)
	for u, at := range c.lastSeen {
		if at.After(cutoff) {
			mau[u] = true
		}
	}
	out.MAU = len(mau)
	out.Funnel.Attempts = c.loginAttempts
	out.Funnel.Success = c.loginSuccess
	if c.loginAttempts > 0 {
		out.Funnel.Rate = float64(c.loginSuccess) / float64(c.loginAttempts)
	}
	return out
}

var _ events.Sink = (*boundSink)(nil)
