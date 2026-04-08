package migrate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/env"
	"github.com/blackpaw-studio/leo/internal/prompt"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/telegram"
)

type openClawJobsFile struct {
	Version int            `json:"version"`
	Jobs    []openClawJob  `json:"jobs"`
}

type openClawSchedule struct {
	Kind string `json:"kind"`
	Expr string `json:"expr"`
	Tz   string `json:"tz"`
}

type openClawPayload struct {
	Kind           string `json:"kind"`
	Message        string `json:"message"`
	TimeoutSeconds int    `json:"timeoutSeconds"`
}

type openClawDelivery struct {
	Mode    string `json:"mode"`
	Channel string `json:"channel"`
	To      string `json:"to"`
}

type openClawJob struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Enabled     bool             `json:"enabled"`
	Schedule    openClawSchedule `json:"schedule"`
	Payload     openClawPayload  `json:"payload"`
	Delivery    openClawDelivery `json:"delivery"`
}

// Run executes the OpenClaw migration wizard with its own banner.
func Run() error {
	reader := prompt.NewReader()

	fmt.Println()
	prompt.Bold.Println("  Leo Migration from OpenClaw")
	fmt.Println()

	return RunInteractive(reader)
}

// RunInteractive executes the migration wizard using the given reader, without printing a banner.
func RunInteractive(reader *bufio.Reader) error {
	ocPath, ocWorkspace, err := promptOpenClawPath(reader)
	if err != nil {
		return err
	}

	agentName, workspace, home, err := promptMigrationIdentity(reader, ocWorkspace)
	if err != nil {
		return err
	}

	if err := createWorkspaceDirs(workspace); err != nil {
		return err
	}

	agentPath, err := migrateAgentFiles(ocWorkspace, agentName, workspace, home)
	if err != nil {
		return err
	}

	migrateWorkspaceFiles(ocWorkspace, ocPath, workspace)

	cfg := buildMigrationConfig(agentName, workspace, reader, ocPath)

	cfgPath := filepath.Join(workspace, "leo.yaml")
	if err := config.Save(cfgPath, cfg); err != nil {
		return fmt.Errorf("writing config: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", cfgPath)

	syncDaemonSchedules(cfg, workspace)
	promptMigrationDaemonInstall(reader, cfg, agentName, workspace, cfgPath)
	sendMigrationTestMessage(reader, cfg)

	prompt.Bold.Println("\nMigration complete!")
	fmt.Printf("  Workspace: %s\n", workspace)
	fmt.Printf("  Agent file: %s\n", agentPath)
	fmt.Printf("  Config: %s\n", cfgPath)
	fmt.Printf("  Tasks: %d\n", len(cfg.Tasks))
	fmt.Println("\nNext steps:")
	fmt.Println("  1. Review the agent file and edit as needed")
	fmt.Println("  2. Run 'leo chat' to test interactive mode")
	fmt.Println("  3. Run 'leo run <task>' to test scheduled tasks")

	return nil
}

func promptOpenClawPath(reader *bufio.Reader) (ocPath, ocWorkspace string, err error) {
	ocPath = FindOpenClaw()
	if ocPath == "" {
		ocPath = prompt.Prompt(reader, "OpenClaw workspace path", "")
		if ocPath == "" {
			return "", "", fmt.Errorf("no OpenClaw installation found")
		}
	} else {
		prompt.Info.Printf("  Found OpenClaw at: %s\n", ocPath)
		if !prompt.YesNo(reader, "Use this installation?", true) {
			ocPath = prompt.Prompt(reader, "OpenClaw workspace path", "")
		}
	}

	ocWorkspace = filepath.Join(ocPath, "workspace")
	if _, err := os.Stat(ocWorkspace); os.IsNotExist(err) {
		ocWorkspace = ocPath
	}
	return ocPath, ocWorkspace, nil
}

func promptMigrationIdentity(reader *bufio.Reader, ocWorkspace string) (agentName, workspace, home string, err error) {
	agentName = detectAgentName(ocWorkspace)
	if agentName != "" {
		prompt.Info.Printf("  Detected agent name: %s\n", agentName)
	}
	agentName = prompt.Prompt(reader, "Agent name", agentName)

	home, _ = os.UserHomeDir()
	defaultWorkspace := filepath.Join(home, ".leo")
	workspace = prompt.Prompt(reader, "New workspace directory", defaultWorkspace)
	workspace = prompt.ExpandHome(workspace)
	return agentName, workspace, home, nil
}

func createWorkspaceDirs(workspace string) error {
	dirs := []string{
		workspace,
		filepath.Join(workspace, "daily"),
		filepath.Join(workspace, "reports"),
		filepath.Join(workspace, "state"),
		filepath.Join(workspace, "config"),
		filepath.Join(workspace, "scripts"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}
	return nil
}

func migrateAgentFiles(ocWorkspace, agentName, workspace, home string) (agentPath string, err error) {
	prompt.Bold.Println("\nMerging agent files...")
	agentContent := mergeAgentFiles(ocWorkspace, agentName, workspace)

	agentDir := filepath.Join(home, ".claude", "agents")
	if err := os.MkdirAll(agentDir, 0750); err != nil {
		return "", fmt.Errorf("creating agent directory: %w", err)
	}
	agentPath = filepath.Join(agentDir, agentName+".md")
	if err := os.WriteFile(agentPath, []byte(agentContent), 0644); err != nil {
		return "", fmt.Errorf("writing agent file: %w", err)
	}
	prompt.Info.Printf("  Wrote %s\n", agentPath)
	return agentPath, nil
}

func migrateWorkspaceFiles(ocWorkspace, ocPath, workspace string) {
	prompt.Bold.Println("\nCopying workspace files...")
	copyCount := copyWorkspaceFiles(ocWorkspace, workspace, ocPath)
	prompt.Info.Printf("  Copied %d files\n", copyCount)

	prompt.Bold.Println("\nRewriting paths...")
	rewriteCount := rewritePaths(workspace, ocWorkspace, workspace)
	if ocPath != ocWorkspace {
		rewriteCount += rewritePaths(workspace, ocPath, workspace)
	}
	prompt.Info.Printf("  Updated %d files\n", rewriteCount)
}

func buildMigrationConfig(agentName, workspace string, reader *bufio.Reader, ocPath string) *config.Config {
	prompt.Bold.Println("\nConverting cron jobs...")
	cfg := &config.Config{
		Agent: config.AgentConfig{
			Name:      agentName,
			Workspace: workspace,
		},
		Defaults: config.DefaultsConfig{
			Model:    config.DefaultModel,
			MaxTurns: config.DefaultMaxTurns,
		},
		Tasks: make(map[string]config.TaskConfig),
	}

	parseCronJobs(ocPath, cfg)

	prompt.Bold.Println("\nTelegram configuration...")
	configureTelegram(reader, ocPath, cfg)
	return cfg
}

func syncDaemonSchedules(cfg *config.Config, workspace string) {
	if len(cfg.Tasks) > 0 {
		if daemon.IsRunning(workspace) {
			if resp, err := daemon.Send(workspace, "POST", "/cron/install", nil); err == nil && resp.OK {
				prompt.Success.Println("  Schedules synced with daemon.")
			}
		} else {
			prompt.Info.Println("  Schedules will load when daemon starts.")
		}
	}
}

func promptMigrationDaemonInstall(reader *bufio.Reader, cfg *config.Config, agentName, workspace, cfgPath string) {
	if prompt.YesNo(reader, "\nInstall chat daemon (runs on login)?", true) {
		leoPath, _ := os.Executable()
		if leoPath == "" {
			leoPath = "leo"
		}
		fmt.Println("  Installing chat daemon...")
		environ := env.Capture()
		if cfg.Telegram.BotToken != "" {
			environ["TELEGRAM_BOT_TOKEN"] = cfg.Telegram.BotToken
		}
		sc := service.ServiceConfig{
			AgentName:  agentName,
			LeoPath:    leoPath,
			ConfigPath: cfgPath,
			WorkDir:    workspace,
			LogPath:    service.LogPathFor(workspace),
			Env:        environ,
		}
		if err := service.InstallDaemon(sc); err != nil {
			prompt.Warn.Printf("  Failed to install daemon: %v\n", err)
		} else {
			status, _ := service.DaemonStatus(agentName)
			prompt.Success.Printf("  Chat daemon installed (%s).\n", status)
			prompt.Info.Printf("  Logs: %s\n", sc.LogPath)
		}
	}
}

func sendMigrationTestMessage(reader *bufio.Reader, cfg *config.Config) {
	if cfg.Telegram.BotToken != "" && cfg.Telegram.ChatID != "" {
		if prompt.YesNo(reader, "\nSend test Telegram message?", true) {
			chatID := cfg.Telegram.ChatID
			if cfg.Telegram.GroupID != "" {
				chatID = cfg.Telegram.GroupID
			}
			if err := telegram.SendMessage(cfg.Telegram.BotToken, chatID, "Hello from Leo! Migration complete.", 0); err != nil {
				prompt.Warn.Printf("  Test message failed: %v\n", err)
			} else {
				prompt.Success.Println("  Test message sent!")
			}
		}
	}
}

// FindOpenClaw searches for an OpenClaw installation in common locations.
func FindOpenClaw() string {
	home, _ := os.UserHomeDir()

	ocPath := filepath.Join(home, ".openclaw")
	if _, err := os.Stat(ocPath); err == nil {
		return ocPath
	}

	return ""
}

func detectAgentName(workspace string) string {
	identityPath := filepath.Join(workspace, "IDENTITY.md")
	data, err := os.ReadFile(identityPath)
	if err != nil {
		return ""
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)

		// Look for **Name:** field (e.g. "- **Name:** Susie (also responds to ...)")
		if idx := strings.Index(line, "**Name:**"); idx >= 0 {
			name := strings.TrimSpace(line[idx+len("**Name:**"):])
			// Take first word/name before parenthetical or comma
			if i := strings.IndexAny(name, "(,"); i > 0 {
				name = strings.TrimSpace(name[:i])
			}
			if name != "" {
				return strings.ToLower(name)
			}
		}

		// Look for "name:" field (YAML-style)
		lower := strings.ToLower(line)
		if strings.HasPrefix(lower, "name:") {
			name := strings.TrimSpace(line[len("name:"):])
			if name != "" {
				return strings.ToLower(name)
			}
		}
	}

	// Fallback: use first heading that isn't the filename
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "# ") {
			heading := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(line, "#")))
			if heading != "identity.md" && heading != "identity" {
				return heading
			}
		}
	}

	return ""
}

