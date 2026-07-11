package service

import (
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSessionSpecBuildArgs(t *testing.T) {
	spec := SessionSpec{
		Name:     "daily",
		Workdir:  "/tmp/d",
		Model:    "sonnet",
		Channels: []string{"plugin:slack@official"},
		ResumeID: "csid-1",
	}
	args := buildSessionClaudeArgs(spec)
	j := strings.Join(args, " ")
	for _, want := range []string{"--model sonnet", "--channels plugin:slack@official", "--resume csid-1", "--add-dir /tmp/d"} {
		if !strings.Contains(j, want) {
			t.Fatalf("expected %q in args: %s", want, j)
		}
	}
}

func TestSessionTmuxName(t *testing.T) {
	if got := SessionTmuxName("daily"); got != "leo-session-daily" {
		t.Fatalf("unexpected tmux name: %s", got)
	}
}

func TestSessionSpecsFromConfigExplicit(t *testing.T) {
	cfg := &config.Config{
		Sessions: map[string]config.SessionConfig{
			"explicit": {
				Workspace: "/tmp/explicit",
				Model:     "sonnet",
				HarnessOptions: map[string]any{
					"permission_mode": "acceptEdits",
				},
			},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].Name != "explicit" || specs[0].Workdir != "/tmp/explicit" {
		t.Fatalf("explicit spec wrong: %+v", specs[0])
	}
	if specs[0].PermissionMode != "acceptEdits" {
		t.Fatalf("expected permission mode decoded from harness_options, got %+v", specs[0])
	}
}

// TestSessionSpecsFromConfigExplicitDefaultsDoNotLeak verifies that
// defaults.harness_options do NOT cascade into explicit sessions —
// SessionHarnessOptions intentionally does not inherit defaults.
func TestSessionSpecsFromConfigExplicitDefaultsDoNotLeak(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			HarnessOptions: map[string]any{
				"permission_mode": "acceptEdits",
			},
		},
		Sessions: map[string]config.SessionConfig{
			"explicit": {Workspace: "/tmp/explicit", Model: "sonnet"},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if specs[0].PermissionMode != "" {
		t.Fatalf("expected defaults.harness_options NOT to leak into explicit session, got PermissionMode=%q", specs[0].PermissionMode)
	}
}

func TestSessionSpecsFromConfigImplicit(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"morning": {
				Runtime:   "persistent",
				Workspace: "/tmp/morning",
				Model:     "sonnet",
			},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 implicit spec, got %d: %+v", len(specs), specs)
	}
	if specs[0].Name != "morning" || specs[0].Workdir != "/tmp/morning" {
		t.Fatalf("implicit spec wrong: %+v", specs[0])
	}
}

// TestSessionSpecsFromConfigImplicitCopiesTaskEnv verifies task.Env reaches
// the implicit session's SessionSpec as an independent copy (not an alias
// of the config map), so a dedicated persistent task can target a custom
// endpoint via env: the same way an explicit session or process can.
func TestSessionSpecsFromConfigImplicitCopiesTaskEnv(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"morning": {
				Runtime:   "persistent",
				Workspace: "/tmp/morning",
				Model:     "sonnet",
				Env: map[string]string{
					"ANTHROPIC_BASE_URL": "https://x.example",
				},
			},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 implicit spec, got %d: %+v", len(specs), specs)
	}
	if got := specs[0].Env["ANTHROPIC_BASE_URL"]; got != "https://x.example" {
		t.Fatalf("expected task.Env to reach implicit session spec, got %+v", specs[0].Env)
	}
	// Mutating the returned spec's env must not alias the config's map.
	specs[0].Env["ANTHROPIC_BASE_URL"] = "mutated"
	if cfg.Tasks["morning"].Env["ANTHROPIC_BASE_URL"] != "https://x.example" {
		t.Fatal("implicit session Env aliases the task config's Env map")
	}
}

// TestSessionSpecsFromConfigImplicitTaskOptionsReachSpecButDefaultsDoNot
// verifies preserved quirk #2: an implicit session reads the task's OWN
// harness_options (no defaults cascade), matching SessionHarnessOptions'
// own-map semantics for explicit sessions.
func TestSessionSpecsFromConfigImplicitTaskOptionsReachSpecButDefaultsDoNot(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{
			HarnessOptions: map[string]any{
				"permission_mode": "acceptEdits",
			},
		},
		Tasks: map[string]config.TaskConfig{
			"morning": {
				Runtime:   "persistent",
				Workspace: "/tmp/morning",
				Model:     "sonnet",
				HarnessOptions: map[string]any{
					"permission_mode": "bypassPermissions",
				},
			},
			"evening": {
				Runtime:   "persistent",
				Workspace: "/tmp/evening",
				Model:     "sonnet",
				// no task-level harness_options — must NOT inherit defaults'.
			},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("expected 2 implicit specs, got %d: %+v", len(specs), specs)
	}
	byName := map[string]SessionSpec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	if got := byName["morning"].PermissionMode; got != "bypassPermissions" {
		t.Fatalf("expected task-level harness_options to reach implicit session, got PermissionMode=%q", got)
	}
	if got := byName["evening"].PermissionMode; got != "" {
		t.Fatalf("expected defaults.harness_options NOT to leak into implicit session with no own options, got PermissionMode=%q", got)
	}
}

func TestSessionSpecsFromConfigSkipsProcessReference(t *testing.T) {
	cfg := &config.Config{
		Processes: map[string]config.ProcessConfig{
			"bot": {Workspace: "/tmp/bot"},
		},
		Tasks: map[string]config.TaskConfig{
			"poke": {
				Runtime: "persistent",
				Session: "process:bot",
			},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("expected 0 specs (process: tasks are supervised elsewhere), got %d", len(specs))
	}
}

func TestSessionSpecsFromConfigSkipsOneshot(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"old": {Schedule: "0 7 * * *", PromptFile: "p.md"}, // no runtime: persistent
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("expected 0 specs for oneshot task, got %d", len(specs))
	}
}

// TestSessionSpecsFromConfigDefaultsWorkspace verifies that a Topology-A
// persistent task with no explicit workspace falls back to the default
// workspace rather than booting tmux with an empty -c (which would land the
// session in an unintended directory).
func TestSessionSpecsFromConfigDefaultsWorkspace(t *testing.T) {
	cfg := &config.Config{
		HomePath: "/home/leo",
		Tasks: map[string]config.TaskConfig{
			"daily": {Runtime: "persistent"}, // no workspace
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatalf("specs: %v", err)
	}
	if len(specs) != 1 {
		t.Fatalf("expected 1 spec, got %d", len(specs))
	}
	if want := cfg.DefaultWorkspace(); specs[0].Workdir != want {
		t.Fatalf("workdir = %q, want default %q", specs[0].Workdir, want)
	}
}
