package claude

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestToolActivityHooksReportEveryToolEventToTheReportCommand(t *testing.T) {
	got, err := ToolActivityHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}
	hook := `[{"hooks":[{"command":"/opt/leo dispatch report","type":"command"}]}]`
	want := []string{"--settings", `{"hooks":{"PostToolUse":` + hook + `,"PostToolUseFailure":` + hook + `,"PreToolUse":` + hook + `}}`}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ToolActivityHooks() =\n%#v\nwant\n%#v", got, want)
	}
	if _, err := ToolActivityHooks(nil); err == nil {
		t.Fatal("empty report command accepted")
	}
}

// Supervised agents share TurnHooks; per-tool-call reports are dispatch-only.
func TestTurnHooksLeaveToolEventsToDispatch(t *testing.T) {
	got, err := (Claude{}).TurnHooks([]string{"/opt/leo", "dispatch", "report"})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"PreToolUse", "PostToolUseFailure"} {
		if strings.Contains(got[1], event) {
			t.Fatalf("TurnHooks installs %s: %s", event, got[1])
		}
	}
}

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
	if err := (Claude{}).PrepareInteractive(map[string]string{"HOME": home}, cwd); err != nil {
		t.Fatalf("PrepareInteractive() = %v", err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := (Claude{}).PrepareInteractive(map[string]string{"HOME": home}, cwd); err != nil {
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
	if err := (Claude{}).PrepareInteractive(map[string]string{"HOME": missingHome}, cwd); err != nil {
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
			if err := (Claude{}).PrepareInteractive(map[string]string{"HOME": home}, project); err != nil {
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

func TestPermissionHooksArgv(t *testing.T) {
	got, err := PermissionHooks([]string{"/opt/leo", "dispatch", "permission"}, 30*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0] != "--settings" {
		t.Fatalf("PermissionHooks() = %#v", got)
	}
	var settings map[string]any
	if err := json.Unmarshal([]byte(got[1]), &settings); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"PermissionRequest": []any{map[string]any{"hooks": []any{map[string]any{
		"type": "command", "command": "/opt/leo dispatch permission", "timeout": float64(1860),
	}}}}}
	if !reflect.DeepEqual(settings["hooks"], want) {
		t.Fatalf("hooks = %#v\nwant %#v", settings["hooks"], want)
	}
	merged, err := MergeSettingsArgs([]string{"--settings", `{"hooks":{"Stop":[]}}`}, got, MergeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(merged[len(merged)-1], `"Stop"`) || !strings.Contains(merged[len(merged)-1], `"PermissionRequest"`) {
		t.Fatalf("merged settings lost a hook: %s", merged[len(merged)-1])
	}
}

func TestShellCommandRoundTripsThroughSh(t *testing.T) {
	args := []string{"/opt/it's here/leo", "--config", `/tmp/a b/$HOME/"q"/leo.yaml`, "plain", "", `x'y'z`, `back\slash`}
	script := `printf '%s\0' ` + shellCommand(args)
	out, err := exec.Command("sh", "-c", script).Output()
	if err != nil {
		t.Fatalf("sh -c %q: %v", script, err)
	}
	got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00")
	if !reflect.DeepEqual(got, args) {
		t.Fatalf("round trip = %q, want %q (command %s)", got, args, shellCommand(args))
	}
}

func TestClaudePrepareInteractiveHonorsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, configDir, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	env := map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": configDir}
	if err := (Claude{}).PrepareInteractive(env, cwd); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("$HOME/.claude.json must stay untouched, stat err = %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(configDir, ".claude.json"))
	if err != nil {
		t.Fatalf("trust not written to the config dir: %v", err)
	}
	key, _ := canonicalPath(cwd)
	if !strings.Contains(string(raw), key) || !strings.Contains(string(raw), "hasTrustDialogAccepted") {
		t.Fatalf("config dir state = %s", raw)
	}
}

func TestStateFile(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"default account", map[string]string{"HOME": "/h"}, "/h/.claude.json"},
		{"config dir", map[string]string{"HOME": "/h", "CLAUDE_CONFIG_DIR": "/b"}, "/b/.claude.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := StateFile(tt.env); err != nil || got != tt.want {
				t.Fatalf("StateFile = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestPreLaunchTrustsOnlyForAlternateConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, configDir, cwd := t.TempDir(), t.TempDir(), t.TempDir()
	pre := func(env map[string]string) {
		t.Helper()
		h := harness.SessionHandle{Workspace: cwd, Env: env}
		if err := preLaunch(h); err != nil {
			t.Fatal(err)
		}
	}

	pre(map[string]string{"HOME": home})
	if _, err := os.Stat(filepath.Join(home, ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("default-account agents must not be touched, stat err = %v", err)
	}

	pre(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": filepath.Join(home, ".claude")})
	if _, err := os.Stat(filepath.Join(home, ".claude", ".claude.json")); !os.IsNotExist(err) {
		t.Fatalf("CLAUDE_CONFIG_DIR equal to the default must not be touched, stat err = %v", err)
	}

	pre(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": configDir})
	if _, err := os.Stat(filepath.Join(configDir, ".claude.json")); err != nil {
		t.Fatalf("alternate config dir was not trusted: %v", err)
	}
}
