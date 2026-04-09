package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/spf13/cobra"
)

func newLogsCmd() *cobra.Command {
	var tail int
	var follow bool

	cmd := &cobra.Command{
		Use:               "logs [process-name]",
		Short:             "Show service or process logs",
		Long:              "Show the main service log, or filter for a specific process by name.",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeProcessNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			logPath := service.LogPathFor(cfg.HomePath)
			if _, err := os.Stat(logPath); err != nil {
				return fmt.Errorf("no log file at %s", logPath)
			}

			if len(args) > 0 {
				// Filter service log for lines matching [processName]
				return grepLog(logPath, args[0], tail, follow)
			}

			// Show full service log
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

// grepLog filters the service log for lines containing [name].
func grepLog(logPath, name string, tail int, follow bool) error {
	pattern := fmt.Sprintf("[%s]", name)

	if follow {
		// tail -f | grep pattern
		tailCmd := exec.Command("tail", "-f", logPath)
		grepCmd := exec.Command("grep", "--line-buffered", pattern)

		pipe, err := tailCmd.StdoutPipe()
		if err != nil {
			return err
		}
		grepCmd.Stdin = pipe
		grepCmd.Stdout = os.Stdout
		grepCmd.Stderr = os.Stderr

		if err := tailCmd.Start(); err != nil {
			return err
		}
		if err := grepCmd.Start(); err != nil {
			tailCmd.Process.Kill()
			return err
		}
		return grepCmd.Wait()
	}

	// grep pattern logPath | tail -n N
	grepCmd := exec.Command("grep", pattern, logPath)
	tailCmd := exec.Command("tail", "-n", fmt.Sprintf("%d", tail))

	pipe, err := grepCmd.StdoutPipe()
	if err != nil {
		return err
	}
	tailCmd.Stdin = pipe
	tailCmd.Stdout = os.Stdout
	tailCmd.Stderr = os.Stderr

	if err := grepCmd.Start(); err != nil {
		return err
	}
	if err := tailCmd.Start(); err != nil {
		return err
	}
	grepCmd.Wait()
	return tailCmd.Wait()
}