func mergeAgentFiles(ocWorkspace, agentName, newWorkspace string) string {
	files := []string{"SOUL.md", "IDENTITY.md", "AGENTS.md", "TOOLS.md"}
	var sections []string

	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(ocWorkspace, f))
		if err != nil {
			continue
		}
		content := string(data)
		content = stripOpenClawContent(content)
		if strings.TrimSpace(content) != "" {
			sections = append(sections, content)
		}
	}

	var sb strings.Builder
	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("name: %s\n", agentName))
	sb.WriteString("description: Personal assistant (migrated from OpenClaw)\n")
	sb.WriteString("model: opus\n")
	sb.WriteString("tools: Read, Write, Edit, Bash, Grep, Glob, WebSearch, WebFetch\n")
	sb.WriteString("---\n\n")

	sb.WriteString(strings.Join(sections, "\n\n"))

	sb.WriteString(fmt.Sprintf("\n\n## Workspace\n\nYour workspace is `%s`. On startup:\n", newWorkspace))
	sb.WriteString("1. Read `USER.md` for context about the person you assist\n")
	sb.WriteString("2. Read `HEARTBEAT.md` if it exists\n")
	sb.WriteString("3. Check `daily/` for recent daily logs\n")

	return sb.String()
}

func stripOpenClawContent(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	skip := false

	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "openclaw") ||
			strings.Contains(lower, "heartbeat polling") ||
			strings.Contains(lower, "gateway health") {
			skip = true
			continue
		}
		if skip && strings.HasPrefix(line, "#") {
			skip = false
		}
		if !skip {
			result = append(result, line)
		}
	}

	return strings.Join(result, "\n")
}

