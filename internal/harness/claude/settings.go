package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// attentionNotificationMatcher limits the Notification hook to the prompts
// that block on the operator.
const attentionNotificationMatcher = "permission_prompt|elicitation_dialog"

// AgentPermissionWait is the longest a supervised agent's PermissionRequest
// hook waits for a Leo Chat answer (config.MaxChatPermissionTimeout). The
// hook's own timeout adds permissionHookGrace, so the hook always exits on
// its own; the daemon applies the configured, shorter wait.
const AgentPermissionWait = time.Hour

// contextHookTimeout bounds the SessionStart/UserPromptSubmit context
// hooks, in seconds. `leo dispatch context` fails open well inside it.
const contextHookTimeout = 10

// contextSessionStartMatcher is every SessionStart source that rebuilds the
// model's context, so the delegation context is told again after each.
const contextSessionStartMatcher = "startup|resume|clear|compact|fork"

// attentionReportEvents are the hooks, besides TurnHooks' Stop,
// UserPromptSubmit and SessionEnd, relayed to the daemon as they fire. They
// carry what the leo-bridge mod reported through the mod API events that
// cc-plugin-sec-default passes over (permission and elicitation attention and
// its clearing, subagents, compaction); command hooks are unaffected by it.
var attentionReportEvents = []string{
	"PostToolUse", "PostToolUseFailure", "PermissionDenied", "Elicitation",
	"SubagentStart", "SubagentStop", "PreCompact", "PostCompact",
}

// AttentionHooks is TurnHooks plus the hooks a supervised agent reports
// attention, subagents and compaction through, and the hooks that add the
// delegation context to its model.
//
// Each of attentionReportEvents, and a Notification group limited to the
// prompts that block on the operator, runs the report command; the daemon
// raises needs_input from a permission prompt or elicitation and clears it
// once the prompt is answered.
//
// A PermissionRequest group (`<leo> dispatch permission`, derived from a
// `<leo> dispatch report` command) offers each permission prompt in Leo
// Chat. Claude keeps its own dialog up while the hook waits, so the
// terminal can still answer first; the hook is then killed.
//
// A SessionStart group and a UserPromptSubmit group (`<leo> dispatch
// context`, derived the same way) add the delegation section as
// additionalContext, and again only when the roster changed.
func (c Claude) AttentionHooks(reportCmd []string) ([]string, error) {
	turn, err := c.TurnHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	report := []any{map[string]any{"type": "command", "command": harness.ShellCommand(reportCmd)}}
	reportGroup := map[string]any{"hooks": report}
	groups := map[string]any{
		"Notification": []any{map[string]any{
			"matcher": attentionNotificationMatcher,
			"hooks":   report,
		}},
		"PermissionRequest": []any{reportGroup},
	}
	for _, event := range attentionReportEvents {
		groups[event] = []any{reportGroup}
	}
	if n := len(reportCmd); n > 0 && reportCmd[n-1] == "report" {
		derived := func(subcommand string) string {
			return harness.ShellCommand(append(slices.Clone(reportCmd[:n-1]), subcommand))
		}
		groups["PermissionRequest"] = append(groups["PermissionRequest"].([]any), map[string]any{"hooks": []any{map[string]any{
			"type": "command", "command": derived("permission"),
			"timeout": int((AgentPermissionWait + permissionHookGrace).Seconds()),
		}}})
		contextHook := map[string]any{"type": "command", "command": derived("context"), "timeout": contextHookTimeout}
		groups["SessionStart"] = []any{map[string]any{
			"matcher": contextSessionStartMatcher,
			"hooks":   []any{contextHook},
		}}
		groups["UserPromptSubmit"] = []any{map[string]any{"hooks": []any{contextHook}}}
	}
	encoded, err := json.Marshal(map[string]any{"hooks": groups})
	if err != nil {
		return nil, fmt.Errorf("claude: encoding attention hooks: %w", err)
	}
	return MergeSettingsArgs(turn, []string{"--settings", string(encoded)}, MergeOptions{})
}

// attentionLaunch backs the driver's harness.AttentionHooker capability.
func attentionLaunch(h harness.SessionHandle, args, reportCmd []string) ([]string, error) {
	hooks, err := (Claude{}).AttentionHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	return MergeSettingsArgs(args, hooks, MergeOptions{BaseDir: h.Workspace, SpillPath: SettingsSpillPath(h.HomePath, h.Name)})
}

// MergeOptions configures MergeSettingsArgs.
type MergeOptions struct {
	// BaseDir resolves relative settings file paths (the session's working
	// directory).
	BaseDir string
	// SpillPath is the private file the merged settings are written to when
	// any input was a settings file.
	SpillPath string
}

// settingsFlag is claude's settings flag; it takes a JSON string or a path
// to a settings file, as `--settings <v>` or `--settings=<v>`.
const settingsFlag = "--settings"

