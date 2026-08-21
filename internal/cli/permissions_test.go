package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leotools"
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

	for _, name := range []string{"stop", "reset", "restart", "rename"} {
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
		// agent worktree is a second spawn route, in its own file — it was
		// missed the first time precisely because it does not live in
		// agent.go. TestEverySpawnRouteIsGated below stops that recurring.
		{path: []string{"agent", "worktree"}, deny: "leo_spawn_agent", args: []string{"scout", "feat/x"}},
		{path: []string{"agent", "stop"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "reset"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "restart"}, deny: "leo_stop_agent", args: []string{"scout"}},
		{path: []string{"agent", "rename"}, deny: "leo_stop_agent", args: []string{"scout", "scout2"}},
		{path: []string{"run"}, deny: "leo_run_task", args: []string{"nightly"}},
		{path: []string{"task", "enable"}, deny: "leo_toggle_task", args: []string{"nightly"}},
		{path: []string{"task", "disable"}, deny: "leo_toggle_task", args: []string{"nightly"}},
		// These do not target one agent, they take out every live session at
		// once — the same capability leo_stop_agent withholds, at more scale.
		{path: []string{"service", "stop"}, deny: "leo_stop_agent", args: nil},
		{path: []string{"service", "restart"}, deny: "leo_stop_agent", args: nil},
		{path: []string{"service", "reparent"}, deny: "leo_stop_agent", args: nil},
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

// The first pass at the CLI gate missed `leo agent worktree` because it lives
// in its own file and the search only covered agent.go. This test looks at
// what the commands *do* rather than where they are written: any CLI command
// that reaches a spawn or lifecycle endpoint on the daemon must be gated, so
// a new one cannot be added without either gating it or failing here.
func TestEverySpawnRouteIsGated(t *testing.T) {
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}

	// daemon calls that create or disrupt an agent, and the tool each is
	// expected to be governed by.
	governed := map[string]string{
		"daemon.AgentSpawnRequest": "leo_spawn_agent",
		"daemon.AgentStop":         "leo_stop_agent",
		"daemon.AgentReset":        "leo_stop_agent",
		"daemon.AgentRestart":      "leo_stop_agent",
		"daemon.AgentSuspend":      "leo_stop_agent",
		"daemon.AgentRename":       "leo_stop_agent",
		"daemon.AgentPruneRequest": "leo_stop_agent",
	}

	for _, src := range sources {
		if strings.HasSuffix(src, "_test.go") {
			continue
		}
		body, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		text := string(body)

		for call, tool := range governed {
			if !strings.Contains(text, call) {
				continue
			}
			// update_stale.go restarts agents on the operator's behalf after
			// a leo upgrade; it is not agent-reachable command surface.
			if src == "update_stale.go" {
				continue
			}
			if !strings.Contains(text, "gateCommand(cmd, \""+tool+"\")") &&
				!strings.Contains(text, "gateSpawnTemplate(cmd,") {
				t.Errorf("%s calls %s but has no %s gate — every agent-reachable route to that capability must be gated",
					src, call, tool)
			}
		}
	}
}

// Permission allowlists reference templates by name and Validate() requires
// those references to resolve, so removing a referenced template writes a
// config that every subsequent leo command then refuses to load — leaving
// hand-editing leo.yaml as the only way out. saveConfig must not write a
// config leo cannot read back.
func TestSaveConfigRefusesToBrickTheConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")

	valid := &config.Config{
		HomePath: dir,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"codex": {},
			"scout": {Permissions: leotools.Permissions{CanSpawn: []string{"codex"}}},
		},
	}
	if err := config.Save(path, valid); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	prev := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = prev })

	// Removing codex leaves scout referencing a template that no longer exists.
	broken := *valid
	broken.Templates = map[string]config.TemplateConfig{
		"scout": valid.Templates["scout"],
	}
	if err := saveConfig(&broken); err == nil {
		t.Fatal("saveConfig wrote a config that cannot be loaded back")
	}

	// The on-disk config must be untouched by the refused write.
	reloaded, err := config.Load(path)
	if err != nil {
		t.Fatalf("config on disk is no longer loadable: %v", err)
	}
	if _, ok := reloaded.Templates["codex"]; !ok {
		t.Error("refused save must leave the previous config in place")
	}
}
