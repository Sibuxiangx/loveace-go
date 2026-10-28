// Package events defines typed observations for availability telemetry.
//
// The core emits Observations to an optional Sink. Telemetry must never delay
// or fail login: implementations are expected to enqueue and return
// immediately. A separate analytics uploader keeps its own transport so
// analytics faults cannot recursively trigger auth recovery.
package events

import "sync"

// Kind is the logical phase of an observation.
type Kind string

// Observation kinds. Spellings match the Rust serde snake_case rename.
const (
	KindGatewayAuth         Kind = "gateway_auth"
	KindIdentityAuth        Kind = "identity_auth"
	KindServiceTicket       Kind = "service_ticket"
	KindFirstBusinessReques Kind = "first_business_request"
	KindReconnect           Kind = "reconnect"
	KindSessionExpiry       Kind = "session_expiry"
	KindLogin               Kind = "login"
	KindLogout              Kind = "logout"
	KindOperation           Kind = "operation"
	KindCacheHit            Kind = "cache_hit"
	KindCancellation        Kind = "cancellation"
	KindDebugFault          Kind = "debug_fault"
)

// Observation is a single telemetry observation. It contains no credentials,
// grades, schedules, balances or raw URLs.
type Observation struct {
	Kind       Kind   `json:"kind"`
	Operation  string `json:"operation"`
	Outcome    string `json:"outcome"`
	DurationMS int64  `json:"duration_ms"`
	Generation uint64 `json:"generation"`
	Synthetic  bool   `json:"synthetic"`
}

// Sink receives observations. Record must not block or panic.
type Sink interface {
	Record(observation Observation)
}

// Multi fans one observation out to several sinks (in order; a panic in
// one consumer must not starve the others).
type Multi []Sink

// Record implements Sink.
func (m Multi) Record(o Observation) {
	for _, s := range m {
		func() {
			defer func() { _ = recover() }()
			s.Record(o)
		}()
	}
}

// Null is a sink that discards everything (default / tests).
type Null struct{}

// Record implements Sink.
func (Null) Record(Observation) {}

// Memory collects observations in memory (tests/diagnostics).
type Memory struct {
	mu   sync.Mutex
	list []Observation
}

// NewMemory creates an empty sink.
func NewMemory() *Memory { return &Memory{} }

// Record implements Sink.
func (m *Memory) Record(o Observation) {
	m.mu.Lock()
	m.list = append(m.list, o)
	m.mu.Unlock()
}

// Snapshot returns the recorded observations.
func (m *Memory) Snapshot() []Observation {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Observation, len(m.list))
	copy(out, m.list)
	return out
}

// Async wraps a Sink with a bounded channel and a single dispatcher goroutine
// so hot paths never block on slow consumers. When the buffer is full the
// observation is dropped (telemetry must never delay or fail login).
type Async struct {
	ch     chan Observation
	inner  Sink
	done   chan struct{}
	closed sync.Once
}

// NewAsync starts a dispatcher goroutine draining into inner.
func NewAsync(inner Sink, buffer int) *Async {
	if buffer <= 0 {
		buffer = 256
	}
	a := &Async{ch: make(chan Observation, buffer), inner: inner, done: make(chan struct{})}
	go func() {
		defer close(a.done)
		for o := range a.ch {
			a.inner.Record(o)
		}
	}()
	return a
}

// Record implements Sink. Never blocks; drops when the buffer is full or
// after Close (send on a closed channel is avoided via a recover guard for
// the racy Close-during-Record case).
func (a *Async) Record(o Observation) {
	defer func() { _ = recover() }()
	select {
	case a.ch <- o:
	default:
	}
}

// Close drains and stops the dispatcher goroutine.
func (a *Async) Close() {
	a.closed.Do(func() {
		close(a.ch)
		<-a.done
	})
}

// AvailabilityCategory classifies outcomes for the future analyst backend.
type AvailabilityCategory string

// Availability categories.
const (
	CatServiceError  AvailabilityCategory = "service_error"
	CatTimeout       AvailabilityCategory = "timeout"
	CatClientOffline AvailabilityCategory = "client_offline"
	CatTlsError      AvailabilityCategory = "tls_error"
	CatBadCreds      AvailabilityCategory = "bad_credentials"
	CatRateLimited   AvailabilityCategory = "rate_limited"
	CatInteraction   AvailabilityCategory = "interaction_required"
	CatRecovered     AvailabilityCategory = "session_expiry_recovered"
	CatCancelled     AvailabilityCategory = "cancellation"
	CatParserDrift   AvailabilityCategory = "parser_api_change"
	CatOther         AvailabilityCategory = "other"
)
