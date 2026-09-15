package cli

import (
	"fmt"
	"net/http"
	"net/url"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/spf13/cobra"
)

func newDispatchOutputCmd() *cobra.Command {
	var host string
	var tail int
	cmd := &cobra.Command{Use: "output <id>", Short: "Show recorded dispatch output", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if tail <= 0 {
			return fmt.Errorf("tail must be a positive integer")
		}
		cfg, res, err := dispatch(host)
		if err != nil {
			return err
		}
		if !res.Localhost {
			return fmt.Errorf("dispatch output is not supported for remote hosts")
		}
		var output consult.Output
		if err := dispatchHTTP(cmd.Context(), cfg, http.MethodGet, "/api/dispatch/"+url.PathEscape(args[0])+"/output?tail="+fmt.Sprintf("%d", tail), nil, &output); err != nil {
			return err
		}
		for _, line := range output.Lines {
			fmt.Fprintln(consultStdout, line)
		}
		return nil
	}}
	cmd.Flags().IntVar(&tail, "tail", 60, "number of rendered output lines")
	addHostFlag(cmd, &host)
	return cmd
}
