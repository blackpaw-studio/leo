package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// cmdFor builds a cobra command with the given path so gate errors can be
// asserted against the command name the user actually typed.
func cmdFor(path ...string) *cobra.Command {
	root := &cobra.Command{Use: "leo"}
	parent := root
	for _, p := range path {
		c := &cobra.Command{Use: p}
		parent.AddCommand(c)
		parent = c
	}
	return parent
}

func TestGateCommandUnrestricted(t *testing.T) {
	// No LEO_PERMISSIONS at all: a human shell, or an unrestricted template.
	t.Setenv("LEO_PERMISSIONS", "")
	if err := gateCommand(cmdFor("agent", "spawn"), "leo_spawn_agent"); err != nil {
		t.Fatalf("an unset payload must not gate anything: %v", err)
	}
}

func TestGateCommandDeniedTool(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_spawn_agent"]}`)

	err := gateCommand(cmdFor("agent", "spawn"), "leo_spawn_agent")
	if err == nil {
		t.Fatal("expected the denied command to be refused")
	}
	// The message has to name the command typed and the tool that governs it,
	// or the operator cannot tell which config knob to change.
	if !strings.Contains(err.Error(), "leo agent spawn") {
		t.Errorf("error should name the command: %v", err)
	}
	if !strings.Contains(err.Error(), "leo_spawn_agent") {
		t.Errorf("error should name the governing tool: %v", err)
	}

	// An unrelated command stays available.
	if err := gateCommand(cmdFor("agent", "list"), "leo_list_agents"); err != nil {
		t.Errorf("undenied commands must still run: %v", err)
	}
}

// Lifecycle commands with no MCP equivalent are governed by leo_stop_agent:
// denying "stop other agents" plainly intends to deny disrupting them.
func TestGateCommandCoversLifecycleCommands(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_stop_agent"]}`)

	for _, name := range []string{"stop", "reset", "prune", "restart", "rename", "suspend"} {
		if err := gateCommand(cmdFor("agent", name), "leo_stop_agent"); err == nil {
			t.Errorf("leo agent %s should be refused when leo_stop_agent is denied", name)
		}
	}
}

func TestGateSpawnTemplate(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", `{"can_spawn":["codex"]}`)

	if err := gateSpawnTemplate(cmdFor("agent", "spawn"), "codex"); err != nil {
		t.Errorf("an allowed template must spawn: %v", err)
	}
	err := gateSpawnTemplate(cmdFor("agent", "spawn"), "scout")
	if err == nil {
		t.Fatal("expected a template outside can_spawn to be refused")
	}
	if !strings.Contains(err.Error(), "scout") || !strings.Contains(err.Error(), "codex") {
		t.Errorf("error should quote the rejected template and list the allowed ones: %v", err)
	}
}

// A denied tool outranks the allowlist: deny_tools removes the capability
// outright, so the error must say that rather than listing allowed templates.
func TestGateSpawnTemplateRespectsDenyTools(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["leo_spawn_agent"],"can_spawn":["codex"]}`)

	err := gateSpawnTemplate(cmdFor("agent", "spawn"), "codex")
	if err == nil {
		t.Fatal("a denied tool must refuse even an allowlisted template")
	}
	if !strings.Contains(err.Error(), "leo_spawn_agent") {
		t.Errorf("error should name the denied tool: %v", err)
	}
}

// Leo writes this payload itself, so a malformed one means Leo has lost track
// of what the agent should be allowed to do. Refusing is the only safe read —
// the alternative silently restores full access.
func TestGateCommandFailsClosedOnMalformedPayload(t *testing.T) {
	t.Setenv("LEO_PERMISSIONS", "{not json")

	err := gateCommand(cmdFor("agent", "spawn"), "leo_spawn_agent")
	if err == nil {
		t.Fatal("a malformed payload must not read as unrestricted")
	}
	if !strings.Contains(err.Error(), "LEO_PERMISSIONS") {
		t.Errorf("error should name the variable so it can be fixed: %v", err)
	}
}

// findCommand walks the real command tree so the test asserts against the
// commands leo actually ships, not a stand-in.
func findCommand(t *testing.T, path ...string) *cobra.Command {
	t.Helper()
	cur := newRootCmd()
	for _, want := range path {
		var next *cobra.Command
		for _, c := range cur.Commands() {
			if c.Name() == want {
				next = c
				break
			}
		}
		if next == nil {
			t.Fatalf("command %q not found under %q", want, cur.CommandPath())
		}
		cur = next
	}
	return cur
}

// The gate is only worth anything if it is actually wired into the shipped
// commands. Running each one's RunE with a denying payload proves it, and
// fails loudly if a refactor drops a call.
func TestShippedCommandsAreGated(t *testing.T) {
	tests := []struct {
		path []string
		deny string
		args []string
	}{
		{path: []string{"agent", "spawn"}, deny: "leo_spawn_agent", args: []string{"scout"}},
		{path: []string{"agent", "stop"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "suspend"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "reset"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "restart"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "rename"}, deny: "leo_stop_agent", args: []string{"scout", "scout2"}},
		{path: []string{"agent", "prune"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"run"}, deny: "leo_run_task", args: []string{"nightly"}},
		{path: []string{"task", "enable"}, deny: "leo_toggle_task", args: []string{"nightly"}},
		{path: []string{"task", "disable"}, deny: "leo_toggle_task", args: []string{"nightly"}},
	}

	for _, tc := range tests {
		name := strings.Join(tc.path, " ")
		t.Run(name, func(t *testing.T) {
			t.Setenv("LEO_PERMISSIONS", `{"deny_tools":["`+tc.deny+`"]}`)

			cmd := findCommand(t, tc.path...)
			err := cmd.RunE(cmd, tc.args)
			if err == nil {
				t.Fatalf("leo %s ran despite %s being denied", name, tc.deny)
			}
			if !strings.Contains(err.Error(), "not permitted") {
				t.Errorf("leo %s failed for some other reason, so the gate may not be wired: %v", name, err)
			}
		})
	}
}
