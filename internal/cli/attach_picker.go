package cli

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/picker"
)

// agentListFn is a testability seam for daemon.AgentList — tests override this
// to simulate the daemon's agent list without spinning up a real socket. Used
// by `leo agent list` (via the seam) and by the attach picker's daemon-down
// fail-fast probe.
var agentListFn = daemon.AgentList

// pickerRunFn is a testability seam for picker.Run — tests override this to
// avoid driving the real full-screen Bubble Tea program.
var pickerRunFn = picker.Run

// attachChoiceKind distinguishes what an attach target resolves to, so the
// attach path can route non-claude agents through their SessionDriver instead
// of assuming every target is a tmux session.
type attachChoiceKind int

const (
	attachChoiceAgent attachChoiceKind = iota
	attachChoiceRemote
)

// attachChoice is a resolved attach target: a human label, the tmux session
// name to fall back to for claude targets, and enough identity (kind + bare
// name) to resolve a non-claude harness's driver attach spec.
type attachChoice struct {
	label   string
	session string
	kind    attachChoiceKind
	name    string // bare agent name; empty for remote rows
}

// runAttachPicker handles `leo attach` / `leo agent attach` with no positional
// arg. It opens the full-screen Bubble Tea picker over all agents (local daemon
// + every configured client.hosts entry), then attaches to the chosen agent
// after the TUI exits. The picker is always shown when no name is given — the
// former single-candidate auto-attach is intentionally gone, because the picker
// is now the management surface too.
func runAttachPicker(ctx context.Context, cfg *config.Config, _ config.HostResolution, opts attachOptions) error {
	if !stdinIsTerminal() {
		return fmt.Errorf("no session name given and stdin is not a terminal — pass a name explicitly")
	}

	// Probe the local daemon before entering alt-screen — a blank picker over a
	// dead daemon is worse than a clear error. On a client-only machine (remote
	// hosts configured, no local daemon) a dead local daemon is not fatal: skip
	// the local backend and proceed with the configured SSH backends instead.
	// Only fail fast when there is nowhere else to look, i.e. no remote hosts
	// are configured either.
	_, localErr := agentListFn(ctx, cfg.HomePath)
	if localErr != nil && len(cfg.Client.Hosts) == 0 {
		return fmt.Errorf("cannot reach the leo daemon (is 'leo service' running?): %w", localErr)
	}

	backends := buildPickerBackends(cfg, localErr == nil)
	// The gate rides with the picker, not with a backend: it encodes THIS
	// process's permissions, and a remote leo cannot see them.
	result, err := pickerRunFn(ctx, backends, func(template string) error {
		return gateTemplateSwitch("leo attach: set template", template)
	})
	if err != nil {
		return fmt.Errorf("picker: %w", err)
	}
	if result.Agent == nil {
		return nil // quit without attaching
	}
	return attachPickedAgent(ctx, cfg, *result.Agent, opts)
}

// localTemplateNames lists the local host's configured templates for the
// picker's template menu, sorted so the menu order is stable across openings
// (config.Templates is a map, whose range order is not).
func localTemplateNames(cfg *config.Config) ([]string, error) {
	names := make([]string, 0, len(cfg.Templates))
	for name := range cfg.Templates {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// buildPickerBackends assembles one backend per host: the local daemon under
// picker.LocalHost (only when includeLocal is true — the caller sets this to
// false when the local daemon probe failed but remote hosts are configured,
// so a dead local daemon doesn't produce a phantom error row), plus an SSH
// backend for every configured client.hosts entry.
func buildPickerBackends(cfg *config.Config, includeLocal bool) map[string]picker.Backend {
	backends := map[string]picker.Backend{}
	if includeLocal {
		backends[picker.LocalHost] = picker.NewLocalBackend(cfg.HomePath, func() ([]string, error) {
			return localTemplateNames(cfg)
		})
	}
	for name := range cfg.Client.Hosts {
		res, err := cfg.ResolveHost(name)
		if err != nil {
			continue // skip a host that fails to resolve rather than aborting the picker
		}
		r := res // capture per-iteration copy for the closure
		backends[name] = picker.NewSSHBackend(
			name,
			r.Host.RemoteLeoPath(),
			r.Host.RemoteTmuxPath(),
			func(tail ...string) []string { return buildSSHArgs(r, tail...) },
		)
	}
	return backends
}

// attachPickedAgent routes the chosen agent to the correct attach path. Local
// agents go through the existing attachChosenSession flow (driver-aware); remote
// agents delegate the whole `agent attach <name>` invocation to the remote leo
// over SSH so it does its own resolution and driver routing.
func attachPickedAgent(ctx context.Context, cfg *config.Config, a picker.Agent, opts attachOptions) error {
	if a.Host == picker.LocalHost {
		// Dormant agents have no tmux session to attach to yet — prompt to
		// start (or fail fast off a TTY) before anything else, same as the
		// two cobra-command attach doors. The picker has no cobra.Command to
		// gate through (its in-place actions run inside `leo attach` itself),
		// so it uses ensureAgentRunningForPicker's label-based gate instead
		// — see that function's doc comment. Running this before
		// attachChosenSession also means its attach-spec lookup happens
		// AFTER a just-started agent's ResolveHandle can actually resolve,
		// matching attachLocal's fix for the same ordering trap.
		ok, err := ensureAgentRunningForPicker(ctx, "leo attach: start agent", cfg.HomePath, a.Name, a.Status == "stopped")
		if err != nil {
			return err
		}
		if !ok {
			return nil
		}
		choice := attachChoice{
			label:   a.Name,
			session: agent.SessionName(a.Name),
			kind:    attachChoiceAgent,
			name:    a.Name,
		}
		return attachChosenSession(ctx, cfg, config.HostResolution{Localhost: true}, choice, opts)
	}
	res, err := cfg.ResolveHost(a.Host)
	if err != nil {
		return fmt.Errorf("resolving host %q: %w", a.Host, err)
	}
	if a.AttachOnly {
		// tmux-fallback row: a.Name is already the full remote tmux session
		// name (e.g. "leo-foo"), not a bare agent name — attach the session
		// directly instead of routing through `agent attach <name>`, which
		// expects a bare name and would fail to resolve.
		choice := attachChoice{
			label:   a.Name,
			session: a.Name,
			kind:    attachChoiceRemote,
		}
		return attachChosenSession(ctx, cfg, res, choice, opts)
	}
	return runRemoteAttach(res, "agent", "attach", a.Name)
}

// attachChosenSession dispatches a resolved attachChoice: a non-claude agent
// (localhost only) routes through its SessionDriver; everything else keeps the
// tmux attach flow.
func attachChosenSession(ctx context.Context, cfg *config.Config, res config.HostResolution, choice attachChoice, opts attachOptions) error {
	if res.Localhost {
		switch choice.kind {
		case attachChoiceAgent:
			if spec, err := agentAttachSpecFn(ctx, cfg.HomePath, choice.name); err == nil && spec.Harness != "" && spec.Harness != "claude" {
				return attachViaDriver(res, toAttachSpec(spec), opts)
			}
		case attachChoiceRemote:
			// No per-row identity to resolve against — always tmux.
		}
	}
	return attachTmuxSession(res, choice.session, opts)
}

// stdinIsTerminal reports whether os.Stdin is an interactive TTY. Indirected as
// a var so tests can simulate a pipe or terminal.
var stdinIsTerminal = defaultStdinIsTerminal

func defaultStdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
