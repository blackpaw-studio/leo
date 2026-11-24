package claude

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

var roWorkspaces = []string{"/ws/leo", "/real/ws/leo"}

func readOnlySpec(kind harness.Kind) harness.LaunchSpec {
	return harness.LaunchSpec{
		Kind: kind, Model: "sonnet", Name: "d-1", Workspace: "/ws/leo", Prompt: "read it", Dispatched: true,
		ReadOnly: &harness.ReadOnlySandbox{Workspaces: roWorkspaces},
	}
}

// settingsOf returns the decoded value of the single --settings in args,
// failing when there is not exactly one.
func settingsOf(t *testing.T, args []string) map[string]any {
	t.Helper()
	var values []string
	for i, a := range args {
		if a == "--settings" && i+1 < len(args) {
			values = append(values, args[i+1])
		}
	}
	if len(values) != 1 {
		t.Fatalf("args carry %d --settings, want exactly 1: %q", len(values), args)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(values[0]), &out); err != nil {
		t.Fatalf("--settings is not JSON: %v (%s)", err, values[0])
	}
	return out
}

func strs(t *testing.T, v any) []string {
	t.Helper()
	raw, ok := v.([]any)
	if !ok {
		t.Fatalf("%v is %T, want a list", v, v)
	}
	out := make([]string, len(raw))
	for i, e := range raw {
		out[i] = e.(string)
	}
	return out
}

func hasFlagValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

// assertReadOnlySettings checks every sandbox and deny key a granted read
// depends on, for both workspaces and every secret pattern at both depths.
func assertReadOnlySettings(t *testing.T, args []string) map[string]any {
	t.Helper()
	s := settingsOf(t, args)
	sb, ok := s["sandbox"].(map[string]any)
	if !ok {
		t.Fatalf("no sandbox in settings: %v", s)
	}
	for key, want := range map[string]any{"enabled": true, "failIfUnavailable": true, "allowUnsandboxedCommands": false, "autoAllowBashIfSandboxed": true} {
		if sb[key] != want {
			t.Errorf("sandbox.%s = %v, want %v", key, sb[key], want)
		}
	}
	fs, ok := sb["filesystem"].(map[string]any)
	if !ok {
		t.Fatalf("no sandbox.filesystem: %v", sb)
	}
	assertPinnedSandbox(t, sb)
	denyWrite, denyRead := strs(t, fs["denyWrite"]), strs(t, fs["denyRead"])
	perms, ok := s["permissions"].(map[string]any)
	if !ok {
		t.Fatalf("no permissions in settings: %v", s)
	}
	deny := strs(t, perms["deny"])
	for _, ws := range roWorkspaces {
		if !slices.Contains(denyWrite, ws) {
			t.Errorf("denyWrite %v lacks %s", denyWrite, ws)
		}
		if !slices.Contains(deny, "Edit(/"+ws+"/**)") {
			t.Errorf("permissions.deny %v lacks Edit(/%s/**)", deny, ws)
		}
		for _, p := range harness.SecretDenylist() {
			for _, path := range []string{ws + "/" + p, ws + "/**/" + p} {
				if !slices.Contains(denyRead, path) {
					t.Errorf("denyRead lacks %s", path)
				}
				if !slices.Contains(deny, "Read(/"+path+")") {
					t.Errorf("permissions.deny lacks Read(/%s)", path)
				}
			}
		}
	}
	if !hasFlagValue(args, "--setting-sources", "user") {
		t.Errorf("args lack --setting-sources user: %q", args)
	}
	return s
}

func TestTaskArgsReadOnlyEmitsOneSettingsWithSandbox(t *testing.T) {
	args, err := Claude{}.Args(withOpts(readOnlySpec(harness.KindTask), Options{}))
	if err != nil {
		t.Fatal(err)
	}
	assertReadOnlySettings(t, args)
}

func TestAgentArgsReadOnlySettings(t *testing.T) {
	args, err := Claude{}.Args(withOpts(readOnlySpec(harness.KindAgent), Options{}))
	if err != nil {
		t.Fatal(err)
	}
	s := assertReadOnlySettings(t, args)
	if s["crossSessionInbound"] != "accept" {
		t.Errorf("folded settings lost crossSessionInbound: %v", s)
	}
}

