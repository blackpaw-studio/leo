package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// TestSpawnOpeningPromptDeliveredViaBriefFile locks the tmux-16KiB-command-
// limit fix (see claudeharness.ArgvPromptLimit): a claude ephemeral agent's
// opening prompt must never appear as literal launch-argv text. Instead it is
// written to a private per-agent brief file and the launch argv carries only
// a small $(cat ...) command-substitution word (wrapped so the shell-quoting
// pass in internal/service/process.go emits it unquoted — see
// harness.SplitRawArg).
func TestSpawnOpeningPromptDeliveredViaBriefFile(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	const prompt = "do the thing, carefully"
	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: prompt})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if sup.spawnCall == nil {
		t.Fatal("supervisor SpawnAgent was never called")
	}

	for _, a := range sup.spawnCall.ClaudeArgs {
		if strings.Contains(a, prompt) {
			t.Fatalf("launch argv must not contain the literal prompt text; got %q in %v", a, sup.spawnCall.ClaudeArgs)
		}
	}

	briefPath := claudeharness.AgentBriefPath(home, rec.Name)
	var rawWord string
	for _, a := range sup.spawnCall.ClaudeArgs {
		if word, raw := harness.SplitRawArg(a); raw {
			rawWord = word
		}
	}
	if rawWord == "" {
		t.Fatalf("expected a raw $(cat ...) argv word in ClaudeArgs, got %v", sup.spawnCall.ClaudeArgs)
	}
	wantWord := claudeharness.BriefArgvWord(briefPath)
	if rawWord != wantWord {
		t.Fatalf("raw argv word = %q, want %q", rawWord, wantWord)
	}

	info, err := os.Stat(briefPath)
	if err != nil {
		t.Fatalf("brief file not written: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("brief file mode = %o, want 0600", perm)
	}
	content, err := os.ReadFile(briefPath)
	if err != nil {
		t.Fatalf("reading brief file: %v", err)
	}
	if string(content) != prompt {
		t.Errorf("brief file content = %q, want %q", content, prompt)
	}

	// The persisted agentstore record must carry the same raw argv word, not
	// the literal prompt — Reset replays rec.ClaudeArgs verbatim, so it must
	// already be safe to re-launch with.
	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	stored, ok := recs[rec.Name]
	if !ok {
		t.Fatalf("no stored record for %q", rec.Name)
	}
	found := false
	for _, a := range stored.ClaudeArgs {
		if strings.Contains(a, prompt) {
			t.Fatalf("stored ClaudeArgs must not contain the literal prompt; got %q", a)
		}
		if word, raw := harness.SplitRawArg(a); raw && word == wantWord {
			found = true
		}
	}
	if !found {
		t.Errorf("stored ClaudeArgs missing raw brief argv word, got %v", stored.ClaudeArgs)
	}
}

// TestSpawnNoPromptWritesNoBriefFile ensures the brief-file mechanism is
// opt-in: a spawn with no opening prompt must not create any brief file or
// raw argv word.
func TestSpawnNoPromptWritesNoBriefFile(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	briefPath := claudeharness.AgentBriefPath(home, rec.Name)
	if _, err := os.Stat(briefPath); !os.IsNotExist(err) {
		t.Fatalf("expected no brief file, stat err = %v", err)
	}
	for _, a := range sup.spawnCall.ClaudeArgs {
		if _, raw := harness.SplitRawArg(a); raw {
			t.Fatalf("expected no raw argv word with an empty prompt, got %v", sup.spawnCall.ClaudeArgs)
		}
	}
}

// TestSpawnOversizedPromptRejected confirms Spawn fails fast, with a clear
// error, rather than truncating or silently misdelivering a prompt above
// claudeharness.ArgvPromptLimit — ephemeral claude agents have no
// tmux-paste-injection fallback (unlike interactive dispatch's
// injectOpening), so anything above the limit cannot be delivered safely at
// launch time.
func TestSpawnOversizedPromptRejected(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	oversized := strings.Repeat("a", claudeharness.ArgvPromptLimit+1)
	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: oversized})
	if err == nil {
		t.Fatal("expected an error for an oversized opening prompt")
	}
	if sup.spawnCall != nil {
		t.Fatalf("supervisor must not be invoked when the prompt is rejected, got %+v", sup.spawnCall)
	}
	if _, statErr := os.Stat(claudeharness.AgentBriefPath(home, "assistant")); !os.IsNotExist(statErr) {
		t.Fatalf("no brief file should be left behind on rejection, stat err = %v", statErr)
	}
}

