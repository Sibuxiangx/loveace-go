// Package health implements the passive, layered, dependency-aware health
// judgement (DESIGN.md §9) plus the observation counters of §10.
//
// Rules pinned here:
//   - data comes from REAL traffic only; the registry never probes the
//     school (zero-credential, zero-extra-load);
//   - one logical operation is one sample (the ops layer emits per op);
//   - zero samples reads as "unknown", never "down";
//   - a 200 login page is not a success (the detector already classifies
//     it as expiry upstream);
//   - wrong credentials are the user's failure, not a school outage;
//   - when L0 (gateway/UAAP) degrades, L1 layers read as
//     blocked_by_gateway instead of each reporting an outage;
//   - judgement needs a minimum sample count AND a minimum number of
//     distinct accounts (one noisy user must not page anyone);
//   - hysteresis: recovery from degraded requires a streak of successes;
//   - without traffic the state decays to "unknown" (last known state and
//     its age are kept).
package health

import (
	"strings"
	"sync"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/events"
)

// Layer states.
const (
	StateHealthy          = "healthy"
	StateDegraded         = "degraded"
	StateUnknown          = "unknown"
	StateBlockedByGateway = "blocked_by_gateway"
	StateMaintenance      = "maintenance"
)

// Judgement thresholds (§9).
const (
	// Window bounds the L1 sample ring.
	Window = 10 * time.Minute
	// GatewayWindow bounds L0 judgement (faster reaction).
	GatewayWindow = 5 * time.Minute
	// MinSamples: below this a layer cannot change state.
	MinSamples = 5
	// MinAccounts: degradation needs distinct recent accounts.
	MinAccounts = 2
	// FailRate degrades a layer.
	FailRate = 0.8
	// RecoverStreak: consecutive successes to leave degraded.
	RecoverStreak = 3
	// IdleDecay without samples decays the layer to unknown.
	IdleDecay = 5 * time.Minute
	// captchaTimeline caps the pollution timeline.
	captchaTimeline = 64
)

// services are the L1 operation-name prefixes.
var services = []string{"jwc", "ykt", "aac", "labor", "isim", "competition", "repair", "manifest"}

// userFault outcomes are never counted as school failures (§9).
var userFault = map[string]bool{
	"failure:invalid_credentials":     true,
	"failure:non_ascii_credential":    true,
	"failure:invalid_argument":        true,
	"failure:not_implemented":         true,
	"failure:cancelled":               true,
	"failure:password_invalid_format": true,
}

// Registry is the passive health state. It is an events fan-in: acquire a
// per-account sink with SinkFor and attach it to sessions.
type Registry struct {
	mu     sync.Mutex
	now    func() time.Time
	layers map[string]*layer

	buckets [60]minuteBucket // ring of one-minute buckets (public status)
	captcha []time.Time      // pollution timeline (capped)

	maintenance map[string]time.Time // layer -> override until

	lastActivity time.Time // last real user request (drives probing)
	lastProbe    ProbeStatus

	counters counters
}

type sample struct {
	at   time.Time
	ok   bool
	user string
}

type layer struct {
	name      string
	samples   []sample
	state     string
	lastKnown string
	lastAt    time.Time
	streakOK  int
}

type counters struct {
	captchaRejections int64
	sessionExpiries   int64
	recoverOK         int64
	recoverDeferred   int64
	recoverFailed     int64
	recoveryLatencyMS int64 // cumulative; avg = /recoverOK
	parseErrors       map[string]int64
}

type minuteBucket struct {
	minute time.Time
	ok     map[string]int
	fail   map[string]int
}

// New builds an empty registry with every known layer pre-seeded as
// unknown (the public status page shows the full map from boot).
func New() *Registry {
	r := &Registry{
		now:         time.Now,
		layers:      map[string]*layer{},
		maintenance: map[string]time.Time{},
		counters:    counters{parseErrors: map[string]int64{}},
	}
	r.layers["gateway"] = &layer{name: "gateway", state: StateUnknown, lastKnown: StateUnknown}
	r.layers["uaap"] = &layer{name: "uaap", state: StateUnknown, lastKnown: StateUnknown}
	for _, svc := range services {
		r.layers[svc] = &layer{name: svc, state: StateUnknown, lastKnown: StateUnknown}
	}
	return r
}

type boundSink struct {
	r    *Registry
	user string
}

func (b *boundSink) Record(o events.Observation) { b.r.ingest(b.user, o) }

// SinkFor returns an events.Sink tagging observations with the (opaque,
// random) server user id — the distinct-account rule needs it.
func (r *Registry) SinkFor(user string) events.Sink { return &boundSink{r: r, user: user} }

// SetMaintenance manually overrides a layer ("维护中"); minutes <= 0 clears.
func (r *Registry) SetMaintenance(layerName string, minutes int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if minutes <= 0 {
		delete(r.maintenance, layerName)
		return
	}
	r.maintenance[layerName] = r.now().Add(time.Duration(minutes) * time.Minute)
}

