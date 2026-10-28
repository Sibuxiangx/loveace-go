// Session cooling (DESIGN.md §9.1): the server must not hold school
// sessions forever. Tiers by last activity:
//
//	hot      < 5 min   session lives in memory, reads reuse it
//	warm     < 30 min  kept, no heartbeats; lazy revalidation on next use
//	cold     >= 30 min (configurable) evicted: cookies snapshot sealed to
//	          the store, then plaintext destroyed (local-only logout — a
//	          network sign-out is forbidden on shared-cookie sessions,
//	          §7.4; the school reaps the seat via its own TTL, E7)
//	dormant  >= 7 days delete the snapshot; the vaulted credentials stay
//	          (user consented); no background work ever
//
// A cold user's first request restores the snapshot (no login) or falls
// back to the vault silent relogin — one slower first request, nothing
// more.
package school

import (
	"context"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/cookies"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// Cooling thresholds.
const (
	HotAfter     = 5 * time.Minute
	DefaultCold  = 30 * time.Minute
	DormantAfter = 7 * 24 * time.Hour
)

// UserSnapshot is the sealed-at-rest form of one evicted session entry.
// ProxyURL records the session's home proxy exit — always empty since M4
// (direct egress everywhere); the field survives so snapshots saved in the
// pool era still deserialize.
//
// Tickets carries the CAS-derived service tickets (aac/labor). They are
// effectively static (one observed manual rotation across years), so a
// restored session re-seeds them and talks to the business APIs directly
// instead of re-walking the 4-hop mint on every cold session.
type UserSnapshot struct {
	UserID   string
	DeviceID string
	ProxyURL string
	Tickets  map[string]string
	Cookies  []*cookies.Cookie
	SavedAt  time.Time
}

// SnapshotStore persists user session snapshots. Implementations MUST
// seal the cookies (the server wires a vault adapter, like pool slots).
type SnapshotStore interface {
	SaveUserSnapshot(ctx context.Context, snap UserSnapshot) error
	DeleteUserSnapshot(ctx context.Context, userID string) error
	LoadUserSnapshot(ctx context.Context, userID string) (*UserSnapshot, error)
}

// WithSnapshotStore enables cold-eviction with restore.
func WithSnapshotStore(st SnapshotStore) Option {
	return func(m *Manager) {
		if st != nil {
			m.snapshots = st
		}
	}
}

// WithColdAfter overrides the cold threshold.
func WithColdAfter(d time.Duration) Option {
	return func(m *Manager) {
		if d > 0 {
			m.coldAfter = d
		}
	}
}

// CoolingStats is the §9.1 metric view.
type CoolingStats struct {
	Hot            int   `json:"hot"`
	Warm           int   `json:"warm"`
	Cold           int   `json:"cold"` // users currently evicted with a snapshot
	Dormant        int   `json:"dormant"`
	Evictions      int64 `json:"evictions"`
	SnapshotsSaved int64 `json:"snapshots_saved"`
	Restores       int64 `json:"restores"`
	RestoreMisses  int64 `json:"restore_misses"`
}

// lastActivity returns the user's most recent touch (zero time = never).
func (m *Manager) lastActivity(userID string) time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activity[userID]
}

func (m *Manager) touch(userID string, at time.Time) {
	m.mu.Lock()
	m.activity[userID] = at
	m.mu.Unlock()
}

// TierOf classifies one user (public for the admin view).
func (m *Manager) TierOf(userID string) string {
	last := m.lastActivity(userID)
	if last.IsZero() {
		return "dormant"
	}
	now := m.now()
	idle := now.Sub(last)
	switch {
	case idle >= DormantAfter:
		return "dormant"
	case idle >= m.coldAfter:
		return "cold"
	case idle >= HotAfter:
		return "warm"
	default:
		return "hot"
	}
}

// StartCooling runs the janitor until ctx is done: a sweep every interval
// (default 1 minute, honours ctx cancellation). Returns immediately; the
// caller owns the lifecycle.
func (m *Manager) StartCooling(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.SweepCold(ctx)
			}
		}
	}()
}

