package pg

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

const deviceGrantCols = `id, device_code_hash, user_code_hash, client_name, platform, status,
		user_id, attempts, interval_s, last_polled_at, expires_at, created_at`

func (s *Store) CreateDeviceGrant(ctx context.Context, g store.DeviceGrant) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO device_grants(id, device_code_hash, user_code_hash, client_name, platform, status,
			user_id, attempts, interval_s, last_polled_at, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)`,
		g.ID, g.DeviceCodeHash, g.UserCodeHash, g.ClientName, g.Platform, g.Status,
		g.UserID, g.Attempts, g.Interval, nullableTime(g.LastPolledAt), g.ExpiresAt, g.CreatedAt)
	if err != nil {
		return fmt.Errorf("pg: create device grant: %w", err)
	}
	return nil
}

func (s *Store) GetDeviceGrant(ctx context.Context, id string) (*store.DeviceGrant, error) {
	return s.getDeviceGrant(ctx, `WHERE id = $1`, id)
}

func (s *Store) GetDeviceGrantByDeviceCode(ctx context.Context, codeHash string) (*store.DeviceGrant, error) {
	return s.getDeviceGrant(ctx, `WHERE device_code_hash = $1`, codeHash)
}

func (s *Store) GetDeviceGrantByUserCode(ctx context.Context, codeHash string) (*store.DeviceGrant, error) {
	return s.getDeviceGrant(ctx, `WHERE user_code_hash = $1`, codeHash)
}

func (s *Store) getDeviceGrant(ctx context.Context, where string, arg any) (*store.DeviceGrant, error) {
	g, err := scanDeviceGrant(s.pool.QueryRow(ctx,
		`SELECT `+deviceGrantCols+` FROM device_grants `+where, arg))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("pg: get device grant: %w", err)
	}
	return &g, nil
}

// ApproveDeviceGrant moves a pending grant to approved while it is unexpired
// at time at. It reports false when the grant is not pending or has expired.
func (s *Store) ApproveDeviceGrant(ctx context.Context, id, userID string, at time.Time) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE device_grants SET status = 'approved', user_id = $2
		WHERE id = $1 AND status = 'pending' AND expires_at > $3`,
		id, userID, at)
	if err != nil {
		return false, fmt.Errorf("pg: approve device grant: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ConsumeDeviceGrant moves an approved grant to consumed. It reports false
// when the grant was already consumed or was never approved.
func (s *Store) ConsumeDeviceGrant(ctx context.Context, id string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE device_grants SET status = 'consumed' WHERE id = $1 AND status = 'approved'`, id)
	if err != nil {
		return false, fmt.Errorf("pg: consume device grant: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func (s *Store) RecordDeviceGrantPoll(ctx context.Context, id string, at time.Time, interval int) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE device_grants SET last_polled_at = $2, interval_s = $3 WHERE id = $1`,
		id, at, interval)
	if err != nil {
		return fmt.Errorf("pg: record device grant poll: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return store.ErrNotFound
	}
	return nil
}

func (s *Store) IncrementDeviceGrantAttempts(ctx context.Context, id string) (int, error) {
	var attempts int
	err := s.pool.QueryRow(ctx,
		`UPDATE device_grants SET attempts = attempts + 1 WHERE id = $1 RETURNING attempts`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, store.ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("pg: increment device grant attempts: %w", err)
	}
	return attempts, nil
}

func (s *Store) PurgeDeviceGrants(ctx context.Context, before time.Time) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM device_grants WHERE expires_at < $1`, before); err != nil {
		return fmt.Errorf("pg: purge device grants: %w", err)
	}
	return nil
}

func scanDeviceGrant(row interface{ Scan(dest ...any) error }) (store.DeviceGrant, error) {
	var g store.DeviceGrant
	var lastPolled *time.Time
	err := row.Scan(&g.ID, &g.DeviceCodeHash, &g.UserCodeHash, &g.ClientName, &g.Platform, &g.Status,
		&g.UserID, &g.Attempts, &g.Interval, &lastPolled, &g.ExpiresAt, &g.CreatedAt)
	g.LastPolledAt = timeOrZero(lastPolled)
	return g, err
}
