//go:build darwin

package cli

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

const (
	doctorDialTimeout   = 3 * time.Second
	doctorGwDialTimeout = 2 * time.Second
	mdnsGroupAddr       = "224.0.0.251:5353"
	// treeProbeTimeout bounds a single in-tree probe. Generous relative to the
	// 3s connect timeout inside the probe command itself, to absorb session
	// startup.
	treeProbeTimeout = 10 * time.Second
)

// mdnsQuery is a minimal, well-formed mDNS query packet (a PTR query for
// "_services._dns-sd._udp.local."), used purely to exercise a local-network
// send per TN3179 — its content doesn't matter for the privacy check.
var mdnsQuery = []byte{
	0x00, 0x00, // transaction ID
	0x00, 0x00, // flags
	0x00, 0x01, // questions
	0x00, 0x00, // answer RRs
	0x00, 0x00, // authority RRs
	0x00, 0x00, // additional RRs
	0x09, '_', 's', 'e', 'r', 'v', 'i', 'c', 'e', 's',
	0x07, '_', 'd', 'n', 's', '-', 's', 'd',
	0x04, '_', 'u', 'd', 'p',
	0x05, 'l', 'o', 'c', 'a', 'l',
	0x00,       // end of name
	0x00, 0x0c, // type PTR
	0x00, 0x01, // class IN
}

// checkLocalNetwork performs a best-effort macOS Local Network privacy
// diagnosis: an mDNS multicast trigger (to raise the consent dialog, if
// trigger is set) plus a TCP connectivity probe against either the caller's
// explicit probeHost or the default gateway.
func checkLocalNetwork(probeHost string, trigger bool) LocalNetworkStatus {
	status := LocalNetworkStatus{}

	var mdnsDetail string
	if trigger {
		status.Triggered = true
		mdnsDetail = triggerMulticast()
	}

	target := probeHost
	var detailPrefix string
	if target == "" {
		gw, err := defaultGateway()
		if err != nil {
			status.State = "undetermined"
			status.Detail = fmt.Sprintf("could not determine default gateway: %s", err)
			if mdnsDetail != "" {
				status.Detail += "; " + mdnsDetail
			}
			return status
		}
		target = net.JoinHostPort(gw, "80")
		detailPrefix = fmt.Sprintf("probed default gateway %s", target)
	} else {
		detailPrefix = fmt.Sprintf("probed %s", target)
	}

	conn, dialErr := net.DialTimeout("tcp", target, doctorDialTimeoutFor(probeHost))
	connected := dialErr == nil
	if connected {
		_ = conn.Close()
		status.ProbeResult = "connected"
	} else {
		status.ProbeResult = dialErr.Error()
	}

	inProcessState := classifyDial(dialErr, connected)
	status.Detail = detailPrefix
	if mdnsDetail != "" {
		status.Detail += "; " + mdnsDetail
	}

	// mDNS "broken pipe" is a strong corroborating denial signal even when
	// the TCP probe was inconclusive (e.g. a firewalled gateway timeout).
	if inProcessState == "undetermined" && strings.Contains(mdnsDetail, "denied signal") {
		inProcessState = "denied"
	}

	// Everything above measured THIS process, which is the signed leo binary
	// holding its own grant — it cannot observe a broken tmux tree. Probe the
	// tree too, and let it decide the verdict when it has one.
	tree := probeTree(target)
	status.TreeState = tree.State
	status.TreeProbe = tree.Detail
	status.TreeBinary = tree.Binary
	status.State = combineLocalNetworkStates(tree.State, inProcessState)

	return status
}

