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
	if specs[0].Name != "explicit" || specs[0].Workdir != "/tmp/explicit" {
		t.Fatalf("explicit spec wrong: %+v", specs[0])
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

func TestSessionSpecsProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY", DefaultModel: "glm-5.2"},
		},
		Sessions: map[string]config.SessionConfig{
			"research": {Workspace: "/tmp/w", Provider: "glm", Env: map[string]string{"FOO": "bar"}},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 {
		t.Fatalf("got %d specs", len(specs))
	}
	s := specs[0]
	if s.Env["ANTHROPIC_BASE_URL"] != "https://x.example" || s.Env["ANTHROPIC_AUTH_TOKEN"] != "sk-glm" {
		t.Errorf("provider env missing: %v", s.Env)
	}
	if s.Env["FOO"] != "bar" {
		t.Errorf("configured env lost: %v", s.Env)
	}
	if s.Model != "glm-5.2" {
		t.Errorf("Model = %q, want provider default", s.Model)
	}
}

func TestSessionSpecsSkipsUnresolvableProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_DEFINITELY_UNSET_KEY"},
		},
		Sessions: map[string]config.SessionConfig{
			"broken": {Workspace: "/tmp/w", Provider: "glm"},
			"fine":   {Workspace: "/tmp/w2"},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Name != "fine" {
		t.Fatalf("expected only fine to survive, got %+v", specs)
	}
}

func TestImplicitSessionProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY"},
		},
		Tasks: map[string]config.TaskConfig{
			"digest": {Schedule: "0 * * * *", PromptFile: "p.md", Runtime: "persistent", Provider: "glm"},
		},
	}
	specs, err := SessionSpecsFromConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 1 || specs[0].Env["ANTHROPIC_AUTH_TOKEN"] != "sk-glm" {
		t.Fatalf("implicit session missing provider env: %+v", specs)
	}
}
