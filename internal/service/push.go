package service

import (
	"context"
	"log"
	"os"

	"github.com/blackpaw-studio/leo/internal/devices"
	"github.com/blackpaw-studio/leo/internal/observe"
	"github.com/blackpaw-studio/leo/internal/push"
)

// wirePush wraps next so every attention transition into a needs-you state
// also pushes to the phones paired under stateDir (see internal/push). The
// returned publisher forwards to next unchanged; if the daemon id cannot be
// established, push is skipped and next is returned as is.
func wirePush(ctx context.Context, next observe.Publisher, stateDir string, opts ...push.RelayOption) observe.Publisher {
	daemonID, err := devices.EnsureDaemonID(stateDir)
	if err != nil {
		log.Printf("push disabled: %v", err)
		return next
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	opts = append([]push.RelayOption{push.WithLogger(logger)}, opts...)
	sender := push.NewRelaySender(push.NewStoreRegistry(devices.NewStore(stateDir, nil)), daemonID, opts...)
	notifier := push.NewNotifier(next, sender)
	notifier.Start(ctx)
	return notifier
}
