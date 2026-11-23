package teachereval

import (
	"context"

	tecore "github.com/Sibuxiangx/loveace-go/core/teachereval"
)

// RemoteFactory resolves a user's live school session for one step. Every step goes through it so
// that the session manager sees the activity and recovers expired sessions before any call that
// cannot be replayed.
type RemoteFactory interface {
	For(ctx context.Context, userID, deviceID string) (tecore.Remote, error)
}

// unavailableFactory is used when no school binding is configured. Status and admission then report
// the school as unreachable instead of panicking.
type unavailableFactory struct{}

func (unavailableFactory) For(context.Context, string, string) (tecore.Remote, error) {
	return nil, tecore.ErrSessionUnavailable
}
