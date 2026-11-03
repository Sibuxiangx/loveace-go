package pg

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// TestDeviceGrant covers the device authorization grant table (migration 0008)
// on real Postgres. Skipped unless LOVEACE_TEST_PG holds a postgres URL.
func TestDeviceGrant(t *testing.T) {
	url := os.Getenv("LOVEACE_TEST_PG")
	if url == "" {
		t.Skip("LOVEACE_TEST_PG not set; pg integration test skipped")
	}
	ctx := context.Background()
	s, err := New(ctx, url)
	if err != nil {
		t.Fatalf("connect+migrate: %v", err)
	}
	defer s.Close()

	// Re-runnable: unique hashes per run.
	now := time.Now().UTC().Truncate(time.Microsecond)
	g := store.DeviceGrant{
		ID:             store.NewID(),
		DeviceCodeHash: "dch-" + store.NewID(),
		UserCodeHash:   "uch-" + store.NewID(),
		ClientName:     "client",
		Platform:       "ios",
		Status:         store.DeviceGrantPending,
		Interval:       5,
		ExpiresAt:      now.Add(10 * time.Minute),
		CreatedAt:      now,
	}
	if err := s.CreateDeviceGrant(ctx, g); err != nil {
		t.Fatalf("create: %v", err)
	}
	for name, get := range map[string]func() (*store.DeviceGrant, error){
		"id":          func() (*store.DeviceGrant, error) { return s.GetDeviceGrant(ctx, g.ID) },
		"device code": func() (*store.DeviceGrant, error) { return s.GetDeviceGrantByDeviceCode(ctx, g.DeviceCodeHash) },
		"user code":   func() (*store.DeviceGrant, error) { return s.GetDeviceGrantByUserCode(ctx, g.UserCodeHash) },
	} {
		got, err := get()
		if err != nil || got.ID != g.ID || got.Status != store.DeviceGrantPending ||
			got.Interval != 5 || !got.LastPolledAt.IsZero() || !got.ExpiresAt.Equal(g.ExpiresAt) {
			t.Fatalf("get by %s: %+v err=%v", name, got, err)
		}
	}
	if _, err := s.GetDeviceGrant(ctx, store.NewID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("missing grant: want ErrNotFound, got %v", err)
	}

	// Approve only while pending and unexpired.
	if ok, err := s.ApproveDeviceGrant(ctx, g.ID, "user-1", now.Add(20*time.Minute)); err != nil || ok {
		t.Fatalf("approve after expiry: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ApproveDeviceGrant(ctx, g.ID, "user-1", now); err != nil || !ok {
		t.Fatalf("approve pending: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ApproveDeviceGrant(ctx, g.ID, "user-2", now); err != nil || ok {
		t.Fatalf("approve twice: ok=%v err=%v", ok, err)
	}
	approved, err := s.GetDeviceGrant(ctx, g.ID)
	if err != nil || approved.Status != store.DeviceGrantApproved || approved.UserID != "user-1" {
		t.Fatalf("approved state: %+v err=%v", approved, err)
	}

	// Consume only once.
	if ok, err := s.ConsumeDeviceGrant(ctx, g.ID); err != nil || !ok {
		t.Fatalf("consume: ok=%v err=%v", ok, err)
	}
	if ok, err := s.ConsumeDeviceGrant(ctx, g.ID); err != nil || ok {
		t.Fatalf("consume twice: ok=%v err=%v", ok, err)
	}

	// Poll record and attempt counter.
	pollAt := now.Add(time.Second)
	if err := s.RecordDeviceGrantPoll(ctx, g.ID, pollAt, 10); err != nil {
		t.Fatalf("record poll: %v", err)
	}
	polled, err := s.GetDeviceGrant(ctx, g.ID)
	if err != nil || !polled.LastPolledAt.Equal(pollAt) || polled.Interval != 10 {
		t.Fatalf("poll state: %+v err=%v", polled, err)
	}
	if err := s.RecordDeviceGrantPoll(ctx, store.NewID(), pollAt, 10); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("poll missing: want ErrNotFound, got %v", err)
	}
	for want := 1; want <= 2; want++ {
		n, err := s.IncrementDeviceGrantAttempts(ctx, g.ID)
		if err != nil || n != want {
			t.Fatalf("attempts: got %d err=%v, want %d", n, err, want)
		}
	}
	if _, err := s.IncrementDeviceGrantAttempts(ctx, store.NewID()); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("attempts missing: want ErrNotFound, got %v", err)
	}

	// Purge removes expired grants only.
	expired := store.DeviceGrant{
		ID:             store.NewID(),
		DeviceCodeHash: "dch-" + store.NewID(),
		UserCodeHash:   "uch-" + store.NewID(),
		Status:         store.DeviceGrantPending,
		Interval:       5,
		ExpiresAt:      now.Add(-time.Minute),
		CreatedAt:      now.Add(-time.Hour),
	}
	if err := s.CreateDeviceGrant(ctx, expired); err != nil {
		t.Fatalf("create expired: %v", err)
	}
	if err := s.PurgeDeviceGrants(ctx, now); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, err := s.GetDeviceGrant(ctx, expired.ID); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("expired grant after purge: want ErrNotFound, got %v", err)
	}
	if _, err := s.GetDeviceGrant(ctx, g.ID); err != nil {
		t.Fatalf("live grant after purge: %v", err)
	}
}
