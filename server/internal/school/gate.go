package school

import (
	"context"
	"math/rand"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"golang.org/x/time/rate"
)

// The global EC login gate (DESIGN §7.1, §7.3 step 7): every full login
// (fresh, vault relogin, CAPTCHA solve) queues on one limiter instead of
// stampeding. With the proxy pool each login rides a DIFFERENT exit IP
// (round-robin over 100 ports), so the per-IP login rate — the number the
// captcha ladder actually reacts to — is the global rate divided by the
// live ports; the global gate only guards against a stampede hitting one
// port before rotation spreads the load.
const (
	// DefaultLoginRatePerMinute caps gateway logins per minute globally.
	// 60/min ≈ 0.6/min per port at 100 ports — per-IP far below anything
	// the gateway has ever reacted to, while a burst of 60 spreads across
	// ~60 different exits.
	DefaultLoginRatePerMinute = 60
	// DefaultLoginJitter spreads queued logins in time.
	DefaultLoginJitter = 750 * time.Millisecond
)

// WithLoginGate replaces the global EC login gate (tuning, tests).
// A nil limiter disables the gate.
func WithLoginGate(l *rate.Limiter, jitter time.Duration) Option {
	return func(m *Manager) {
		m.loginGate, m.loginJitter = l, jitter
	}
}

// WithoutLoginGate disables the gate. Tests that drive many full logins
// use this; production keeps the §7.1 default.
func WithoutLoginGate() Option { return WithLoginGate(nil, 0) }

// WithWritesEnabled sets whether sessions created by the manager permit
// write operations. The server default is true (the API layer enforces
// authentication + idempotency); the flag exists for parity with the
// CLI's --allow-write posture in tests.
func WithWritesEnabled(v bool) Option {
	return func(m *Manager) { m.writesAllowed = v }
}

// gateEC blocks until the limiter admits one gateway login, then spreads
// the arrival with jitter. ctx-aware: a cancelled caller leaves the queue.
// The wait outcome is surfaced with retryable semantics: a queue deadline
// (the server-side cap) is temporarily-unavailable (503 school.busy — the
// client retries and gets in as the queue drains), never a bare 500; only
// a caller-initiated cancellation propagates as cancelled.
func (m *Manager) gateEC(ctx context.Context) error {
	if m.loginGate == nil {
		return nil
	}
	if err := m.loginGate.Wait(ctx); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return errs.NewTemporarilyUnavailable("登录排队超时，请稍后重试")
		}
		return errs.NewCancelled()
	}
	if m.loginJitter > 0 {
		timer := time.NewTimer(time.Duration(rand.Int63n(int64(m.loginJitter))))
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			if ctx.Err() == context.DeadlineExceeded {
				return errs.NewTemporarilyUnavailable("登录排队超时，请稍后重试")
			}
			return errs.NewCancelled()
		}
	}
	return nil
}
