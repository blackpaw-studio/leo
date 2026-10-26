package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func attentionHooker(t *testing.T) harness.AttentionHooker {
	t.Helper()
	h, ok := (Claude{}).Driver().(harness.AttentionHooker)
	if !ok {
		t.Fatal("claude driver is not a harness.AttentionHooker")
	}
	if !h.AttentionSupported() {
		t.Fatal("AttentionSupported() = false, want true")
	}
	return h
}

func TestMergeSettingsArgsSingleSettings(t *testing.T) {
	args, err := MergeSettingsArgs(
		[]string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept"}`},
		[]string{"--settings", `{"hooks":{"Stop":[{}],"UserPromptSubmit":[{}],"SessionEnd":[{}]}}`},
		MergeOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	var raw string
	for i, arg := range args {
		if arg == "--settings" {
			if raw != "" {
				t.Fatalf("args contain more than one --settings: %#v", args)
			}
			raw = args[i+1]
		}
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(raw), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["crossSessionInbound"] != "accept" {
		t.Fatalf("settings = %#v", settings)
	}
	hooks := settings["hooks"].(map[string]any)
	for _, event := range []string{"Stop", "UserPromptSubmit", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Fatalf("settings hooks = %#v, missing %s", hooks, event)
		}
	}
}

// hookGroup is one decoded hook group: its matcher and each hook's command
// and timeout.
type hookGroup struct {
	Matcher  string
	Commands []hookCommand
}

type hookCommand struct {
	Command string
	Timeout int
}

// decodeHooks returns the hooks of the one --settings in args, by event.
func decodeHooks(t *testing.T, args []string) map[string][]hookGroup {
	t.Helper()
	if len(args) != 2 || args[0] != "--settings" {
		t.Fatalf("args = %#v, want one --settings", args)
	}
	var settings struct {
		CrossSessionInbound string `json:"crossSessionInbound"`
		Hooks               map[string][]struct {
			Matcher string `json:"matcher"`
			Hooks   []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
				Timeout int    `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal([]byte(args[1]), &settings); err != nil {
		t.Fatal(err)
	}
	out := map[string][]hookGroup{}
	for event, groups := range settings.Hooks {
		for _, g := range groups {
			group := hookGroup{Matcher: g.Matcher}
			for _, h := range g.Hooks {
				if h.Type != "command" {
					t.Fatalf("%s hook type = %q, want command", event, h.Type)
				}
				group.Commands = append(group.Commands, hookCommand{h.Command, h.Timeout})
			}
			out[event] = append(out[event], group)
		}
	}
	return out
}

func TestAttentionHooksCoverEveryEventTheBridgeUsedToReport(t *testing.T) {
	got, err := (Claude{}).AttentionHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}

	report := hookGroup{Commands: []hookCommand{{Command: "/opt/leo dispatch report"}}}
	permission := hookGroup{Commands: []hookCommand{{Command: "/opt/leo dispatch permission", Timeout: 3660}}}
	want := map[string][]hookGroup{
		"Stop":               {report},
		"SessionEnd":         {report},
		"PostToolUse":        {report},
		"PostToolUseFailure": {report},
		"PermissionDenied":   {report},
		"Elicitation":        {report},
		"SubagentStart":      {report},
		"SubagentStop":       {report},
		"PreCompact":         {report},
		"PostCompact":        {report},
		"Notification":       {{Matcher: "permission_prompt|elicitation_dialog", Commands: report.Commands}},
		"PermissionRequest":  {report, permission},
		"UserPromptSubmit":   {report, {Commands: []hookCommand{{Command: "/opt/leo dispatch context", Timeout: 10}}}},
		"SessionStart":       {{Matcher: "startup|resume|clear|compact|fork", Commands: []hookCommand{{Command: "/opt/leo dispatch context", Timeout: 10}}}},
	}
	if hooks := decodeHooks(t, got); !reflect.DeepEqual(hooks, want) {
		t.Fatalf("hooks =\n%#v\nwant\n%#v", hooks, want)
	}
}

func TestAttentionHooksWithoutAReportSuffixSkipTheDerivedCommands(t *testing.T) {
	got, err := (Claude{}).AttentionHooks([]string{"/opt/leo", "custom"})
	if err != nil {
		t.Fatal(err)
	}

	hooks := decodeHooks(t, got)
	if _, ok := hooks["SessionStart"]; ok {
		t.Fatalf("hooks = %#v, want no context hook without a report command to derive it from", hooks)
	}
	if len(hooks["PermissionRequest"]) != 1 || hooks["PermissionRequest"][0].Commands[0].Command != "/opt/leo custom" {
		t.Fatalf("PermissionRequest = %#v, want only the report command", hooks["PermissionRequest"])
	}
}

func TestAttentionLaunchMergesSingleSettingsExactly(t *testing.T) {
	base := []string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept","theme":"dark"}`, "--name", "leo-a"}
	report := []string{"/opt/leo", "dispatch", "report"}

	got, supported, err := attentionHooker(t).AttentionLaunch(harness.SessionHandle{}, base, report)
	if err != nil || !supported {
		t.Fatalf("AttentionLaunch err=%v supported=%v", err, supported)
	}

	if len(got) != 6 || got[0] != "--model" || got[1] != "sonnet" || got[2] != "--name" || got[3] != "leo-a" || got[4] != "--settings" {
		t.Fatalf("argv = %#v, want the base flags then one trailing --settings", got)
	}
	hooks := decodeHooks(t, got[4:])
	if len(hooks["SessionStart"]) != 1 || len(hooks["PostCompact"]) != 1 {
		t.Fatalf("merged hooks = %#v, want the attention hooks", hooks)
	}
	var merged struct {
		Theme               string `json:"theme"`
		CrossSessionInbound string `json:"crossSessionInbound"`
	}
	if err := json.Unmarshal([]byte(got[5]), &merged); err != nil || merged.Theme != "dark" || merged.CrossSessionInbound != "accept" {
		t.Fatalf("merged settings = %s (%v), want the operator's keys kept", got[5], err)
	}
	if base[3] != `{"crossSessionInbound":"accept","theme":"dark"}` || len(base) != 6 {
		t.Fatalf("AttentionLaunch mutated its input: %#v", base)
	}

	again, _, err := attentionHooker(t).AttentionLaunch(harness.SessionHandle{}, got, report)
	if err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("not idempotent:\n%#v\nvs\n%#v (err=%v)", again, got, err)
	}
}

func TestMergeSettingsArgsForms(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "user-settings.json")
	if err := os.WriteFile(file, []byte(`{"theme":"dark","hooks":{"PreToolUse":[{}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	extra := []string{"--settings", `{"hooks":{"Stop":[{}]}}`}
	merged := `{"hooks":{"PreToolUse":[{}],"Stop":[{}]},"theme":"dark"}`
	for _, tc := range []struct {
		name   string
		args   []string
		inline bool
	}{
		{"equals json", []string{"--model", "sonnet", `--settings={"theme":"dark","hooks":{"PreToolUse":[{}]}}`}, true},
		{"file path", []string{"--model", "sonnet", "--settings", file}, false},
		{"equals file path", []string{"--model", "sonnet", "--settings=" + file}, false},
		{"relative file path", []string{"--model", "sonnet", "--settings", "user-settings.json"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spill := filepath.Join(t.TempDir(), "state", "settings", "a.json")
			got, err := MergeSettingsArgs(tc.args, extra, MergeOptions{BaseDir: dir, SpillPath: spill})
			if err != nil {
				t.Fatal(err)
			}
			if tc.inline {
				if want := []string{"--model", "sonnet", "--settings", merged}; !reflect.DeepEqual(got, want) {
					t.Fatalf("argv =\n%#v\nwant\n%#v", got, want)
				}
				return
			}
			if want := []string{"--model", "sonnet", "--settings", spill}; !reflect.DeepEqual(got, want) {
				t.Fatalf("argv =\n%#v\nwant\n%#v", got, want)
			}
			if data, _ := os.ReadFile(spill); string(data) != merged {
				t.Fatalf("spill file = %s, want %s", data, merged)
			}
		})
	}
}

// A settings FILE may hold credentials (env), so its contents must never
// reach argv (ps, tmux's retained pane command): the merged settings go to
// a private file, rewritten on every launch.
func TestMergeSettingsArgsFileNeverInlinedIntoArgv(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "secret-settings.json")
	if err := os.WriteFile(file, []byte(`{"env":{"API_KEY":"s3cret-value"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	spill := filepath.Join(t.TempDir(), "state", "settings", "leo-a.json")
	if err := os.MkdirAll(filepath.Dir(spill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spill, []byte(`{"stale":true}`), 0o644); err != nil { //nolint:gosec // stale file with loose perms on purpose
		t.Fatal(err)
	}

	got, err := MergeSettingsArgs([]string{"--settings", file}, []string{"--settings", `{"hooks":{"Stop":[{}]}}`}, MergeOptions{SpillPath: spill})
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(strings.Join(got, " "), "s3cret-value") {
		t.Fatalf("argv leaks the settings file's contents: %#v", got)
	}
	if !reflect.DeepEqual(got, []string{"--settings", spill}) {
		t.Fatalf("argv = %#v, want the single private settings path", got)
	}
	info, err := os.Stat(spill)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("spill file mode = %v (err %v), want 0600", info.Mode().Perm(), err)
	}
	data, _ := os.ReadFile(spill)
	if string(data) != `{"env":{"API_KEY":"s3cret-value"},"hooks":{"Stop":[{}]}}` {
		t.Fatalf("spill file = %s, want the merged settings (stale content overwritten)", data)
	}
}

func TestMergeSettingsArgsFileWithoutSpillPathErrors(t *testing.T) {
	file := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := MergeSettingsArgs([]string{"--settings", file}, []string{"--settings", `{"a":1}`}, MergeOptions{}); err == nil {
		t.Fatalf("argv = %#v, want an error rather than inlining a settings file", got)
	}
}

func TestAttentionLaunchSpillsFileSettingsUnderStateDir(t *testing.T) {
	home, work := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "s.json"), []byte(`{"env":{"K":"secret"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := harness.SessionHandle{Name: "leo-a", Workspace: work, HomePath: home}

	got, supported, err := attentionHooker(t).AttentionLaunch(h, []string{"--settings", "s.json"}, []string{"/opt/leo", "dispatch", "report"})

	want := []string{"--settings", filepath.Join(home, "state", "settings", "leo-a.json")}
	if err != nil || !supported || !reflect.DeepEqual(got, want) {
		t.Fatalf("AttentionLaunch = %#v, %v, %v; want %#v", got, supported, err, want)
	}
}

// An unreadable or invalid settings file must fail the merge (the caller
// then launches with its original argv, unhooked) rather than produce two
// --settings or silently drop the operator's settings.
func TestMergeSettingsArgsUnusableFileErrors(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(bad, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.json"), bad} {
		_, err := MergeSettingsArgs([]string{"--settings", path}, []string{"--settings", `{"a":1}`}, MergeOptions{BaseDir: dir, SpillPath: filepath.Join(dir, "spill.json")})
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("MergeSettingsArgs(%s) err = %v, want an error naming the file", path, err)
		}
	}
}

func TestAttentionLaunchWithUnusableSettingsLeavesArgvUnhooked(t *testing.T) {
	for _, base := range [][]string{
		{"--settings", "/nonexistent/leo-settings.json"},
		{"--model", "sonnet", "--settings"},
	} {
		got, supported, err := attentionHooker(t).AttentionLaunch(harness.SessionHandle{Workspace: t.TempDir()}, base, []string{"/opt/leo", "dispatch", "report"})

		if err == nil || supported || !reflect.DeepEqual(got, base) {
			t.Fatalf("AttentionLaunch(%q) = %#v, supported=%v, err=%v; want original argv, unsupported, error", base, got, supported, err)
		}
	}
}

func TestMergeSettingsArgsAppendsToOperatorHookArrays(t *testing.T) {
	operator := `{"hooks":{"PostToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"audit"}]}]}}`
	leo := `{"hooks":{"PostToolUse":[{"hooks":[{"type":"command","command":"report"}]}],"Stop":[{"hooks":[{"type":"command","command":"report"}]}]}}`

	got, err := MergeSettingsArgs([]string{"--settings", operator}, []string{"--settings", leo}, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"--settings", `{"hooks":{"PostToolUse":[` +
		`{"hooks":[{"command":"audit","type":"command"}],"matcher":"Bash"},` +
		`{"hooks":[{"command":"report","type":"command"}]}],` +
		`"Stop":[{"hooks":[{"command":"report","type":"command"}]}]}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%#v\nwant\n%#v", got, want)
	}
}

func TestMergeSettingsArgsTrailingFlagWithoutValueErrors(t *testing.T) {
	for _, args := range [][]string{{"--model", "sonnet", "--settings"}, {"--settings", `{"a":1}`, "--settings"}} {
		if got, err := MergeSettingsArgs(args, []string{"--settings", `{"b":2}`}, MergeOptions{}); err == nil {
			t.Errorf("MergeSettingsArgs(%q) = %#v, want an error", args, got)
		}
	}
}

func TestSpillTightensPreexistingSettingsDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state", "settings")
	if err := os.MkdirAll(dir, 0o755); err != nil { //nolint:gosec // loose on purpose
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o755); err != nil { //nolint:gosec // loose on purpose
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "s.json")
	if err := os.WriteFile(file, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := MergeSettingsArgs([]string{"--settings", file}, []string{"--settings", `{"a":1}`}, MergeOptions{SpillPath: filepath.Join(dir, "a.json")}); err != nil {
		t.Fatal(err)
	}

	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("settings dir mode = %v (err %v), want 0700", info.Mode().Perm(), err)
	}
}
