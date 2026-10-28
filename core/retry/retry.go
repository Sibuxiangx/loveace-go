// Package retry implements bounded retry with exponential backoff.
//
// Mirrors RetryHandler.retry: the first attempt is immediate, then delays of
// `initial * factor^(attempt-1)` seconds. Only errors classified as retryable
// are retried; authentication, interaction and outcome_unknown errors surface
// immediately.
package retry

import (
	"context"
	"math"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
)

// Policy is the retry configuration.
type Policy struct {
	// MaxAttempts is the total attempt count including the first.
	MaxAttempts int
	// InitialDelayMS is the delay before the second attempt.
	InitialDelayMS int64
	// Factor is the exponential growth factor.
	Factor float64
	// MaxDelayMS caps the backoff delay.
	MaxDelayMS int64
}

// DefaultPolicy mirrors the Rust RetryPolicy::default.
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:    3,
		InitialDelayMS: 1_000,
		Factor:         2.0,
		MaxDelayMS:     15_000,
	}
}

// Attempts returns a policy with a specific attempt count and default delays.
func Attempts(max int) Policy {
	p := DefaultPolicy()
	if max < 1 {
		max = 1
	}
	p.MaxAttempts = max
	return p
}

// Once returns a policy that performs a single attempt (used for writes).
func Once() Policy { return Policy{MaxAttempts: 1, InitialDelayMS: 1, Factor: 2, MaxDelayMS: 1} }

// DelayFor returns the backoff delay before the given attempt (1-based).
func (p Policy) DelayFor(attempt int) time.Duration {
	exp := attempt - 1
	if exp < 0 {
		exp = 0
	}
	raw := float64(p.InitialDelayMS) * math.Pow(p.Factor, float64(exp))
	if raw > float64(p.MaxDelayMS) {
		raw = float64(p.MaxDelayMS)
	}
	if raw < 0 {
		raw = 0
	}
	return time.Duration(int64(raw)) * time.Millisecond
}

// Operation is one attempt of the retried work.
type Operation[T any] func(ctx context.Context) (T, error)

// With runs operation under the policy, honouring ctx cancellation.
func With[T any](ctx context.Context, policy Policy, op Operation[T]) (T, error) {
	var zero T
	attempt := 0
	for {
		if err := ctx.Err(); err != nil {
			return zero, errs.NewCancelled()
		}
		attempt++
		value, err := op(ctx)
		if err == nil {
			return value, nil
		}
		var core *errs.Error
		if !asCore(err, &core) || attempt >= policy.MaxAttempts || !core.Retryable() {
			return zero, err
		}
		delay := policy.DelayFor(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, errs.NewCancelled()
		case <-timer.C:
		}
	}
}

func asCore(err error, target **errs.Error) bool {
	e, ok := err.(*errs.Error)
	if ok {
		*target = e
		return true
	}
	return false
}
