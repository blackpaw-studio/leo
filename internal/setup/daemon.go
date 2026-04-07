package setup

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
)

func installDaemon(name, workspace, cfgPath, botToken string) {
	leoPath, _ := os.Executable()
	if leoPath == "" {
		leoPath = "leo"
	}
	environ := env.Capture()
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
	if err := service.InstallDaemon(sc); err != nil {
		prompt.Warn.Printf("  Failed to install daemon: %v\n", err)
	} else {
		status, _ := service.DaemonStatus(name)
		prompt.Success.Printf("  Chat daemon installed (%s).\n", status)
		prompt.Info.Printf("  Logs: %s\n", sc.LogPath)
	}
}
