// Session groups (DESIGN.md §7.5): every school account may hold at most
// GroupSize live UAAP sessions. The 6th login is rejected with the active
// list (no silent LRU eviction — a user must never be logged out of their
// main device behind their back). Locally-dead entries are swept lazily;
// a group that is full of idle zombies recycles its oldest member.
package school

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/Sibuxiangx/loveace-go/core/errs"
	"github.com/Sibuxiangx/loveace-go/core/events"
	"github.com/Sibuxiangx/loveace-go/core/session"
)

// GroupSize is the default per-account cap on live UAAP sessions. The
// school-side real limit is unverified (§12 E7); 5 mirrors the observed
// client behaviour.
const GroupSize = 5

// ZombieIdleAfter: a group full of entries that have all been idle longer
// than this is considered a zombie set; the oldest one is recycled to make
// room (§7.5).
const ZombieIdleAfter = 10 * time.Minute

// SessionEntry is one live UAAP session of a user's group (public view;
// never contains cookies or credentials).
type SessionEntry struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"device_id,omitempty"`
	DeviceName string    `json:"device_name"`
	Platform   string    `json:"platform,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastActive time.Time `json:"last_active"`
}

// SessionLimitError rejects a login that would exceed the group capacity.
// The API maps it to 409 auth.session_limit and returns Active so the
// client can show which devices hold slots.
type SessionLimitError struct {
	UserID string
	Active []SessionEntry
}

func (e *SessionLimitError) Error() string { return "school: session group full" }

type groupEntry struct {
	id         string
	deviceID   string
	deviceName string
	platform   string
	s          *session.Session
	createdAt  time.Time
	lastActive time.Time
}

func (e *groupEntry) view() SessionEntry {
	return SessionEntry{ID: e.id, DeviceID: e.deviceID, DeviceName: e.deviceName,
		Platform: e.platform, CreatedAt: e.createdAt, LastActive: e.lastActive}
}

// GroupStat is the per-user occupancy view for the admin endpoint.
type GroupStat struct {
	UserID   string `json:"user_id"`
	Active   int    `json:"active"`
	Capacity int    `json:"capacity"`
}

// Option customises the Manager.
type Option func(*Manager)

// WithGroupSize overrides the per-account UAAP session cap.
func WithGroupSize(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.groupSize = n
		}
	}
}

// WithClock injects the clock (tests).
func WithClock(now func() time.Time) Option {
	return func(m *Manager) {
		if now != nil {
			m.now = now
		}
	}
}

// WithSinkFactory attaches a per-user observation sink to every session the
// manager creates (health telemetry, DESIGN §9/§10).
func WithSinkFactory(factory func(userID string) events.Sink) Option {
	return func(m *Manager) {
		if factory != nil {
			m.sinkFor = factory
		}
	}
}

// tagSink attaches the user-bound observation sink to a fresh session.
func (m *Manager) tagSink(userID string, s *session.Session) {
	if m.sinkFor != nil {
		s.SetEventSink(m.sinkFor(userID))
	}
}

// deadSession reports whether a session is certainly unusable without a
// network call: a signed-out session or one whose every cookie has
// expired (school sessions always carry expiries).
func deadSession(s *session.Session) bool {
	for _, c := range s.Jar().All() {
		if c.Expires == nil || c.Expires.After(time.Now()) {
			return false
		}
	}
	return true
}

