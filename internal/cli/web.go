package cli

import (
	"fmt"

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
			b := bind
			p := port

			// Fall back to loaded config when flags are unset.
			if sd == "" || b == "" || p == 0 {
				cfg, err := loadConfig()
				if err != nil {
					return err
				}
				if sd == "" {
					sd = cfg.StatePath()
				}
				if b == "" {
					b = cfg.WebBind()
				}
				if p == 0 {
					p = cfg.WebPort()
				}
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
