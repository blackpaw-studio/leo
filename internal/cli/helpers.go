package cli

import (
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/setup"
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

// loadDaemonConfig is loadConfig for the daemon. In a managed install the app
// registers the daemon before anything has run `leo setup`, so a missing
// default config is created here (see config.CreateIfAbsent) instead of the
// daemon exiting before the app can onboard Claude. Unmanaged installs, and an
// explicit --config, keep loadConfig's behavior.
func loadDaemonConfig(logw io.Writer) (*config.Config, error) {
	cfg, err := loadConfig()
	if err == nil || cfgFile != "" || !errors.Is(err, config.ErrConfigNotFound) {
		return cfg, err
	}
	home := config.DefaultHome()
	if home == "" || !loadManaged(filepath.Join(home, "state")).Managed {
		return nil, err
	}
	path := filepath.Join(home, "leo.yaml")
	created, createErr := config.CreateIfAbsent(path, setup.DefaultConfig(home))
	if createErr != nil {
		return nil, fmt.Errorf("creating default config: %w", createErr)
	}
	if created {
		fmt.Fprintf(logw, "managed install: no leo.yaml found; created default config at %s\n", path)
	}
	return loadConfig()
}

// leoExecutablePath is the leo path to persist (service plist): the marker's
// self_path when managed, else the running executable.
func leoExecutablePath(cfg *config.Config) (string, error) {
	path, err := selfExecutable(cfg)()
	if err != nil {
		// Fallback to looking up in PATH
		return exec.LookPath("leo")
	}
	return path, nil
}
