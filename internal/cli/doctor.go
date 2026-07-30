package cli

import (
	"errors"
	"strings"
	"syscall"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/spf13/cobra"
)

// LocalNetworkStatus is the structured result of the macOS Local Network
// privacy check performed by checkLocalNetwork. It's shared by the
// darwin and non-darwin implementations so `leo doctor` can render a
// consistent report regardless of platform.
type LocalNetworkStatus struct {
	// State is one of "granted", "denied", "undetermined", or "n/a" (non-macOS).
	State string
	// Detail is a short human-readable explanation of how State was derived.
	Detail string
	// Triggered reports whether the consent-raising probe (mDNS multicast
	// send + group join) was attempted.
	Triggered bool
	// ProbeResult carries the raw dial/error text from the connectivity
	// probe, useful for debugging a misclassification.
	ProbeResult string
	// TreeState is the verdict from probing inside leo's tmux server with a
	// third-party binary — the path agents actually use. Empty when no verdict
	// was reachable (no server, no probe binary), in which case TreeProbe says
	// why. This, not ProbeResult, is what State reflects when both disagree.
	TreeState string
	// TreeProbe carries the in-tree probe's raw output or the reason no
	// verdict was reached.
	TreeProbe string
	// TreeBinary is the absolute path of the third-party binary used in-tree.
	TreeBinary string
}

const doctorLong = `Run local health checks, including macOS Local Network privacy diagnosis.

Under Leo's daemon, third-party binaries spawned in a background tmux
session can be silently denied macOS "Local Network" access — connections
to other devices on the LAN fail with EHOSTUNREACH even though the same
binary works fine from Terminal. leo doctor deliberately performs a
local-network operation (per Apple TN3179) so that, when run interactively
as the signed leo binary, macOS surfaces the one-time Allow/Deny dialog
attributed to leo. It then reports a best-effort granted/denied/undetermined
verdict.

Two probes run, and the distinction matters. The first dials from leo's own
process, which always holds leo's own grant and therefore cannot detect a
degraded agent environment. The second runs a third-party binary (node or
python3) inside leo's tmux server — the same process context agent panes get
— and it is that verdict leo reports. An in-tree denial while leo's own dial
succeeds means the tmux server's Local Network attribution has lapsed and
agents are cut off from the LAN.`

func newDoctorCmd() *cobra.Command {
	var probeHost string
	var trigger bool

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose local network and daemon health",
		Long:  doctorLong,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runDoctor(probeHost, trigger)
		},
	}

	cmd.Flags().StringVar(&probeHost, "probe-host", "", "explicit LAN endpoint (host:port) to test connectivity against")
	cmd.Flags().BoolVar(&trigger, "trigger", true, "attempt to trigger the macOS Local Network consent dialog")

	return cmd
}

func runDoctor(probeHost string, trigger bool) error {
	cfg, err := loadConfig()
	if err != nil {
		warn.Printf("Config: %s\n", err)
	} else {
		if daemon.IsRunning(cfg.HomePath) {
			success.Println("Daemon:         running")
		} else {
			info.Println("Daemon:         not running")
		}
	}

	status := checkLocalNetwork(probeHost, trigger)

	switch status.State {
	case "granted":
		success.Printf("Local Network:  %s\n", status.State)
	case "denied", treeDeniedState:
		warn.Printf("Local Network:  %s\n", status.State)
	default:
		info.Printf("Local Network:  %s\n", status.State)
	}
	if status.Detail != "" {
		info.Printf("  Detail:       %s\n", status.Detail)
	}
	if status.ProbeResult != "" {
		info.Printf("  Probe (leo):  %s\n", status.ProbeResult)
	}
	if status.TreeProbe != "" {
		treeLabel := status.TreeState
		if treeLabel == "" {
			treeLabel = "no verdict"
		}
		info.Printf("  Probe (tmux): %s — %s\n", treeLabel, status.TreeProbe)
	}
	if status.TreeBinary != "" {
		info.Printf("  Probe binary: %s\n", status.TreeBinary)
	}
	info.Printf("  Triggered:    %v\n", status.Triggered)

	if status.State == treeDeniedState {
		warn.Println("\nAgents cannot reach the LAN even though leo itself can: the tmux server's")
		warn.Println("Local Network attribution has lapsed. Note that 'leo service restart' does")
		warn.Println("NOT fix this — the tmux server survives a daemon restart by design. Recycle")
		warn.Println("it with 'tmux -L leo kill-server' (this terminates live agent sessions).")
	}

	return nil
}

// classifyDial maps the outcome of a TCP dial attempt against a known
// on-subnet LAN host to a Local Network privacy verdict.
//
//   - EHOSTUNREACH ("no route to host") means the OS blocked the packet
//     before it left the machine — the classic denied signature.
//   - A successful connect, or a "connection refused" (ECONNREFUSED,
//     meaning the packet reached the host and was rejected there), means
//     the packet left the machine — granted.
//   - Anything else (timeout, DNS failure, etc.) is inconclusive.
func classifyDial(err error, connected bool) string {
	if connected || err == nil {
		return "granted"
	}

	if errors.Is(err, syscall.EHOSTUNREACH) || strings.Contains(err.Error(), "no route to host") {
		return "denied"
	}

	if errors.Is(err, syscall.ECONNREFUSED) || strings.Contains(err.Error(), "connection refused") {
		return "granted"
	}

	return "undetermined"
}
