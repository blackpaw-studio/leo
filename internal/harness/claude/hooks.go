package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var prepareInteractiveMu sync.Mutex

// TurnHooks adds Claude's native lifecycle hooks to its existing settings
// document. The report command exits successfully unless dispatch launch
// variables are present, so ordinary Claude sessions remain unaffected.
func (Claude) TurnHooks(reportCmd []string) ([]string, error) {
	if len(reportCmd) == 0 {
		return nil, fmt.Errorf("claude: empty dispatch report command")
	}
	settings := map[string]any{"crossSessionInbound": "accept", "hooks": map[string]any{}}
	hooks := settings["hooks"].(map[string]any)
	command := shellCommand(reportCmd)
	for _, event := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		hooks[event] = []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}}}
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("claude: encoding hook settings: %w", err)
	}
	return []string{"--settings", string(encoded)}, nil
}

// PrepareInteractive accepts Claude's workspace trust dialog before launch.
// Its settings file contains unrelated user configuration, so only the
// dispatch project entry is changed and the result replaces the file atomically.
func (Claude) PrepareInteractive(home, cwd string) error {
	prepareInteractiveMu.Lock()
	defer prepareInteractiveMu.Unlock()
	path := filepath.Join(home, ".claude.json")
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("claude: resolving workspace: %w", err)
	}
	resolvedCwd, err := canonicalPath(cwd)
	if err != nil {
		return fmt.Errorf("claude: resolving workspace: %w", err)
	}
	settings := map[string]any{}
	raw, err := os.ReadFile(path) // #nosec G304 -- Claude home settings path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("claude: reading %s: %w", path, err)
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("claude: parsing %s: %w", path, err)
		}
	}
	projects, ok := settings["projects"].(map[string]any)
	if !ok {
		projects = map[string]any{}
		settings["projects"] = projects
	}
	// Claude has used both lexical and symlink-resolved workspace keys. Honor
	// an existing key first; new entries use the resolved path Claude uses on
	// macOS (notably /tmp -> /private/tmp).
	key := resolvedCwd
	if _, exists := projects[absCwd]; exists {
		key = absCwd
	} else if _, exists := projects[resolvedCwd]; exists {
		key = resolvedCwd
	}
	project, ok := projects[key].(map[string]any)
	if !ok {
		project = map[string]any{}
		projects[key] = project
	}
	if project["hasTrustDialogAccepted"] == true {
		return nil
	}
	project["hasTrustDialogAccepted"] = true
	encoded, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("claude: encoding %s: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("claude: creating settings directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".claude.json-*")
	if err != nil {
		return fmt.Errorf("claude: creating temporary settings: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(encoded); err != nil {
		tmp.Close()
		return fmt.Errorf("claude: writing temporary settings: %w", err)
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("claude: setting temporary settings permissions: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("claude: closing temporary settings: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("claude: replacing %s: %w", path, err)
	}
	return nil
}

func canonicalPath(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	return abs, nil
}

func shellCommand(args []string) string {
	words := make([]string, 0, len(args))
	for _, arg := range args {
		if arg != "" && !strings.ContainsAny(arg, " \t\n\r'\"\\$`;&|<>()*?[]{}!") {
			words = append(words, arg)
			continue
		}
		words = append(words, "'"+strings.ReplaceAll(arg, "'", "'\\\"'\\\"'")+"'")
	}
	return strings.Join(words, " ")
}
