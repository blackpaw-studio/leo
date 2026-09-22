package service

import (
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

// persistAttentionHooks records on the agent's store record whether its
// current session was launched with attention hooks, so an adopt after a
// daemon restart knows whether the agent has an attention source. Records
// that don't exist (non-agent specs, tests) are left alone.
func persistAttentionHooks(homePath, name string, hooked bool) {
	if agentAttentionHooks(homePath, name) == hooked {
		return
	}
	_ = agentstore.Update(homePath, name, func(r agentstore.Record) agentstore.Record {
		r.AttentionHooks = hooked
		return r
	})
}

// agentAttentionHooks reads the persisted attention_hooks flag.
func agentAttentionHooks(homePath, name string) bool {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return false
	}
	return records[name].AttentionHooks
}
