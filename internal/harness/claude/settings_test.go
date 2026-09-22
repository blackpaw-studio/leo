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
	return h
}

func TestMergeSettingsArgsSingleSettings(t *testing.T) {
	args, err := MergeSettingsArgs(
		[]string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept"}`},
		[]string{"--settings", `{"hooks":{"Stop":[{}],"UserPromptSubmit":[{}],"SessionEnd":[{}]}}`},
		"",
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

func TestAttentionHooksAddsNotificationGroupToTurnHooks(t *testing.T) {
	got, err := (Claude{}).AttentionHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"--settings", `{"crossSessionInbound":"accept","hooks":{` +
		`"Notification":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}],"matcher":"permission_prompt|elicitation_dialog"}],` +
		`"PostToolUse":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}],` +
		`"SessionEnd":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}],` +
		`"Stop":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}],` +
		`"UserPromptSubmit":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}]}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AttentionHooks() =\n%#v\nwant\n%#v", got, want)
	}
}

func TestAttentionLaunchMergesSingleSettingsExactly(t *testing.T) {
	base := []string{"--model", "sonnet", "--settings", `{"crossSessionInbound":"accept","theme":"dark"}`, "--name", "leo-a"}
	report := []string{"/opt/leo", "dispatch", "report"}

	got, supported, err := attentionHooker(t).AttentionLaunch(harness.SessionHandle{}, base, report)
	if err != nil || !supported {
		t.Fatalf("AttentionLaunch err=%v supported=%v", err, supported)
	}

	hook := `[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}]`
	want := []string{"--model", "sonnet", "--name", "leo-a", "--settings", `{"crossSessionInbound":"accept","hooks":{` +
		`"Notification":[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}],"matcher":"permission_prompt|elicitation_dialog"}],` +
		`"PostToolUse":` + hook + `,"SessionEnd":` + hook + `,"Stop":` + hook + `,"UserPromptSubmit":` + hook + `},"theme":"dark"}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%#v\nwant\n%#v", got, want)
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
	want := []string{"--model", "sonnet", "--settings", `{"hooks":{"PreToolUse":[{}],"Stop":[{}]},"theme":"dark"}`}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"equals json", []string{"--model", "sonnet", `--settings={"theme":"dark","hooks":{"PreToolUse":[{}]}}`}},
		{"file path", []string{"--model", "sonnet", "--settings", file}},
		{"equals file path", []string{"--model", "sonnet", "--settings=" + file}},
		{"relative file path", []string{"--model", "sonnet", "--settings", "user-settings.json"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MergeSettingsArgs(tc.args, extra, dir)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("argv =\n%#v\nwant\n%#v", got, want)
			}
		})
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
		_, err := MergeSettingsArgs([]string{"--settings", path}, []string{"--settings", `{"a":1}`}, dir)
		if err == nil || !strings.Contains(err.Error(), path) {
			t.Errorf("MergeSettingsArgs(%s) err = %v, want an error naming the file", path, err)
		}
	}
}

func TestAttentionLaunchWithUnreadableSettingsFileLeavesArgvUnhooked(t *testing.T) {
	base := []string{"--settings", "/nonexistent/leo-settings.json"}

	got, supported, err := attentionHooker(t).AttentionLaunch(harness.SessionHandle{Workspace: t.TempDir()}, base, []string{"/opt/leo", "dispatch", "report"})

	if err == nil || supported || !reflect.DeepEqual(got, base) {
		t.Fatalf("AttentionLaunch = %#v, supported=%v, err=%v; want original argv, unsupported, error", got, supported, err)
	}
}
