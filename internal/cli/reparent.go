// `leo service reparent` — the explicit, operator-chosen repair for a tmux
// server whose Local Network attribution has lapsed.
//
// Leo adopts a surviving tmux server across daemon restarts on purpose: that
// is what keeps agent sessions alive through `leo update`. The cost is that
// the signed leo process macOS holds responsible for every agent pane can be
// long gone, and when the grant lapses agents lose LAN access while leo itself
// looks perfectly healthy (see internal/tmux/ownership.go and `leo doctor`).
//
// Recycling the server restores the invariant but terminates every live agent
// session, so it is never done automatically — an automatic respawn would fire
// on every `leo update` and bounce every agent. This command makes the repair
// available, states its cost up front, and confirms before acting.
package cli

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/spf13/cobra"
)

// reparentWaitTimeout / reparentWaitPoll bound how long we wait for the
// daemon's supervise loop (which polls every 2s) to notice the server is gone
// and start a fresh one.
const (
	reparentWaitTimeout = 30 * time.Second
	reparentWaitPoll    = 500 * time.Millisecond
)

// tmuxTreeReport is the operator-facing summary of who owns leo's tmux server.
type tmuxTreeReport struct {
	Line string
	// OwnerVerified is true only when a live process is confirmed to be the
	// one that created the server.
	OwnerVerified bool
	// ServerPresent distinguishes "nothing running" from "running, owner
	// unknown or gone".
	ServerPresent bool
}

// describeTmuxTree renders an Ownership (and any lookup error) as one line of
// operator output.
//
// A dead owner is stated as a fact, not an alarm: it is the expected state
// after any daemon restart, and on a healthy machine agents keep working. It
// becomes actionable only when an in-tree probe actually fails, which is why
// the escalation lives in `leo doctor` rather than here.
func describeTmuxTree(own tmux.Ownership, err error) tmuxTreeReport {
	if err != nil {
		return tmuxTreeReport{Line: "no tmux server running on leo's socket"}
	}

	switch {
	case own.OwnerLive:
		return tmuxTreeReport{
			Line:          fmt.Sprintf("server pid %d, owned by live leo pid %d", own.ServerPID, own.OwnerPID),
			OwnerVerified: true,
			ServerPresent: true,
		}
	case own.Stamped:
		return tmuxTreeReport{
			Line: fmt.Sprintf("server pid %d, adopted — creating leo process (pid %d) has exited; "+
				"Local Network attribution is no longer verifiable", own.ServerPID, own.OwnerPID),
			ServerPresent: true,
		}
	default:
		return tmuxTreeReport{
			Line: fmt.Sprintf("server pid %d, owner unknown (predates ownership tracking)", own.ServerPID),

			ServerPresent: true,
		}
	}
}

// reparentDeps injects everything reparentServer touches, so the decision
// logic around a destructive action is testable without a daemon, a tmux
// server, or a terminal.
type reparentDeps struct {
	daemonRunning func() bool
	locate        func() (string, error)
	serverRunning func(tmuxPath string) bool
	ownership     func(tmuxPath string) (tmux.Ownership, error)
	killServer    func(tmuxPath string) error
	counts        func() (running, dormant int)
	confirm       func(message string) bool
	waitForOwner  func(tmuxPath string) (tmux.Ownership, bool)
	out           io.Writer
	assumeYes     bool
	force         bool
}