// admit enforces the group capacity before any login work happens: a
// rejected 6th login must not create a 6th school-side session (§7.5). A
// re-login from a device that already holds an entry will REPLACE it, so
// it does not need a free slot. Callers run inside the per-user
// singleflight, so admission and registration never interleave for one
// account — but different users log in concurrently, so the manager-wide
// lock guards the maps here too. School-side teardown of retired sessions
// runs after the lock is released.
func (m *Manager) admit(userID, deviceName string) error {
	var doomed []*session.Session
	m.mu.Lock()
	g := m.group[userID]
	now := m.now()
	// Lazy sweep: certainly-dead sessions do not occupy quota (§7.5).
	for id, e := range g {
		if deadSession(e.s) {
			doomed = append(doomed, e.s)
			delete(g, id)
		}
	}
	admit := len(g) < m.groupSize
	if !admit {
		for _, e := range g {
			if deviceName != "" && e.deviceName == deviceName {
				admit = true // replaces its own entry at register
				break
			}
		}
	}
	if !admit {
		// All members idle beyond the zombie threshold → recycle the oldest.
		zombies := true
		for _, e := range g {
			if now.Sub(e.lastActive) <= ZombieIdleAfter {
				zombies = false
				break
			}
		}
		if zombies {
			var oldest *groupEntry
			for _, e := range g {
				if oldest == nil || e.lastActive.Before(oldest.lastActive) {
					oldest = e
				}
			}
			if oldest != nil {
				doomed = append(doomed, oldest.s)
				delete(g, oldest.id)
				admit = true
			}
		}
	}
	var limitErr error
	if !admit {
		active := make([]SessionEntry, 0, len(g))
		for _, e := range g {
			active = append(active, e.view())
		}
		limitErr = &SessionLimitError{UserID: userID, Active: active}
	}
	m.mu.Unlock()
	for _, s := range doomed {
		s.Logout() // network teardown outside the manager lock
	}
	return limitErr
}

// register stores a freshly logged-in session in the user's group. A
// re-login from the same device name replaces that device's entry (a
// fresh login orphans the old client state; it must not grow the group).
// Callers run inside the per-user singleflight after admit.
func (m *Manager) register(userID, deviceName, platform string, s *session.Session) {
	g := m.group[userID]
	now := m.now()
	m.activity[userID] = now
	var superseded []*session.Session
	replaced := false
	if deviceName != "" {
		for _, e := range g {
			if e.deviceName == deviceName {
				if e.s != s {
					superseded = append(superseded, e.s)
				}
				e.s = s
				e.platform = platform
				e.lastActive = now
				replaced = true
				break
			}
		}
	}
	if !replaced {
		if g == nil {
			g = map[string]*groupEntry{}
			m.group[userID] = g
		}
		e := &groupEntry{
			id: newEntryID(), deviceName: deviceName, platform: platform,
			s: s, createdAt: now, lastActive: now,
		}
		g[e.id] = e
	}
	// Sharing convergence (§7.5 revised): every OTHER entry repoints to
	// the newest session too — one user holds exactly ONE school session
	// no matter how many devices log in. The UAAP layer has no ban
	// budget; keeping one session per user minimises even that.
	for _, e := range g {
		if e.s != s {
			superseded = append(superseded, e.s)
			e.s = s
			e.lastActive = now
		}
	}
	for _, old := range superseded {
		old.Logout() // local teardown of superseded sessions
	}
}

// AttachDevice binds the device row (created after the school login) to
// the group entry logged in from that device NAME. With session sharing
// every entry points at the same session, so the session pointer cannot
// identify the entry — the device name is the binding key (the same key
// register uses for same-device replacement).
func (m *Manager) AttachDevice(userID, deviceID, deviceName string, s *session.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if deviceName != "" {
		for _, e := range m.group[userID] {
			if e.deviceName == deviceName {
				e.deviceID = deviceID
				return
			}
		}
		return
	}
	// Nameless logins (vault relogin): bind the most recently active
	// entry riding this session.
	var best *groupEntry
	for _, e := range m.group[userID] {
		if e.s != s {
			continue
		}
		if best == nil || e.lastActive.After(best.lastActive) {
			best = e
		}
	}
	if best != nil {
		best.deviceID = deviceID
	}
}

// LiveSession returns any live authenticated session in the user's group
// (the sharing candidate for a new device's login).
func (m *Manager) LiveSession(userID string) (*session.Session, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var best *groupEntry
	for _, e := range m.group[userID] {
		if e.s.State() != session.StateAuthenticated {
			continue
		}
		if best == nil || e.lastActive.After(best.lastActive) {
			best = e
		}
	}
	if best == nil {
		return nil, false
	}
	return best.s, true
}

