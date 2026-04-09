package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prompt"
)

var (
	success = prompt.Success
	warn    = prompt.Warn
	info    = prompt.Info
)

func loadConfig() (*config.Config, error) {
	path := cfgFile
	if path == "" {
		var err error
		path, err = config.FindConfig("")
		if err != nil {
			return nil, fmt.Errorf("no leo.yaml found — run 'leo setup' first: %w", err)
		}
	}

	cfg, err := config.Load(path)
	if err != nil {
		return nil, err
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
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
