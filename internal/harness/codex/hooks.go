package codex

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

var defaultLeoHookCommand = func() string {
	path, err := os.Executable()
	if err != nil {
		return "leo dispatch report"
	}
	return shellCommand([]string{path, "dispatch", "report"})
}

var prepareLeoHookCommand = defaultLeoHookCommand

var codexHookEvents = []string{"Stop", "UserPromptSubmit", "Interrupt", "SessionEnd"}

// CodexHome resolves the same home directory used by a Codex launch: an
// explicitly supplied launch environment wins over the daemon environment,
// then Codex's conventional $HOME/.codex location is used.
func CodexHome(env map[string]string) string {
	if home := env["CODEX_HOME"]; home != "" {
		return home
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".codex")
	}
	return filepath.Join(home, ".codex")
}

// TurnHooks disables Codex's startup update dialog. Codex 0.153.4 ignores
// hooks.* command-line overrides, so PrepareInteractive installs home-file
// hooks instead. The report command itself is intentionally a no-op unless
// LEO_DISPATCH_ID and LEO_CONFIG are present (implemented by dispatch report).
func (Codex) TurnHooks(_ []string) ([]string, error) {
	return []string{"-c", "check_for_update_on_startup=false"}, nil
}

// PrepareInteractive installs and trusts Leo's home-file hooks before an
// interactive dispatch launch. Existing user hooks are retained; an untrusted
// one fails fast rather than allowing Codex to show its review dialog.
func (Codex) PrepareInteractive(home, _ string) error {
	command := prepareLeoHookCommand()
	hooksPath, err := canonicalPath(filepath.Join(home, "hooks.json"))
	if err != nil {
		return fmt.Errorf("codex: resolving hooks path: %w", err)
	}
	configPath, err := canonicalPath(filepath.Join(home, "config.toml"))
	if err != nil {
		return fmt.Errorf("codex: resolving config path: %w", err)
	}
	hooks, err := readHooks(hooksPath)
	if err != nil {
		return err
	}
	mergeLeoHooks(hooks, command)

	config, err := os.ReadFile(configPath) // #nosec G304 -- CODEX_HOME config path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: reading %s: %w", configPath, err)
	}
	trusted := trustedHashes(string(config))
	entries, untrusted := hookTrustEntries(hooksPath, hooks, trusted)
	if len(untrusted) > 0 {
		return fmt.Errorf("codex: untrusted hooks would trigger review: %s", strings.Join(untrusted, ", "))
	}
	if err := writeHooks(hooksPath, hooks); err != nil {
		return err
	}
	if err := appendTrustEntries(configPath, string(config), entries); err != nil {
		return err
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

func readHooks(path string) (map[string]any, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- CODEX_HOME hooks path
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("codex: reading %s: %w", path, err)
	}
	hooks := map[string]any{"hooks": map[string]any{}}
	if len(raw) == 0 {
		return hooks, nil
	}
	if err := json.Unmarshal(raw, &hooks); err != nil {
		return nil, fmt.Errorf("codex: parsing %s: %w", path, err)
	}
	if _, ok := hooks["hooks"].(map[string]any); !ok {
		return nil, fmt.Errorf("codex: %s has no hooks object", path)
	}
	return hooks, nil
}

func mergeLeoHooks(file map[string]any, command string) {
	events := file["hooks"].(map[string]any)
	for _, event := range codexHookEvents {
		groups, _ := events[event].([]any)
		if containsCommand(groups, command) {
			continue
		}
		events[event] = append(groups, map[string]any{"hooks": []any{map[string]any{"type": "command", "command": command}}})
	}
}

func containsCommand(groups []any, command string) bool {
	for _, group := range groups {
		g, _ := group.(map[string]any)
		for _, handler := range asSlice(g["hooks"]) {
			h, _ := handler.(map[string]any)
			if h["type"] == "command" && h["command"] == command {
				return true
			}
		}
	}
	return false
}