// probeTree runs the in-tree Local Network probe against target, resolving
// tmux and wiring the real RunInServer. A missing tmux binary is reported as
// "no verdict" rather than a failure — doctor's job is to explain, not to
// abort.
func probeTree(target string) treeProbeResult {
	tmuxPath, err := tmux.Locate()
	if err != nil {
		return treeProbeResult{Detail: err.Error()}
	}
	deps := treeProbeDeps{
		lookPath: exec.LookPath,
		runInServer: func(path, shellCmd string) (string, error) {
			return tmux.RunInServer(path, shellCmd, treeProbeTimeout)
		},
	}
	return runTreeProbe(deps, tmuxPath, target)
}

func doctorDialTimeoutFor(probeHost string) time.Duration {
	if probeHost == "" {
		return doctorGwDialTimeout
	}
	return doctorDialTimeout
}

// defaultGateway shells out to `route -n get default` and parses the
// "gateway: <ip>" line, giving us a host that's guaranteed to be on-subnet
// without requiring any config or CGO-based interface introspection.
func defaultGateway() (string, error) {
	out, err := exec.Command("route", "-n", "get", "default").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("running route -n get default: %w", err)
	}
	return parseGatewayLine(string(out))
}

// parseGatewayLine extracts the gateway IP from `route -n get default`
// output, e.g. a line like "    gateway: 10.0.2.1".
func parseGatewayLine(output string) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "gateway:") {
			gw := strings.TrimSpace(strings.TrimPrefix(trimmed, "gateway:"))
			if gw == "" {
				return "", fmt.Errorf("empty gateway field")
			}
			return gw, nil
		}
	}
	return "", fmt.Errorf("no gateway line in route output")
}

// triggerMulticast performs the two canonical local-network operations from
// TN3179 — a multicast UDP send and a multicast group join — to raise
// macOS's Local Network consent dialog when run interactively. Errors are
// expected under denial and are folded into the returned detail string
// rather than surfaced as failures.
func triggerMulticast() string {
	var notes []string

	if err := sendMulticastProbe(); err != nil {
		if isDeniedSignal(err) {
			notes = append(notes, fmt.Sprintf("mDNS send denied signal (%s)", err))
		} else {
			notes = append(notes, fmt.Sprintf("mDNS send: %s", err))
		}
	} else {
		notes = append(notes, "mDNS send ok")
	}

	if err := joinMulticastGroup(); err != nil {
		notes = append(notes, fmt.Sprintf("mDNS listen: %s", err))
	} else {
		notes = append(notes, "mDNS listen ok")
	}

	return strings.Join(notes, ", ")
}

// isDeniedSignal reports whether err looks like the macOS "broken
// pipe"/permission-denied signature seen when Local Network access has
// been denied for this process.
func isDeniedSignal(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "operation not permitted")
}

func sendMulticastProbe() error {
	addr, err := net.ResolveUDPAddr("udp4", mdnsGroupAddr)
	if err != nil {
		return fmt.Errorf("resolving mDNS group address: %w", err)
	}

	conn, err := net.DialUDP("udp4", nil, addr)
	if err != nil {
		return fmt.Errorf("dialing mDNS group: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := conn.SetWriteDeadline(time.Now().Add(doctorGwDialTimeout)); err != nil {
		return fmt.Errorf("setting write deadline: %w", err)
	}
	if _, err := conn.Write(mdnsQuery); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// joinMulticastGroup attempts to join the mDNS multicast group on the first
// multicast-capable, non-loopback, up interface it finds. This "browse"
// style operation is TN3179's strongest consent trigger.
func joinMulticastGroup() error {
	ifaces, err := net.Interfaces()
	if err != nil {
		return fmt.Errorf("listing interfaces: %w", err)
	}

	group, err := net.ResolveUDPAddr("udp4", "224.0.0.251:5353")
	if err != nil {
		return fmt.Errorf("resolving mDNS group address: %w", err)
	}

	var lastErr error
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagMulticast == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		conn, err := net.ListenMulticastUDP("udp4", &iface, group)
		if err != nil {
			lastErr = err
			continue
		}
		_ = conn.Close()
		return nil
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("no multicast-capable interface found")
	}
	return lastErr
}
