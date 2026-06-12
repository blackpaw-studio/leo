package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// HostResolution is the outcome of resolving which host a CLI command should target.
// Name is "" and Host is zero when the resolution chose localhost.
type HostResolution struct {
	Name      string
	Host      HostConfig
	Localhost bool
	// ControlPath is the deterministic SSH ControlMaster socket path for this
	// host (empty for localhost). All host-targeted SSH calls reuse it so they
	// multiplex over a single connection — the `leo host forward` master, every
	// `agent` dispatch, and the `--cc` cell stream share one TCP session. Lives
	// under <state>/remotes/<name>.ctl alongside the forwarded daemon socket.
	ControlPath string
}

// LocalhostSentinel is the literal flag value that forces localhost even when
// remotes are configured.
const LocalhostSentinel = "localhost"

// ResolveHost applies the documented precedence for selecting a target host:
//
//  1. explicit flag value (when non-empty)
//  2. LEO_HOST environment variable
//  3. client.default_host
//  4. first entry in client.hosts (sorted by key for determinism)
//  5. localhost (only if no hosts are configured)
//
// The string "localhost" is a hard override that forces localhost regardless
// of configured hosts. Returns an error when a named host is requested but not
// configured.
func (c *Config) ResolveHost(flagValue string) (HostResolution, error) {
	if flagValue == LocalhostSentinel {
		return HostResolution{Localhost: true}, nil
	}

	candidates := []string{flagValue, os.Getenv("LEO_HOST"), c.Client.DefaultHost}
	for _, name := range candidates {
		if name == "" {
			continue
		}
		if name == LocalhostSentinel {
			return HostResolution{Localhost: true}, nil
		}
		host, ok := c.Client.Hosts[name]
		if !ok {
			return HostResolution{}, fmt.Errorf("host %q not defined in client.hosts", name)
		}
		return c.remoteResolution(name, host), nil
	}

	// No explicit selection — fall through to the first configured host if any.
	if len(c.Client.Hosts) > 0 {
		names := make([]string, 0, len(c.Client.Hosts))
		for name := range c.Client.Hosts {
			names = append(names, name)
		}
		sort.Strings(names)
		first := names[0]
		return c.remoteResolution(first, c.Client.Hosts[first]), nil
	}

	return HostResolution{Localhost: true}, nil
}

// remoteResolution builds a HostResolution for a named remote host, attaching
// the deterministic per-host ControlMaster socket path so every SSH call to the
// host can multiplex over one connection.
func (c *Config) remoteResolution(name string, host HostConfig) HostResolution {
	return HostResolution{
		Name:        name,
		Host:        host,
		ControlPath: c.HostControlPath(name),
	}
}

// HostControlPath returns the SSH ControlMaster socket path for a named host:
// <state>/remotes/<name>.ctl. Deterministic so independently-invoked CLI
// commands converge on the same master.
func (c *Config) HostControlPath(name string) string {
	return filepath.Join(c.StatePath(), "remotes", name+".ctl")
}

// HostForwardSocket returns the local path where the host's remote daemon
// socket is forwarded: <state>/remotes/<name>.sock. A local socket client can
// speak the normal leo HTTP/socket API to the remote daemon through it.
func (c *Config) HostForwardSocket(name string) string {
	return filepath.Join(c.StatePath(), "remotes", name+".sock")
}
