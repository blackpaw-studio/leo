package mcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
)

// newOKDaemon returns a fake daemon that accepts every call with an empty
// success envelope, so permission tests can assert on what does and does not
// reach it.
func newOKDaemon(t *testing.T) *fakeDaemon {
	t.Helper()
	d := newFakeDaemon(func(string, string, []byte) (int, string) {
		return 200, `{"ok":true,"data":{}}`
	})
	t.Cleanup(d.close)
	return d
}

// recorded returns the calls the fake daemon has received so far.
func (d *fakeDaemon) recorded() []recordedCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.calls)
}

// toolNames returns the names the registry advertises via tools/list.
func toolNames(r *registry) []string {
	names := make([]string, 0, len(r.list()))
	for _, def := range r.list() {
		names = append(names, def.Name)
	}
	return names
}

// findTool returns the advertised definition for name.
func findTool(t *testing.T, r *registry, name string) toolDef {
	t.Helper()
	for _, def := range r.list() {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("tool %q not advertised; have %v", name, toolNames(r))
	return toolDef{}
}

// callTool invokes a tool with the given arguments.
func callTool(r *registry, name string, args map[string]any) (string, error) {
	raw, _ := json.Marshal(args)
	return r.callContext(context.Background(), name, raw)
}

// TestRegistryNamesMatchLeotools is the drift gate between the tool
// definitions here and the canonical list config validates against. A tool
// added in one place and not the other is a silent hole: config would reject
// a valid deny_tools entry, or accept one that denies nothing.
func TestRegistryNamesMatchLeotools(t *testing.T) {
	reg := newRegistry(newDaemonClient("0", ""), "test-process", leotools.Permissions{})
	got := toolNames(reg)

	for _, name := range got {
		if !leotools.IsKnownTool(name) {
			t.Errorf("registry defines %q but leotools.Names does not list it — add it to internal/leotools", name)
		}
	}
	for _, name := range leotools.Names {
		if !slices.Contains(got, name) {
			t.Errorf("leotools.Names lists %q but the registry does not define it", name)
		}
	}
	if len(got) != len(leotools.Names) {
		t.Errorf("registry has %d tools, leotools.Names has %d", len(got), len(leotools.Names))
	}
}

func TestDeniedToolIsNotAdvertised(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent", "leo_stop_agent"}}
	reg := newRegistry(newDaemonClient("0", ""), "test-process", perms)

	names := toolNames(reg)
	for _, denied := range perms.DenyTools {
		if slices.Contains(names, denied) {
			t.Errorf("denied tool %q is still advertised", denied)
		}
	}
	if !slices.Contains(names, "leo_list_agents") {
		t.Error("undenied tools must remain advertised")
	}
}

// A model that remembers a tool from a previous session can call it even
// though tools/list hides it, so the call path must reject it too — and say
// why, rather than claiming the tool does not exist.
func TestDeniedToolCallIsRejectedWithReason(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent"}}
	reg := newRegistry(newDaemonClient("0", ""), "test-process", perms)

	_, err := callTool(reg, "leo_spawn_agent", map[string]any{"template": "codex"})
	if err == nil {
		t.Fatal("expected an error calling a denied tool")
	}
	if !strings.Contains(err.Error(), "not permitted") {
		t.Errorf("error should say the tool is not permitted, got: %v", err)
	}
	if strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("denied tools must not report as unknown: %v", err)
	}

	_, err = callTool(reg, "leo_not_a_tool", nil)
	if err == nil || !strings.Contains(err.Error(), "unknown tool") {
		t.Errorf("genuinely unknown tools should still report as unknown, got: %v", err)
	}
}

func TestLeoSkillCannotBeDenied(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{leotools.SkillTool}}
	reg := newRegistry(newDaemonClient("0", ""), "test-process", perms)

	if !slices.Contains(toolNames(reg), leotools.SkillTool) {
		t.Fatalf("%s must always be advertised", leotools.SkillTool)
	}
}

func TestSendMessageAllowlist(t *testing.T) {
	daemon := newOKDaemon(t)
	perms := leotools.Permissions{CanMessage: []string{"rocket", "scout-*"}}
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary", perms)

	if _, err := callTool(reg, "leo_send_message", map[string]any{"to": "olympus", "message": "hi"}); err == nil {
		t.Fatal("expected a rejection for a target outside the allowlist")
	} else {
		if !strings.Contains(err.Error(), "olympus") {
			t.Errorf("error should quote the rejected target: %v", err)
		}
		if !strings.Contains(err.Error(), "rocket") {
			t.Errorf("error should list the allowed targets: %v", err)
		}
	}
	if got := len(daemon.recorded()); got != 0 {
		t.Fatalf("a rejected message must not reach the daemon; got %d calls", got)
	}

	for _, target := range []string{"rocket", "scout-leo"} {
		if _, err := callTool(reg, "leo_send_message", map[string]any{"to": target, "message": "hi"}); err != nil {
			t.Fatalf("allowed target %q was rejected: %v", target, err)
		}
	}
	if got := len(daemon.recorded()); got != 2 {
		t.Fatalf("allowed messages should reach the daemon; got %d calls", got)
	}
}

