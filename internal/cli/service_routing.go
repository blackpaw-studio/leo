package cli

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/service"
)

// Seams over the service package: tests replace them, so a refused or routed
// command can be shown to have reached (or not reached) the OS service layer.
var (
	startServiceFn      = service.Start
	stopServiceFn       = service.Stop
	installDaemonFn     = service.InstallDaemon
	removeDaemonFn      = service.RemoveDaemon
	restartLabelFn      = service.RestartLabel
	restartDaemonFn     = service.RestartDaemon
	daemonStatusFn      = service.DaemonStatus
	daemonStatusLabelFn = service.DaemonStatusLabel
	daemonRegisteredFn  = service.DaemonRegistered
	labelLoadedFn       = service.LabelLoaded
	teardownSessionsFn  = service.TeardownSessions
)

// managedServiceRefusal is why start/stop/install/uninstall do nothing when
// Leo.app owns the daemon: the app registers the LaunchAgent, and its
// KeepAlive would undo a CLI stop anyway.
func managedServiceRefusal(label string) error {
	return fmt.Errorf("the leo daemon is managed by Leo.app (launchd job %s); start, stop and (un)register it from the app, or use 'leo service restart'", label)
}

// refuseIfManaged returns managedServiceRefusal when a valid marker exists.
// A missing or invalid leo.yaml must not hide the marker.
func refuseIfManaged() error {
	st := managedState(configOrDefault())
	if !st.Managed {
		return nil
	}
	return managedServiceRefusal(st.LaunchdLabel)
}

// configOrDefault loads the config, falling back to the default home when
// there is none, for commands that only need the home to find the marker.
func configOrDefault() *config.Config {
	cfg, err := loadConfig()
	if err != nil {
		return &config.Config{HomePath: config.DefaultHome()}
	}
	return cfg
}

// newDaemonRestartDeps routes the restart to the job that owns the daemon:
// the marker's launchd label when managed, leo's own job otherwise.
func newDaemonRestartDeps(cfg *config.Config) daemonRestartDeps {
	deps := defaultDaemonRestartDeps
	if st := managedState(cfg); st.Managed {
		label := st.LaunchdLabel
		deps.restart = func(string) error { return restartLabelFn(label) }
	}
	return deps
}

// daemonStatusFor reports the status of the job that owns the daemon.
func daemonStatusFor(cfg *config.Config) (string, error) {
	if st := managedState(cfg); st.Managed {
		return daemonStatusLabelFn(st.LaunchdLabel)
	}
	return daemonStatusFn(cfg.HomePath)
}

// daemonJobLoaded reports whether the job that would restart the daemon is
// registered with the service manager.
func daemonJobLoaded(cfg *config.Config, st managed.State) bool {
	if st.Managed {
		return labelLoadedFn(st.LaunchdLabel)
	}
	return daemonRegisteredFn(cfg.HomePath)
}

var daemonLabelFn = service.DaemonLabel
