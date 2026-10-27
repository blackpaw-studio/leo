package web

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/follow"
)

// Stable error classes of the follow routes.
const (
	CodeNotFollowing      = "not_following"
	CodeFollowUnavailable = "follow_unavailable"
)

// FollowSyncer runs a follower's pull now (the daemon's follow loop).
type FollowSyncer interface {
	SyncNow(ctx context.Context) (follow.Outcome, error)
}

// followSyncer is the attached follow loop, nil when none runs.
func (c *ConfigService) followSyncer() FollowSyncer {
	if c.follower == nil {
		return nil
	}
	return c.follower()
}

func (c *ConfigService) registerFollowRoutes(route func(string, http.HandlerFunc)) {
	route("GET /config/follow", c.handleFollowStatusAPI)
	route("POST /config/follow/sync", c.handleFollowSyncAPI)
}

// followReport loads the config fresh and describes its follow state.
func (c *ConfigService) followReport() (follow.Report, error) {
	cfg, err := c.loadConfig()
	if err != nil {
		return follow.Report{}, err
	}
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	return follow.BuildReport(cfg, now()), nil
}

func (c *ConfigService) handleFollowStatusAPI(w http.ResponseWriter, _ *http.Request) {
	report, err := c.followReport()
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeConfigData(w, http.StatusOK, report)
}

// followSyncResult is POST /config/follow/sync's success body.
type followSyncResult struct {
	Changed bool          `json:"changed"`
	Status  follow.Report `json:"status"`
}

// SyncFollow pulls the lead now through the daemon's follow loop and returns
// the resulting status. A failed pull is a 502 carrying the follow error code
// (docs/configuration/follow-lead.md) and the status in data.
func (c *ConfigService) SyncFollow(ctx context.Context) (followSyncResult, error) {
	syncer := c.followSyncer()
	if syncer == nil {
		return followSyncResult{}, &ConfigError{Status: http.StatusServiceUnavailable, Code: CodeFollowUnavailable,
			Message: "the follow loop is not running in this daemon"}
	}
	out, err := syncer.SyncNow(ctx)
	if errors.Is(err, follow.ErrNotFollowing) {
		return followSyncResult{}, &ConfigError{Status: http.StatusConflict, Code: CodeNotFollowing, Message: err.Error()}
	}
	report, rerr := c.followReport()
	var fe *config.FollowError
	if errors.As(err, &fe) {
		return followSyncResult{}, &ConfigError{Status: http.StatusBadGateway, Code: string(fe.Code), Message: fe.Error(), Data: report}
	}
	if err != nil {
		return followSyncResult{}, err
	}
	if rerr != nil {
		return followSyncResult{}, rerr
	}
	return followSyncResult{Changed: out.Changed, Status: report}, nil
}

// followSyncWriteTimeout is a sync response's write deadline: a pull may
// take 30s, the servers' whole WriteTimeout, before the reload after it.
const followSyncWriteTimeout = 60 * time.Second

// allowSlowSync extends the server's write deadline for a sync request, so
// a slow pull is answered instead of cut off.
func allowSlowSync(w http.ResponseWriter) {
	_ = http.NewResponseController(w).SetWriteDeadline(time.Now().Add(followSyncWriteTimeout))
}

func (c *ConfigService) handleFollowSyncAPI(w http.ResponseWriter, r *http.Request) {
	allowSlowSync(w)
	res, err := c.SyncFollow(r.Context())
	if err != nil {
		writeConfigError(w, err)
		return
	}
	writeConfigData(w, http.StatusOK, res)
}