func TestReadOnlyWithPluginsNoneStillOneSettings(t *testing.T) {
	opts := Options{Plugins: "none", EnabledPlugins: []string{"p@x"}, MCP: "none"}
	for _, kind := range []harness.Kind{harness.KindTask, harness.KindAgent} {
		args, err := Claude{}.Args(withOpts(readOnlySpec(kind), opts))
		if err != nil {
			t.Fatal(err)
		}
		s := assertReadOnlySettings(t, args)
		if _, ok := s["enabledPlugins"].(map[string]any)["p@x"]; !ok {
			t.Errorf("%s: folded settings lost enabledPlugins: %v", kind, s)
		}
	}
}

func TestArgsWithoutReadOnlyUnchanged(t *testing.T) {
	for _, kind := range []harness.Kind{harness.KindTask, harness.KindAgent} {
		spec := readOnlySpec(kind)
		spec.ReadOnly = nil
		args, err := Claude{}.Args(withOpts(spec, Options{}))
		if err != nil {
			t.Fatal(err)
		}
		for _, a := range args {
			if a == "--setting-sources" {
				t.Errorf("%s: unsandboxed args carry --setting-sources: %q", kind, args)
			}
		}
		for i, a := range args {
			if a == "--settings" {
				var s map[string]any
				if err := json.Unmarshal([]byte(args[i+1]), &s); err == nil {
					if _, ok := s["sandbox"]; ok {
						t.Errorf("%s: unsandboxed args carry a sandbox: %q", kind, args)
					}
				}
			}
		}
	}
}

func withOpts(spec harness.LaunchSpec, o Options) harness.LaunchSpec {
	spec.Options = o
	return spec
}

// pinnedSandbox are the sandbox booleans a user-scope setting could flip to
// weaken a granted run (scalars: a --settings value wins over user settings).
var pinnedSandbox = map[string]map[string]any{
	"":           {"enableWeakerNestedSandbox": false, "allowNestedUserNamespaces": false, "enableWeakerNetworkIsolation": false, "allowAppleEvents": false},
	"filesystem": {"disabled": false},
	"network":    {"allowAllUnixSockets": false, "allowLocalBinding": false},
}

// misplacedSandbox are keys Claude Code ignores at these paths; pinning them
// there would leave the real setting unpinned.
var misplacedSandbox = map[string][]string{"network": {"allowAppleEvents"}}

func assertPinnedSandbox(t *testing.T, sb map[string]any) {
	t.Helper()
	for section, keys := range pinnedSandbox {
		m := sb
		if section != "" {
			var ok bool
			if m, ok = sb[section].(map[string]any); !ok {
				t.Fatalf("no sandbox.%s: %v", section, sb)
			}
		}
		for key, want := range keys {
			got, set := m[key]
			if !set || got != want {
				t.Errorf("sandbox.%s.%s = %v (set=%v), want %v", section, key, got, set, want)
			}
		}
	}
	for section, keys := range misplacedSandbox {
		m, _ := sb[section].(map[string]any)
		for _, key := range keys {
			if _, set := m[key]; set {
				t.Errorf("sandbox.%s.%s is set; Claude Code reads it from sandbox.%s", section, key, key)
			}
		}
	}
}

// An earlier --settings that weakens the sandbox (as a user-scope file
// would) loses to the read-only settings folded after it.
func TestReadOnlyOverridesConflictingSandboxSettings(t *testing.T) {
	weak := `{"sandbox":{"enabled":false,"allowUnsandboxedCommands":true,"enableWeakerNestedSandbox":true,"filesystem":{"disabled":true},"allowAppleEvents":true,"network":{"allowAllUnixSockets":true}}}`
	args, err := appendReadOnly([]string{"--model", "sonnet", "--settings", weak}, readOnlySpec(harness.KindTask))
	if err != nil {
		t.Fatal(err)
	}
	assertReadOnlySettings(t, args)
}
