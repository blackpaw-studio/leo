package config

import (
	"fmt"
	"os"
	"sort"
)

// HostResolution is the outcome of resolving which host a CLI command should target.
// Name is "" and Host is zero when the resolution chose localhost.
type HostResolution struct {
	Name      string
	Host      HostConfig
	Localhost bool
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
		return HostResolution{Name: name, Host: host}, nil
	}

	// No explicit selection — fall through to the first configured host if any.
	if len(c.Client.Hosts) > 0 {
		names := make([]string, 0, len(c.Client.Hosts))
		for name := range c.Client.Hosts {
			names = append(names, name)
		}
		sort.Strings(names)
		first := names[0]
		return HostResolution{Name: first, Host: c.Client.Hosts[first]}, nil
	}

	return HostResolution{Localhost: true}, nil
}
