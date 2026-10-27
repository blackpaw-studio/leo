package daemon

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/follow"
	"github.com/blackpaw-studio/leo/internal/web"
)

// Follower is the daemon's follow loop (follow.Loop) as the server uses it:
// SyncNow serves `leo sync` and the web Sync button, Kick tells it a
// reload may have changed follow settings.
type Follower interface {
	SyncNow(ctx context.Context) (follow.Outcome, error)
	Kick()
}

// SetFollower attaches the follow loop. Call it before Start, so the config
// API's sync route sees it.
func (s *Server) SetFollower(f Follower) { s.follower = f }

// followSyncer exposes the follower to the shared config service; nil when
// none is attached.
func (s *Server) followSyncer() web.FollowSyncer {
	if s.follower == nil {
		return nil
	}
	return s.follower
}
