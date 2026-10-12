package service

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/push"
)

// wirePush wraps next so every attention transition into a needs-you state
// also pushes to the phones paired under stateDir (see internal/push). The
// returned publisher forwards to next unchanged; if the daemon id cannot be
// established, push is skipped and next is returned as is. allow filters which
// transitions push (see newPushPolicy); nil pushes every one.
func wirePush(ctx context.Context, next observe.Publisher, stateDir string, allow func(agent, state string) bool, opts ...push.RelayOption) observe.Publisher {
	daemonID, err := devices.EnsureDaemonID(stateDir)
	if err != nil {
		log.Printf("push disabled: %v", err)
		return next
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	opts = append([]push.RelayOption{push.WithLogger(logger)}, opts...)
	sender := push.NewRelaySender(push.NewStoreRegistry(devices.NewStore(stateDir, nil)), daemonID, opts...)
	var notifierOpts []push.NotifierOption
	if allow != nil {
		notifierOpts = append(notifierOpts, push.WithPolicy(allow))
	}
	notifier := push.NewNotifier(next, sender, notifierOpts...)
	notifier.Start(ctx)
	return notifier
}

// newPushPolicy builds the notifier's filter from the live config: each call
// reloads it through load, resolves the agent's template from the agent store
// under homePath, and asks Config.PushEnabled. Transitions are rare, so
// reading per call is cheap and picks up web or hand edits without a restart.
// An agent with no template record uses defaults. Missing a needs-permission
// push is worse than an extra one, so any failure to decide is logged and
// sends.
func newPushPolicy(load func() (*config.Config, error), homePath string) func(agent, state string) bool {
	return func(agent, state string) bool {
		cfg, err := load()
		if err != nil {
			log.Printf("push policy: loading config, sending %s alert for %s: %v", state, agent, err)
			return true
		}
		records, err := agentstore.Load(agentstore.FilePath(homePath))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Printf("push policy: loading agent store, sending %s alert for %s: %v", state, agent, err)
			return true
		}
		return cfg.PushEnabled(records[agent].Template, state)
	}
}
