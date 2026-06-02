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
