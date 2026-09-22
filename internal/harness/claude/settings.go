package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// attentionNotificationMatcher limits the Notification hook to the prompts
// that block on the operator.
const attentionNotificationMatcher = "permission_prompt|elicitation_dialog"

// AttentionHooks is TurnHooks plus a Notification group, so a supervised
// agent also reports when it is blocked on a permission prompt or an
// elicitation dialog, and a PostToolUse group, whose report clears that
// needs_input once the prompt is answered and the tool runs.
func (c Claude) AttentionHooks(reportCmd []string) ([]string, error) {
	turn, err := c.TurnHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	command := []any{map[string]any{"type": "command", "command": shellCommand(reportCmd)}}
	notification, err := json.Marshal(map[string]any{"hooks": map[string]any{
		"Notification": []any{map[string]any{
			"matcher": attentionNotificationMatcher,
			"hooks":   command,
		}},
		"PostToolUse": []any{map[string]any{"hooks": command}},
	}})
	if err != nil {
		return nil, fmt.Errorf("claude: encoding attention hooks: %w", err)
	}
	return MergeSettingsArgs(turn, []string{"--settings", string(notification)}, "")
}

// attentionLaunch backs the driver's harness.AttentionHooker capability.
func attentionLaunch(h harness.SessionHandle, args, reportCmd []string) ([]string, error) {
	hooks, err := (Claude{}).AttentionHooks(reportCmd)
	if err != nil {
		return nil, err
	}
	return MergeSettingsArgs(args, hooks, h.Workspace)
}

// settingsFlag is claude's settings flag; it takes a JSON string or a path
// to a settings file, as `--settings <v>` or `--settings=<v>`.
const settingsFlag = "--settings"

// MergeSettingsArgs folds every --settings in args and extra into a single
// trailing --settings, because claude honors only one. Values may be inline
// JSON or a settings file path (relative paths resolve against baseDir, the
// session's working directory); a file is read and inlined. Top-level keys
// merge, and object values (e.g. hooks) merge one level deep, later wins;
// other flags keep their order. An unreadable or invalid value is an error,
// never a second --settings or a silently dropped one. Neither input is
// mutated.
func MergeSettingsArgs(args, extra []string, baseDir string) ([]string, error) {
	settings := map[string]any{}
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
			next, err := loadSettings(value, baseDir)
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
	return append(base, settingsFlag, string(encoded)), nil
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
	if strings.HasPrefix(strings.TrimSpace(value), "{") {
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
