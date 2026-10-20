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

// skipManaged reports whether Leo.app owns the daemon for leoHome, saying so:
// the app registers its own LaunchAgent, so the wizard must not install one.
func skipManaged(leoHome string) bool {
	st := loadManagedFn(filepath.Join(leoHome, "state"))
	if !st.Managed {
		return false
	}
	prompt.Info.Printf("  The leo daemon is managed by Leo.app (launchd job %s); skipping daemon install.\n", st.LaunchdLabel)
	return true
}

// installDaemon installs the LaunchAgent/systemd service. leoHome is the
// leo home directory (~/.leo or an override) — it doubles as the
// service's WorkDir and now also drives the OS-service identity (launchd
// label / systemd unit name), so this name is load-bearing.
// daemonStatusFn is declared in setup.go (same package).
func installDaemon(leoHome, cfgPath string) {
	if skipManaged(leoHome) {
		return
	}
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
