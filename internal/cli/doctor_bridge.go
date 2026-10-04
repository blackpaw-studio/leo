package cli

import (
	"context"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// doctorAgentListTimeout bounds the daemon call listing agents' bridges.
const doctorAgentListTimeout = 5 * time.Second

// bridgeDoctorDeps are reportBridge's effects, injected for tests.
type bridgeDoctorDeps struct {
	lookClaude func() (string, error)
	probe      bridgemod.VersionProbe
	daemonUp   func(home string) bool
	listAgents func(ctx context.Context, home string) ([]agent.Record, error)
}

func defaultBridgeDoctorDeps() bridgeDoctorDeps {
	return bridgeDoctorDeps{
		lookClaude: func() (string, error) { return exec.LookPath(claudeharness.Claude{}.Binary()) },
		probe:      bridgemod.ProbeClaudeVersion,
		daemonUp:   daemon.IsRunning,
		listAgents: daemon.AgentList,
	}
}

// reportBridge prints whether claude agents get the leo bridge: the
// installed claude's version against the mods API's floor and the release
// the mod was verified on, then each live claude agent's bridge.
func reportBridge(ctx context.Context, home string, deps bridgeDoctorDeps) {
	reportClaudeForBridge(ctx, deps)
	if !deps.daemonUp(home) {
		return
	}
	listCtx, cancel := context.WithTimeout(ctx, doctorAgentListTimeout)
	defer cancel()
	records, err := deps.listAgents(listCtx, home)
	if err != nil {
		warn.Printf("Agent bridges:  %s\n", err)
		return
	}
	header := false
	for _, r := range records {
		if r.Bridge == "" {
			continue
		}
		if !header {
			info.Println("Agent bridges:")
			header = true
		}
		if r.Bridge == agent.BridgeConnected {
			success.Printf("  %s: bridge %s\n", r.Name, r.Bridge)
		} else {
			warn.Printf("  %s: bridge %s\n", r.Name, r.Bridge)
		}
	}
}

func reportClaudeForBridge(ctx context.Context, deps bridgeDoctorDeps) {
	path, err := deps.lookClaude()
	if err != nil {
		info.Printf("Claude Code:    not found (%s); leo bridge: off\n", err)
		return
	}
	raw, err := deps.probe(ctx, path)
	var v bridgemod.Version
	if err == nil {
		v, err = bridgemod.ParseVersion(raw)
	}
	switch {
	case err != nil:
		warn.Printf("Claude Code:    version unreadable (%s); leo bridge: off\n", err)
	case !bridgemod.SupportsMods(v):
		info.Printf("Claude Code:    %s, older than %s (the mods API); leo bridge: off\n", v, bridgemod.MinClaudeVersion)
	case bridgemod.NewerThanTested(v):
		warn.Printf("Claude Code:    %s; leo bridge: on, but newer than %s, the release it was verified on\n", v, bridgemod.TestedClaudeVersion)
		warn.Println("  The mods API may have changed. If agents stop taking messages, check")
		warn.Println("  'leo agent list' for bridges stuck absent and the service log for bridge errors.")
	default:
		success.Printf("Claude Code:    %s; leo bridge: on\n", v)
	}
}
