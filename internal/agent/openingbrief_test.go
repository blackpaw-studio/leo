package agent

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// TestSpawnOpeningPromptDeliveredViaBriefFile locks the tmux-16KiB-command-
// limit fix (see claudeharness.ArgvPromptLimit): a claude ephemeral agent's
// opening prompt must never appear as literal launch-argv text in ClaudeArgs.
// Instead it is written to a private per-agent brief file and carried on the
// typed SpawnRequest/agentstore.Record.OpeningBriefPath field — never inside
// ClaudeArgs, which stays fully shell-quotable with no exceptions.
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
	if sup.spawnCall.OpeningBriefPath != briefPath {
		t.Fatalf("SpawnRequest.OpeningBriefPath = %q, want %q", sup.spawnCall.OpeningBriefPath, briefPath)
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

	// The persisted agentstore record must carry the path on its own typed
	// field, not the literal prompt — Reset replays it, so it must already
	// be safe to re-launch with.
	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	stored, ok := recs[rec.Name]
	if !ok {
		t.Fatalf("no stored record for %q", rec.Name)
	}
	for _, a := range stored.ClaudeArgs {
		if strings.Contains(a, prompt) {
			t.Fatalf("stored ClaudeArgs must not contain the literal prompt; got %q", a)
		}
	}
	if stored.OpeningBriefPath != briefPath {
		t.Errorf("stored OpeningBriefPath = %q, want %q", stored.OpeningBriefPath, briefPath)
	}
}

