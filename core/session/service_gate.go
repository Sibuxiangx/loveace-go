package session

import (
	"sync"
	"time"
)

// ServiceGate serialises one account's handshake with a school service that
// needs its own session (the portal's hpdata call), and remembers the VPN
// cooldown that follows a security intercept. Gates live only in memory: a
// restored session starts with no cooldown.
type ServiceGate struct {
	mu sync.Mutex

	cooldownMu    sync.Mutex
	cooldownUntil time.Time
}

// Lock serialises the handshake, so concurrent first callers share one attempt.
func (g *ServiceGate) Lock() { g.mu.Lock() }

// Unlock releases the handshake.
func (g *ServiceGate) Unlock() { g.mu.Unlock() }

// CooldownUntil reports the instant before which the handshake must not be
// retried. The zero time means no cooldown.
func (g *ServiceGate) CooldownUntil() time.Time {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	return g.cooldownUntil
}

// SetCooldown blocks the handshake until the given instant.
func (g *ServiceGate) SetCooldown(until time.Time) {
	g.cooldownMu.Lock()
	defer g.cooldownMu.Unlock()
	g.cooldownUntil = until
}

// ServiceGate returns the gate for one service name, creating it on first use.
func (s *Session) ServiceGate(service string) *ServiceGate {
	s.gatesMu.Lock()
	defer s.gatesMu.Unlock()
	if s.gates == nil {
		s.gates = map[string]*ServiceGate{}
	}
	gate, ok := s.gates[service]
	if !ok {
		gate = &ServiceGate{}
		s.gates[service] = gate
	}
	return gate
}
