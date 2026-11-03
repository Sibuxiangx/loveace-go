package mem

import (
	"context"
	"errors"
	"time"

	"github.com/Sibuxiangx/loveace-go/server/internal/store"
)

// CreateDeviceGrant stores a grant. Code hashes must be unique.
func (s *Store) CreateDeviceGrant(_ context.Context, g store.DeviceGrant) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, other := range s.deviceGrants {
		if other.DeviceCodeHash == g.DeviceCodeHash || other.UserCodeHash == g.UserCodeHash {
			return errors.New("store: device grant code already in use")
		}
	}
	cp := g
	s.deviceGrants[g.ID] = &cp
	return nil
}

// GetDeviceGrant returns a copy of the grant with the given id.
func (s *Store) GetDeviceGrant(_ context.Context, id string) (*store.DeviceGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.deviceGrants[id]
	if !ok {
		return nil, store.ErrNotFound
	}
	cp := *g
	return &cp, nil
}

// GetDeviceGrantByDeviceCode finds the grant whose device code hashes to codeHash.
func (s *Store) GetDeviceGrantByDeviceCode(_ context.Context, codeHash string) (*store.DeviceGrant, error) {
	return s.findDeviceGrant(func(g *store.DeviceGrant) bool { return g.DeviceCodeHash == codeHash })
}

// GetDeviceGrantByUserCode finds the grant whose user code hashes to codeHash.
func (s *Store) GetDeviceGrantByUserCode(_ context.Context, codeHash string) (*store.DeviceGrant, error) {
	return s.findDeviceGrant(func(g *store.DeviceGrant) bool { return g.UserCodeHash == codeHash })
}

func (s *Store) findDeviceGrant(match func(*store.DeviceGrant) bool) (*store.DeviceGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, g := range s.deviceGrants {
		if match(g) {
			cp := *g
			return &cp, nil
		}
	}
	return nil, store.ErrNotFound
}

// ApproveDeviceGrant approves a pending grant that has not expired at `at`.
func (s *Store) ApproveDeviceGrant(_ context.Context, id, userID string, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.deviceGrants[id]
	if !ok || g.Status != store.DeviceGrantPending || !at.Before(g.ExpiresAt) {
		return false, nil
	}
	g.Status = store.DeviceGrantApproved
	g.UserID = userID
	return true, nil
}

// ConsumeDeviceGrant moves an approved grant to consumed; a grant can be
// consumed only once.
func (s *Store) ConsumeDeviceGrant(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.deviceGrants[id]
	if !ok || g.Status != store.DeviceGrantApproved {
		return false, nil
	}
	g.Status = store.DeviceGrantConsumed
	return true, nil
}

// RecordDeviceGrantPoll stores the poll time and the interval the device must keep.
func (s *Store) RecordDeviceGrantPoll(_ context.Context, id string, at time.Time, interval int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.deviceGrants[id]
	if !ok {
		return store.ErrNotFound
	}
	g.LastPolledAt = at
	g.Interval = interval
	return nil
}

// IncrementDeviceGrantAttempts counts one failed credential attempt.
func (s *Store) IncrementDeviceGrantAttempts(_ context.Context, id string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, ok := s.deviceGrants[id]
	if !ok {
		return 0, store.ErrNotFound
	}
	g.Attempts++
	return g.Attempts, nil
}

// PurgeDeviceGrants drops grants that expired before `before`.
func (s *Store) PurgeDeviceGrants(_ context.Context, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, g := range s.deviceGrants {
		if g.ExpiresAt.Before(before) {
			delete(s.deviceGrants, id)
		}
	}
	return nil
}