func copyWorkspaceFiles(ocWorkspace, newWorkspace, ocRoot string) int {
	count := 0

	directCopies := map[string]string{
		"USER.md":      "USER.md",
		"HEARTBEAT.md": "HEARTBEAT.md",
	}
	for src, dst := range directCopies {
		if err := copyFile(filepath.Join(ocWorkspace, src), filepath.Join(newWorkspace, dst)); err == nil {
			count++
		}
	}

	copyDir(filepath.Join(ocWorkspace, "memory"), filepath.Join(newWorkspace, "daily"), &count)
	copyDir(filepath.Join(ocWorkspace, "Daily"), filepath.Join(newWorkspace, "daily"), &count)
	copyDir(filepath.Join(ocWorkspace, "reports"), filepath.Join(newWorkspace, "reports"), &count)
	copyDir(filepath.Join(ocWorkspace, "state"), filepath.Join(newWorkspace, "state"), &count)
	copyDir(filepath.Join(ocWorkspace, "config"), filepath.Join(newWorkspace, "config"), &count)
	copyDir(filepath.Join(ocWorkspace, "scripts"), filepath.Join(newWorkspace, "scripts"), &count)

	return count
}

func copyDir(src, dst string, count *int) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return
	}
	if err := os.MkdirAll(dst, 0750); err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := copyFile(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err == nil {
			*count++
		}
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0644)
}

