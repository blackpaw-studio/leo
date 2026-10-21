package setup

import (
	"os"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
)

var (
	osExecutableFn  = os.Executable
	envCaptureFn    = env.Capture
	installDaemonFn = service.InstallDaemon
	loadManagedFn   = func(stateDir string) managed.State { return managed.Load(stateDir, managed.DefaultDeps()) }
)

// installDaemon installs the LaunchAgent/systemd service. leoHome is the
// leo home directory (~/.leo or an override) — it doubles as the
// service's WorkDir and now also drives the OS-service identity (launchd
// label / systemd unit name), so this name is load-bearing.
// daemonStatusFn is declared in setup.go (same package).
func installDaemon(leoHome, cfgPath string) {
	// A managed install persists its stable self_path, not the running binary.
	leoPath, _ := managed.SelfExecutable(loadManagedFn(filepath.Join(leoHome, "state")), osExecutableFn)()
	if leoPath == "" {
		leoPath = "leo"
	}
	environ := envCaptureFn()
	sc := service.ServiceConfig{
		LeoPath:    leoPath,
		ConfigPath: cfgPath,
		WorkDir:    leoHome,
		LogPath:    service.LogPathFor(leoHome),
		Env:        environ,
	}
	if err := installDaemonFn(sc); err != nil {
		prompt.Warn.Printf("  Failed to install daemon: %v\n", err)
	} else {
		status, _ := daemonStatusFn(leoHome)
		prompt.Success.Printf("  Chat daemon installed (%s).\n", status)
		prompt.Info.Printf("  Logs: %s\n", sc.LogPath)
	}
}
