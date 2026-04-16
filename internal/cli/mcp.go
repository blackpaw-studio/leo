package cli

import (
	"github.com/spf13/cobra"

	"github.com/blackpaw-studio/leo/internal/mcp"
)

func newMCPServerCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp-server",
		Short: "Run the leo MCP server (stdio, called by Claude Code via --mcp-config)",
		Long: `Run the leo MCP server on stdin/stdout.

Not invoked directly by users. Leo wires this command into the supervised
Claude process's MCP config so the supervised assistant can dispatch the
universal channel slash commands (/clear, /compact, /stop, /tasks, /agent,
/agents) by calling the leo_* tools this server exposes.

Reads LEO_PROCESS_NAME and LEO_WEB_PORT from the environment to identify
the running process and reach the daemon HTTP API. Both are injected by
the Leo supervisor.`,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return mcp.Run()
		},
	}
}