func writeHooks(path string, hooks map[string]any) error {
	encoded, err := json.MarshalIndent(hooks, "", "  ")
	if err != nil {
		return fmt.Errorf("codex: encoding hooks: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("codex: creating %s: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, encoded, 0o600); err != nil { // #nosec G306 -- Codex config is private
		return fmt.Errorf("codex: writing %s: %w", path, err)
	}
	return nil
}

func hookTrustEntries(path string, file map[string]any, trusted map[string]string) ([]string, []string) {
	var entries, untrusted []string
	events := file["hooks"].(map[string]any)
	for _, event := range sortedKeys(events) {
		groups, ok := events[event].([]any)
		if !ok {
			continue
		}
		for gi, group := range groups {
			g, _ := group.(map[string]any)
			matcher, _ := g["matcher"].(string)
			for hi, raw := range asSlice(g["hooks"]) {
				handler, ok := raw.(map[string]any)
				if !ok || handler["type"] != "command" {
					continue
				}
				key := fmt.Sprintf("%s:%s:%d:%d", path, eventLabel(event), gi, hi)
				hash := trustHash(event, matcherPtr(matcher), handler)
				if trusted[key] == hash {
					continue
				}
				command, _ := handler["command"].(string)
				if command == prepareLeoHookCommand() {
					entries = append(entries, trustEntry(key, hash))
				} else {
					untrusted = append(untrusted, command)
				}
			}
		}
	}
	return entries, untrusted
}

func matcherPtr(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// trustHash mirrors codex-rs' version_for_toml(hook_hash(...)): hash the
// canonical JSON projection of one normalized matcher group, including the
// event's default command timeout and async=false.
func trustHash(event string, matcher *string, handler map[string]any) string {
	timeout := number(handler["timeout"])
	if timeout == 0 {
		if event == "SessionEnd" || event == "Interrupt" {
			timeout = 1
		} else {
			timeout = 600
		}
	}
	if event == "SessionEnd" || event == "Interrupt" {
		if timeout > 3 {
			timeout = 3
		}
		if timeout < 1 {
			timeout = 1
		}
	}
	normalized := map[string]any{"type": "command", "command": handler["command"], "timeout": timeout, "async": false}
	if async, ok := handler["async"].(bool); ok {
		normalized["async"] = async
	}
	for _, key := range []string{"commandWindows", "statusMessage", "additionalContextLimit"} {
		if value, ok := handler[key]; ok {
			normalized[key] = value
		}
	}
	identity := map[string]any{"event_name": eventLabel(event), "hooks": []any{normalized}}
	if matcher != nil {
		identity["matcher"] = *matcher
	}
	encoded, _ := json.Marshal(identity) // encoding/json sorts map keys, matching canonical_json.
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func number(value any) int64 {
	switch n := value.(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

func eventLabel(event string) string {
	var out []rune
	for i, r := range event {
		if i > 0 && r >= 'A' && r <= 'Z' {
			out = append(out, '_')
		}
		out = append(out, []rune(strings.ToLower(string(r)))...)
	}
	return string(out)
}

func trustedHashes(config string) map[string]string {
	result := map[string]string{}
	lines := strings.Split(config, "\n")
	for i := 0; i+1 < len(lines); i++ {
		const prefix = "[hooks.state."
		if !strings.HasPrefix(lines[i], prefix) || !strings.HasSuffix(lines[i], "]") {
			continue
		}
		key, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(lines[i], prefix), "]"))
		if err != nil || !strings.HasPrefix(strings.TrimSpace(lines[i+1]), "trusted_hash = ") {
			continue
		}
		value := strings.TrimPrefix(strings.TrimSpace(lines[i+1]), "trusted_hash = ")
		if hash, err := strconv.Unquote(value); err == nil {
			result[key] = hash
		}
	}
	return result
}

func appendTrustEntries(path, config string, entries []string) error {
	if len(entries) == 0 {
		return nil
	}
	for _, entry := range entries {
		if strings.Contains(config, entry) {
			continue
		}
		if err := appendConfigEntry(path, entry); err != nil {
			return err
		}
	}
	return nil
}

// appendConfigEntry is shared by Codex's workspace-trust and hook-trust
// prelaunch paths so both preserve user config and write with the same mode.
func appendConfigEntry(path, entry string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("codex: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304 -- CODEX_HOME config path
	if err != nil {
		return fmt.Errorf("codex: opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n%s\n", entry); err != nil {
		return fmt.Errorf("codex: writing config entry: %w", err)
	}
	return nil
}

func trustEntry(key, hash string) string {
	return "[hooks.state." + strconv.Quote(key) + "]\ntrusted_hash = " + strconv.Quote(hash)
}

func asSlice(value any) []any {
	items, _ := value.([]any)
	return items
}

func sortedKeys(values map[string]any) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// shellCommand joins an argv as POSIX-shell words because Codex executes a
// command hook through a shell. Keeping safe words bare preserves the
// documented command shape while protecting executable paths with spaces.
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
