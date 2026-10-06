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

// briefPathForTest is the test-only helper mirroring how the supervisor
// derives a brief's path from its id — tests never fabricate a path
// directly, matching how nothing else in the codebase is allowed to either.
func briefPathForTest(t *testing.T, home, id string) string {
	t.Helper()
	path, err := claudeharness.AgentBriefPathForID(home, id)
	if err != nil {
		t.Fatalf("AgentBriefPathForID(%q): %v", id, err)
	}
	return path
}

// TestSpawnOpeningPromptDeliveredViaBriefFile locks the tmux-16KiB-command-
// limit fix (see claudeharness.ArgvPromptLimit): a claude ephemeral agent's
// opening prompt must never appear as literal launch-argv text in ClaudeArgs.
// Instead it is written to a private brief file keyed by a random id, and
// that id is carried on the typed SpawnRequest/agentstore.Record.
// OpeningBriefID field — never a path, and never inside ClaudeArgs, which
// stays fully shell-quotable with no exceptions.
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

	id := sup.spawnCall.OpeningBriefID
	if !claudeharness.ValidAgentBriefID(id) {
		t.Fatalf("SpawnRequest.OpeningBriefID = %q, not a valid brief id", id)
	}
	briefPath := briefPathForTest(t, home, id)

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

	// The persisted agentstore record must carry the id on its own typed
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
	if stored.OpeningBriefID != id {
		t.Errorf("stored OpeningBriefID = %q, want %q", stored.OpeningBriefID, id)
	}
}

// TestSpawnNoPromptWritesNoBriefFile ensures the brief-file mechanism is
// opt-in: a spawn with no opening prompt must not create any brief file or
// set OpeningBriefID.
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

	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo"}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if sup.spawnCall.OpeningBriefID != "" {
		t.Fatalf("expected empty OpeningBriefID with no prompt, got %q", sup.spawnCall.OpeningBriefID)
	}
	matches, _ := os.ReadDir(home + "/state/" + claudeharness.AgentBriefSpillDir)
	if len(matches) != 0 {
		t.Fatalf("expected no brief files, found %d", len(matches))
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
	matches, _ := os.ReadDir(home + "/state/" + claudeharness.AgentBriefSpillDir)
	if len(matches) != 0 {
		t.Fatalf("no brief file should be left behind on rejection, found %d", len(matches))
	}
}