func rewritePaths(workspace, oldPath, newPath string) int {
	root, err := os.OpenRoot(workspace)
	if err != nil {
		return 0
	}
	defer root.Close()

	count := 0
	filepath.WalkDir(workspace, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".md") {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		rel, err := filepath.Rel(workspace, path)
		if err != nil {
			return nil
		}
		data, err := root.ReadFile(rel)
		if err != nil {
			return nil
		}
		content := string(data)
		if strings.Contains(content, oldPath) {
			content = strings.ReplaceAll(content, oldPath, newPath)
			if err := root.WriteFile(rel, []byte(content), 0644); err != nil {
				return nil
			}
			count++
		}
		return nil
	})
	return count
}

func parseCronJobs(ocRoot string, cfg *config.Config) {
	jobsFile := filepath.Join(ocRoot, "cron", "jobs.json")
	data, err := os.ReadFile(jobsFile)
	if err != nil {
		prompt.Info.Println("  No cron/jobs.json found, skipping")
		return
	}

	var jobsWrapper openClawJobsFile
	if err := json.Unmarshal(data, &jobsWrapper); err != nil {
		prompt.Warn.Printf("  Failed to parse jobs.json: %v\n", err)
		return
	}

	var skipped []string
	for _, job := range jobsWrapper.Jobs {
		name := sanitizeTaskName(job.Name)

		lower := strings.ToLower(name)
		if strings.Contains(lower, "gateway") || strings.Contains(lower, "openclaw") {
			skipped = append(skipped, name)
			continue
		}

		tz := job.Schedule.Tz
		if tz == "" {
			tz = config.DefaultTimezone
		}

		task := config.TaskConfig{
			Schedule: job.Schedule.Expr,
			Timezone: tz,
			Enabled:  job.Enabled,
		}

		// Write inline prompt to a file
		if job.Payload.Message != "" {
			promptFile := fmt.Sprintf("reports/%s.md", name)
			promptPath := filepath.Join(cfg.Agent.Workspace, promptFile)
			if err := os.MkdirAll(filepath.Dir(promptPath), 0750); err != nil {
				prompt.Warn.Printf("  Failed to create prompt directory for %s: %v\n", name, err)
				continue
			}
			if err := os.WriteFile(promptPath, []byte(job.Payload.Message), 0644); err != nil {
				prompt.Warn.Printf("  Failed to write prompt file for %s: %v\n", name, err)
				continue
			}
			task.PromptFile = promptFile
		}

		// Note: OpenClaw used named topics (e.g. "telegram:topic:news").
		// Leo now uses numeric topic_id. Run `leo telegram topics` after
		// migration to discover IDs and set topic_id on tasks manually.

		cfg.Tasks[name] = task
		prompt.Info.Printf("  Migrated task: %s (%s)\n", name, job.Schedule.Expr)
	}

	for _, name := range skipped {
		prompt.Info.Printf("  Skipped: %s\n", name)
	}
}

