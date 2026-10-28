package school

// 2026-10-17 latency round (operator feedback: "cold logins are slow"),
// narrowed by M4 (the borrow-probe and its validation stamp are retired —
// E-D 2026-10-21 proved a wrong UAAP password never touches the EC quota,
// so pre-validation bought nothing but borrowed-host risk).
//
// The lever pinned here: the aac/labor CAS tickets are effectively STATIC —
// the operator saw a single manual rotation across years, and only after
// reporting the flaw. They persist with the session snapshot and are
// re-seeded into every new session (full login AND snapshot restore), so
// the 4-hop mint walk is a once-ever cost per user instead of a
// per-session one: with TWFID + ticket the business APIs answer directly.

import (
	"context"
	"testing"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/services/aac"
	"github.com/Sibuxiangx/loveace-go/core/transport"
)

// TestLoginSeedsPersistedTickets: a full login for a user whose snapshot
// carries tickets serves them WITHOUT walking the CAS mint chain.
func TestLoginSeedsPersistedTickets(t *testing.T) {
	gw := newChaosGateway(t)
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{"u1": {
		UserID:  "u1",
		Tickets: map[string]string{"aac": "ST-AAC-FIXED", "labor": "ST-LABOR-FIXED"},
		SavedAt: time.Now(),
	}}}
	m := NewManager(func() transport.Transport { return gw },
		WithSnapshotStore(st), WithoutLoginGate())

	sess, err := m.Login(context.Background(), "u1", "20000003", "Passw0rd!123", "", "dev", "test")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	want := map[string]string{"aac": "ST-AAC-FIXED", "labor": "ST-LABOR-FIXED"}
	for svc, w := range want {
		if got, ok := sess.CachedTicket(svc); !ok || got != w {
			t.Fatalf("%s ticket = %q ok=%v, want %q", svc, got, ok, w)
		}
	}
	// The aac service must answer from the cache: no mint walk at all.
	if ticket, err := aac.New(sess).Ticket(context.Background(), false); err != nil || ticket != "ST-AAC-FIXED" {
		t.Fatalf("aac ticket via service = %q err=%v", ticket, err)
	}
	if mints := gw.aacMintsTotal(); mints != 0 {
		t.Fatalf("aac mint walk ran %d times, want 0", mints)
	}
}

// TestSnapshotRestoreSeedsTickets: a cold session restored from its sealed
// snapshot also skips the mint walk.
func TestSnapshotRestoreSeedsTickets(t *testing.T) {
	gw := newChaosGateway(t)
	snap := snapshotWithCookies()
	snap.Tickets = map[string]string{"aac": "ST-AAC-FIXED"}
	st := &fakeSnapshotStore{snaps: map[string]UserSnapshot{"u1": snap}}
	m := NewManager(func() transport.Transport { return gw },
		WithSnapshotStore(st),
		WithCredentialResolver(func(context.Context, string) (string, string, string, error) {
			return "20000003", "gw", "school-pw", nil
		}))

	sess, err := m.SessionFor(context.Background(), "u1", "d1")
	if err != nil {
		t.Fatalf("snapshot restore: %v", err)
	}
	if got, ok := sess.CachedTicket("aac"); !ok || got != "ST-AAC-FIXED" {
		t.Fatalf("restored aac ticket = %q ok=%v, want ST-AAC-FIXED", got, ok)
	}
	if ticket, err := aac.New(sess).Ticket(context.Background(), false); err != nil || ticket != "ST-AAC-FIXED" {
		t.Fatalf("aac ticket via service = %q err=%v", ticket, err)
	}
	if mints := gw.aacMintsTotal(); mints != 0 {
		t.Fatalf("aac mint walk ran %d times, want 0", mints)
	}
}