// BindDevice adds a per-device entry REUSING an already-live session
// (one UAAP session per user, shared by every device — the UAAP layer has
// no ban budget; the EC gateway does). Never logs out or re-logins: the
// caller validated the credentials before binding. An extra binding costs
// zero school-side quota.
func (m *Manager) BindDevice(userID, deviceID, deviceName, platform string, s *session.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.activity[userID] = now
	g := m.group[userID]
	if g == nil {
		g = map[string]*groupEntry{}
		m.group[userID] = g
	}
	g[newEntryID()] = &groupEntry{
		deviceID: deviceID, deviceName: deviceName,
		platform: platform, s: s, createdAt: now, lastActive: now,
	}
}

// SessionFor returns the live school session for a user, preferring the
// entry bound to the requesting device and touching its last-active (the
// zombie heuristic and the cooling tiers depend on it). A miss falls back
// to the sealed cold-eviction snapshot (no login, no network).
func (m *Manager) SessionFor(ctx context.Context, userID, deviceID string) (*session.Session, error) {
	m.mu.Lock()
	g := m.group[userID]
	m.activity[userID] = m.now()
	m.mu.Unlock()
	if len(g) > 0 {
		if s := m.pick(ctx, g, deviceID); s != nil {
			return s, nil
		}
	}
	if s := m.restoreSnapshot(ctx, userID, deviceID); s != nil {
		return s, nil
	}
	return nil, ErrNoSession
}

// pick selects the entry for the device (or the most recently active one)
// and touches it. Caller has released m.mu; pick re-locks briefly.
func (m *Manager) pick(_ context.Context, g map[string]*groupEntry, deviceID string) *session.Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(g) == 0 {
		return nil
	}
	if deviceID != "" {
		for _, e := range g {
			if e.deviceID == deviceID {
				e.lastActive = m.now()
				return e.s
			}
		}
	}
	var best *groupEntry
	for _, e := range g {
		if best == nil || e.lastActive.After(best.lastActive) {
			best = e
		}
	}
	if best == nil {
		return nil
	}
	best.lastActive = m.now()
	return best.s
}

// LogoutDevice drops the session entry bound to a device (local-only,
// like Logout — sessions share gateway cookies, never sign out remotely).
// With session sharing several devices point at ONE school session: the
// core Logout only runs when the LAST binding goes away, so one device's
// exit never kills the others.
func (m *Manager) LogoutDevice(userID, deviceID string) {
	m.mu.Lock()
	var doomed *session.Session
	others := false
	for id, e := range m.group[userID] {
		if e.deviceID == deviceID {
			doomed = e.s
			delete(m.group[userID], id)
			continue
		}
		others = true
	}
	m.mu.Unlock()
	if !others && doomed != nil {
		// The last binding is gone: tear the shared session down (local
		// state only — still no remote school logout).
		doomed.Logout()
	}
}

// MarkSessionDead removes the entry whose session just failed with an
// auth-level error: lazy expiry detection from real use (§7.5).
func (m *Manager) MarkSessionDead(userID string, s *session.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, e := range m.group[userID] {
		if e.s == s {
			delete(m.group[userID], id)
		}
	}
}

// markAuthFailed removes the entry on auth-level failures (lazy expiry
// detection from real use); other errors say nothing about session death.
func (m *Manager) markAuthFailed(userID string, s *session.Session, err error) {
	if errs.CodeOf(err) != errs.AuthRequired {
		return
	}
	m.MarkSessionDead(userID, s)
}

// MarkAuthFailed is the exported hook for dispatch paths outside the
// manager's typed methods (the generic op endpoint): auth-level failures
// drop the session entry so the next request recovers (§8).
func (m *Manager) MarkAuthFailed(userID string, s *session.Session, err error) {
	m.markAuthFailed(userID, s, err)
}

// GroupStats reports per-user occupancy for the admin endpoint.
func (m *Manager) GroupStats() []GroupStat {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]GroupStat, 0, len(m.group))
	for uid, g := range m.group {
		if len(g) == 0 {
			continue
		}
		out = append(out, GroupStat{UserID: uid, Active: len(g), Capacity: m.groupSize})
	}
	return out
}

func newEntryID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return "se_" + hex.EncodeToString(b[:])
}