func reparentServer(deps reparentDeps) error {
	// Without a running daemon nothing would respawn the server, so this
	// would just destroy every session and leave the socket empty.
	if !deps.daemonRunning() {
		return fmt.Errorf("daemon is not running: start it with 'leo service start' — " +
			"a fresh tmux server is created at daemon startup anyway")
	}

	tmuxPath, err := deps.locate()
	if err != nil {
		return err
	}

	if !deps.serverRunning(tmuxPath) {
		fmt.Fprintln(deps.out, "no tmux server on leo's socket; the daemon will create one — nothing to repair")
		return nil
	}

	// Not describeTmuxTree's error branch: that reads an ownership-probe
	// failure as "no server", which is true for doctor/status but flatly false
	// here — serverRunning just confirmed one, and saying otherwise right
	// before a destructive step would misinform the operator.
	own, ownErr := deps.ownership(tmuxPath)
	report := describeTmuxTree(own, nil)
	if ownErr != nil {
		report = tmuxTreeReport{
			Line:          fmt.Sprintf("server is running but its ownership could not be read: %s", ownErr),
			ServerPresent: true,
		}
	}
	fmt.Fprintf(deps.out, "tmux tree: %s\n", report.Line)

	if report.OwnerVerified && !deps.force {
		fmt.Fprintln(deps.out, "The server is already owned by a live leo process — nothing to repair. "+
			"Pass --force to recycle it anyway.")
		return nil
	}

	running, dormant := deps.counts()
	if !deps.assumeYes {
		msg := fmt.Sprintf("Recycle the tmux server? This terminates %d live agent session(s); "+
			"%d already-dormant agent(s) are unaffected", running, dormant)
		if !deps.confirm(msg) {
			fmt.Fprintln(deps.out, "Aborted.")
			return nil
		}
	}

	if err := deps.killServer(tmuxPath); err != nil {
		return fmt.Errorf("recycling tmux server: %w", err)
	}
	fmt.Fprintln(deps.out, "tmux server killed; waiting for the daemon to start a fresh one...")

	fresh, ok := deps.waitForOwner(tmuxPath)
	if !ok {
		return fmt.Errorf("no daemon-owned tmux server appeared within %s — run 'leo service restart'",
			reparentWaitTimeout)
	}

	fmt.Fprintf(deps.out, "new tmux server pid %d, owned by live leo pid %d\n", fresh.ServerPID, fresh.OwnerPID)
	return nil
}

// waitForOwnedServer polls until a server with a live owner appears, i.e. the
// daemon's supervise loop has replaced the one we killed.
func waitForOwnedServer(tmuxPath string) (tmux.Ownership, bool) {
	deadline := time.Now().Add(reparentWaitTimeout)
	for time.Now().Before(deadline) {
		if tmux.ServerRunning(tmuxPath) {
			if own, err := tmux.ServerOwnership(tmuxPath); err == nil && own.OwnerLive {
				return own, true
			}
		}
		time.Sleep(reparentWaitPoll)
	}
	return tmux.Ownership{}, false
}

func newServiceReparentCmd() *cobra.Command {
	var assumeYes bool
	var force bool

	cmd := &cobra.Command{
		Use:   "reparent",
		Short: "Recycle the tmux server so the running daemon owns it",
		Long: `Recycle leo's tmux server so the currently running daemon is its parent.

Leo deliberately adopts a tmux server that outlived a previous daemon, which
is what keeps agent sessions alive across 'leo update' and 'leo service
restart'. On macOS the cost is that Local Network access for agent panes is
attributed to the leo process that CREATED the server; once that process is
gone, the grant can lapse and third-party binaries under agents lose LAN
access — while leo's own checks still pass. 'leo doctor' detects this by
probing from inside the tmux tree.

This is the repair. It terminates every live agent session (workspaces,
agent definitions, and session ids survive; in-flight conversation context
does not), then waits for the daemon to start a fresh, owned server.`,
		Example: `  leo service reparent
  leo service reparent --yes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := gateCommand(cmd, "leo_stop_agent"); err != nil {
				return err
			}
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			reader := prompt.NewReader()
			return reparentServer(reparentDeps{
				daemonRunning: func() bool { return daemon.IsRunning(cfg.HomePath) },
				locate:        tmux.Locate,
				serverRunning: tmux.ServerRunning,
				ownership:     tmux.ServerOwnership,
				killServer:    tmux.KillServer,
				counts:        func() (int, int) { return agentSessionCounts(cmd.Context()) },
				confirm:       func(msg string) bool { return prompt.YesNo(reader, msg, false) },
				waitForOwner:  waitForOwnedServer,
				out:           cmd.OutOrStdout(),
				assumeYes:     assumeYes,
				force:         force,
			})
		},
	}

	cmd.Flags().BoolVar(&assumeYes, "yes", false, "skip the confirmation prompt")
	cmd.Flags().BoolVar(&force, "force", false, "recycle even when the server's owner is alive")

	return cmd
}

// agentSessionCounts reports live and dormant (stopped) agent counts for the
// confirmation prompt, reusing the status report the daemon already exposes.
func agentSessionCounts(ctx context.Context) (int, int) {
	report := buildStatusReport(ctx)
	return report.Agents.Running, report.Agents.Stopped
}
