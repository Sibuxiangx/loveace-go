// Package pg is the production store.Store: PostgreSQL via pgx v5 with
// embedded, idempotent migrations (DESIGN.md §0).
package pg

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store implements store.Store on top of pgxpool.
type Store struct {
	pool *pgxpool.Pool
}

// New opens a pool and applies pending migrations.
func New(ctx context.Context, databaseURL string) (*Store, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("pg: pool: %w", err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

// Migrate applies pending migrations in order, each in its own transaction.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations(version TEXT PRIMARY KEY, applied_at TIMESTAMPTZ NOT NULL DEFAULT now())`); err != nil {
		return fmt.Errorf("pg: schema_migrations: %w", err)
	}
	entries, err := fs.ReadDir(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("pg: migrations dir: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists bool
		if err := pool.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM schema_migrations WHERE version = $1)`, name).Scan(&exists); err != nil {
			return fmt.Errorf("pg: check migration %s: %w", name, err)
		}
		if exists {
			continue
		}
		sqlBytes, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("pg: read migration %s: %w", name, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("pg: begin %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx, string(sqlBytes)); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("pg: apply %s: %w", name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations(version) VALUES ($1)`, name); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("pg: record %s: %w", name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("pg: commit %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) GetOrCreateUser(ctx context.Context, accountHash string) (*store.User, bool, error) {
	id := store.NewID()
	tag, err := s.pool.Exec(ctx,
		`INSERT INTO users(id, account_hash) VALUES ($1, $2) ON CONFLICT (account_hash) DO NOTHING`,
		id, accountHash)
	if err != nil {
		return nil, false, fmt.Errorf("pg: upsert user: %w", err)
	}
	created := tag.RowsAffected() == 1
	var u store.User
	err = s.pool.QueryRow(ctx,
		`SELECT id, account_hash, credential_state, created_at FROM users WHERE account_hash = $1`,
		accountHash).Scan(&u.ID, &u.AccountHash, &u.CredentialState, &u.CreatedAt)
	if err != nil {
		return nil, false, fmt.Errorf("pg: select user: %w", err)
	}
	return &u, created, nil
}

func (s *Store) GetUser(ctx context.Context, userID string) (*store.User, error) {
	var u store.User
	err := s.pool.QueryRow(ctx,
		`SELECT id, account_hash, credential_state, created_at FROM users WHERE id = $1`,
		userID).Scan(&u.ID, &u.AccountHash, &u.CredentialState, &u.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get user: %w", err)
	}
	return &u, nil
}

func (s *Store) UpsertCredential(ctx context.Context, cred store.Credential) error {
	blob, err := json.Marshal(cred.Blob)
	if err != nil {
		return fmt.Errorf("pg: encode credential: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO credentials(user_id, password_blob, fingerprint, updated_at)
		VALUES ($1, $2, $3, now())
		ON CONFLICT (user_id) DO UPDATE SET
			password_blob = EXCLUDED.password_blob,
			fingerprint = EXCLUDED.fingerprint,
			updated_at = now()`,
		cred.UserID, blob, cred.Fingerprint)
	if err != nil {
		return fmt.Errorf("pg: upsert credential: %w", err)
	}
	return nil
}

func (s *Store) ListCredentialUserIDs(ctx context.Context, limit int) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT user_id FROM credentials ORDER BY updated_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("pg: list credential users: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("pg: scan credential user: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) GetCredential(ctx context.Context, userID string) (*store.Credential, error) {
	var blob []byte
	cred := store.Credential{UserID: userID}
	err := s.pool.QueryRow(ctx,
		`SELECT password_blob, fingerprint, updated_at FROM credentials WHERE user_id = $1`,
		userID).Scan(&blob, &cred.Fingerprint, &cred.UpdatedAt)
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get credential: %w", err)
	}
	if err := json.Unmarshal(blob, &cred.Blob); err != nil {
		return nil, fmt.Errorf("pg: decode credential: %w", err)
	}
	return &cred, nil
}

func (s *Store) SetCredentialState(ctx context.Context, userID, state string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE users SET credential_state = $2 WHERE id = $1`, userID, state)
	if err != nil {
		return fmt.Errorf("pg: set credential state: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CreateDevice(ctx context.Context, userID, name, platform string) (*store.Device, error) {
	d := store.Device{ID: store.NewID(), UserID: userID, Name: name, Platform: platform, CreatedAt: time.Now()}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO devices(id, user_id, name, platform) VALUES ($1, $2, $3, $4)`,
		d.ID, d.UserID, d.Name, d.Platform)
	if err != nil {
		return nil, fmt.Errorf("pg: create device: %w", err)
	}
	return &d, nil
}

func (s *Store) ListDevices(ctx context.Context, userID string) ([]store.Device, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, user_id, name, platform, created_at, revoked_at
		FROM devices WHERE user_id = $1 AND revoked_at IS NULL ORDER BY created_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("pg: list devices: %w", err)
	}
	defer rows.Close()
	var out []store.Device
	for rows.Next() {
		var d store.Device
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.CreatedAt, &d.RevokedAt); err != nil {
			return nil, fmt.Errorf("pg: scan device: %w", err)
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) GetDevice(ctx context.Context, userID, deviceID string) (*store.Device, error) {
	var d store.Device
	err := s.pool.QueryRow(ctx, `
		SELECT id, user_id, name, platform, created_at, revoked_at
		FROM devices WHERE id = $1 AND user_id = $2`, deviceID, userID).
		Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.CreatedAt, &d.RevokedAt)
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get device: %w", err)
	}
	return &d, nil
}

func (s *Store) RevokeDevice(ctx context.Context, userID, deviceID string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE devices SET revoked_at = now() WHERE id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		deviceID, userID)
	if err != nil {
		return fmt.Errorf("pg: revoke device: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) InsertRefreshToken(ctx context.Context, t store.RefreshToken) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO refresh_tokens(token_hash, user_id, device_id, expires_at)
		VALUES ($1, $2, $3, $4)`, t.Hash, t.UserID, t.DeviceID, t.ExpiresAt)
	if err != nil {
		return fmt.Errorf("pg: insert refresh token: %w", err)
	}
	return nil
}

func (s *Store) GetRefreshToken(ctx context.Context, hash string) (*store.RefreshToken, error) {
	var t store.RefreshToken
	err := s.pool.QueryRow(ctx, `
		SELECT token_hash, user_id, device_id, expires_at, rotated_to, revoked, created_at
		FROM refresh_tokens WHERE token_hash = $1`, hash).
		Scan(&t.Hash, &t.UserID, &t.DeviceID, &t.ExpiresAt, &t.RotatedTo, &t.Revoked, &t.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get refresh token: %w", err)
	}
	return &t, nil
}

func (s *Store) RotateRefreshToken(ctx context.Context, oldHash, newHash string, newExpiresAt time.Time) error {
	// Rotation is two steps with the same semantics as the mem store: the
	// old row keeps pointing at its successor (rotated_to — presenting it
	// again is reuse and revokes the device), and a row for the NEW hash
	// must exist or the client's next refresh 401s. The missing INSERT was
	// a live bug: every first refresh after login failed with
	// auth.invalid_token, forcing a re-login.
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: rotate refresh token: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		UPDATE refresh_tokens SET rotated_to = $2
		WHERE token_hash = $1 AND rotated_to = '' AND revoked = FALSE`, oldHash, newHash)
	if err != nil {
		return fmt.Errorf("pg: rotate refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO refresh_tokens(token_hash, user_id, device_id, expires_at)
		SELECT $2, user_id, device_id, $3 FROM refresh_tokens WHERE token_hash = $1`,
		oldHash, newHash, newExpiresAt); err != nil {
		return fmt.Errorf("pg: rotate refresh token successor: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *Store) RevokeDeviceTokens(ctx context.Context, deviceID string) error {
	_, err := s.pool.Exec(ctx, `UPDATE refresh_tokens SET revoked = TRUE WHERE device_id = $1`, deviceID)
	if err != nil {
		return fmt.Errorf("pg: revoke device tokens: %w", err)
	}
	return nil
}

// PruneRotatedRefreshTokens deletes superseded refresh-token rows created
// before the cutoff (reuse detection stays available within the window).
func (s *Store) PruneRotatedRefreshTokens(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `
		DELETE FROM refresh_tokens WHERE rotated_to <> '' AND created_at < $1`, before)
	if err != nil {
		return fmt.Errorf("pg: prune rotated refresh tokens: %w", err)
	}
	return nil
}

func (s *Store) AppendAudit(ctx context.Context, e store.AuditEvent) error {
	detail := map[string]any{}
	if e.Detail != "" {
		if err := json.Unmarshal([]byte(e.Detail), &detail); err != nil {
			detail = map[string]any{"detail": e.Detail}
		}
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO audit_log(at, user_id, device_id, event, detail) VALUES ($1, NULLIF($2,'')::uuid, NULLIF($3,'')::uuid, $4, $5)`,
		e.At, e.UserID, e.DeviceID, e.Event, detail)
	if err != nil {
		return fmt.Errorf("pg: audit: %w", err)
	}
	return nil
}

func (s *Store) PutPoolSlot(ctx context.Context, slot store.PoolSlot) error {
	blob, err := json.Marshal(slot.Blob)
	if err != nil {
		return fmt.Errorf("pg: encode pool slot: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO pool_slots(id, blob, deposited_at, last_verified_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET
			blob = EXCLUDED.blob,
			deposited_at = EXCLUDED.deposited_at,
			last_verified_at = EXCLUDED.last_verified_at`,
		slot.ID, blob, slot.DepositedAt, slot.LastVerifiedAt)
	if err != nil {
		return fmt.Errorf("pg: put pool slot: %w", err)
	}
	return nil
}

func (s *Store) DeletePoolSlot(ctx context.Context, slotID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pool_slots WHERE id = $1`, slotID)
	if err != nil {
		return fmt.Errorf("pg: delete pool slot: %w", err)
	}
	return nil
}

func (s *Store) ListPoolSlots(ctx context.Context) ([]store.PoolSlot, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id, blob, deposited_at, last_verified_at FROM pool_slots`)
	if err != nil {
		return nil, fmt.Errorf("pg: list pool slots: %w", err)
	}
	defer rows.Close()
	var out []store.PoolSlot
	for rows.Next() {
		var slot store.PoolSlot
		var blob []byte
		if err := rows.Scan(&slot.ID, &blob, &slot.DepositedAt, &slot.LastVerifiedAt); err != nil {
			return nil, fmt.Errorf("pg: scan pool slot: %w", err)
		}
		if err := json.Unmarshal(blob, &slot.Blob); err != nil {
			return nil, fmt.Errorf("pg: decode pool slot: %w", err)
		}
		out = append(out, slot)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg: list pool slots: %w", err)
	}
	return out, nil
}

func (s *Store) PutUserSnapshot(ctx context.Context, snap store.UserSnapshot) error {
	blob, err := json.Marshal(snap.Blob)
	if err != nil {
		return fmt.Errorf("pg: encode user snapshot: %w", err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO user_session_snapshots(user_id, blob, saved_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET blob = EXCLUDED.blob, saved_at = EXCLUDED.saved_at`,
		snap.UserID, blob, snap.SavedAt)
	if err != nil {
		return fmt.Errorf("pg: put user snapshot: %w", err)
	}
	return nil
}

func (s *Store) GetUserSnapshot(ctx context.Context, userID string) (*store.UserSnapshot, error) {
	var blob []byte
	snap := store.UserSnapshot{UserID: userID}
	err := s.pool.QueryRow(ctx,
		`SELECT blob, saved_at FROM user_session_snapshots WHERE user_id = $1`, userID).
		Scan(&blob, &snap.SavedAt)
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get user snapshot: %w", err)
	}
	if err := json.Unmarshal(blob, &snap.Blob); err != nil {
		return nil, fmt.Errorf("pg: decode user snapshot: %w", err)
	}
	return &snap, nil
}

func (s *Store) DeleteUserSnapshot(ctx context.Context, userID string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_session_snapshots WHERE user_id = $1`, userID)
	if err != nil {
		return fmt.Errorf("pg: delete user snapshot: %w", err)
	}
	return nil
}

func (s *Store) CreateAnnouncement(ctx context.Context, a store.Announcement) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO announcements(id, level, body_md, audience, pinned, requires_ack, active, starts_at, ends_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		a.ID, a.Level, a.BodyMD, a.Audience, a.Pinned, a.RequiresAck, a.Active, a.StartsAt, a.EndsAt)
	if err != nil {
		return fmt.Errorf("pg: create announcement: %w", err)
	}
	return nil
}

func (s *Store) ListAnnouncements(ctx context.Context, activeOnly bool) ([]store.Announcement, error) {
	where := ""
	if activeOnly {
		where = "WHERE active"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, level, body_md, audience, pinned, requires_ack, active, starts_at, ends_at
		FROM announcements `+where+` ORDER BY pinned DESC, starts_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("pg: list announcements: %w", err)
	}
	defer rows.Close()
	var out []store.Announcement
	for rows.Next() {
		var a store.Announcement
		if err := rows.Scan(&a.ID, &a.Level, &a.BodyMD, &a.Audience, &a.Pinned, &a.RequiresAck, &a.Active, &a.StartsAt, &a.EndsAt); err != nil {
			return nil, fmt.Errorf("pg: scan announcement: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) DeactivateAnnouncement(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE announcements SET active = FALSE WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("pg: deactivate announcement: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) AckAnnouncement(ctx context.Context, userID, announcementID string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO announcement_acks(announcement_id, user_id) VALUES ($1, $2)
		ON CONFLICT (announcement_id, user_id) DO NOTHING`,
		announcementID, userID)
	if err != nil {
		return fmt.Errorf("pg: ack announcement: %w", err)
	}
	return nil
}

func (s *Store) ListAcks(ctx context.Context, userID string) (map[string]time.Time, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT announcement_id, acked_at FROM announcement_acks WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("pg: list acks: %w", err)
	}
	defer rows.Close()
	out := map[string]time.Time{}
	for rows.Next() {
		var id string
		var at time.Time
		if err := rows.Scan(&id, &at); err != nil {
			return nil, fmt.Errorf("pg: scan ack: %w", err)
		}
		out[id] = at
	}
	return out, rows.Err()
}

func (s *Store) UpsertCalendarTerm(ctx context.Context, t store.CalendarTerm) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO calendar_terms(id, first_week_monday, weeks, version) VALUES ($1, $2, $3, $4)
		ON CONFLICT (id) DO UPDATE SET first_week_monday = EXCLUDED.first_week_monday, weeks = EXCLUDED.weeks`,
		t.ID, t.FirstWeekMonday, t.Weeks, t.Version)
	if err != nil {
		return fmt.Errorf("pg: upsert calendar term: %w", err)
	}
	return nil
}

func (s *Store) ListCalendarTerms(ctx context.Context) ([]store.CalendarTerm, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, first_week_monday, weeks, version FROM calendar_terms`)
	if err != nil {
		return nil, fmt.Errorf("pg: list calendar terms: %w", err)
	}
	defer rows.Close()
	var out []store.CalendarTerm
	for rows.Next() {
		var t store.CalendarTerm
		if err := rows.Scan(&t.ID, &t.FirstWeekMonday, &t.Weeks, &t.Version); err != nil {
			return nil, fmt.Errorf("pg: scan calendar term: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AddCalendarRule(ctx context.Context, r store.CalendarRule) error {
	if r.Status == "" {
		r.Status = "draft"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO calendar_rules(id, term_id, day, kind, follow_week, follow_weekday, status, version)
		VALUES ($1, $2, $3, $4, NULLIF($5,0), NULLIF($6,0), $7, $8)`,
		r.ID, r.TermID, r.Day, r.Kind, r.FollowWeek, r.FollowWeekday, r.Status, r.Version)
	if err != nil {
		return fmt.Errorf("pg: add calendar rule: %w", err)
	}
	return nil
}

func (s *Store) ListCalendarRules(ctx context.Context, termID, status string) ([]store.CalendarRule, error) {
	where := " WHERE term_id = $1"
	if status != "" {
		where += " AND status = $2"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, term_id, day, kind, COALESCE(follow_week,0), COALESCE(follow_weekday,0), status, version
		FROM calendar_rules`+where+` ORDER BY day`, termID, status)
	if err != nil {
		return nil, fmt.Errorf("pg: list calendar rules: %w", err)
	}
	defer rows.Close()
	var out []store.CalendarRule
	for rows.Next() {
		var r store.CalendarRule
		if err := rows.Scan(&r.ID, &r.TermID, &r.Day, &r.Kind, &r.FollowWeek, &r.FollowWeekday, &r.Status, &r.Version); err != nil {
			return nil, fmt.Errorf("pg: scan calendar rule: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeleteCalendarRule(ctx context.Context, ruleID string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM calendar_rules WHERE id = $1 AND status = 'draft'`, ruleID)
	if err != nil {
		return fmt.Errorf("pg: delete calendar rule: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) PublishCalendar(ctx context.Context, termID string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("pg: publish begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var version int
	if err := tx.QueryRow(ctx,
		`UPDATE calendar_terms SET version = version + 1 WHERE id = $1 RETURNING version`,
		termID).Scan(&version); err != nil {
		if err == pgx.ErrNoRows {
			return 0, store.ErrNotFound
		}
		return 0, fmt.Errorf("pg: publish term: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE calendar_rules SET status = 'published', version = $2 WHERE term_id = $1 AND status = 'draft'`,
		termID, version); err != nil {
		return 0, fmt.Errorf("pg: publish rules: %w", err)
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM calendar_rules WHERE term_id = $1 AND status = 'published' AND version < $2`,
		termID, version); err != nil {
		return 0, fmt.Errorf("pg: retire old rules: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("pg: publish commit: %w", err)
	}
	return version, nil
}

func (s *Store) CalendarVersion(ctx context.Context, termID string) (int, error) {
	var version int
	err := s.pool.QueryRow(ctx,
		`SELECT version FROM calendar_terms WHERE id = $1`, termID).Scan(&version)
	if err == pgx.ErrNoRows {
		return 0, store.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("pg: calendar version: %w", err)
	}
	return version, nil
}

func (s *Store) Close() error { s.pool.Close(); return nil }

func (s *Store) ReserveIdempotency(ctx context.Context, r store.IdempotencyRecord) (store.IdempotencyRecord, bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO idempotency_keys(key, user_id, op, request_hash, status, created_at)
		VALUES ($1, $2, $3, $4, 'pending', now())
		ON CONFLICT (key, user_id) DO NOTHING`,
		r.Key, r.UserID, r.Op, r.RequestHash)
	if err != nil {
		return store.IdempotencyRecord{}, false, fmt.Errorf("pg: reserve idempotency: %w", err)
	}
	if tag.RowsAffected() == 1 {
		return store.IdempotencyRecord{}, false, nil // this caller owns it
	}
	var prev store.IdempotencyRecord
	var response []byte
	err = s.pool.QueryRow(ctx, `
		SELECT op, request_hash, status, response, created_at
		FROM idempotency_keys WHERE key = $1 AND user_id = $2`,
		r.Key, r.UserID).Scan(&prev.Op, &prev.RequestHash, &prev.Status, &response, &prev.CreatedAt)
	if err != nil {
		return store.IdempotencyRecord{}, false, fmt.Errorf("pg: read idempotency: %w", err)
	}
	prev.Key, prev.UserID, prev.Response = r.Key, r.UserID, response
	return prev, true, nil
}

func (s *Store) FinalizeIdempotency(ctx context.Context, key, userID, status string, response []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE idempotency_keys SET status = $3, response = $4
		WHERE key = $1 AND user_id = $2`,
		key, userID, status, response)
	if err != nil {
		return fmt.Errorf("pg: finalize idempotency: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) CleanupIdempotency(ctx context.Context, before time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM idempotency_keys WHERE created_at < $1`, before)
	if err != nil {
		return fmt.Errorf("pg: cleanup idempotency: %w", err)
	}
	return nil
}

const teacherEvalLogCap = 200

// teacherEvalActiveIndex is the partial unique index enforcing one
// queued/running task per user (migration 0006).
const teacherEvalActiveIndex = "teacher_eval_tasks_one_active"

const teacherEvalTaskCols = `id, user_id, device_id, status, error_kind, error_message,
		cancel_requested, course_total, score_mode, created_at, started_at, finished_at`

const teacherEvalCourseCols = `task_id, course_key, name, teacher, position, status, error_kind,
		message, submit_attempts, submitted_at, updated_at`

// nullableTime maps the zero time to SQL NULL.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func timeOrZero(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func isTeacherEvalActiveViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == teacherEvalActiveIndex
}

func scanTeacherEvalTask(row interface{ Scan(dest ...any) error }) (store.TeacherEvalTask, error) {
	var t store.TeacherEvalTask
	var started, finished *time.Time
	err := row.Scan(&t.ID, &t.UserID, &t.DeviceID, &t.Status, &t.ErrorKind, &t.ErrorMessage,
		&t.CancelRequested, &t.CourseTotal, &t.ScoreMode, &t.CreatedAt, &started, &finished)
	t.ScoreMode = scoreModeOrDefault(t.ScoreMode)
	t.StartedAt, t.FinishedAt = timeOrZero(started), timeOrZero(finished)
	return t, err
}

func scanTeacherEvalCourse(row interface{ Scan(dest ...any) error }) (store.TeacherEvalTaskCourse, error) {
	var c store.TeacherEvalTaskCourse
	var submitted *time.Time
	err := row.Scan(&c.TaskID, &c.CourseKey, &c.Name, &c.Teacher, &c.Position, &c.Status, &c.ErrorKind,
		&c.Message, &c.SubmitAttempts, &submitted, &c.UpdatedAt)
	c.SubmittedAt = timeOrZero(submitted)
	return c, err
}

// CreateTeacherEvalTask inserts the task and its courses in one transaction.
// The task's ID is authoritative for every course row.
func (s *Store) CreateTeacherEvalTask(ctx context.Context, t store.TeacherEvalTask, courses []store.TeacherEvalTaskCourse) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: create teacher eval task: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `
		INSERT INTO teacher_eval_tasks(id, user_id, device_id, status, error_kind, error_message,
			cancel_requested, course_total, score_mode, created_at, started_at, finished_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		t.ID, t.UserID, t.DeviceID, t.Status, t.ErrorKind, t.ErrorMessage,
		t.CancelRequested, t.CourseTotal, scoreModeOrDefault(t.ScoreMode), t.CreatedAt,
		nullableTime(t.StartedAt), nullableTime(t.FinishedAt)); err != nil {
		if isTeacherEvalActiveViolation(err) {
			return store.ErrTeacherEvalTaskActive
		}
		return fmt.Errorf("pg: create teacher eval task: %w", err)
	}
	for _, c := range courses {
		if _, err := tx.Exec(ctx, `
			INSERT INTO teacher_eval_task_courses(`+teacherEvalCourseCols+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			t.ID, c.CourseKey, c.Name, c.Teacher, c.Position, c.Status, c.ErrorKind,
			c.Message, c.SubmitAttempts, nullableTime(c.SubmittedAt), c.UpdatedAt); err != nil {
			return fmt.Errorf("pg: create teacher eval course: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: create teacher eval task commit: %w", err)
	}
	return nil
}

func (s *Store) GetTeacherEvalTask(ctx context.Context, id string) (*store.TeacherEvalTask, error) {
	t, err := scanTeacherEvalTask(s.pool.QueryRow(ctx,
		`SELECT `+teacherEvalTaskCols+` FROM teacher_eval_tasks WHERE id = $1`, id))
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get teacher eval task: %w", err)
	}
	return &t, nil
}

func (s *Store) ActiveTeacherEvalTask(ctx context.Context, userID string) (*store.TeacherEvalTask, error) {
	t, err := scanTeacherEvalTask(s.pool.QueryRow(ctx,
		`SELECT `+teacherEvalTaskCols+` FROM teacher_eval_tasks
		WHERE user_id = $1 AND status IN ('queued', 'running') LIMIT 1`, userID))
	if err == pgx.ErrNoRows {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: active teacher eval task: %w", err)
	}
	return &t, nil
}

// ListTeacherEvalTasks returns the user's tasks newest first; limit<=0 means
// 20, capped at 100.
func (s *Store) ListTeacherEvalTasks(ctx context.Context, userID string, limit int) ([]store.TeacherEvalTask, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+teacherEvalTaskCols+` FROM teacher_eval_tasks
		WHERE user_id = $1 ORDER BY created_at DESC, id DESC LIMIT $2`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("pg: list teacher eval tasks: %w", err)
	}
	defer rows.Close()
	var out []store.TeacherEvalTask
	for rows.Next() {
		t, err := scanTeacherEvalTask(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: scan teacher eval task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ListRecoverableTeacherEvalTasks returns queued/running tasks, oldest first.
func (s *Store) ListRecoverableTeacherEvalTasks(ctx context.Context) ([]store.TeacherEvalTask, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+teacherEvalTaskCols+` FROM teacher_eval_tasks
		WHERE status IN ('queued', 'running') ORDER BY created_at, id`)
	if err != nil {
		return nil, fmt.Errorf("pg: list recoverable teacher eval tasks: %w", err)
	}
	defer rows.Close()
	var out []store.TeacherEvalTask
	for rows.Next() {
		t, err := scanTeacherEvalTask(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: scan teacher eval task: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTeacherEvalTask(ctx context.Context, t store.TeacherEvalTask) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE teacher_eval_tasks SET user_id = $2, device_id = $3, status = $4, error_kind = $5,
			error_message = $6, cancel_requested = $7, course_total = $8, created_at = $9,
			started_at = $10, finished_at = $11, score_mode = $12
		WHERE id = $1`,
		t.ID, t.UserID, t.DeviceID, t.Status, t.ErrorKind, t.ErrorMessage,
		t.CancelRequested, t.CourseTotal, t.CreatedAt, nullableTime(t.StartedAt), nullableTime(t.FinishedAt),
		scoreModeOrDefault(t.ScoreMode))
	if err != nil {
		if isTeacherEvalActiveViolation(err) {
			return store.ErrTeacherEvalTaskActive
		}
		return fmt.Errorf("pg: update teacher eval task: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// ListTeacherEvalCourses returns the task's courses by Position ascending.
func (s *Store) ListTeacherEvalCourses(ctx context.Context, taskID string) ([]store.TeacherEvalTaskCourse, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+teacherEvalCourseCols+` FROM teacher_eval_task_courses
		WHERE task_id = $1 ORDER BY position`, taskID)
	if err != nil {
		return nil, fmt.Errorf("pg: list teacher eval courses: %w", err)
	}
	defer rows.Close()
	var out []store.TeacherEvalTaskCourse
	for rows.Next() {
		c, err := scanTeacherEvalCourse(rows)
		if err != nil {
			return nil, fmt.Errorf("pg: scan teacher eval course: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) UpdateTeacherEvalCourse(ctx context.Context, c store.TeacherEvalTaskCourse) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE teacher_eval_task_courses SET name = $3, teacher = $4, position = $5, status = $6,
			error_kind = $7, message = $8, submit_attempts = $9, submitted_at = $10, updated_at = $11
		WHERE task_id = $1 AND course_key = $2`,
		c.TaskID, c.CourseKey, c.Name, c.Teacher, c.Position, c.Status, c.ErrorKind,
		c.Message, c.SubmitAttempts, nullableTime(c.SubmittedAt), c.UpdatedAt)
	if err != nil {
		return fmt.Errorf("pg: update teacher eval course: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

// AppendTeacherEvalLog inserts one log row (ErrNotFound if the task is gone)
// and deletes rows beyond the newest teacherEvalLogCap in the same transaction.
func (s *Store) AppendTeacherEvalLog(ctx context.Context, taskID string, at time.Time, message string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: append teacher eval log: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	tag, err := tx.Exec(ctx, `
		INSERT INTO teacher_eval_task_logs(task_id, at, message)
		SELECT id, $2::timestamptz, $3::text FROM teacher_eval_tasks WHERE id = $1`,
		taskID, at, message)
	if err != nil {
		return fmt.Errorf("pg: append teacher eval log: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM teacher_eval_task_logs WHERE task_id = $1 AND seq < (
			SELECT seq FROM teacher_eval_task_logs WHERE task_id = $1
			ORDER BY seq DESC OFFSET $2 LIMIT 1)`,
		taskID, teacherEvalLogCap-1); err != nil {
		return fmt.Errorf("pg: trim teacher eval logs: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: append teacher eval log commit: %w", err)
	}
	return nil
}

// ListTeacherEvalLogs returns the most recent limit rows, oldest first;
// limit<=0 means 50.
func (s *Store) ListTeacherEvalLogs(ctx context.Context, taskID string, limit int) ([]store.TeacherEvalLog, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT seq, task_id, at, message FROM teacher_eval_task_logs
		WHERE task_id = $1 ORDER BY seq DESC LIMIT $2`, taskID, limit)
	if err != nil {
		return nil, fmt.Errorf("pg: list teacher eval logs: %w", err)
	}
	defer rows.Close()
	var out []store.TeacherEvalLog
	for rows.Next() {
		var l store.TeacherEvalLog
		if err := rows.Scan(&l.Seq, &l.TaskID, &l.At, &l.Message); err != nil {
			return nil, fmt.Errorf("pg: scan teacher eval log: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("pg: list teacher eval logs: %w", err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// SetTeacherEvalCourses replaces all course rows of the task in one
// transaction. ErrNotFound if the task does not exist.
func (s *Store) SetTeacherEvalCourses(ctx context.Context, taskID string, courses []store.TeacherEvalTaskCourse) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("pg: set teacher eval courses: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM teacher_eval_tasks WHERE id = $1)`, taskID).Scan(&exists); err != nil {
		return fmt.Errorf("pg: set teacher eval courses: %w", err)
	}
	if !exists {
		return store.ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM teacher_eval_task_courses WHERE task_id = $1`, taskID); err != nil {
		return fmt.Errorf("pg: clear teacher eval courses: %w", err)
	}
	for _, c := range courses {
		if _, err := tx.Exec(ctx, `
			INSERT INTO teacher_eval_task_courses(`+teacherEvalCourseCols+`)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
			taskID, c.CourseKey, c.Name, c.Teacher, c.Position, c.Status, c.ErrorKind,
			c.Message, c.SubmitAttempts, nullableTime(c.SubmittedAt), c.UpdatedAt); err != nil {
			return fmt.Errorf("pg: insert teacher eval course: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("pg: set teacher eval courses commit: %w", err)
	}
	return nil
}

// scoreModeOrDefault maps the empty score mode to "random".
func scoreModeOrDefault(m string) string {
	if m == "" {
		return "random"
	}
	return m
}
