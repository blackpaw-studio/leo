package setup

import (
	"os"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
)

func installCron(cfg *config.Config) {
	leoPath, _ := os.Executable()
	if leoPath == "" {
		leoPath = "leo"
	}
	if err := cron.Install(cfg, leoPath); err != nil {
		prompt.Warn.Printf("  Failed to install cron: %v\n", err)
	} else {
		prompt.Success.Println("  Cron entries installed.")
	}
}

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
