package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// pinSWRRefresh waits briefly for the detached revalidation to land.
func pinSWRRefresh(t *testing.T, want int64, calls *atomic.Int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() < want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != want {
		t.Fatalf("fetch ran %d times, want %d", calls.Load(), want)
	}
}

// TestGetOrLoadSWRServesStaleAndRefreshes pins the cold-open path: an
// expired value paints immediately and the refresh lands in the
// background.
func TestGetOrLoadSWRServesStaleAndRefreshes(t *testing.T) {
	c := New()
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }

	var calls atomic.Int64
	fetch := func(context.Context) (any, error) {
		n := calls.Add(1)
		if n == 1 {
			return "v1", nil
		}
		return "v2", nil
	}

	// Cold key: nothing to serve, the load blocks.
	e, err := c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch)
	if err != nil || e.Source != "live" || e.Value != "v1" {
		t.Fatalf("cold = %+v err=%v", e, err)
	}

	// TTL expired: served immediately from cache while revalidating.
	now = now.Add(2 * time.Second)
	e, err = c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch)
	if err != nil || e.Source != "cache" || !e.Revalidating || e.Stale {
		t.Fatalf("stale-serve = %+v err=%v", e, err)
	}
	if e.Value != "v1" {
		t.Fatalf("stale-serve value = %v, want v1", e.Value)
	}
	pinSWRRefresh(t, 2, &calls)

	// The refreshed value is now fresh.
	e, err = c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch)
	if err != nil || e.Source != "cache" || e.Revalidating {
		t.Fatalf("after revalidate = %+v err=%v", e, err)
	}
	if e.Value != "v2" {
		t.Fatalf("revalidated value = %v, want v2", e.Value)
	}
}

// TestGetOrLoadSWRRefreshFailureKeepsValue: a failing background refresh
// never surfaces an error and never drops the last good value.
func TestGetOrLoadSWRRefreshFailureKeepsValue(t *testing.T) {
	c := New()
	now := time.Unix(1_800_000_000, 0)
	c.now = func() time.Time { return now }

	var calls atomic.Int64
	fetch := func(context.Context) (any, error) {
		if calls.Add(1) == 1 {
			return "good", nil
		}
		return nil, errors.New("school down")
	}

	if _, err := c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Second)
	e, err := c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch)
	if err != nil || e.Value != "good" || !e.Revalidating {
		t.Fatalf("stale serve = %+v err=%v", e, err)
	}
	pinSWRRefresh(t, 2, &calls)

	e, err = c.GetOrLoadSWR(context.Background(), "k", time.Second, fetch)
	if err != nil || e.Value != "good" {
		t.Fatalf("after failed refresh = %+v err=%v", e, err)
	}
}
