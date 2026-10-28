package config

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"
)

// FollowConfig makes this host follow a lead: it mirrors the lead's
// templates, environments, delegation and defaults, pulled over SSH from a
// client.hosts entry. See docs/configuration/follow-lead.md.
type FollowConfig struct {
	// Host names the client.hosts entry of the lead.
	Host string `yaml:"host"`
	// Interval is the re-pull period as a Go duration; empty means
	// DefaultFollowInterval, and anything under MinFollowInterval is refused.
	Interval string `yaml:"interval,omitempty"`
}

const (
	// DefaultFollowInterval is follow.interval when it is unset.
	DefaultFollowInterval = 5 * time.Minute
	// MinFollowInterval is the shortest follow.interval Validate accepts.
	MinFollowInterval = 30 * time.Second
)

// IsFollower reports whether this config follows a lead.
func (c *Config) IsFollower() bool { return c.Follow != nil }

// FollowInterval is the effective re-pull period: 0 when not following,
// DefaultFollowInterval when unset or unusable (Validate rejects the latter).
func (c *Config) FollowInterval() time.Duration {
	if c.Follow == nil {
		return 0
	}
	if d, err := parseFollowInterval(c.Follow.Interval); err == nil {
		return d
	}
	return DefaultFollowInterval
}

func parseFollowInterval(raw string) (time.Duration, error) {
	if raw == "" {
		return DefaultFollowInterval, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < MinFollowInterval {
		return 0, fmt.Errorf("must be a duration of at least %s", MinFollowInterval)
	}
	return d, nil
}

func (c *Config) validateFollow() []string {
	f := c.Follow
	if f == nil {
		return nil
	}
	var errs []string
	if _, err := parseFollowInterval(f.Interval); err != nil {
		errs = append(errs, fmt.Sprintf("follow.interval %q %v", f.Interval, err))
	}
	switch f.Host {
	case "":
		errs = append(errs, "follow.host is required (a client.hosts name)")
	case LocalhostSentinel:
		errs = append(errs, fmt.Sprintf("follow.host %q names this host; a host cannot follow itself", f.Host))
	default:
		host, ok := c.Client.Hosts[f.Host]
		if !ok {
			errs = append(errs, fmt.Sprintf("follow.host %q is not defined in client.hosts", f.Host))
		} else if isLoopbackSSHTarget(host.SSH) {
			errs = append(errs, fmt.Sprintf("follow.host %q points at this host (ssh %q); a host cannot follow itself", f.Host, host.SSH))
		}
	}
	return errs
}

// isLoopbackSSHTarget reports whether an ssh destination ([user@]host,
// [user@][v6], or ssh://[user@]host[:port]) names localhost or a loopback IP.
func isLoopbackSSHTarget(target string) bool {
	host := target
	if u, err := url.Parse(target); err == nil && u.Scheme == "ssh" {
		host = u.Hostname()
	} else if i := strings.LastIndex(host, "@"); i >= 0 {
		host = host[i+1:]
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
