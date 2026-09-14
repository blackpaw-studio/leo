package claude

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestTurnHooksArgv(t *testing.T) {
	got, err := (Claude{}).TurnHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "--settings" {
		t.Fatalf("TurnHooks() = %#v", got)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(got[1]), &settings); err != nil {
		t.Fatal(err)
	}
	if settings["crossSessionInbound"] != "accept" {
		t.Fatalf("crossSessionInbound = %#v", settings["crossSessionInbound"])
	}
	hooks := settings["hooks"].(map[string]any)
	if !reflect.DeepEqual(hooks["Stop"], []any{map[string]any{"command": "/opt/leo dispatch report", "type": "command"}}) {
		t.Fatalf("Stop hook = %#v", hooks["Stop"])
	}
	for _, event := range []string{"UserPromptSubmit", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("missing %s hook", event)
		}
	}
}

func TestPrepareInteractiveNoop(t *testing.T) {
	if err := (Claude{}).PrepareInteractive(t.TempDir()); err != nil {
		t.Fatalf("PrepareInteractive() = %v", err)
	}
}
