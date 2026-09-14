package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sync"
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
	if !reflect.DeepEqual(hooks["Stop"], []any{map[string]any{"hooks": []any{map[string]any{"command": "/opt/leo dispatch report", "type": "command"}}}}) {
		t.Fatalf("Stop hook = %#v", hooks["Stop"])
	}
	for _, event := range []string{"UserPromptSubmit", "SessionEnd"} {
		if _, ok := hooks[event]; !ok {
			t.Errorf("missing %s hook", event)
		}
	}
}

func TestClaudePrepareInteractiveTrustsCwd(t *testing.T) {
	home, cwd := t.TempDir(), t.TempDir()
	path := filepath.Join(home, ".claude.json")
	original := `{"projects":{"/other":{"allowedTools":["Bash"],"custom":true}},"unknown":{"keep":true}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := (Claude{}).PrepareInteractive(home, cwd); err != nil {
		t.Fatalf("PrepareInteractive() = %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Claude{}).PrepareInteractive(home, cwd); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("second PrepareInteractive changed file")
	}
	var settings map[string]any
	if err := json.Unmarshal(second, &settings); err != nil {
		t.Fatal(err)
	}
	projects := settings["projects"].(map[string]any)
	key, err := canonicalPath(cwd)
	if err != nil {
		t.Fatal(err)
	}
	if projects[key].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Fatalf("project = %#v", projects[key])
	}
	if !reflect.DeepEqual(projects["/other"], map[string]any{"allowedTools": []any{"Bash"}, "custom": true}) {
		t.Fatalf("other project = %#v", projects["/other"])
	}
	if settings["unknown"].(map[string]any)["keep"] != true {
		t.Fatalf("settings = %#v", settings)
	}

	missingHome := t.TempDir()
	if err := (Claude{}).PrepareInteractive(missingHome, cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(missingHome, ".claude.json")); err != nil {
		t.Fatalf("missing file not created: %v", err)
	}
}

func TestClaudePrepareInteractivePreservesExistingAndConcurrentProjects(t *testing.T) {
	home := t.TempDir()
	cwd := t.TempDir()
	absCwd, err := filepath.Abs(cwd)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude.json")
	if err := os.WriteFile(path, []byte(`{"projects":{"`+absCwd+`":{"allowedTools":["Bash"]}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	var wg sync.WaitGroup
	for _, project := range []string{cwd, other} {
		wg.Add(1)
		go func(project string) {
			defer wg.Done()
			if err := (Claude{}).PrepareInteractive(home, project); err != nil {
				t.Errorf("PrepareInteractive(%q): %v", project, err)
			}
		}(project)
	}
	wg.Wait()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(raw, &settings); err != nil {
		t.Fatal(err)
	}
	projects := settings["projects"].(map[string]any)
	if projects[absCwd].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Fatalf("lexical project was not trusted: %#v", projects)
	}
	otherKey, err := canonicalPath(other)
	if err != nil {
		t.Fatal(err)
	}
	if projects[otherKey].(map[string]any)["hasTrustDialogAccepted"] != true {
		t.Fatalf("concurrent project was dropped: %#v", projects)
	}
}
