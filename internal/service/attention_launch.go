package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// reportCmdFor is the hook callback argv for the leo that executable
// resolves. It must stay `<leo> dispatch report`: codex trusts hooks by hash,
// and interactive dispatch installs the identical command. A failed resolve
// falls back to the bare command, found through the agent's PATH.
func reportCmdFor(executable func() (string, error)) []string {
	exe, err := executable()
	if err != nil {
		return []string{"leo", "dispatch", "report"}
	}
	return []string{exe, "dispatch", "report"}
}

// reportCmdOrDefault is the supervisor's hook callback argv, or the bare
// `leo dispatch report` when the daemon never set one.
func (sv *Supervisor) reportCmdOrDefault() []string {
	if sv.reportCmd == nil {
		return []string{"leo", "dispatch", "report"}
	}
	return sv.reportCmd
}

// attentionLaunchArgs returns the argv for one fresh agent launch with the
// driver's attention hooks applied, and whether hooks are in place. Failure
// is logged and the launch proceeds unhooked.
func attentionLaunchArgs(drv harness.SessionDriver, h harness.SessionHandle, args []string, spec ProcessSpec, name string, reportCmd []string) ([]string, bool) {
	if spec.Kind != harness.KindAgent {
		return args, false
	}
	hooker, ok := drv.(harness.AttentionHooker)
	if !ok {
		return args, false
	}
	out, supported, err := hooker.AttentionLaunch(h, args, reportCmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] attention hooks unavailable: %v\n", name, err)
		return args, false
	}
	if !supported {
		return args, false
	}
	return out, true
}

// spawnHooked predicts whether an agent spawn will launch with attention
// hooks: an adopt keeps the surviving launch's token, a fresh launch hooks
// when the driver supports it. A later launch-time failure drops the preset.
func spawnHooked(drv harness.SessionDriver, homePath, name string, adopt bool) bool {
	if adopt {
		return storedAttentionToken(homePath, name) != ""
	}
	hooker, ok := drv.(harness.AttentionHooker)
	return ok && hooker.AttentionSupported()
}

// newAttentionToken returns a fresh random per-launch attention token.
func newAttentionToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generating attention token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// storedAttentionToken reads the attention token persisted for name's
// current launch ("" when unhooked or record-less).
func storedAttentionToken(homePath, name string) string {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return ""
	}
	return records[name].AttentionToken
}

// persistAttentionToken stores token as name's current launch token BEFORE
// the session is created, so an adopt after a daemon restart re-registers
// exactly the live launch's token. An error means the launch must go
// unhooked.
func persistAttentionToken(homePath, name, token string) error {
	if storedAttentionToken(homePath, name) == token {
		return nil
	}
	return agentstore.SetAttentionToken(homePath, name, token)
}

// launchAttentionToken mints and persists this launch's token. ok=false
// (logged) means the launch proceeds unhooked and nothing is registered.
func launchAttentionToken(homePath, name string) (string, bool) {
	token, err := newAttentionToken()
	if err == nil {
		err = persistAttentionToken(homePath, name, token)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] attention hooks unavailable: persisting launch token: %v\n", name, err)
		return "", false
	}
	return token, true
}

// attentionFilePath is where the attention store persists at-rest attention.
func attentionFilePath(homePath string) string {
	return filepath.Join(homePath, "state", "attention.json")
}

// attentionFlushBound caps how long shutdown waits for the attention file's
// final write, so a stuck disk cannot hang the daemon's exit.
const attentionFlushBound = 2 * time.Second

// awaitBounded runs wait, returning when it does or after bound (timer is the
// injected clock, time.After in production), logging when the bound was hit.
func awaitBounded(what string, wait func(), bound time.Duration, timer func(time.Duration) <-chan time.Time, logf func(string, ...any)) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		wait()
	}()
	select {
	case <-done:
	case <-timer(bound):
		logf("shutdown: %s not finished after %s; exiting without it", what, bound)
	}
}

// awaitAttentionFlush is the shutdown step that holds the daemon's exit for
// the attention store's final flush (see AttentionStore.WaitPersistence).
func awaitAttentionFlush(store *observe.AttentionStore, timer func(time.Duration) <-chan time.Time) {
	awaitBounded("attention flush", store.WaitPersistence, attentionFlushBound, timer, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	})
}
