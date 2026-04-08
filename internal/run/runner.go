package run

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

var execCommand = exec.Command

const silentPreamble = `SILENT SCHEDULED RUN — You are running as a scheduled background task, not responding to a user message.
Work silently. Do not narrate your process or describe your tool usage.
When finished:
- If there is something the user needs to see, send ONLY the final user-facing message via Telegram.
- If there is nothing to report, output exactly: NO_REPLY
Do not include status updates, tool output, or process descriptions.
`

const telegramProtocolTemplate = `
## Telegram Notification Protocol
If anything needs the user's attention, send a Telegram message using:
` + "```bash" + `
curl -s -X POST "https://api.telegram.org/bot%s/sendMessage" \
  -H "Content-Type: application/json" \
  -d '{"chat_id": "%s", %s"parse_mode": "Markdown", "text": "<your message>"}'
` + "```" + `

IMPORTANT: The message is sent as a JSON payload. Escape any double quotes in your
message text with a backslash. Do not use shell variables or unescaped special characters.

If nothing needs attention, reply NO_REPLY and exit.
Do not include process narration, status updates, or tool output. Only emit the final user-facing message or NO_REPLY.
`

// resolveTask looks up a task by name, checking both the tasks map and heartbeat config.
func resolveTask(cfg *config.Config, taskName string) (config.TaskConfig, error) {
	if task, ok := cfg.Tasks[taskName]; ok {
		return task, nil
	}
	if taskName == "heartbeat" && cfg.Heartbeat.Enabled {
		return cfg.Heartbeat.ToTaskConfig()
	}
	return config.TaskConfig{}, fmt.Errorf("task %q not found in config", taskName)
}

// Preview returns the assembled prompt and CLI args without executing.
func Preview(cfg *config.Config, taskName string) (string, []string, error) {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return "", nil, err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return "", nil, fmt.Errorf("assembling prompt: %w", err)
	}

	args := buildArgs(cfg, task, prompt)
	return prompt, args, nil
}

// Run executes a scheduled task.
func Run(cfg *config.Config, taskName string) error {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return fmt.Errorf("assembling prompt: %w", err)
	}

	args := buildArgs(cfg, task, prompt)

	cmd := execCommand("claude", args...)
	cmd.Dir = cfg.Agent.Workspace
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")

	output, err := cmd.CombinedOutput()

	// Log output regardless of error
	if logErr := writeLog(cfg, taskName, output); logErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write log: %v\n", logErr)
	}

	if err != nil {
		return fmt.Errorf("claude exited with error: %w\nOutput: %s", err, string(output))
	}

	return nil
}

func assemblePrompt(cfg *config.Config, task config.TaskConfig) (string, error) {
	promptPath := filepath.Join(cfg.Agent.Workspace, task.PromptFile)
	promptData, err := os.ReadFile(promptPath)
	if err != nil {
		return "", fmt.Errorf("reading prompt file %s: %w", promptPath, err)
	}

	var parts []string

	if task.Silent {
		parts = append(parts, silentPreamble)
	}

	parts = append(parts, string(promptData))

	// Append Telegram notification protocol
	chatID := cfg.Telegram.ChatID
	if cfg.Telegram.GroupID != "" {
		chatID = cfg.Telegram.GroupID
	}

	topicLine := ""
	if task.TopicID > 0 {
		topicLine = fmt.Sprintf(`"message_thread_id": %d, `, task.TopicID)
	}

	telegramProtocol := fmt.Sprintf(telegramProtocolTemplate,
		cfg.Telegram.BotToken,
		chatID,
		topicLine,
	)
	parts = append(parts, telegramProtocol)

	return strings.Join(parts, "\n"), nil
}

func buildArgs(cfg *config.Config, task config.TaskConfig, prompt string) []string {
	args := []string{
		"--agent", cfg.Agent.Name,
		"-p", prompt,
		"--model", cfg.TaskModel(task),
		"--max-turns", strconv.Itoa(cfg.TaskMaxTurns(task)),
		"--output-format", "text",
	}

	if task.ContinueSession {
		args = append(args, "--continue")
	}

	if cfg.Defaults.BypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	mcpConfig := cfg.MCPConfigPath()
	if _, err := os.Stat(mcpConfig); err == nil {
		args = append(args, "--mcp-config", mcpConfig)
	}

	args = append(args, "--add-dir", cfg.Agent.Workspace)

	return args
}

func writeLog(cfg *config.Config, taskName string, output []byte) error {
	stateDir := filepath.Join(cfg.Agent.Workspace, "state")
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		return err
	}

	logPath := filepath.Join(stateDir, taskName+".log")
	return os.WriteFile(logPath, output, 0644)
}
