package school

import (
	"time"
)

// The account cooling ladder (DESIGN §5.2): repeated invalid-credential
// failures cool the account down before the next gateway attempt — the
// failure budget on the school side is a shared, finite resource, and a
// credential-stuffing loop must not burn it for everyone.
var ladderSteps = []time.Duration{
	0, // first failure: no cooldown
	30 * time.Second,
	2 * time.Minute,
	10 * time.Minute,
	30 * time.Minute, // fifth and beyond
}

// AccountCoolingError is returned when a login arrives while the account
// is cooling down from invalid-credential failures.
type AccountCoolingError struct {
	RetryAfter time.Duration
}

func (e *AccountCoolingError) Error() string {
	return "school: account cooling for " + e.RetryAfter.Truncate(time.Second).String()
}

type failLedger struct {
	count int
	until time.Time
}

// accountCooling reports the remaining cooldown for an account (0 = none).
func (m *Manager) accountCooling(account string) time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.ladders[account]
	if !ok {
		return 0
	}
	if remain := l.until.Sub(m.now()); remain > 0 {
		return remain
	}
	return 0
}

// noteAuthFailure records an invalid-credential failure and applies the
// next ladder step.
func (m *Manager) noteAuthFailure(account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l := m.ladders[account]
	if l == nil {
		l = &failLedger{}
		m.ladders[account] = l
	}
	l.count++
	// Failure N applies step N-1 (the first mistake cools nothing).
	step := ladderSteps[len(ladderSteps)-1]
	if l.count-1 < len(ladderSteps) {
		step = ladderSteps[l.count-1]
	}
	l.until = m.now().Add(step)
}

// clearAuthFailures resets the ladder after a successful login.
func (m *Manager) clearAuthFailures(account string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.ladders, account)
}
