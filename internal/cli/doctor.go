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
}

const doctorLong = `Run local health checks, including macOS Local Network privacy diagnosis.

Under Leo's daemon, third-party binaries spawned in a background tmux
session can be silently denied macOS "Local Network" access — connections
to other devices on the LAN fail with EHOSTUNREACH even though the same
binary works fine from Terminal. leo doctor deliberately performs a
local-network operation (per Apple TN3179) so that, when run interactively
as the signed leo binary, macOS surfaces the one-time Allow/Deny dialog
attributed to leo. It then reports a best-effort granted/denied/undetermined
verdict.`

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
	case "denied":
		warn.Printf("Local Network:  %s\n", status.State)
	default:
		info.Printf("Local Network:  %s\n", status.State)
	}
	if status.Detail != "" {
		info.Printf("  Detail:       %s\n", status.Detail)
	}
	if status.ProbeResult != "" {
		info.Printf("  Probe:        %s\n", status.ProbeResult)
	}
	info.Printf("  Triggered:    %v\n", status.Triggered)

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
