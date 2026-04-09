package run

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/history"
	"github.com/blackpaw-studio/leo/internal/session"
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

// claudeResult is the minimal structure for parsing claude --output-format json.
type claudeResult struct {
	SessionID string `json:"session_id"`
	Result    string `json:"result"`
	IsError   bool   `json:"is_error"`
}

// resolveTask looks up a task by name.
func resolveTask(cfg *config.Config, taskName string) (config.TaskConfig, error) {
	if task, ok := cfg.Tasks[taskName]; ok {
		return task, nil
	}
	return config.TaskConfig{}, fmt.Errorf("task %q not found in config", taskName)
}

// Preview returns the assembled prompt and CLI args without executing.
func Preview(cfg *config.Config, taskName string, sessions *session.Store) (string, []string, error) {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return "", nil, err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return "", nil, fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	args := buildArgs(cfg, task, prompt, sessionID)
	return prompt, args, nil
}

// Run executes a scheduled task.
func Run(cfg *config.Config, taskName string, sessions *session.Store) error {
	task, err := resolveTask(cfg, taskName)
	if err != nil {
		return err
	}

	prompt, err := assemblePrompt(cfg, task)
	if err != nil {
		return fmt.Errorf("assembling prompt: %w", err)
	}

	var sessionID string
	if sessions != nil {
		sid, _, getErr := sessions.Get("task:" + taskName)
		if getErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not read session store: %v\n", getErr)
		}
		sessionID = sid
	}

	args := buildArgs(cfg, task, prompt, sessionID)

	taskWorkspace := cfg.TaskWorkspace(task)
	output, execErr := executeCommand(taskWorkspace, args)
	result := parseClaudeOutput(output)

	// If --resume failed with a stale session, retry without it.
	if execErr != nil && sessionID != "" && isSessionError(result, output) {
		if sessions != nil {
			if delErr := sessions.Delete("task:" + taskName); delErr != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to clear stale session: %v\n", delErr)
			}
		}

		args = buildArgs(cfg, task, prompt, "")
		output, execErr = executeCommand(taskWorkspace, args)
		result = parseClaudeOutput(output)
	}

	// Store session ID for next run
	if sessions != nil && result.SessionID != "" {
		if setErr := sessions.Set("task:"+taskName, result.SessionID); setErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to store session ID: %v\n", setErr)
		}
	}

	// Log readable output
	logContent := result.Result
	if logContent == "" {
		logContent = string(output)
	}
	if logErr := writeLog(cfg, taskName, []byte(logContent)); logErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to write log: %v\n", logErr)
	}

	// Record execution history
	exitCode := 0
	if execErr != nil {
		exitCode = 1
	}
	hist := history.NewStore(cfg.HomePath)
	if histErr := hist.Record(taskName, exitCode); histErr != nil {
		fmt.Fprintf(os.Stderr, "warning: failed to record history: %v\n", histErr)
	}

	if execErr != nil {
		return fmt.Errorf("claude exited with error: %w\nOutput: %s", execErr, string(output))
	}

	return nil
}

// isSessionError checks whether a claude failure was caused by an invalid/stale session.
func isSessionError(result claudeResult, output []byte) bool {
	text := strings.ToLower(result.Result)
	if text == "" {
		text = strings.ToLower(string(output))
	}
	return strings.Contains(text, "session") &&
		(strings.Contains(text, "not found") || strings.Contains(text, "invalid") || strings.Contains(text, "expired"))
}

func executeCommand(workDir string, args []string) ([]byte, error) {
	cmd := execCommand("claude", args...)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(), "CLAUDE_CODE_ENTRYPOINT=cli")
	return cmd.CombinedOutput()
}

// parseClaudeOutput attempts to extract structured data from claude JSON output.
func parseClaudeOutput(output []byte) claudeResult {
	var result claudeResult
	_ = json.Unmarshal(output, &result)
	return result
}

func assemblePrompt(cfg *config.Config, task config.TaskConfig) (string, error) {
	taskWorkspace := cfg.TaskWorkspace(task)
	promptPath := filepath.Join(taskWorkspace, task.PromptFile)

	// Prevent path traversal: ensure the resolved path is within the workspace.
	absPrompt, err := filepath.Abs(promptPath)
	if err != nil {
		return "", fmt.Errorf("resolving prompt path: %w", err)
	}
	absWorkspace, err := filepath.Abs(taskWorkspace)
	if err != nil {
		return "", fmt.Errorf("resolving workspace path: %w", err)
	}
	if !strings.HasPrefix(absPrompt, absWorkspace+string(filepath.Separator)) {
		return "", fmt.Errorf("prompt file %q escapes workspace", task.PromptFile)
	}

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

func buildArgs(cfg *config.Config, task config.TaskConfig, prompt string, sessionID string) []string {
	args := []string{
		"-p", prompt,
		"--model", cfg.TaskModel(task),
		"--max-turns", strconv.Itoa(cfg.TaskMaxTurns(task)),
		"--output-format", "json",
	}

	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}

	if cfg.Defaults.BypassPermissions {
		args = append(args, "--dangerously-skip-permissions")
	}

	mcpConfig := cfg.TaskMCPConfigPath(task)
	if hasMCPServers(mcpConfig) {
		args = append(args, "--mcp-config", mcpConfig)
	}

	taskWorkspace := cfg.TaskWorkspace(task)
	args = append(args, "--add-dir", taskWorkspace)

	return args
}

// hasMCPServers returns true if the MCP config file exists and contains
// at least one server entry.
func hasMCPServers(path string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	var cfg struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return false
	}
	return len(cfg.MCPServers) > 0
}

func writeLog(cfg *config.Config, taskName string, output []byte) error {
	stateDir := cfg.StatePath()
	if err := os.MkdirAll(stateDir, 0750); err != nil {
		return err
	}

	logPath := filepath.Join(stateDir, taskName+".log")
	return os.WriteFile(logPath, output, 0600)
}