// SweepCold evicts every user past the cold threshold and deletes
// dormant snapshots. Returns the number of evictions. Called by the
// janitor and directly by tests.
func (m *Manager) SweepCold(ctx context.Context) int {
	m.mu.Lock()
	now := m.now()
	type victim struct {
		userID string
		device string
		sess   *session.Session
		idle   time.Duration
	}
	var victims []victim
	for userID, g := range m.group {
		if len(g) == 0 {
			continue
		}
		idle := now.Sub(m.activity[userID])
		if idle < m.coldAfter {
			continue
		}
		// Snapshot the most recently active entry's session only; the
		// others' school-side seats are reaped by the school TTL.
		var best *groupEntry
		for _, e := range g {
			if best == nil || e.lastActive.After(best.lastActive) {
				best = e
			}
		}
		victims = append(victims, victim{userID: userID, device: best.deviceID, sess: best.s, idle: idle})
	}
	evicted := 0
	for _, v := range victims {
		// Only sessions with cookies are worth a snapshot; an empty jar
		// would restore to a guaranteed miss.
		// A JWC callback still in flight may yet set JSESSIONID: let it land
		// before the jar is captured, or the snapshot restores JWC-cold.
		jarReady := v.sess.AwaitServiceCallback(ctx) == nil
		if m.snapshots != nil && jarReady && len(v.sess.Jar().All()) > 0 {
			snap := UserSnapshot{
				UserID:   v.userID,
				DeviceID: v.device,
				ProxyURL: v.sess.ProxyURL(),
				Tickets:  v.sess.Tickets(),
				Cookies:  v.sess.Jar().All(),
				SavedAt:  now,
			}
			if err := m.snapshots.SaveUserSnapshot(ctx, snap); err == nil {
				m.cooling.snapshotsSaved++
			}
			// A failed save only means a colder restart (vault relogin).
		}
		for _, e := range m.group[v.userID] {
			e.s.Logout()
		}
		delete(m.group, v.userID)
		m.cooling.evictions++
		evicted++
	}
	// Dormant: forget snapshots entirely.
	for userID := range m.activity {
		if now.Sub(m.activity[userID]) >= DormantAfter && m.snapshots != nil {
			_ = m.snapshots.DeleteUserSnapshot(ctx, userID)
		}
	}
	m.mu.Unlock()
	return evicted
}

// restoreSnapshot rebuilds a session from the sealed snapshot when the
// group has no live entry. Returns nil when there is nothing to restore.
func (m *Manager) restoreSnapshot(ctx context.Context, userID, deviceID string) *session.Session {
	if m.snapshots == nil || m.newTransport == nil {
		return nil
	}
	snap, err := m.snapshots.LoadUserSnapshot(ctx, userID)
	if err != nil || snap == nil || len(snap.Cookies) == 0 {
		m.mu.Lock()
		m.cooling.restoreMisses++
		m.mu.Unlock()
		return nil
	}
	// The session must carry REAL credentials: the cookies will eventually
	// expire and the recovery re-logins with whatever the session holds.
	// Empty credentials here made every snapshot-restored session a zombie
	// (2026-10-16: recovery logins with an empty password failed forever,
	// surfacing as 409 outcome_unknown on plain reads). A resolver miss
	// degrades to no restore — the next request takes the vault-relogin
	// path, which surfaces proper errors.
	var account, gwPass, password string
	if m.credResolver != nil {
		account, gwPass, password, err = m.credResolver(ctx, userID)
		if err != nil || account == "" {
			m.mu.Lock()
			m.cooling.restoreMisses++
			m.mu.Unlock()
			return nil
		}
	}
	s := session.New(account, gwPass, password, m.newTransport())
	s.Jar().Replace(snap.Cookies)
	s.SeedTickets(snap.Tickets) // static service tickets: skip the mint walk
	m.wireRecovery(userID, s)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cooling.restores++
	now := m.now()
	g := m.group[userID]
	if g == nil {
		g = map[string]*groupEntry{}
		m.group[userID] = g
	}
	e := &groupEntry{
		id: newEntryID(), deviceID: deviceID,
		s: s, createdAt: now, lastActive: now,
	} // The snapshot's own device binding wins when the request has none.
	if deviceID == "" {
		e.deviceID = snap.DeviceID
	}
	g[e.id] = e
	return s
}

// tierUnlocked is TierOf without locking (callers hold m.mu).
func (m *Manager) tierUnlocked(userID string, now time.Time) string {
	last, ok := m.activity[userID]
	if !ok {
		return "dormant"
	}
	idle := now.Sub(last)
	switch {
	case idle >= DormantAfter:
		return "dormant"
	case idle >= m.coldAfter:
		return "cold"
	case idle >= HotAfter:
		return "warm"
	default:
		return "hot"
	}
}

// CoolingStats reports the tier occupancy (§9.1 metrics).
func (m *Manager) CoolingStats() CoolingStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := CoolingStats{
		Evictions:      m.cooling.evictions,
		SnapshotsSaved: m.cooling.snapshotsSaved,
		Restores:       m.cooling.restores,
		RestoreMisses:  m.cooling.restoreMisses,
	}
	now := m.now()
	users := map[string]bool{}
	for uid := range m.activity {
		users[uid] = true
	}
	for uid := range m.group {
		users[uid] = true
	}
	for uid := range users {
		switch m.tierUnlocked(uid, now) {
		case "hot":
			out.Hot++
		case "warm":
			out.Warm++
		case "cold":
			out.Cold++
		case "dormant":
			out.Dormant++
		}
	}
	return out
}