// TestStopKeepsOpeningPromptBriefFile confirms Stop no longer deletes the
// brief file: Reset replays a record's OpeningBriefID verbatim, so a
// stop-then-reset must still find it (this is the MEDIUM finding from the
// first security review of this fix — Stop used to delete the file out from
// under a later Reset).
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
	briefPath := briefPathForTest(t, home, sup.spawnCall.OpeningBriefID)
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
// replays the original ClaudeArgs/OpeningBriefID rather than rebuilding them
// promptless.
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
	id := sup.spawnCall.OpeningBriefID
	briefPath := briefPathForTest(t, home, id)

	if err := m.Stop(rec.Name, StopOptions{}); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := m.Reset(rec.Name); err != nil {
		t.Fatalf("reset: %v", err)
	}
	if sup.spawnCall.OpeningBriefID != id {
		t.Fatalf("reset after stop: OpeningBriefID = %q, want %q", sup.spawnCall.OpeningBriefID, id)
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
	briefPath := briefPathForTest(t, home, sup.spawnCall.OpeningBriefID)

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

// TestDeleteRejectsInvalidOpeningBriefID confirms Delete never touches the
// filesystem for a corrupted/hand-edited record whose OpeningBriefID isn't a
// well-formed id — the whole point of validating before any os.Remove call.
func TestDeleteRejectsInvalidOpeningBriefID(t *testing.T) {
	home := t.TempDir()
	// A file that would be deleted if id validation were skipped and the
	// "path" were built by naive concatenation from a traversal id.
	if err := os.MkdirAll(home+"/state/agent-briefs", 0o700); err != nil {
		t.Fatal(err)
	}
	canary := home + "/canary.txt"
	if err := os.WriteFile(canary, []byte("do not delete me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := agentstore.Save(home, agentstore.Record{
		Name:           "leo-tainted",
		Workspace:      home,
		OpeningBriefID: "../../canary",
	}); err != nil {
		t.Fatal(err)
	}
	sup := &fakeSupervisor{ephemeral: map[string]ProcessState{}}
	m := newTestManager(t, home, sup)

	if err := m.Delete(context.Background(), "leo-tainted", DeleteOptions{}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := os.Stat(canary); err != nil {
		t.Fatalf("delete touched a file outside the agent-briefs dir via an invalid id: %v", err)
	}
}

// TestRenameKeepsOldOpeningPromptBriefFile confirms a rename (live or not)
// never deletes or needs to move the brief file: the id (and
// OpeningBriefID) travels with the record regardless of name, so persistRename
// leaving it untouched is already correct.
func TestRenameKeepsOldOpeningPromptBriefFile(t *testing.T) {
	for _, live := range []bool{false, true} {
		home := t.TempDir()
		id, err := claudeharness.GenerateAgentBriefID()
		if err != nil {
			t.Fatal(err)
		}
		briefPath := briefPathForTest(t, home, id)
		if err := claudeharness.WritePrivateBrief(briefPath, "hello"); err != nil {
			t.Fatal(err)
		}
		_ = agentstore.Save(home, agentstore.Record{
			Name:           "leo-old",
			Workspace:      "/w",
			Stopped:        !live,
			ClaudeArgs:     []string{"--name", "leo-old"},
			OpeningBriefID: id,
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
		if recs["leo-new"].OpeningBriefID != id {
			t.Errorf("rename (live=%v): OpeningBriefID = %q, want %q", live, recs["leo-new"].OpeningBriefID, id)
		}
	}
}

// TestRenameThenNewAgentUnderFreedNameKeepsSeparateBriefs is the direct
// regression test for the collision this redesign fixes: a name-derived
// brief id would let a brand-new agent spawned under a freed name silently
// share (and overwrite) the previous occupant's brief file. A random id
// cannot collide.
func TestRenameThenNewAgentUnderFreedNameKeepsSeparateBriefs(t *testing.T) {
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

	first, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Name: "shared", Prompt: "first agent's prompt"})
	if err != nil {
		t.Fatalf("spawn first: %v", err)
	}
	firstID := sup.spawnCall.OpeningBriefID
	firstBriefPath := briefPathForTest(t, home, firstID)

	if _, err := m.Rename(first.Name, "renamed-away"); err != nil {
		t.Fatalf("rename: %v", err)
	}

	// A brand-new agent spawned under the now-freed original name.
	second, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Name: "shared", Prompt: "second agent's prompt"})
	if err != nil {
		t.Fatalf("spawn second: %v", err)
	}
	secondID := sup.spawnCall.OpeningBriefID
	if secondID == firstID {
		t.Fatalf("second agent's OpeningBriefID collided with the first: %q", secondID)
	}
	secondBriefPath := briefPathForTest(t, home, secondID)
	if secondBriefPath == firstBriefPath {
		t.Fatalf("second agent's brief path collided with the first: %q", secondBriefPath)
	}

	firstContent, err := os.ReadFile(firstBriefPath)
	if err != nil {
		t.Fatalf("reading first agent's brief: %v", err)
	}
	if string(firstContent) != "first agent's prompt" {
		t.Errorf("first agent's brief was overwritten: got %q", firstContent)
	}
	secondContent, err := os.ReadFile(secondBriefPath)
	if err != nil {
		t.Fatalf("reading second agent's brief: %v", err)
	}
	if string(secondContent) != "second agent's prompt" {
		t.Errorf("second agent's brief content = %q", secondContent)
	}
	_ = second
}

