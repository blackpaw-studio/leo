package claude

import (
	"encoding/json"
	"reflect"
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
