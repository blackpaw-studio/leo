package cli

import (
	"encoding/json"
	"fmt"
	"sort"
	"text/tabwriter"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

// newHostCmd groups commands for working with remote leo hosts: listing the
// configured hosts and standing up a persistent forwarded daemon socket so a
// local socket client (e.g. leoterm) can speak the normal leo HTTP/socket API
// to a remote daemon. The daemon listener itself stays unix-socket-only; all
// remote access is SSH at the CLI layer.
func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "host",
		Short: "Manage remote leo hosts and SSH socket forwards",
		Long: `Work with the remote leo hosts defined in client.hosts.

'leo host list' enumerates the configured hosts (plus the local daemon).
'leo host forward <name>' stands up a persistent SSH forward of the host's
daemon socket to a local unix socket so a local client can drive the remote
daemon over the normal leo socket API. The daemon listener stays unix-only —
all remote access is SSH.`,
	}
	cmd.AddCommand(newHostListCmd(), newHostForwardCmd(), newHostUnforwardCmd())
	return cmd
}

// hostEntry is the JSON shape emitted by `leo host list --json`. A synthesized
// entry with Local=true represents the local daemon so a picker can offer it
// alongside the SSH hosts without special-casing.
type hostEntry struct {
	Name    string `json:"name"`
	SSH     string `json:"ssh,omitempty"`
	Default bool   `json:"default"`
	Local   bool   `json:"local"`
}

func newHostListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List configured remote hosts (and the local daemon)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}
			entries := hostEntries(cfg)
			if asJSON {
				enc := json.NewEncoder(agentStdout)
				enc.SetIndent("", "  ")
				return enc.Encode(entries)
			}
			tw := tabwriter.NewWriter(agentStdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw, "NAME\tSSH\tDEFAULT")
			for _, e := range entries {
				ssh := e.SSH
				if e.Local {
					ssh = "(local daemon)"
				}
				def := ""
				if e.Default {
					def = "*"
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\n", e.Name, dashIfEmpty(ssh), def)
			}
			return tw.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}

// hostEntries returns the configured hosts (sorted by name) preceded by a
// synthesized "local" entry. The local entry is the default only when no
// remote default_host is configured — mirroring ResolveHost's precedence,
// where localhost wins only in the absence of configured hosts.
func hostEntries(cfg *config.Config) []hostEntry {
	names := make([]string, 0, len(cfg.Client.Hosts))
	for name := range cfg.Client.Hosts {
		names = append(names, name)
	}
	sort.Strings(names)

	defaultName := cfg.Client.DefaultHost
	if defaultName == "" && len(names) > 0 {
		// ResolveHost falls through to the first host (sorted) when no
		// default_host is set and no flag/env overrides it.
		defaultName = names[0]
	}

	entries := []hostEntry{{
		Name:    config.LocalhostSentinel,
		Local:   true,
		Default: defaultName == "",
	}}
	for _, name := range names {
		entries = append(entries, hostEntry{
			Name:    name,
			SSH:     cfg.Client.Hosts[name].SSH,
			Default: name == defaultName,
		})
	}
	return entries
}

func newHostForwardCmd() *cobra.Command {
	var asJSON, stop bool
	cmd := &cobra.Command{
		Use:   "forward <name>",
		Short: "Forward a remote host's leo daemon socket to a local socket over SSH",
		Long: `Stand up a persistent SSH forward of <name>'s remote daemon socket
(~/.leo/state/leo.sock, honoring $LEO_HOME) to a deterministic local socket at
<state>/remotes/<name>.sock. A local socket client can then speak the normal
leo HTTP/socket API to the remote daemon through that path.

The command runs in the foreground and prints the local socket path on its
first line (or a JSON object with --json) once the forward is healthy. It holds
an SSH ControlMaster that subsequent 'leo --host <name> agent attach --cc' calls
reuse, so the control plane and the cell stream share one connection.

Lifecycle is the caller's: leoterm spawns this as a long-lived child, reads the
path, and kills the child to tear down. If the first connection cannot be
established the command exits non-zero before printing a path. After a healthy
connection drops it reconnects automatically with backoff (the local path is
stable across reconnects). It is idempotent: if a healthy forward already
exists it prints the path and exits 0 without starting a duplicate.

Pass --stop (or use 'leo host unforward <name>') to tear down a lingering
ControlMaster and remove the stale local socket.`,
		Example: `  # Foreground; leoterm reads the socket path and manages the process
  leo host forward dionysus --json

  # Tear down the persistent master and stale socket
  leo host forward dionysus --stop`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHostNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := resolveRemoteHost(args[0])
			if err != nil {
				return err
			}
			fwd := &hostForwarder{
				res:       res,
				localSock: cfg.HostForwardSocket(res.Name),
				asJSON:    asJSON,
			}
			if stop {
				return fwd.stop()
			}
			return fwd.run(cmd.Context())
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, `emit {"socket","host","pid"} instead of the bare socket path`)
	cmd.Flags().BoolVar(&stop, "stop", false, "tear down the forward's ControlMaster and remove the local socket")
	return cmd
}

func newHostUnforwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "unforward <name>",
		Short:             "Tear down a host forward (alias for 'host forward <name> --stop')",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeHostNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, res, err := resolveRemoteHost(args[0])
			if err != nil {
				return err
			}
			fwd := &hostForwarder{res: res, localSock: cfg.HostForwardSocket(res.Name)}
			return fwd.stop()
		},
	}
	return cmd
}

// resolveRemoteHost resolves a host name to a remote HostResolution, rejecting
// localhost and hosts without an SSH target — forwarding only makes sense
// against a configured remote daemon.
func resolveRemoteHost(name string) (*config.Config, config.HostResolution, error) {
	cfg, err := loadConfig()
	if err != nil {
		return nil, config.HostResolution{}, err
	}
	res, err := cfg.ResolveHost(name)
	if err != nil {
		return nil, config.HostResolution{}, err
	}
	if res.Localhost {
		return nil, config.HostResolution{}, fmt.Errorf("%q is the local daemon; host forward targets a remote host from client.hosts", name)
	}
	if res.Host.SSH == "" {
		return nil, config.HostResolution{}, fmt.Errorf("host %q has no ssh target configured", res.Name)
	}
	return cfg, res, nil
}

// completeHostNames supplies shell-completion values from client.hosts.
func completeHostNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cfg.Client.Hosts))
	for name := range cfg.Client.Hosts {
		names = append(names, name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
