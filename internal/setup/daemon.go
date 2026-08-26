package setup

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
)

var (
	osExecutableFn  = os.Executable
	envCaptureFn    = env.Capture
	installDaemonFn = service.InstallDaemon
)

// installDaemon installs the LaunchAgent/systemd service. leoHome is the
// leo home directory (~/.leo or an override) — it doubles as the
// service's WorkDir and now also drives the OS-service identity (launchd
// label / systemd unit name), so this name is load-bearing.
// daemonStatusFn is declared in setup.go (same package).
func installDaemon(leoHome, cfgPath string) {
	leoPath, _ := osExecutableFn()
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
