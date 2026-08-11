package agent

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
	"github.com/blackpaw-studio/leo/internal/leotools"
)

// restrictedTemplate is a template carrying every kind of permission, so
// tests assert the whole payload survives the trip into the environment.
func restrictedTemplate(harness string) config.TemplateConfig {
	return config.TemplateConfig{
		Harness: harness,
		Permissions: leotools.Permissions{
			DenyTools:  []string{"leo_spawn_agent"},
			CanMessage: []string{"rocket", "scout-*"},
			CanSpawn:   []string{"codex"},
			CanConsult: []string{"fable"},
		},
	}
}

func TestBuildTemplateArgsExportsPermissions(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			cfg := &config.Config{
				HomePath: t.TempDir(),
				Web:      config.WebConfig{Enabled: true},
				Defaults: config.DefaultsConfig{Harness: harness},
			}
			_, env := BuildTemplateArgs(cfg, restrictedTemplate(harness), "agent-x", "/tmp/ws", "", "tok")

			raw, ok := env[permissionsEnvVar]
			if !ok {
				t.Fatalf("%s did not export %s; env = %v", harness, permissionsEnvVar, env)
			}

			var got leotools.Permissions
			if err := json.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("exported payload is not valid JSON (%q): %v", raw, err)
			}
			if !got.DeniesTool("leo_spawn_agent") {
				t.Errorf("deny_tools did not survive: %+v", got)
			}
			if got.AllowsMessage("olympus") || !got.AllowsMessage("scout-leo") {
				t.Errorf("can_message did not survive: %+v", got)
			}
			if !got.AllowsSpawn("codex") || !got.AllowsConsult("fable") {
				t.Errorf("can_spawn/can_consult did not survive: %+v", got)
			}
			// The payload rides an env var read by a shell-launched process;
			// a literal newline would truncate it in any line-oriented path.
			if strings.ContainsAny(raw, "\n\r") {
				t.Errorf("payload must be single-line, got %q", raw)
			}
		})
	}
}

// A template with no permissions block must produce exactly the environment
// it produced before this feature existed — no empty payload, no key.
func TestBuildTemplateArgsOmitsPermissionsWhenUnrestricted(t *testing.T) {
	for _, harness := range []string{"claude", "codex", "opencode"} {
		t.Run(harness, func(t *testing.T) {
			cfg := &config.Config{
				HomePath: t.TempDir(),
				Web:      config.WebConfig{Enabled: true},
				Defaults: config.DefaultsConfig{Harness: harness},
			}
			_, env := BuildTemplateArgs(cfg, config.TemplateConfig{Harness: harness}, "agent-x", "/tmp/ws", "", "tok")
			if _, ok := env[permissionsEnvVar]; ok {
				t.Errorf("%s exported %s for an unrestricted template", harness, permissionsEnvVar)
			}
		})
	}
}

// An empty-but-present permissions block (what a config round trip through
// the web UI can produce) is still unrestricted, so it must not export either.
func TestBuildTemplateArgsOmitsPermissionsWhenEmpty(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	tmpl := config.TemplateConfig{Permissions: leotools.Permissions{
		DenyTools:  []string{},
		CanMessage: []string{},
	}}
	_, env := BuildTemplateArgs(cfg, tmpl, "agent-x", "/tmp/ws", "", "tok")
	if _, ok := env[permissionsEnvVar]; ok {
		t.Errorf("an empty permissions block must not export %s", permissionsEnvVar)
	}
}

// codex forwards env by *name*, so the bridge must list the variable or the
// MCP server never sees it.
func TestCodexBridgeForwardsPermissionsEnvVar(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	_, spec, err := resolveTemplateLaunch(cfg, restrictedTemplate("codex"), "agent-x", "/tmp/ws", "", "tok")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	opts := spec.Options.(codexharness.Options)
	if opts.LeoMCP == nil {
		t.Fatal("expected a LeoMCP bridge")
	}
	if !slices.Contains(opts.LeoMCP.EnvVars, permissionsEnvVar) {
		t.Errorf("codex bridge EnvVars = %v, want it to include %s", opts.LeoMCP.EnvVars, permissionsEnvVar)
	}
}

// opencode passes explicit values rather than names, so the bridge map must
// carry the payload itself.
func TestOpencodeBridgeCarriesPermissionsValue(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "opencode"},
	}
	_, spec, err := resolveTemplateLaunch(cfg, restrictedTemplate("opencode"), "agent-x", "/tmp/ws", "", "tok")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	opts := spec.Options.(opencodeharness.Options)
	if opts.LeoMCP == nil {
		t.Fatal("expected a LeoMCP bridge")
	}
	raw, ok := opts.LeoMCP.Env[permissionsEnvVar]
	if !ok {
		t.Fatalf("opencode bridge Env = %v, want %s", opts.LeoMCP.Env, permissionsEnvVar)
	}
	var got leotools.Permissions
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("bridge payload is not valid JSON (%q): %v", raw, err)
	}
	if !got.DeniesTool("leo_spawn_agent") {
		t.Errorf("bridge payload lost deny_tools: %+v", got)
	}

	// Unrestricted templates must leave the bridge map as it was.
	_, plain, err := resolveTemplateLaunch(cfg, config.TemplateConfig{}, "agent-x", "/tmp/ws", "", "tok")
	if err != nil {
		t.Fatalf("resolveTemplateLaunch: %v", err)
	}
	if _, ok := plain.Options.(opencodeharness.Options).LeoMCP.Env[permissionsEnvVar]; ok {
		t.Errorf("unrestricted template must not put %s in the bridge env", permissionsEnvVar)
	}
}

