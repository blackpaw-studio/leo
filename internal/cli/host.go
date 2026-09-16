package cli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"text/tabwriter"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/hosts"
	"github.com/spf13/cobra"
)

func newHostCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "host", Short: "Manage remote leo hosts"}
	cmd.AddCommand(newHostListCmd(), newHostActionCmd("connect"), newHostActionCmd("disconnect"))
	return cmd
}
func newHostListCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{Use: "list", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		resp, err := daemon.Send(cmd.Context(), cfg.HomePath, http.MethodGet, "/hosts", nil)
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("%s", resp.Error)
		}
		var rows []hosts.Row
		if err := json.Unmarshal(resp.Data, &rows); err != nil {
			return err
		}
		if asJSON {
			return writeHostJSON(rows)
		}
		tw := tabwriter.NewWriter(agentStdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSSH\tSTATE\tDEFAULT")
		for _, r := range rows {
			ssh := r.SSH
			if r.Local {
				ssh = "(local daemon)"
			}
			def := ""
			if r.Default {
				def = "*"
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Name, dashIfEmpty(ssh), r.State, def)
		}
		return tw.Flush()
	}}
	cmd.Flags().BoolVar(&asJSON, "json", false, "output as JSON")
	return cmd
}
func newHostActionCmd(action string) *cobra.Command {
	return &cobra.Command{Use: action + " <name>", Args: cobra.ExactArgs(1), ValidArgsFunction: completeHostNames, RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		resp, err := daemon.Send(cmd.Context(), cfg.HomePath, http.MethodPost, "/hosts/"+args[0]+"/"+action, nil)
		if err != nil {
			return err
		}
		if !resp.OK {
			return fmt.Errorf("%s: %s", resp.Code, resp.Error)
		}
		var row hosts.Row
		if err := json.Unmarshal(resp.Data, &row); err != nil {
			return err
		}
		return writeHostJSON(row)
	}}
}
func writeHostJSON(v any) error {
	e := json.NewEncoder(agentStdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
func completeHostNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	cfg, err := loadConfig()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	names := make([]string, 0, len(cfg.Client.Hosts))
	for n := range cfg.Client.Hosts {
		names = append(names, n)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}