// MergeSettingsArgs folds every --settings in args and extra into a single
// trailing --settings, because claude honors only one. Values may be inline
// JSON or a settings file path (relative paths resolve against baseDir, the
// session's working directory). Inline JSON stays inline, but when any value
// was a file the merged result is written to opts.SpillPath (0600) and
// passed by path, so a file's contents (credentials in env, say) never reach
// argv, ps, or tmux's retained pane command. Top-level keys
// merge, and object values (e.g. hooks) merge one level deep, later wins;
// other flags keep their order. An unreadable or invalid value is an error,
// never a second --settings or a silently dropped one. Neither input is
// mutated.
func MergeSettingsArgs(args, extra []string, opts MergeOptions) ([]string, error) {
	settings := map[string]any{}
	fromFile := false
	base := make([]string, 0, len(args)+len(extra))
	for _, group := range []struct {
		name string
		argv []string
	}{{"base", args}, {"extra", extra}} {
		for i := 0; i < len(group.argv); i++ {
			value, consumed, ok, err := settingsValue(group.argv, i)
			if err != nil {
				return nil, fmt.Errorf("merge %s settings: %w", group.name, err)
			}
			if !ok {
				base = append(base, group.argv[i])
				continue
			}
			i += consumed
			fromFile = fromFile || !isInlineSettings(value)
			next, err := loadSettings(value, opts.BaseDir)
			if err != nil {
				return nil, fmt.Errorf("merge %s settings: %w", group.name, err)
			}
			mergeSettings(settings, next)
		}
	}
	if len(settings) == 0 {
		return base, nil
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		return nil, fmt.Errorf("encode merged settings: %w", err)
	}
	if !fromFile {
		return append(base, settingsFlag, string(encoded)), nil
	}
	if err := writePrivateFile(opts.SpillPath, encoded); err != nil {
		return nil, fmt.Errorf("write merged settings: %w", err)
	}
	return append(base, settingsFlag, opts.SpillPath), nil
}

// isInlineSettings reports whether a --settings value is inline JSON rather
// than a settings file path.
func isInlineSettings(value string) bool {
	return strings.HasPrefix(strings.TrimSpace(value), "{")
}

// writePrivateFile replaces path with data, readable only by the owner,
// via a same-directory temp file and rename so claude never reads a
// partial write.
func writePrivateFile(path string, data []byte) error {
	if path == "" {
		return fmt.Errorf("a settings file was given but no private settings path is available; refusing to inline it into argv")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory's mode alone.
	// #nosec G302 -- a directory needs the owner execute bit; 0700 is owner-only
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".settings-*.json") // created 0600
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) //nolint:errcheck // gone after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// SettingsSpillPath is where a session's merged settings are written when
// they include a settings file: <home>/state/settings/<name>.json, one per
// session name, overwritten on each launch. "" when name is not a plain
// file name, which makes a file-settings merge fail closed.
func SettingsSpillPath(homePath, name string) string {
	if homePath == "" || name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\`) {
		return ""
	}
	return filepath.Join(homePath, "state", "settings", name+".json")
}

// settingsValue reports whether argv[i] is a --settings flag, returning its
// value and how many extra argv entries it consumed. A trailing --settings
// with no value is an error: passing it through would leave claude with two.
func settingsValue(argv []string, i int) (value string, consumed int, ok bool, err error) {
	arg := argv[i]
	if v, found := strings.CutPrefix(arg, settingsFlag+"="); found {
		return v, 0, true, nil
	}
	if arg != settingsFlag {
		return "", 0, false, nil
	}
	if i+1 >= len(argv) {
		return "", 0, false, fmt.Errorf("%s has no value", settingsFlag)
	}
	return argv[i+1], 1, true, nil
}

// loadSettings decodes one --settings value: inline JSON when it starts
// with '{', otherwise a settings file path.
func loadSettings(value, baseDir string) (map[string]any, error) {
	var out map[string]any
	if isInlineSettings(value) {
		if err := json.Unmarshal([]byte(value), &out); err != nil {
			return nil, fmt.Errorf("decoding inline settings: %w", err)
		}
		return out, nil
	}
	path := value
	if !filepath.IsAbs(path) && baseDir != "" {
		path = filepath.Join(baseDir, path)
	}
	data, err := os.ReadFile(path) //nolint:gosec // operator-configured settings file
	if err != nil {
		return nil, fmt.Errorf("reading settings file %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decoding settings file %s: %w", path, err)
	}
	return out, nil
}

// mergeSettings folds next into dst: top-level keys, and object values one
// level deep, later wins — except that arrays one level deep (hook event
// groups such as hooks.PostToolUse) append, so the operator's own hooks run
// alongside leo's. A group already present is not appended again, which
// keeps re-merging an already-merged argv idempotent.
func mergeSettings(dst, next map[string]any) {
	for key, value := range next {
		child, ok := value.(map[string]any)
		existing, isMap := dst[key].(map[string]any)
		if !ok || !isMap {
			dst[key] = value
			continue
		}
		for childKey, childValue := range child {
			existing[childKey] = appendGroups(existing[childKey], childValue)
		}
	}
}

// appendGroups appends next's elements to prev when both are arrays,
// skipping elements prev already holds; otherwise next wins.
func appendGroups(prev, next any) any {
	prevGroups, ok := prev.([]any)
	nextGroups, isArr := next.([]any)
	if !ok || !isArr {
		return next
	}
	out := slices.Clone(prevGroups)
	for _, group := range nextGroups {
		if !slices.ContainsFunc(out, func(g any) bool { return reflect.DeepEqual(g, group) }) {
			out = append(out, group)
		}
	}
	return out
}
