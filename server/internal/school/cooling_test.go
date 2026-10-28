package school

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/session"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// TestCoolingTiersAndSweep pins §9.1: hot/warm/cold/dormant tiers, cold
// eviction (snapshot + local logout + memory drop) and dormant cleanup.
func TestCoolingTiersAndSweep(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{}}
	// A non-nil transport factory keeps snapshot restores constructible;
	// the tests never touch the network.
	factory := func() transport.Transport { return nil }
	m := NewManager(factory, WithClock(func() time.Time { return now }), WithColdAfter(30*time.Minute), WithSnapshotStore(st))
	ctx := context.Background()

	m.mu.Lock() // not needed for register (it locks internally) — plain calls below
	m.mu.Unlock()
	sess := sessionNoNet()
	sess.Jar().Set("TWFID", "tw1", ".vpn2.aufe.edu.cn", "/")
	m.register("u1", "d1", "web", sess)
	m.AttachDevice("u1", "dev-1", "d1", sess)
	if got := m.TierOf("u1"); got != "hot" {
		t.Fatalf("fresh tier = %q, want hot", got)
	}
	now = now.Add(6 * time.Minute)
	if got := m.TierOf("u1"); got != "warm" {
		t.Fatalf("tier = %q, want warm", got)
	}
	now = now.Add(25 * time.Minute) // 31m idle
	if got := m.TierOf("u1"); got != "cold" {
		t.Fatalf("tier = %q, want cold", got)
	}

	if n := m.SweepCold(ctx); n != 1 {
		t.Fatalf("sweep evicted %d, want 1", n)
	}
	if len(st.snaps) != 1 || st.snaps["u1"].DeviceID != "dev-1" || len(st.snaps["u1"].Cookies) == 0 {
		t.Fatalf("snapshot not saved correctly: %+v", st.snaps)
	}
	if cs := m.CoolingStats(); cs.Evictions != 1 || cs.SnapshotsSaved != 1 || cs.Cold != 1 {
		t.Fatalf("cooling stats = %+v", cs)
	}
	// The next read transparently restores the snapshot — no login.
	s, err := m.Session("u1")
	if err != nil || s == nil {
		t.Fatalf("Session after eviction: %v (restore expected)", err)
	}
	if cs := m.CoolingStats(); cs.Restores != 1 {
		t.Fatalf("restores = %d, want 1", cs.Restores)
	}

	// Dormancy: past 7 days the snapshot is deleted on the next sweep.
	now = now.Add(8 * 24 * time.Hour)
	m.SweepCold(ctx)
	if _, ok := st.snaps["u1"]; ok {
		t.Fatal("dormant snapshot not deleted")
	}
}

// TestSnapshotRestoreOnSessionFor pins the transparent path: a group miss
// falls back to the sealed snapshot and re-binds the entry.
func TestSnapshotRestoreOnSessionFor(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	st := &fakeSnapshotStore{
		snaps: map[string]UserSnapshot{},
	}
	m := NewManager(func() transport.Transport { return nil }, WithClock(func() time.Time { return now }), WithSnapshotStore(st))
	st.snaps["u9"] = UserSnapshot{UserID: "u9", DeviceID: "d9", SavedAt: now, Cookies: nil}

	// An empty snapshot restores nothing → ErrNoSession (vault relogin
	// path takes over at the API layer).
	if _, err := m.SessionFor(context.Background(), "u9", ""); err != ErrNoSession {
		t.Fatalf("empty snapshot: %v, want ErrNoSession", err)
	}
	if cs := m.CoolingStats(); cs.RestoreMisses != 1 {
		t.Fatalf("restore misses = %d, want 1", cs.RestoreMisses)
	}
	_ = st
}

// fakeSnapshotStore is an in-memory SnapshotStore.
type fakeSnapshotStore struct {
	snaps map[string]UserSnapshot
}

// sessionNoNet builds a session that never touches the network (cooling
// tests only exercise jar/state lifecycle).
func sessionNoNet() *session.Session {
	return session.New("20000003", "", "", nil)
}

func (f *fakeSnapshotStore) SaveUserSnapshot(_ context.Context, snap UserSnapshot) error {
	f.snaps[snap.UserID] = snap
	return nil
}

func (f *fakeSnapshotStore) DeleteUserSnapshot(_ context.Context, userID string) error {
	delete(f.snaps, userID)
	return nil
}

func (f *fakeSnapshotStore) LoadUserSnapshot(_ context.Context, userID string) (*UserSnapshot, error) {
	s, ok := f.snaps[userID]
	if !ok {
		return nil, ErrNoSession
	}
	return &s, nil
}