// Leo owns LEO_PERMISSIONS: the value the agent runs with must always be the
// one the CURRENT template config produces. A stored or inherited env layer
// resurrecting a stale payload would leave an agent restricted after the
// operator removed the restriction — a change that looks applied but is not.
func TestResolveRestartArgsDropsStalePermissions(t *testing.T) {
	cfg := &config.Config{
		HomePath:  t.TempDir(),
		Web:       config.WebConfig{Enabled: true},
		Templates: map[string]config.TemplateConfig{"scout": {}}, // permissions removed
	}
	stale := `{"deny_tools":["leo_spawn_agent"]}`

	tests := []struct {
		name string
		rec  agentstore.Record
	}{
		{
			name: "legacy record carrying it in Env",
			rec: agentstore.Record{
				Name: "scout", Template: "scout", Workspace: "/tmp/ws",
				Env: map[string]string{permissionsEnvVar: stale, "KEEP": "1"},
			},
		},
		{
			name: "modern record carrying it in InheritedEnv",
			rec: agentstore.Record{
				Name: "scout", Template: "scout", Workspace: "/tmp/ws",
				InheritedEnv: map[string]string{permissionsEnvVar: stale, "KEEP": "1"},
			},
		},
		{
			name: "modern record carrying it in SpawnEnv",
			rec: agentstore.Record{
				Name: "scout", Template: "scout", Workspace: "/tmp/ws",
				SpawnEnv: map[string]string{permissionsEnvVar: stale, "KEEP": "1"},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, env := resolveRestartArgs(cfg, tc.rec, "tok")
			if got, ok := env[permissionsEnvVar]; ok {
				t.Errorf("stale %s survived restart as %q; the template no longer restricts", permissionsEnvVar, got)
			}
			if env["KEEP"] != "1" {
				t.Errorf("unrelated env keys must survive; got %v", env)
			}
		})
	}
}

// The mirror case: a template that gained permissions must impose them on
// restart, overriding whatever the record stored.
func TestResolveRestartArgsAppliesCurrentPermissions(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Templates: map[string]config.TemplateConfig{
			"scout": {Permissions: leotools.Permissions{CanMessage: []string{"rocket"}}},
		},
	}
	rec := agentstore.Record{
		Name: "scout", Template: "scout", Workspace: "/tmp/ws",
		SpawnEnv: map[string]string{permissionsEnvVar: `{"can_message":["anyone"]}`},
	}

	_, env := resolveRestartArgs(cfg, rec, "tok")
	var got leotools.Permissions
	if err := json.Unmarshal([]byte(env[permissionsEnvVar]), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", env[permissionsEnvVar], err)
	}
	if got.AllowsMessage("anyone") || !got.AllowsMessage("rocket") {
		t.Errorf("restart must apply current config, got %+v", got)
	}
}

// Reset is the heavier hammer — it discards the conversation entirely — so an
// operator has every reason to expect it to pick up current config. It
// respawns from the stored record, which means stale permissions ride along
// unless they are re-resolved, exactly as they would on restart.
func TestResetReResolvesPermissions(t *testing.T) {
	tests := []struct {
		name     string
		template config.TemplateConfig
		stored   string
		want     string // "" means the variable must be absent
	}{
		{
			name:     "restriction removed from config is dropped",
			template: config.TemplateConfig{},
			stored:   `{"deny_tools":["leo_spawn_agent"]}`,
			want:     "",
		},
		{
			name:     "restriction added to config is applied",
			template: config.TemplateConfig{Permissions: leotools.Permissions{CanMessage: []string{"rocket"}}},
			stored:   "",
			want:     `{"can_message":["rocket"]}`,
		},
		{
			name:     "restriction changed in config wins over the stored one",
			template: config.TemplateConfig{Permissions: leotools.Permissions{CanMessage: []string{"rocket"}}},
			stored:   `{"can_message":["anyone"]}`,
			want:     `{"can_message":["rocket"]}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			cfg := &config.Config{
				HomePath:  home,
				Templates: map[string]config.TemplateConfig{"scout": tc.template},
			}
			sup := &capturingSupervisor{
				agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
			}
			env := map[string]string{"KEEP": "1"}
			if tc.stored != "" {
				env[permissionsEnvVar] = tc.stored
			}
			if err := agentstore.Save(home, agentstore.Record{
				Name: "leo-x", Template: "scout", Workspace: "/w", Env: env,
			}); err != nil {
				t.Fatalf("save: %v", err)
			}

			m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
			if err := m.Reset("leo-x"); err != nil {
				t.Fatalf("reset: %v", err)
			}
			if sup.spawnCall == nil {
				t.Fatal("reset did not respawn")
			}

			got, ok := sup.spawnCall.Env[permissionsEnvVar]
			if tc.want == "" {
				if ok {
					t.Errorf("stale %s survived reset as %q", permissionsEnvVar, got)
				}
			} else if got != tc.want {
				t.Errorf("%s = %q, want %q", permissionsEnvVar, got, tc.want)
			}
			if sup.spawnCall.Env["KEEP"] != "1" {
				t.Errorf("unrelated env must survive: %v", sup.spawnCall.Env)
			}
		})
	}
}

// A record whose template is gone from config has no policy to re-resolve
// against, so reset must leave its stored env alone rather than silently
// lifting the restriction — matching what restart does in the same situation.
func TestResetLeavesEnvAloneWhenTemplateMissing(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Templates: map[string]config.TemplateConfig{}}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	stored := `{"deny_tools":["leo_spawn_agent"]}`
	if err := agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "gone", Workspace: "/w",
		Env: map[string]string{permissionsEnvVar: stored},
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if err := m.Reset("leo-x"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if got := sup.spawnCall.Env[permissionsEnvVar]; got != stored {
		t.Errorf("%s = %q, want the stored value %q preserved", permissionsEnvVar, got, stored)
	}
}
