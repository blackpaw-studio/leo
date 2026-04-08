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

func installDaemon(name, workspace, cfgPath, botToken string) {
	leoPath, _ := osExecutableFn()
	if leoPath == "" {
		leoPath = "leo"
	}
	environ := envCaptureFn()
	if botToken != "" {
		environ["TELEGRAM_BOT_TOKEN"] = botToken
	}
	sc := service.ServiceConfig{
		AgentName:  name,
		LeoPath:    leoPath,
		ConfigPath: cfgPath,
		WorkDir:    workspace,
		LogPath:    service.LogPathFor(workspace),
		Env:        environ,
	}
	if err := installDaemonFn(sc); err != nil {
		prompt.Warn.Printf("  Failed to install daemon: %v\n", err)
	} else {
		status, _ := daemonStatusFn(name)
		prompt.Success.Printf("  Chat daemon installed (%s).\n", status)
		prompt.Info.Printf("  Logs: %s\n", sc.LogPath)
	}
}
