package config

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/leotools"
	"gopkg.in/yaml.v3"
)

// permCfg builds a minimal valid config carrying the given template
// permissions, so each test asserts on the permissions error alone.
func permCfg(perms leotools.Permissions, extra ...string) *Config {
	templates := map[string]TemplateConfig{"scout": {Permissions: perms}}
	for _, name := range extra {
		templates[name] = TemplateConfig{}
	}
	return &Config{
		Defaults:  DefaultsConfig{Model: "sonnet"},
		Templates: templates,
	}
}

func TestValidateAcceptsAbsentPermissions(t *testing.T) {
	if err := permCfg(leotools.Permissions{}).Validate(); err != nil {
		t.Fatalf("a template without permissions must validate: %v", err)
	}
}

func TestValidateRejectsUnknownDenyTool(t *testing.T) {
	err := permCfg(leotools.Permissions{DenyTools: []string{"leo_spwan_agent"}}).Validate()
	if err == nil {
		t.Fatal("expected an error for a misspelled tool name")
	}
	if !strings.Contains(err.Error(), "leo_spwan_agent") {
		t.Errorf("error should quote the bad name: %v", err)
	}
	// The valid names must be listed — a typo that silently grants full
	// access is exactly what this check exists to prevent.
	if !strings.Contains(err.Error(), "leo_spawn_agent") {
		t.Errorf("error should list the valid tool names: %v", err)
	}
}

func TestValidateAcceptsKnownDenyTools(t *testing.T) {
	perms := leotools.Permissions{DenyTools: []string{"leo_spawn_agent", "leo_stop_agent", "leo_toggle_task"}}
	if err := permCfg(perms).Validate(); err != nil {
		t.Fatalf("known tool names must validate: %v", err)
	}
}

func TestValidateRejectsDenyingLeoSkill(t *testing.T) {
	err := permCfg(leotools.Permissions{DenyTools: []string{leotools.SkillTool}}).Validate()
	if err == nil {
		t.Fatal("expected an error for denying leo_skill")
	}
	if !strings.Contains(err.Error(), leotools.SkillTool) {
		t.Errorf("error should name leo_skill: %v", err)
	}
}

func TestValidateChecksSpawnAndConsultTemplates(t *testing.T) {
	tests := []struct {
		name    string
		perms   leotools.Permissions
		others  []string
		wantErr string
	}{
		{
			name:    "can_spawn naming an undefined template",
			perms:   leotools.Permissions{CanSpawn: []string{"ghost"}},
			wantErr: "ghost",
		},
		{
			name:    "can_consult naming an undefined template",
			perms:   leotools.Permissions{CanConsult: []string{"ghost"}},
			wantErr: "ghost",
		},
		{
			name:   "can_spawn naming a defined template",
			perms:  leotools.Permissions{CanSpawn: []string{"codex"}},
			others: []string{"codex"},
		},
		{
			name:  "glob entries are accepted unchecked",
			perms: leotools.Permissions{CanSpawn: []string{"worker-*"}, CanConsult: []string{"*"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := permCfg(tc.perms, tc.others...).Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("expected no error, got %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("expected a validation error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error should mention %q: %v", tc.wantErr, err)
			}
		})
	}
}

func TestValidateSkipsCanMessage(t *testing.T) {
	// Agent names are generated at spawn time, so can_message entries cannot
	// be checked against config and must never block a save.
	perms := leotools.Permissions{CanMessage: []string{"rocket", "not-a-template", "scout-*"}}
	if err := permCfg(perms).Validate(); err != nil {
		t.Fatalf("can_message must not be validated against templates: %v", err)
	}
}

func TestPermissionsYAMLRoundTrip(t *testing.T) {
	const src = `
templates:
  scout:
    permissions:
      deny_tools: [leo_spawn_agent]
      can_message: [rocket, "scout-*"]
      can_spawn: [codex]
      can_consult: [fable]
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(src), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := cfg.Templates["scout"].Permissions
	if len(got.DenyTools) != 1 || got.DenyTools[0] != "leo_spawn_agent" {
		t.Errorf("deny_tools: got %v", got.DenyTools)
	}
	if len(got.CanMessage) != 2 || got.CanMessage[1] != "scout-*" {
		t.Errorf("can_message: got %v", got.CanMessage)
	}
	if len(got.CanSpawn) != 1 || len(got.CanConsult) != 1 {
		t.Errorf("can_spawn/can_consult: got %v / %v", got.CanSpawn, got.CanConsult)
	}

	// Marshalling a template without permissions must not emit the key —
	// otherwise every existing config grows a noisy empty block on save.
	out, err := yaml.Marshal(&Config{Templates: map[string]TemplateConfig{"bare": {}}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), "permissions") {
		t.Errorf("empty permissions must be omitted on save:\n%s", out)
	}
}

// Permission allowlists reference templates by name, so a rename has to
// cascade into them the way it already does into tasks. Without that, the
// rename validates against a name that no longer exists and is rejected —
// with an error naming a template the operator never touched.
func TestRenameTemplateCascadesIntoPermissions(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{Model: "sonnet"},
		Templates: map[string]TemplateConfig{
			"codex": {},
			"scout": {Permissions: leotools.Permissions{
				CanSpawn:   []string{"codex", "worker-*"},
				CanConsult: []string{"codex"},
			}},
		},
	}

	if err := RenameTemplate(cfg, "codex", "codex2"); err != nil {
		t.Fatalf("RenameTemplate: %v", err)
	}

	perms := cfg.Templates["scout"].Permissions
	if len(perms.CanSpawn) != 2 || perms.CanSpawn[0] != "codex2" {
		t.Errorf("can_spawn did not follow the rename: %v", perms.CanSpawn)
	}
	if perms.CanSpawn[1] != "worker-*" {
		t.Errorf("glob entries must be left alone: %v", perms.CanSpawn)
	}
	if len(perms.CanConsult) != 1 || perms.CanConsult[0] != "codex2" {
		t.Errorf("can_consult did not follow the rename: %v", perms.CanConsult)
	}

	// The whole point: the renamed config must still validate.
	if err := cfg.Validate(); err != nil {
		t.Errorf("config must stay valid after a rename: %v", err)
	}
}

// A template that only references itself must survive its own rename.
func TestRenameTemplateCascadesIntoOwnPermissions(t *testing.T) {
	cfg := &Config{
		Defaults: DefaultsConfig{Model: "sonnet"},
		Templates: map[string]TemplateConfig{
			"scout": {Permissions: leotools.Permissions{CanSpawn: []string{"scout"}}},
		},
	}

	if err := RenameTemplate(cfg, "scout", "scout2"); err != nil {
		t.Fatalf("RenameTemplate: %v", err)
	}
	if got := cfg.Templates["scout2"].Permissions.CanSpawn; len(got) != 1 || got[0] != "scout2" {
		t.Errorf("a self-reference must follow the rename: %v", got)
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("config must stay valid after a rename: %v", err)
	}
}
