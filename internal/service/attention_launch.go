package service

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// attentionReportCmd is the hook callback argv. It must stay
// `<leo> dispatch report`: codex trusts hooks by hash, and interactive
// dispatch installs the identical command. A seam for tests.
var attentionReportCmd = func() []string {
	exe, err := os.Executable()
	if err != nil {
		return []string{"leo", "dispatch", "report"}
	}
	return []string{exe, "dispatch", "report"}
}

// attentionLaunchArgs returns the argv for one fresh agent launch with the
// driver's attention hooks applied, and whether hooks are in place. Failure
// is logged and the launch proceeds unhooked.
func attentionLaunchArgs(drv harness.SessionDriver, h harness.SessionHandle, args []string, spec ProcessSpec, name string) ([]string, bool) {
	if spec.Kind != harness.KindAgent {
		return args, false
	}
	hooker, ok := drv.(harness.AttentionHooker)
	if !ok {
		return args, false
	}
	out, supported, err := hooker.AttentionLaunch(h, args, attentionReportCmd())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[%s] attention hooks unavailable: %v\n", name, err)
		return args, false
	}
	if !supported {
		return args, false
	}
	return out, true
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

// persistAttentionToken records token as name's current launch token so an
// adopt after a daemon restart can re-register it. Record-less specs (tests,
// shared processes) are left alone.
func persistAttentionToken(homePath, name, token string) {
	if storedAttentionToken(homePath, name) == token {
		return
	}
	_ = agentstore.SetAttentionToken(homePath, name, token)
}