func (r *Registry) ingest(user string, o events.Observation) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()

	switch o.Kind {
	case events.KindGatewayAuth, events.KindLogin:
		switch o.Outcome {
		case "success":
			r.add("gateway", now, true, user)
		case "unavailable", "failed", "throttled", "request_rejected":
			r.add("gateway", now, false, user)
		case "captcha_required", "captcha_invalid":
			// A challenge signal — pollution evidence, not (yet) a failure.
			r.recordCaptchaSignalLocked(now)
			if o.Outcome == "captcha_invalid" {
				r.add("gateway", now, false, user)
			}
		}
		// bad_credentials / non_ascii: the user's fault — no sample.
	case events.KindIdentityAuth:
		switch o.Outcome {
		case "success":
			r.add("uaap", now, true, user)
		case "unavailable", "failed":
			r.add("uaap", now, false, user)
		}
	case events.KindReconnect:
		switch o.Outcome {
		case "success":
			r.counters.recoverOK++
			r.counters.recoveryLatencyMS += o.DurationMS
			r.add("gateway", now, true, user)
			r.add("uaap", now, true, user)
		case "unavailable":
			r.counters.recoverDeferred++
			r.add("gateway", now, false, user)
		case "failed":
			r.counters.recoverFailed++
			r.add("gateway", now, false, user)
		}
	case events.KindSessionExpiry:
		r.counters.sessionExpiries++
	case events.KindServiceTicket:
		// The JWC callback is a real (best-effort) jwc dependency probe.
		r.add("jwc", now, o.Outcome == "success", user)
	case events.KindOperation:
		service, _, ok := strings.Cut(o.Operation, ".")
		if !ok || !contains(services, service) {
			break
		}
		switch {
		case o.Outcome == "success":
			r.add(service, now, true, user)
		case userFault[o.Outcome]:
			// The school is not at fault.
		default:
			r.add(service, now, false, user)
			if strings.HasSuffix(o.Outcome, "parse_error") {
				r.counters.parseErrors[o.Operation]++
			}
		}
	}
}

func contains(list []string, v string) bool {
	for _, s := range list {
		if s == v {
			return true
		}
	}
	return false
}

func (r *Registry) add(name string, at time.Time, ok bool, user string) {
	l, exist := r.layers[name]
	if !exist {
		l = &layer{name: name, state: StateUnknown, lastKnown: StateUnknown}
		r.layers[name] = l
	}
	l.samples = append(l.samples, sample{at: at, ok: ok, user: user})
	cut := at.Add(-Window)
	i := 0
	for i < len(l.samples) && l.samples[i].at.Before(cut) {
		i++
	}
	l.samples = l.samples[min(i, len(l.samples)):]
	if ok {
		l.streakOK++
	} else {
		l.streakOK = 0
	}
	r.bump(at, ok, name)
	r.evaluate(l, at)
}

// recordCaptchaSignal appends one pollution signal to the capped
// timeline. Safe from any goroutine; the Locked variant requires r.mu.
func (r *Registry) recordCaptchaSignal(at time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recordCaptchaSignalLocked(at)
}

func (r *Registry) recordCaptchaSignalLocked(at time.Time) {
	r.counters.captchaRejections++
	r.captcha = append(r.captcha, at)
	if len(r.captcha) > captchaTimeline {
		r.captcha = r.captcha[len(r.captcha)-captchaTimeline:]
	}
}

func (r *Registry) bump(at time.Time, ok bool, layerName string) {
	b := &r.buckets[(at.Unix()/60)%60]
	m := at.Truncate(time.Minute)
	if !b.minute.Equal(m) {
		b.minute = m
		b.ok = map[string]int{}
		b.fail = map[string]int{}
	}
	if ok {
		b.ok[layerName]++
	} else {
		b.fail[layerName]++
	}
}

// evaluate applies §9: min samples + min accounts + hysteresis + decay.
func (r *Registry) evaluate(l *layer, now time.Time) {
	if until, ok := r.maintenance[l.name]; ok {
		if now.Before(until) {
			l.state = StateMaintenance
			return
		}
		delete(r.maintenance, l.name)
		if l.state == StateMaintenance {
			// The override expired: fresh judgement required.
			l.state = StateUnknown
		}
	}

	window := Window
	if l.name == "gateway" || l.name == "uaap" {
		window = GatewayWindow
	}
	recent := l.samples[:0]
	for _, s := range l.samples {
		if now.Sub(s.at) <= window {
			recent = append(recent, s)
		}
	}
	l.samples = recent

	if l.name != "gateway" && l.name != "uaap" && r.l0Degraded() {
		l.state = StateBlockedByGateway
		return
	}

	switch {
	case len(l.samples) == 0 && now.Sub(l.lastAt) > IdleDecay:
		l.state = StateUnknown
	case len(l.samples) < MinSamples || r.distinctUsers(l) < MinAccounts:
		// Too little evidence: keep the current state (unknown at boot).
	case r.failRate(l) >= FailRate:
		l.state = StateDegraded
		l.lastKnown = StateDegraded
		l.lastAt = now
	case l.state == StateDegraded && l.streakOK < RecoverStreak:
		// Hysteresis: stay degraded until a streak of successes.
	default:
		l.state = StateHealthy
		l.lastKnown = StateHealthy
		l.lastAt = now
	}
}