// TestRestartDoesNotReplayOpeningPromptBrief confirms the existing
// "restart/resume never resend the opening prompt" invariant: a restart's
// re-resolved ClaudeArgs must carry neither the literal prompt nor a
// leftover OpeningBriefID, and the stored record's OpeningBriefID is cleared
// too (so a LATER Reset — after this restart — doesn't try to replay a
// prompt the rebuilt ClaudeArgs no longer expects).
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
	if sup.spawnCall.OpeningBriefID != "" {
		t.Fatalf("restart must not replay OpeningBriefID, got %q", sup.spawnCall.OpeningBriefID)
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
	if recs[rec.Name].OpeningBriefID != "" {
		t.Errorf("stored OpeningBriefID should be cleared after restart, got %q", recs[rec.Name].OpeningBriefID)
	}
}

// TestSwitchTemplateClearsOpeningBriefID is the regression test for the
// third security review's finding: SwitchTemplate rebuilds ClaudeArgs
// promptless for the arriving template (same as Start/Restart) but, unlike
// them, left the record's OpeningBriefID untouched — next started as a copy
// of rec (see withTemplate). A later RestoreAgents forwards whatever id the
// record carries verbatim, so a stale id would replay the DEPARTING
// template's opening prompt into the arriving one, possibly on a different
// harness entirely. Simulates "live switch, then restore" end to end: after
// a live switch, the persisted record must carry neither a brief id nor any
// argv referencing one, so a restore built straight from that record (the
// same rec.OpeningBriefID a real RestoreAgents would forward — see
// internal/service/agents.go) has nothing to replay.
func TestSwitchTemplateClearsOpeningBriefID(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	id, err := claudeharness.GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	briefPath := briefPathForTest(t, home, id)
	if err := claudeharness.WritePrivateBrief(briefPath, "the departing template's opening prompt"); err != nil {
		t.Fatal(err)
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name:           "leo-x",
		Template:       "coding",
		Harness:        "claude",
		Workspace:      "/w",
		SessionID:      "coding-session",
		ClaudeArgs:     []string{"--model", "sonnet", "--session-id", "coding-session"},
		OpeningBriefID: id,
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	if _, err := m.SwitchTemplate("leo-x", "review"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}

	// The live respawn itself must not carry the departing template's brief.
	if sup.spawnCall.OpeningBriefID != "" {
		t.Fatalf("switch spawn carried a stale OpeningBriefID: %q", sup.spawnCall.OpeningBriefID)
	}
	for _, a := range sup.spawnCall.ClaudeArgs {
		if strings.Contains(a, "departing template's opening prompt") {
			t.Fatalf("switch spawn's ClaudeArgs replayed the departing prompt: %v", sup.spawnCall.ClaudeArgs)
		}
	}

	// "Restore" == whatever RestoreAgents would forward from the persisted
	// record: rec.OpeningBriefID verbatim (see internal/service/agents.go).
	// It must be empty, or a boot-time restore would replay the stale prompt.
	rec := loadRec(t, home, "leo-x")
	if rec.OpeningBriefID != "" {
		t.Fatalf("stored OpeningBriefID after switch = %q, want empty (a restore would replay it)", rec.OpeningBriefID)
	}

	// The now-orphaned file is swept, not left behind forever.
	SweepOpeningPromptBriefs(home)
	if _, err := os.Stat(briefPath); !os.IsNotExist(err) {
		t.Errorf("orphaned brief file survived the sweep: stat err = %v", err)
	}
}

// TestSweepOpeningPromptBriefsRemovesOrphanKeepsReferenced mirrors
// TestSweepSettingsSpillsRemovesOrphanAgentFiles for the sibling brief-file
// mechanism: a file no agentstore record's OpeningBriefID references is
// removed; one that is still referenced is kept; a symlink is removed
// regardless of whether its name matches a referenced id; an invalid-name
// entry is left alone.
func TestSweepOpeningPromptBriefsRemovesOrphanKeepsReferenced(t *testing.T) {
	home := t.TempDir()
	referencedID, err := claudeharness.GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	orphanID, err := claudeharness.GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	referenced := briefPathForTest(t, home, referencedID)
	orphan := briefPathForTest(t, home, orphanID)
	if err := claudeharness.WritePrivateBrief(referenced, "hello"); err != nil {
		t.Fatal(err)
	}
	if err := claudeharness.WritePrivateBrief(orphan, "stale"); err != nil {
		t.Fatal(err)
	}

	// A symlink whose NAME matches a referenced id: must still be removed,
	// since WritePrivateBrief never creates symlinks and a real file
	// underneath referenced already covers the legitimate case.
	symlinkID, err := claudeharness.GenerateAgentBriefID()
	if err != nil {
		t.Fatal(err)
	}
	symlinkPath := briefPathForTest(t, home, symlinkID)
	if err := os.Symlink(referenced, symlinkPath); err != nil {
		t.Fatal(err)
	}

	// An invalid-name entry that happens to end in .txt: must be left alone.
	invalidNamePath := home + "/state/" + claudeharness.AgentBriefSpillDir + "/not-a-valid-id.txt"
	if err := os.WriteFile(invalidNamePath, []byte("ignore me"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := agentstore.Save(home, agentstore.Record{Name: "leo-live", Workspace: "/w", OpeningBriefID: referencedID}); err != nil {
		t.Fatal(err)
	}
	// The symlink's own id is also "referenced" by nothing, but symlinks are
	// removed unconditionally regardless of referenced-ness.
	if err := agentstore.Save(home, agentstore.Record{Name: "leo-symlinked", Workspace: "/w", OpeningBriefID: symlinkID}); err != nil {
		t.Fatal(err)
	}

	SweepOpeningPromptBriefs(home)

	if _, err := os.Stat(referenced); err != nil {
		t.Errorf("sweep removed a still-referenced brief file: %v", err)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Errorf("sweep left an orphan brief file in place: err=%v", err)
	}
	if _, err := os.Lstat(symlinkPath); !os.IsNotExist(err) {
		t.Errorf("sweep left a referenced symlink in place: err=%v", err)
	}
	if _, err := os.Stat(invalidNamePath); err != nil {
		t.Errorf("sweep removed an invalid-name entry it should have left alone: %v", err)
	}
}

// When claude launches with the leo bridge, the opening prompt is delivered
// over the bridge rather than argv, so the argv limit no longer applies.
func TestSpawnOversizedPromptAcceptedWhenTheBridgeCarriesIt(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{"t": {Workspace: home}},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetBridgeCapable(func() bool { return true })

	big := strings.Repeat("b", claudeharness.ArgvPromptLimit*2)
	if _, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: big}); err != nil {
		t.Fatalf("spawn: %v", err)
	}
	if sup.spawnCall == nil || sup.spawnCall.OpeningBriefID == "" {
		t.Fatal("no opening brief handed to the supervisor")
	}
	got, err := os.ReadFile(briefPathForTest(t, home, sup.spawnCall.OpeningBriefID))
	if err != nil || string(got) != big {
		t.Fatalf("brief holds %d bytes (err %v), want the whole %d-byte prompt", len(got), err, len(big))
	}
}

// Even over the bridge an opening prompt is bounded.
func TestSpawnRejectsAnOpeningPastTheBridgeLimit(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath:  home,
		Defaults:  config.DefaultsConfig{Model: "sonnet"},
		Templates: map[string]config.TemplateConfig{"t": {Workspace: home}},
	}
	sup := &capturingSupervisor{}
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	m.SetBridgeCapable(func() bool { return true })
	_, err := m.Spawn(context.Background(), SpawnSpec{Template: "t", Repo: "demo", Prompt: strings.Repeat("b", MaxBridgedOpeningBytes+1)})
	if err == nil || sup.spawnCall != nil {
		t.Fatalf("err=%v spawned=%v; want a rejection before spawning", err, sup.spawnCall != nil)
	}
}
