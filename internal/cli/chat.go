package cli

import (
	"os"
	"os/exec"
	"syscall"

	"github.com/spf13/cobra"
)

func newChatCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "chat",
		Short: "Start interactive Telegram session",
		Long:  "Start a long-running claude session with Telegram channel plugin for inbound messages.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return err
			}

			claudeArgs := []string{
				"--agent", cfg.Agent.Name,
				"--channels", "plugin:telegram@claude-plugins-official",
				"--add-dir", cfg.Agent.Workspace,
			}

			mcpConfig := cfg.MCPConfigPath()
			if _, err := os.Stat(mcpConfig); err == nil {
				claudeArgs = append(claudeArgs, "--mcp-config", mcpConfig)
			}

			claudePath, err := exec.LookPath("claude")
			if err != nil {
				return err
			}

			info.Printf("Starting interactive session for agent %q...\n", cfg.Agent.Name)

			// Exec replaces this process with claude
			return syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), os.Environ())
		},
	}
}
