package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/fatih/color"
)

var (
	bold    = color.New(color.Bold)
	success = color.New(color.FgGreen, color.Bold)
	warn    = color.New(color.FgYellow, color.Bold)
	errC    = color.New(color.FgRed, color.Bold)
	info    = color.New(color.FgCyan)
)

func loadConfig() (*config.Config, error) {
	path := cfgFile
	if path == "" {
		if workspace != "" {
			path = workspace + "/leo.yaml"
		} else {
			var err error
			path, err = config.FindConfig("")
			if err != nil {
				return nil, fmt.Errorf("no leo.yaml found — run 'leo setup' first")
			}
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	if workspace != "" {
		cfg.Agent.Workspace = workspace
	}

	return cfg, nil
}

func leoExecutablePath() (string, error) {
	path, err := os.Executable()
	if err != nil {
		// Fallback to looking up in PATH
		return exec.LookPath("leo")
	}
	return path, nil
}

func fatal(format string, args ...any) {
	errC.Fprintf(os.Stderr, "Error: "+format+"\n", args...)
	os.Exit(1)
}
