package retry

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
)

func TestRetriesThenSucceeds(t *testing.T) {
	attempts := 0
	policy := Policy{InitialDelayMS: 1, Factor: 2, MaxDelayMS: 5, MaxAttempts: 3}
	value, err := With(context.Background(), policy, func(ctx context.Context) (int, error) {
		attempts++
		if attempts < 3 {
			return 0, errs.NewTimeout(1)
		}
		return 42, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if value != 42 || attempts != 3 {
		t.Fatalf("value=%d attempts=%d", value, attempts)
	}
}

func TestDoesNotRetryNonRetryable(t *testing.T) {
	attempts := 0
	_, err := With(context.Background(), Attempts(3), func(ctx context.Context) (int, error) {
		attempts++
		return 0, errs.NewInvalidCredentials()
	})
	if !errors.Is(err, err) || attempts != 1 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	var e *errs.Error
	if !asCore(err, &e) || e.Code != errs.InvalidCredentials {
		t.Fatalf("err = %v", err)
	}
}

func TestCancellationShortCircuits(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := With(ctx, Attempts(3), func(ctx context.Context) (int, error) {
		return 0, nil
	})
	var e *errs.Error
	if !asCore(err, &e) || e.Code != errs.Cancelled {
		t.Fatalf("err = %v", err)
	}
}

func TestDelaysGrowAndCap(t *testing.T) {
	p := DefaultPolicy()
	// attempt N means "delay applied after failure N", i.e. before attempt N+1.
	cases := map[int]time.Duration{
		1: 1 * time.Second,
		2: 2 * time.Second,
		3: 4 * time.Second,
		9: 15 * time.Second, // capped
	}
	for attempt, want := range cases {
		if got := p.DelayFor(attempt); got != want {
			t.Errorf("DelayFor(%d) = %v, want %v", attempt, got, want)
		}
	}
}
