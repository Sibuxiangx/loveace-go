package health

import (
	"context"
	"math/rand"
	"time"
)

// Zero-credential L0 probing (§9, confirmed-on): a plain GET of the
// gateway auth page — no account, no submission, no failure budget —
// tells us whether the gateway is reachable, whether our egress IP is
// captcha-polluted, and (over time) how long pollution takes to clear.
//
// Activity discipline: probes run ONLY while real user traffic exists
// (TouchActivity from the request path); after idleAfter with no user
// request the loop fully stops until the next request wakes it. One
// probe in flight, interval jittered.

// Probe outcomes.
const (
	ProbeAlive    = "alive"    // auth page served (gateway reachable)
	ProbePolluted = "polluted" // auth page demands a CAPTCHA (RndImg=1)
	ProbeDown     = "down"     // unreachable / unexpected body
)

// ProbeStatus is the latest zero-credential probe result.
type ProbeStatus struct {
	Result string    `json:"result"`
	At     time.Time `json:"at"`
}

// TouchActivity records that a real user request just happened (request
// middleware calls this; never the prober itself).
func (r *Registry) TouchActivity() {
	r.mu.Lock()
	r.lastActivity = r.now()
	r.mu.Unlock()
}

// RunProber drives the probe loop until ctx ends. probe must be
// self-timeouting (it runs on the loop goroutine).
func (r *Registry) RunProber(ctx context.Context, probe func(context.Context) string, interval, idleAfter time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	if idleAfter <= 0 {
		idleAfter = 10 * time.Minute
	}
	for {
		jittered := interval + time.Duration(rand.Int63n(int64(interval/5))) - interval/10
		select {
		case <-ctx.Done():
			return
		case <-time.After(jittered):
		}
		r.mu.Lock()
		idle := r.now().Sub(r.lastActivity) > idleAfter
		r.mu.Unlock()
		if idle {
			continue // nobody is using the system: no probing at all
		}
		result := probe(ctx)
		r.mu.Lock()
		r.lastProbe = ProbeStatus{Result: result, At: r.now()}
		r.mu.Unlock()
		if result == ProbePolluted {
			r.recordCaptchaSignal(r.now()) // pollution timeline: recovery clock
		}
	}
}

// LatestProbe returns the last probe result (zero value = none yet).
func (r *Registry) LatestProbe() ProbeStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastProbe
}
