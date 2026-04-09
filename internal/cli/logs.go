package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var tail int
	var follow bool

	cmd := &cobra.Command{
		Use:   "logs [process-name]",
		Short: "Show service or process logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			var logPath string
			if len(args) > 0 {
				logPath = filepath.Join(cfg.HomePath, "state", args[0]+".log")
			} else {
				logPath = service.LogPathFor(cfg.HomePath)
			}

			if _, err := os.Stat(logPath); err != nil {
				return fmt.Errorf("no log file at %s", logPath)
			}

			tailArgs := []string{"-n", fmt.Sprintf("%d", tail)}
			if follow {
				tailArgs = append(tailArgs, "-f")
			}
			tailArgs = append(tailArgs, logPath)

			tailCmd := exec.Command("tail", tailArgs...)
			tailCmd.Stdout = os.Stdout
			tailCmd.Stderr = os.Stderr
			return tailCmd.Run()
		},
	}

	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "number of lines to show")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")

	return cmd
}