// TestStopRemovesOpeningPromptBriefFile confirms a stopped agent's brief file
// is cleaned up: the supervise loop that could have replayed it is gone once
// StopAgent succeeds, so nothing needs it any more.
func TestStopRemovesOpeningPromptBriefFile(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: "hello"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	briefPath := claudeharness.AgentBriefPath(home, rec.Name)
	if _, err := os.Stat(briefPath); err != nil {
		t.Fatalf("expected brief file to exist before stop: %v", err)
	}

	if err := m.Stop(rec.Name, StopOptions{}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if _, err := os.Stat(briefPath); !os.IsNotExist(err) {
		t.Fatalf("expected brief file removed after stop, stat err = %v", err)
	}
}

// TestDeleteRemovesOpeningPromptBriefFile mirrors TestStopRemovesOpeningPromptBriefFile
// for the permanent-removal path, and tolerates the file already being gone
// (e.g. a prior Stop already cleaned it up).
func TestDeleteRemovesOpeningPromptBriefFile(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: "hello"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}
	briefPath := claudeharness.AgentBriefPath(home, rec.Name)

	if err := m.Stop(rec.Name, StopOptions{}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	// Delete must tolerate the brief file already being gone.
	if err := m.Delete(context.Background(), rec.Name, DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(briefPath); !os.IsNotExist(err) {
		t.Fatalf("expected brief file removed after delete, stat err = %v", err)
	}
}

// TestRenameRemovesOldOpeningPromptBriefOnlyWhenNotLive mirrors
// TestRenameRemovesOldSettingsSpillOnlyWhenNotLive: a live agent's
// stored ClaudeArgs already reference the OLD name's brief path (rewriteNameArg
// only rewrites --name, not a raw $(cat ...) argv word baked in at spawn
// time), so only a non-live rename — about to be started fresh under the new
// name with no opening prompt — cleans up the old file right away.
func TestRenameRemovesOldOpeningPromptBriefOnlyWhenNotLive(t *testing.T) {
	for _, live := range []bool{false, true} {
		home := t.TempDir()
		briefPath := claudeharness.AgentBriefPath(home, "leo-old")
		if err := claudeharness.WritePrivateBrief(briefPath, "hello"); err != nil {
			t.Fatal(err)
		}
		_ = agentstore.Save(home, agentstore.Record{Name: "leo-old", Workspace: "/w", Stopped: !live, ClaudeArgs: []string{"--name", "leo-old"}})
		sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
		if live {
			sup.ephemeral["leo-old"] = ProcessState{Name: "leo-old", Status: "running"}
		}
		m := newTestManager(t, home, sup)

		if _, err := m.Rename("leo-old", "leo-new"); err != nil {
			t.Fatalf("Rename (live=%v): %v", live, err)
		}

		_, err := os.Stat(briefPath)
		if live && err != nil {
			t.Fatalf("live rename removed the still-referenced brief file: %v", err)
		}
		if !live && !os.IsNotExist(err) {
			t.Fatalf("non-live rename kept the old brief file: err=%v", err)
		}
	}
}

// TestRestartDoesNotReplayOpeningPromptBrief confirms the existing
// "restart/resume never resend the opening prompt" invariant extends
// unchanged to the brief-file mechanism: a restart's re-resolved ClaudeArgs
// must carry neither the literal prompt nor a raw brief argv word.
func TestRestartDoesNotReplayOpeningPromptBrief(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{
			"t": {Workspace: home},
		},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	rec, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: "hello"})
	if err != nil {
		t.Fatalf("spawn: %v", err)
	}

	if err := m.Restart(rec.Name); err != nil {
		t.Fatalf("restart: %v", err)
	}
	for _, a := range sup.spawnCall.ClaudeArgs {
		if _, raw := harness.SplitRawArg(a); raw {
			t.Fatalf("restart must not replay the opening-prompt brief argv word, got %v", sup.spawnCall.ClaudeArgs)
		}
		if strings.Contains(a, "hello") {
			t.Fatalf("restart must not replay the literal opening prompt, got %v", sup.spawnCall.ClaudeArgs)
		}
	}
}