// TestSpawnNoPromptWritesNoBriefFile ensures the brief-file mechanism is
// opt-in: a spawn with no opening prompt must not create any brief file or
// set OpeningBriefPath.
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
	if sup.spawnCall.OpeningBriefPath != "" {
		t.Fatalf("expected empty OpeningBriefPath with no prompt, got %q", sup.spawnCall.OpeningBriefPath)
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

// TestStopKeepsOpeningPromptBriefFile confirms Stop no longer deletes the
// brief file: Reset replays a record's OpeningBriefPath verbatim, so a
// stop-then-reset must still find it (this is the MEDIUM finding from the
// security review of the first cut of this fix — Stop used to delete the
// file out from under a later Reset).
func TestStopKeepsOpeningPromptBriefFile(t *testing.T) {
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
	if _, err := os.Stat(briefPath); err != nil {
		t.Fatalf("expected brief file to survive Stop, stat err = %v", err)
	}
}

// TestStopThenResetReplaysOpeningPrompt is the end-to-end regression test for
// the MEDIUM finding: stopping an agent and then resetting it must still
// deliver the original opening prompt, because Reset (unlike Start/Restart)
// replays the original ClaudeArgs/OpeningBriefPath rather than rebuilding
// them promptless.
func TestStopThenResetReplaysOpeningPrompt(t *testing.T) {
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
	if err := m.Reset(rec.Name); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if sup.spawnCall.OpeningBriefPath != briefPath {
		t.Fatalf("reset after stop: OpeningBriefPath = %q, want %q", sup.spawnCall.OpeningBriefPath, briefPath)
	}
	if _, err := os.Stat(briefPath); err != nil {
		t.Fatalf("brief file gone after stop+reset: %v", err)
	}
}

// TestDeleteRemovesOpeningPromptBriefFile is the one path that actually
// removes the brief file — the agent is gone for good.
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
	if err := m.Delete(context.Background(), rec.Name, DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(briefPath); !os.IsNotExist(err) {
		t.Fatalf("expected brief file removed after delete, stat err = %v", err)
	}
}

// TestDeleteToleratesMissingOpeningPromptBriefFile confirms Delete never
// fails just because the brief file is already gone (e.g. hand-removed, or a
// record with no opening prompt at all).
func TestDeleteToleratesMissingOpeningPromptBriefFile(t *testing.T) {
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
	if err := m.Stop(rec.Name, StopOptions{}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Delete(context.Background(), rec.Name, DeleteOptions{}); err != nil {
		t.Fatalf("delete should tolerate a record with no opening-prompt brief: %v", err)
	}
}

// TestRenameKeepsOldOpeningPromptBriefFile confirms a rename (live or not)
// never deletes the brief file: persistRename leaves OpeningBriefPath
// untouched (only rewriteNameArg touches ClaudeArgs, and only the --name
// flag value), so a later Reset under the new name can still replay the
// original prompt.
func TestRenameKeepsOldOpeningPromptBriefFile(t *testing.T) {
	for _, live := range []bool{false, true} {
		home := t.TempDir()
		briefPath := claudeharness.AgentBriefPath(home, "leo-old")
		if err := claudeharness.WritePrivateBrief(briefPath, "hello"); err != nil {
			t.Fatal(err)
		}
		_ = agentstore.Save(home, agentstore.Record{
			Name:             "leo-old",
			Workspace:        "/w",
			Stopped:          !live,
			ClaudeArgs:       []string{"--name", "leo-old"},
			OpeningBriefPath: briefPath,
		})
		sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
		if live {
			sup.ephemeral["leo-old"] = ProcessState{Name: "leo-old", Status: "running"}
		}
		m := newTestManager(t, home, sup)

		if _, err := m.Rename("leo-old", "leo-new"); err != nil {
			t.Fatalf("Rename (live=%v): %v", live, err)
		}

		if _, err := os.Stat(briefPath); err != nil {
			t.Fatalf("rename (live=%v) removed the brief file: %v", live, err)
		}
		recs, err := agentstore.Load(agentstore.FilePath(home))
		if err != nil {
			t.Fatalf("loading agentstore: %v", err)
		}
		if recs["leo-new"].OpeningBriefPath != briefPath {
			t.Errorf("rename (live=%v): OpeningBriefPath = %q, want %q", live, recs["leo-new"].OpeningBriefPath, briefPath)
		}
	}
}

// TestRestartDoesNotReplayOpeningPromptBrief confirms the existing
// "restart/resume never resend the opening prompt" invariant: a restart's
// re-resolved ClaudeArgs must carry neither the literal prompt nor a
// leftover OpeningBriefPath, and the stored record's OpeningBriefPath is
// cleared too (so a LATER Reset — after this restart — doesn't try to replay
// a prompt whose $(cat ...) word the rebuilt ClaudeArgs no longer expects).
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
	if sup.spawnCall.OpeningBriefPath != "" {
		t.Fatalf("restart must not replay OpeningBriefPath, got %q", sup.spawnCall.OpeningBriefPath)
	}
	for _, a := range sup.spawnCall.ClaudeArgs {
		if strings.Contains(a, "hello") {
			t.Fatalf("restart must not replay the literal opening prompt, got %v", sup.spawnCall.ClaudeArgs)
		}
	}

	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	if recs[rec.Name].OpeningBriefPath != "" {
		t.Errorf("stored OpeningBriefPath should be cleared after restart, got %q", recs[rec.Name].OpeningBriefPath)
	}
}

// TestSweepOpeningPromptBriefsRemovesOrphanKeepsReferenced mirrors
// TestSweepSettingsSpillsRemovesOrphanAgentFiles for the sibling brief-file
// mechanism: a file no agentstore record's OpeningBriefPath references is
// removed; one that is still referenced is kept.
func TestSweepOpeningPromptBriefsRemovesOrphanKeepsReferenced(t *testing.T) {
	home := t.TempDir()
	referenced := claudeharness.AgentBriefPath(home, "leo-live")
	orphan := claudeharness.AgentBriefPath(home, "leo-gone")
	if err := claudeharness.WritePrivateBrief(referenced, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := claudeharness.WritePrivateBrief(orphan, "stale"); err != nil {
		t.Fatal(err)
	}
	if err := agentstore.Save(home, agentstore.Record{Name: "leo-live", Workspace: "/w", OpeningBriefPath: referenced}); err != nil {
		t.Fatal(err)
	}

	SweepOpeningPromptBriefs(home)

	if _, err := os.Stat(referenced); err != nil {
		t.Errorf("sweep removed a still-referenced brief file: %v", err)
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Errorf("sweep left an orphan brief file in place: err=%v", err)
	}
}