func configureTelegram(reader *bufio.Reader, ocRoot string, cfg *config.Config) {
	// Primary source: openclaw.json channels.telegram
	ocConfigPath := filepath.Join(ocRoot, "openclaw.json")
	if data, err := os.ReadFile(ocConfigPath); err == nil {
		var ocConfig map[string]any
		if err := json.Unmarshal(data, &ocConfig); err == nil {
			if channels, ok := ocConfig["channels"].(map[string]any); ok {
				if tg, ok := channels["telegram"].(map[string]any); ok {
					if token, ok := tg["botToken"].(string); ok && token != "" {
						cfg.Telegram.BotToken = token
					}
					// Extract group IDs from the groups map
					if groups, ok := tg["groups"].(map[string]any); ok {
						for groupID := range groups {
							if groupID != "*" {
								cfg.Telegram.GroupID = groupID
							}
						}
					}
				}
			}
		}
	}

	// Fallback: .claude/channels/telegram/.env for bot token
	if cfg.Telegram.BotToken == "" {
		home, _ := os.UserHomeDir()
		envPath := filepath.Join(home, ".claude", "channels", "telegram", ".env")
		if data, err := os.ReadFile(envPath); err == nil {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "TELEGRAM_BOT_TOKEN=") {
					cfg.Telegram.BotToken = strings.TrimPrefix(line, "TELEGRAM_BOT_TOKEN=")
					cfg.Telegram.BotToken = strings.Trim(cfg.Telegram.BotToken, "\"'")
				}
			}
		}
	}

	// Fallback: credentials directory for chat/group/topics
	credDir := filepath.Join(ocRoot, "credentials")
	entries, _ := os.ReadDir(credDir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "telegram-") && strings.HasSuffix(e.Name(), ".json") {
			data, err := os.ReadFile(filepath.Join(credDir, e.Name()))
			if err != nil {
				continue
			}
			var cred map[string]any
			if err := json.Unmarshal(data, &cred); err != nil {
				continue
			}
			if chatID, ok := cred["chat_id"]; ok {
				cfg.Telegram.ChatID = fmt.Sprintf("%v", chatID)
			}
			if groupID, ok := cred["group_id"]; ok && cfg.Telegram.GroupID == "" {
				cfg.Telegram.GroupID = fmt.Sprintf("%v", groupID)
			}
			// Note: OpenClaw stored topic name→ID maps in credentials.
			// Leo now uses numeric topic_id on tasks directly.
			// Run `leo telegram topics` after migration to discover IDs.
		}
	}

	// Chat ID from allowFrom in credentials
	if cfg.Telegram.ChatID == "" {
		allowPath := filepath.Join(credDir, "telegram-default-allowFrom.json")
		if data, err := os.ReadFile(allowPath); err == nil {
			var allow map[string]any
			if err := json.Unmarshal(data, &allow); err == nil {
				if ids, ok := allow["allowFrom"].([]any); ok && len(ids) > 0 {
					cfg.Telegram.ChatID = fmt.Sprintf("%v", ids[0])
				}
			}
		}
	}

	if cfg.Telegram.BotToken != "" {
		prompt.Info.Printf("  Found bot token: %s...%s\n", cfg.Telegram.BotToken[:8], cfg.Telegram.BotToken[len(cfg.Telegram.BotToken)-4:])
	} else {
		cfg.Telegram.BotToken = prompt.Prompt(reader, "Bot token", "")
	}

	if cfg.Telegram.ChatID != "" {
		prompt.Info.Printf("  Found chat ID: %s\n", cfg.Telegram.ChatID)
	} else {
		cfg.Telegram.ChatID = prompt.Prompt(reader, "Chat ID", "")
	}

	if cfg.Telegram.GroupID != "" {
		prompt.Info.Printf("  Found group ID: %s\n", cfg.Telegram.GroupID)
	}
}

func sanitizeTaskName(name string) string {
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, " ", "-")
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

