package cli

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web"
	"github.com/spf13/cobra"
)

func newWebCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "web",
		Short: "Web UI utilities",
	}
	cmd.AddCommand(newWebLoginURLCmd())
	return cmd
}

// resolveURLBind returns the host to embed in the login URL.
//
//   - If flagBind is non-empty, it was set explicitly by the caller and wins.
//   - If cfgBind is a loopback address, it is safe to use directly.
//   - If cfgBind is non-loopback and allowedHosts has at least one entry, the
//     first entry is used and a human-readable note is returned.
//   - If cfgBind is non-loopback and allowedHosts is empty, an error is returned
//     directing the caller to pass --bind or populate web.allowed_hosts.
func resolveURLBind(flagBind, cfgBind string, allowedHosts []string) (host, note string, err error) {
	if flagBind != "" {
		return flagBind, "", nil
	}
	if config.IsLoopbackBind(cfgBind) {
		return cfgBind, "", nil
	}
	// Non-loopback bind: use allowed_hosts[0] if available.
	if len(allowedHosts) > 0 {
		h := allowedHosts[0]
		n := fmt.Sprintf("note: using allowed_hosts[0] (%s) as URL host; bind is %s", h, cfgBind)
		return h, n, nil
	}
	return "", "", fmt.Errorf("bind is %s but web.allowed_hosts is empty; pass --bind <host>", cfgBind)
}

func newWebLoginURLCmd() *cobra.Command {
	var stateDir, bind string
	var port int
	cmd := &cobra.Command{
		Use:   "login-url",
		Short: "Print a one-click login URL with the current API token",
		Long: "Reads ~/.leo/state/api.token (creating one if missing) and prints a URL you " +
			"can open in a browser to auto-submit the login form. Do not share the URL — the token is in it.",
		RunE: func(cmd *cobra.Command, args []string) error {
			sd := stateDir
			p := port

			// Fall back to loaded config when flags are unset.
			var cfgBind string
			var allowedHosts []string
			if sd == "" || bind == "" || p == 0 {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if sd == "" {
					sd = cfg.StatePath()
				}
				if p == 0 {
					p = cfg.WebPort()
				}
				cfgBind = cfg.WebBind()
				allowedHosts = cfg.Web.AllowedHosts
			}

			b, note, err := resolveURLBind(bind, cfgBind, allowedHosts)
			if err != nil {
				return err
			}
			if note != "" {
				fmt.Fprintln(cmd.ErrOrStderr(), note)
			}

			tok, err := web.EnsureAPIToken(sd)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "http://%s:%d/login?token=%s\n", b, p, tok)
			return nil
		},
	}
	cmd.Flags().StringVar(&stateDir, "state-dir", "", "override state dir (defaults to config)")
	cmd.Flags().StringVar(&bind, "bind", "", "host to embed in URL (defaults to config web.bind)")
	cmd.Flags().IntVar(&port, "port", 0, "port to embed in URL (defaults to config web.port)")
	return cmd
}