func (r *Registry) l0Degraded() bool {
	for _, name := range []string{"gateway", "uaap"} {
		if l, ok := r.layers[name]; ok && l.state == StateDegraded {
			return true
		}
	}
	return false
}

func (r *Registry) failRate(l *layer) float64 {
	fails := 0
	for _, s := range l.samples {
		if !s.ok {
			fails++
		}
	}
	return float64(fails) / float64(len(l.samples))
}

func (r *Registry) distinctUsers(l *layer) int {
	users := map[string]bool{}
	for _, s := range l.samples {
		users[s.user] = true
	}
	return len(users)
}

// LayerStatus is the public/admin view of one layer.
type LayerStatus struct {
	State         string  `json:"state"`
	LastKnown     string  `json:"last_known,omitempty"`
	LastKnownAgeS int     `json:"last_known_age_s,omitempty"`
	Samples10m    int     `json:"samples_10m"`
	FailRate10m   float64 `json:"fail_rate_10m,omitempty"`
	DistinctUsers int     `json:"distinct_users_10m"`
}

// MinuteView is one minute of the public availability history (§9):
// every logical operation is one sample; probe-free, credentials-free.
type MinuteView struct {
	At   time.Time `json:"minute"`
	Ops  int       `json:"ops"`
	Fail int       `json:"fails,omitempty"`
}

// PublicStatus is the no-credentials view (GET /v1/public/status).
type PublicStatus struct {
	AsOf    time.Time              `json:"as_of"`
	Layers  map[string]LayerStatus `json:"layers"`
	Gateway *ProbeStatus           `json:"gateway_probe,omitempty"`
	History []MinuteView           `json:"history,omitempty"`
}

// Public renders the public view.
func (r *Registry) Public() PublicStatus {
	full := r.Status()
	out := PublicStatus{AsOf: full.AsOf, Layers: full.Layers}
	if p := r.LatestProbe(); p.Result != "" {
		out.Gateway = &p
	}
	now := r.now()
	for i := 0; i < len(r.buckets); i++ {
		b := &r.buckets[(now.Unix()/60+int64(60-i))%60] // newest first
		if b.minute.IsZero() {
			continue
		}
		ops, fails := 0, 0
		for _, n := range b.ok {
			ops += n
		}
		for _, n := range b.fail {
			ops += n
			fails += n
		}
		if ops == 0 {
			continue
		}
		out.History = append(out.History, MinuteView{At: b.minute, Ops: ops, Fail: fails})
	}
	return out
}

// CountersView aggregates the §10 counters.
type CountersView struct {
	CaptchaRejections int64       `json:"captcha_rejections"`
	CaptchaTimeline   []time.Time `json:"captcha_timeline,omitempty"`
	SessionExpiries   int64       `json:"session_expiries"`
	Recoveries        struct {
		OK           int64 `json:"ok"`
		Deferred     int64 `json:"deferred"`
		Failed       int64 `json:"failed"`
		AvgLatencyMS int64 `json:"avg_latency_ms"`
	} `json:"recoveries"`
	ParseErrors map[string]int64 `json:"parse_errors,omitempty"`
}

// FullStatus is the admin view: layers plus counters and the pollution
// timeline.
type FullStatus struct {
	AsOf     time.Time              `json:"as_of"`
	Layers   map[string]LayerStatus `json:"layers"`
	Counters CountersView           `json:"counters"`
}

// Status renders the full (admin) view.
func (r *Registry) Status() FullStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	// Evaluate everything so decay/blocked states are fresh even without
	// incoming traffic.
	for _, l := range r.layers {
		r.evaluate(l, now)
	}
	out := FullStatus{AsOf: now, Layers: map[string]LayerStatus{}}
	for name, l := range r.layers {
		st := LayerStatus{State: l.state, LastKnown: l.lastKnown}
		if !l.lastAt.IsZero() {
			st.LastKnownAgeS = int(now.Sub(l.lastAt).Seconds())
		}
		st.Samples10m = len(l.samples)
		st.DistinctUsers = r.distinctUsers(l)
		if st.Samples10m > 0 {
			st.FailRate10m = r.failRate(l)
		}
		out.Layers[name] = st
	}
	out.Counters = CountersView{
		CaptchaRejections: r.counters.captchaRejections,
		CaptchaTimeline:   append([]time.Time(nil), r.captcha...),
		SessionExpiries:   r.counters.sessionExpiries,
		ParseErrors:       r.counters.parseErrors,
	}
	out.Counters.Recoveries.OK = r.counters.recoverOK
	out.Counters.Recoveries.Deferred = r.counters.recoverDeferred
	out.Counters.Recoveries.Failed = r.counters.recoverFailed
	if n := r.counters.recoverOK; n > 0 {
		out.Counters.Recoveries.AvgLatencyMS = r.counters.recoveryLatencyMS / n
	}
	return out
}

var _ events.Sink = (*boundSink)(nil)