func TestSpawnAndConsultAllowlists(t *testing.T) {
	daemon := newOKDaemon(t)
	perms := leotools.Permissions{CanSpawn: []string{"codex"}, CanConsult: []string{"fable"}}
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary", perms)

	if _, err := callTool(reg, "leo_spawn_agent", map[string]any{"template": "scout"}); err == nil ||
		!strings.Contains(err.Error(), "codex") {
		t.Errorf("spawn outside the allowlist should be rejected listing codex, got: %v", err)
	}
	if _, err := callTool(reg, "leo_consult", map[string]any{"template": "opus", "prompt": "hi"}); err == nil ||
		!strings.Contains(err.Error(), "fable") {
		t.Errorf("consult outside the allowlist should be rejected listing fable, got: %v", err)
	}
	if got := len(daemon.recorded()); got != 0 {
		t.Fatalf("rejected calls must not reach the daemon; got %d", got)
	}
}

// Restricting one allowlist must not restrict the others.
func TestAllowlistsAreIndependent(t *testing.T) {
	daemon := newOKDaemon(t)
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary",
		leotools.Permissions{CanSpawn: []string{"codex"}})

	if _, err := callTool(reg, "leo_send_message", map[string]any{"to": "anyone", "message": "hi"}); err != nil {
		t.Errorf("an absent can_message must stay unrestricted: %v", err)
	}
}

// A narrowed tool advertises its limits, so the model sees the boundary
// instead of discovering it by failing.
func TestDescriptionsAdvertiseAllowlists(t *testing.T) {
	daemon := newOKDaemon(t)
	perms := leotools.Permissions{
		CanMessage: []string{"rocket", "olympus"},
		CanSpawn:   []string{"codex"},
		CanConsult: []string{"fable"},
	}
	reg := newRegistry(newDaemonClient(daemon.port(), ""), "primary", perms)

	for tool, want := range map[string]string{
		"leo_send_message": "rocket, olympus",
		"leo_spawn_agent":  "codex",
		"leo_consult":      "fable",
	} {
		if desc := findTool(t, reg, tool).Description; !strings.Contains(desc, want) {
			t.Errorf("%s description should mention %q: %s", tool, want, desc)
		}
	}

	// Unrestricted tools keep their descriptions untouched.
	plain := newRegistry(newDaemonClient(daemon.port(), ""), "primary", leotools.Permissions{})
	if desc := findTool(t, plain, "leo_send_message").Description; strings.Contains(desc, "You may only") {
		t.Errorf("unrestricted tools must not advertise a limit: %s", desc)
	}
}

func TestPermissionsFromEnv(t *testing.T) {
	t.Run("absent means unrestricted", func(t *testing.T) {
		perms, ok := parsePermissions("")
		if !ok {
			t.Fatal("an absent value must parse successfully")
		}
		if !perms.IsZero() {
			t.Errorf("absent value should yield zero Permissions, got %+v", perms)
		}
	})

	t.Run("valid payload", func(t *testing.T) {
		perms, ok := parsePermissions(`{"deny_tools":["leo_clear"],"can_message":["rocket"]}`)
		if !ok {
			t.Fatal("a valid payload must parse")
		}
		if !perms.DeniesTool("leo_clear") || perms.AllowsMessage("olympus") {
			t.Errorf("payload not applied: %+v", perms)
		}
	})

	t.Run("malformed payload fails closed", func(t *testing.T) {
		if _, ok := parsePermissions("{not json"); ok {
			t.Fatal("a malformed payload must not parse as unrestricted")
		}
	})
}

// A malformed LEO_PERMISSIONS must not silently hand the agent the full
// daemon-backed surface. It degrades to local-only mode instead: loud on
// stderr, still usable, and stripped of everything that touches the daemon.
func TestRegistryFromEnvFailsClosedOnMalformedPermissions(t *testing.T) {
	t.Setenv("LEO_PROCESS_NAME", "primary")
	t.Setenv("LEO_WEB_PORT", "1")
	t.Setenv("LEO_API_TOKEN", "token")
	t.Setenv("LEO_PERMISSIONS", "{not json")

	names := toolNames(registryFromEnv())
	if !slices.Contains(names, leotools.SkillTool) {
		t.Errorf("local-only mode must keep %s; got %v", leotools.SkillTool, names)
	}
	if slices.Contains(names, "leo_send_message") {
		t.Errorf("malformed permissions must not leave daemon tools registered; got %v", names)
	}
}

func TestRegistryFromEnvAppliesPermissions(t *testing.T) {
	t.Setenv("LEO_PROCESS_NAME", "primary")
	t.Setenv("LEO_WEB_PORT", "1")
	t.Setenv("LEO_API_TOKEN", "token")
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_spawn_agent"]}`)

	names := toolNames(registryFromEnv())
	if slices.Contains(names, "leo_spawn_agent") {
		t.Errorf("denied tool should be absent; got %v", names)
	}
	if !slices.Contains(names, "leo_list_agents") {
		t.Errorf("undenied daemon tools should remain; got %v", names)
	}
}
