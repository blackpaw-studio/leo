package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/session"
)

// switchCfg builds a config with a claude template ("coding"), a second claude
// template ("review"), and a codex template ("codex") — the three shapes a
// switch has to handle: same-harness, cross-harness, and back.
func switchCfg(home string) *config.Config {
	return &config.Config{
		HomePath: home,
		Templates: map[string]config.TemplateConfig{
			"coding": {Model: "sonnet"},
			"review": {Model: "opus"},
			"codex":  {Harness: "codex"},
		},
	}
}

// writeTranscriptBeforeSwitch plants a claude transcript dated two hours ago and
// returns a switch time one hour ago, so the transcript belongs to the template
// the agent left rather than the one it arrived at.
func writeTranscriptBeforeSwitch(t *testing.T, projDir, name string) time.Time {
	t.Helper()
	path := filepath.Join(projDir, name)
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return time.Now().Add(-time.Hour)
}

func loadRec(t *testing.T, home, name string) agentstore.Record {
	t.Helper()
	recs, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatalf("loading agentstore: %v", err)
	}
	rec, ok := recs[name]
	if !ok {
		t.Fatalf("no record for %q", name)
	}
	return rec
}

// TestSwitchTemplateRunningStopsAndRespawns: the core contract. A running
// agent is stopped and respawned on the target template, with the record
// re-pointed at that template's harness and wiring.
func TestSwitchTemplateRunningStopsAndRespawns(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Harness:    "claude",
		Workspace:  "/w",
		SessionID:  "coding-session",
		ClaudeArgs: []string{"--model", "sonnet", "--session-id", "coding-session"},
	})

	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")
	res, err := m.SwitchTemplate("leo-x", "codex")
	if err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}

	if res.FromTemplate != "coding" || res.ToTemplate != "codex" {
		t.Errorf("templates = %q → %q, want coding → codex", res.FromTemplate, res.ToTemplate)
	}
	if res.FromHarness != "claude" || res.ToHarness != "codex" {
		t.Errorf("harnesses = %q → %q, want claude → codex", res.FromHarness, res.ToHarness)
	}
	if res.Resumed {
		t.Error("Resumed = true, want false — codex has no archived session yet")
	}
	if len(sup.stopCalls) != 1 || sup.stopCalls[0] != "leo-x" {
		t.Errorf("stopCalls = %v, want one stop of leo-x", sup.stopCalls)
	}
	if sup.spawnCall == nil {
		t.Fatal("SpawnAgent was not called")
	}
	if sup.spawnCall.Harness != "codex" {
		t.Errorf("spawned Harness = %q, want codex", sup.spawnCall.Harness)
	}
	// The old template's claude wiring must not survive into a codex spawn.
	if containsFlag(sup.spawnCall.ClaudeArgs, "--session-id") {
		t.Errorf("codex spawn carried claude's --session-id: %v", sup.spawnCall.ClaudeArgs)
	}

	rec := loadRec(t, home, "leo-x")
	if rec.Template != "codex" || rec.Harness != "codex" {
		t.Errorf("record = template %q harness %q, want codex/codex", rec.Template, rec.Harness)
	}
	if rec.SessionsByTemplate["coding"] != "coding-session" {
		t.Errorf("archive[coding] = %q, want coding-session", rec.SessionsByTemplate["coding"])
	}
	if rec.SessionPinnedAt == nil {
		t.Error("no switch pin recorded — the next resume would take the other template's transcript")
	}
	if rec.Workspace != "/w" {
		t.Errorf("Workspace = %q, want /w — a switch never relocates the agent", rec.Workspace)
	}
}

// TestSwitchTemplateRoundTripRestoresSession is the feature's whole point:
// A → B → A hands A back its own conversation.
func TestSwitchTemplateRoundTripRestoresSession(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name:       "leo-x",
		Template:   "coding",
		Harness:    "claude",
		Workspace:  "/w",
		SessionID:  "coding-session",
		ClaudeArgs: []string{"--model", "sonnet"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "codex"); err != nil {
		t.Fatalf("switch to codex: %v", err)
	}
	// Pretend codex discovered its own session id post-hoc, as its driver does.
	rec := loadRec(t, home, "leo-x")
	rec.SessionID = "codex-rollout"
	_ = agentstore.Save(home, rec)

	res, err := m.SwitchTemplate("leo-x", "coding")
	if err != nil {
		t.Fatalf("switch back to coding: %v", err)
	}
	if !res.Resumed {
		t.Error("Resumed = false, want true — coding's archived session should come back")
	}

	rec = loadRec(t, home, "leo-x")
	if rec.SessionID != "coding-session" {
		t.Errorf("SessionID = %q, want coding-session restored from the archive", rec.SessionID)
	}
	if rec.SessionsByTemplate["codex"] != "codex-rollout" {
		t.Errorf("archive[codex] = %q, want codex-rollout", rec.SessionsByTemplate["codex"])
	}
	if _, still := rec.SessionsByTemplate["coding"]; still {
		t.Error("coding must be popped out of the archive once it is the active template")
	}
	if !containsPair(sup.spawnCall.ClaudeArgs, "--resume", "coding-session") {
		t.Errorf("claude args = %v, want --resume coding-session", sup.spawnCall.ClaudeArgs)
	}
}

// TestSwitchTemplateFirstVisitMintsClaudeSession: with nothing archived for the
// target, claude gets a brand-new --session-id (matching a fresh spawn), while
// a non-claude target is left empty for its driver's post-hoc discovery.
func TestSwitchTemplateFirstVisitMintsClaudeSession(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "codex", Harness: "codex", Workspace: "/w", SessionID: "codex-rollout",
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "review"); err != nil {
		t.Fatalf("switch: %v", err)
	}
	args := sup.spawnCall.ClaudeArgs
	if !containsFlag(args, "--session-id") {
		t.Errorf("args = %v, want a freshly minted --session-id for a first visit to a claude template", args)
	}
	if containsFlag(args, "--resume") {
		t.Errorf("args = %v, must not resume anything on a first visit", args)
	}
	rec := loadRec(t, home, "leo-x")
	if rec.SessionID == "" || rec.SessionID == "codex-rollout" {
		t.Errorf("SessionID = %q, want the newly minted claude session id", rec.SessionID)
	}
}

// TestSwitchTemplateSuspendedRewritesRecordOnly: a suspended agent has no
// process to bounce. The switch rewrites its record so the next resume comes up
// on the new template.
func TestSwitchTemplateSuspendedRewritesRecordOnly(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w",
		SessionID: "coding-session", Suspended: true,
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	res, err := m.SwitchTemplate("leo-x", "codex")
	if err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if res.Status != "suspended" {
		t.Errorf("Status = %q, want suspended", res.Status)
	}
	if sup.spawnCall != nil || len(sup.stopCalls) != 0 {
		t.Errorf("suspended switch touched the supervisor: spawn=%v stops=%v", sup.spawnCall, sup.stopCalls)
	}
	rec := loadRec(t, home, "leo-x")
	if !rec.Suspended {
		t.Error("Suspended flag must survive a switch")
	}
	if rec.Template != "codex" || rec.SessionsByTemplate["coding"] != "coding-session" {
		t.Errorf("record not re-pointed: template=%q archive=%v", rec.Template, rec.SessionsByTemplate)
	}
}

func TestSwitchTemplateGuards(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	cfg.Tasks = map[string]config.TaskConfig{
		"nightly": {Runtime: "persistent", Template: "review", Schedule: "0 3 * * *"},
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{
		"leo-x":  {Name: "leo-x", Status: "running"},
		"review": {Name: "review", Status: "running"},
	}}
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w"})
	_ = agentstore.Save(home, agentstore.Record{Name: "review", Template: "review", Harness: "claude", Workspace: "/w"})
	_ = agentstore.Save(home, agentstore.Record{Name: "leo-stopped", Template: "coding", Harness: "claude", Workspace: "/w", Stopped: true})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	t.Run("unknown template", func(t *testing.T) {
		if _, err := m.SwitchTemplate("leo-x", "nope"); err == nil {
			t.Fatal("switching to an undefined template must error")
		}
	})
	t.Run("unknown agent", func(t *testing.T) {
		if _, err := m.SwitchTemplate("ghost", "codex"); err == nil {
			t.Fatal("switching an agent with no record must error")
		}
	})
	t.Run("stopped agent", func(t *testing.T) {
		if _, err := m.SwitchTemplate("leo-stopped", "codex"); err == nil {
			t.Fatal("switching a stopped agent must error")
		}
	})
	t.Run("same template is a no-op", func(t *testing.T) {
		res, err := m.SwitchTemplate("leo-x", "coding")
		if err != nil {
			t.Fatalf("same-template switch should not error: %v", err)
		}
		if !res.Unchanged {
			t.Error("Unchanged = false, want true")
		}
		if len(sup.stopCalls) != 0 {
			t.Errorf("same-template switch bounced the agent: %v", sup.stopCalls)
		}
	})
	t.Run("persistent task target", func(t *testing.T) {
		if _, err := m.SwitchTemplate("review", "codex"); err == nil {
			t.Fatal("switching an agent that backs a persistent task must error")
		}
	})
}

// TestSwitchTemplateReResolvesEnvFromNewTemplate: the target template's env and
// permissions apply, not the departing template's.
func TestSwitchTemplateReResolvesEnvFromNewTemplate(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	cfg.Templates["review"] = config.TemplateConfig{
		Model: "opus",
		Env:   map[string]string{"REVIEW_ONLY": "1"},
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w",
		Env:      map[string]string{"CODING_ONLY": "1"},
		SpawnEnv: map[string]string{"CALLER": "keepme"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "review"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	env := sup.spawnCall.Env
	if env["REVIEW_ONLY"] != "1" {
		t.Errorf("env missing the new template's REVIEW_ONLY: %v", env)
	}
	if _, leaked := env["CODING_ONLY"]; leaked {
		t.Errorf("env leaked the departing template's CODING_ONLY: %v", env)
	}
	if env["CALLER"] != "keepme" {
		t.Errorf("caller-supplied SpawnEnv must survive a switch: %v", env)
	}
}

// TestSwitchTemplateFailsWhenTargetWiringUnbuildable: if the target template
// cannot produce launch args, the switch must fail loudly rather than fall back
// to the departing template's args — which would respawn the agent under the
// wrong wiring while the record claims the new template.
func TestSwitchTemplateFailsWhenTargetWiringUnbuildable(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	// A harness with no registered adapter: config validation would normally
	// reject it, but the manager must not respawn the agent on the departing
	// template's args when the target cannot produce any.
	cfg.Templates["broken"] = config.TemplateConfig{Harness: "no-such-harness"}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w",
		ClaudeArgs: []string{"--model", "sonnet"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "broken"); err == nil {
		t.Fatal("expected an error when the target template's wiring cannot be built")
	}
	rec := loadRec(t, home, "leo-x")
	if rec.Template != "coding" {
		t.Errorf("record moved to %q despite a failed switch, want coding", rec.Template)
	}
}

// TestRestartHonorsAndClearsSessionPinned: a pinned record must resume exactly
// the archived session, ignoring the newest transcript in the workspace — which
// after a switch belongs to the OTHER template. The flag is one-shot.
func TestRestartHonorsAndClearsSessionPinned(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })
	// The other template's transcript: newest in the workspace, but written
	// before the switch, so the pin outranks it.
	switchedAt := writeTranscriptBeforeSwitch(t, projDir, "other-template-session.jsonl")

	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: workspace,
		SessionID: "mine", SessionPinnedAt: &switchedAt, ClaudeArgs: []string{"--model", "sonnet"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if err := m.Restart("leo-x"); err != nil {
		t.Fatalf("Restart: %v", err)
	}
	if !containsPair(sup.spawnCall.ClaudeArgs, "--resume", "mine") {
		t.Errorf("args = %v, want --resume mine (the pinned session), not the newest jsonl", sup.spawnCall.ClaudeArgs)
	}
	rec := loadRec(t, home, "leo-x")
	if rec.SessionPinnedAt != nil {
		t.Error("the switch pin must be cleared once consumed")
	}
	if rec.SessionID != "mine" {
		t.Errorf("SessionID = %q, want mine", rec.SessionID)
	}
}

// TestResumeHonorsAndClearsSessionPinned: same contract on the suspend/resume
// path, which is how a switched-while-suspended agent comes back.
func TestResumeHonorsAndClearsSessionPinned(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })
	switchedAt := writeTranscriptBeforeSwitch(t, projDir, "other-template-session.jsonl")

	cfg := switchCfg(home)
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: workspace,
		SessionID: "mine", SessionPinnedAt: &switchedAt, Suspended: true, ClaudeArgs: []string{"--model", "sonnet"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.Resume("leo-x"); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if !containsPair(sup.spawnCall.ClaudeArgs, "--resume", "mine") {
		t.Errorf("args = %v, want --resume mine (the pinned session)", sup.spawnCall.ClaudeArgs)
	}
	if loadRec(t, home, "leo-x").SessionPinnedAt != nil {
		t.Error("the switch pin must be cleared once consumed")
	}
}

// A suspended agent that never had a session on the template it is leaving must
// still get back the session archived for the template it is arriving at. The
// two are independent: what the departing template was doing says nothing about
// whether the arriving one has a conversation waiting.
func TestSwitchTemplateSuspendedRestoresArchivedSessionRegardlessOfDepartingOne(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "codex", Harness: "codex", Workspace: "/w",
		SessionID:          "", // codex never reported a session id
		SessionsByTemplate: map[string]string{"coding": "codings-session"},
		Suspended:          true,
		ClaudeArgs:         []string{"--model", "sonnet"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	res, err := m.SwitchTemplate("leo-x", "coding")
	if err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if !res.Resumed {
		t.Error("Resumed = false, want true — coding has an archived session")
	}
	rec := loadRec(t, home, "leo-x")
	if rec.SessionID != "codings-session" {
		t.Errorf("SessionID = %q, want codings-session", rec.SessionID)
	}
	if !containsPair(rec.ClaudeArgs, "--resume", "codings-session") {
		t.Errorf("stored args = %v, want --resume codings-session so the next resume rejoins it", rec.ClaudeArgs)
	}
}

// The mirror case: with nothing archived for the arriving template, a suspended
// agent must not be left holding a --session-id for a conversation that was
// never created — Resume would try to rejoin a session that does not exist.
func TestSwitchTemplateSuspendedDoesNotPersistAMintedSession(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "codex", Harness: "codex", Workspace: "/w",
		SessionID: "codex-rollout", Suspended: true,
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "coding"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	rec := loadRec(t, home, "leo-x")
	if rec.SessionID != "" {
		t.Errorf("SessionID = %q, want empty — nothing was archived for coding and no session has been created yet", rec.SessionID)
	}
	if containsFlag(rec.ClaudeArgs, "--session-id") || containsFlag(rec.ClaudeArgs, "--resume") {
		t.Errorf("stored args = %v, want no session-selection flag", rec.ClaudeArgs)
	}
}

// The arriving template's env must replace the departing template's, on an
// ordinary agent — one spawned with no --env, so the record carries neither
// SpawnEnv nor InheritedEnv. That is the common shape, and it is exactly the
// shape that took resolveTemplateWiring's preserve-what-we-cannot-attribute
// branch: correct for a restart onto the SAME template, wrong for a switch,
// where it layered the departing template's env (proxy endpoints, auth tokens)
// on top of the new one and dropped the new one's own keys entirely.
func TestSwitchTemplateRebuildsEnvOnAnOrdinaryAgent(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	cfg.Templates["proxied"] = config.TemplateConfig{
		Env: map[string]string{"ANTHROPIC_BASE_URL": "https://proxy", "ANTHROPIC_AUTH_TOKEN": "secret"},
	}
	cfg.Templates["plain"] = config.TemplateConfig{
		Env: map[string]string{"PLAIN_ONLY": "1"},
	}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "proxied", Harness: "claude", Workspace: "/w",
		// No SpawnEnv, no InheritedEnv: a plain `leo agent spawn proxied`.
		Env: map[string]string{"ANTHROPIC_BASE_URL": "https://proxy", "ANTHROPIC_AUTH_TOKEN": "secret"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "plain"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	env := sup.spawnCall.Env
	if env["PLAIN_ONLY"] != "1" {
		t.Errorf("arriving template's env missing: %v", env)
	}
	for _, leaked := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN"} {
		if _, present := env[leaked]; present {
			t.Errorf("departing template's %s followed the agent onto the new template: %v", leaked, env)
		}
	}
}

// Archiving must file away the session the agent is actually in, not the id the
// store last wrote down. A /clear starts a session leo never saw; switching away
// after one used to archive the pre-/clear conversation, so switching back
// resurrected the wrong thread and orphaned the real one.
func TestSwitchTemplateArchivesThePostClearSession(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	home := t.TempDir()
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })
	if err := os.WriteFile(filepath.Join(projDir, "after-clear.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: workspace,
		SessionID: "before-clear",
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "codex"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if got := loadRec(t, home, "leo-x").SessionsByTemplate["coding"]; got != "after-clear" {
		t.Errorf("archive[coding] = %q, want after-clear — the conversation the agent was actually in", got)
	}
}

// The pin exists to stop the newest-transcript preference from handing back the
// DEPARTING template's conversation in the window before the arriving one has
// written anything. It must not outlive that window: once a transcript exists
// that postdates the switch, it belongs to the template the agent is on now —
// including a /clear session started hours later — and it wins.
func TestResumeIDForPinExpiresOnceTheNewTemplateWrites(t *testing.T) {
	userHome, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	workspace := filepath.Join(t.TempDir(), "agent-ws")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}
	projDir := filepath.Join(userHome, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })

	transcript := filepath.Join(projDir, "written-after-the-switch.jsonl")
	if err := os.WriteFile(transcript, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write jsonl: %v", err)
	}

	switchedAt := time.Now().Add(-time.Hour)
	rec := agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: workspace,
		SessionID: "restored-at-switch-time", SessionPinnedAt: &switchedAt,
	}
	if got := ResumeIDFor(rec); got != "written-after-the-switch" {
		t.Errorf("ResumeIDFor = %q, want written-after-the-switch (the transcript postdates the switch)", got)
	}

	// The same record, with the only transcript predating the switch: that one
	// belongs to the template just left, so the pin still wins.
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(transcript, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	if got := ResumeIDFor(rec); got != "restored-at-switch-time" {
		t.Errorf("ResumeIDFor = %q, want the pinned id when the only transcript predates the switch", got)
	}
}

// Idle-suspend is template-cascade config, so the record's stored interval
// describes the template the agent left. Left alone, an agent switched off a
// template with idle_suspend_after would keep suspending on that schedule
// forever — the sweep reads the interval straight off the record — and one
// switched onto such a template would never start.
func TestSwitchTemplateReResolvesIdleSuspend(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	cfg.Templates["napper"] = config.TemplateConfig{IdleSuspendAfter: "30m"}
	cfg.Templates["always-on"] = config.TemplateConfig{}
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "napper", Harness: "claude", Workspace: "/w",
		IdleSuspendAfter: "30m",
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "always-on"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if got := loadRec(t, home, "leo-x").IdleSuspendAfter; got != "" {
		t.Errorf("IdleSuspendAfter = %q, want empty — always-on sets no idle suspend", got)
	}

	if _, err := m.SwitchTemplate("leo-x", "napper"); err != nil {
		t.Fatalf("SwitchTemplate back: %v", err)
	}
	// Normalized through time.Duration.String(), exactly as a fresh spawn
	// stores it (see spawnShared).
	if got := loadRec(t, home, "leo-x").IdleSuspendAfter; got != "30m0s" {
		t.Errorf("IdleSuspendAfter = %q, want 30m0s from the arriving template", got)
	}
}

// The supervisor reads the agent's session id off the agentstore record when it
// launches a non-claude harness (RefreshSessionArgs, internal/service/process.go),
// so the record has to already name the archived session at the moment
// SpawnAgent is called. Clearing it across the spawn meant codex and opencode
// silently started a brand-new conversation, and post-hoc discovery then
// overwrote the id — leaving the archived one unreachable, since the switch had
// already popped it out of SessionsByTemplate.
func TestSwitchTemplateHandsTheSupervisorTheArchivedSession(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}

	var sessionAtSpawn string
	sup.onSpawn = func(SpawnRequest) {
		recs, _ := agentstore.Load(agentstore.FilePath(home))
		sessionAtSpawn = recs["leo-x"].SessionID
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w",
		SessionID:          "codings-session",
		SessionsByTemplate: map[string]string{"codex": "codex-rollout"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "codex"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	if sessionAtSpawn != "codex-rollout" {
		t.Errorf("record held SessionID %q when the supervisor launched, want codex-rollout", sessionAtSpawn)
	}
}

// A respawn that fails must not take the arriving template's archived session
// with it: the switch has already popped that id out of the archive, so if the
// record does not carry it the conversation is gone for good. The agent is left
// suspended so the documented recovery actually works — it is not live, so a
// retry of the switch (or a restart) cannot touch it.
func TestSwitchTemplateFailedRespawnKeepsTheSessionAndStaysRecoverable(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{
		agents:   map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}},
		spawnErr: errors.New("tmux: no server running"),
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w",
		SessionID:          "codings-session",
		SessionsByTemplate: map[string]string{"codex": "codex-rollout"},
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "codex"); err == nil {
		t.Fatal("expected the spawn failure to surface")
	}

	rec := loadRec(t, home, "leo-x")
	if rec.SessionID != "codex-rollout" {
		t.Errorf("SessionID = %q after a failed respawn, want codex-rollout — otherwise that conversation is unreachable", rec.SessionID)
	}
	if !rec.Suspended {
		t.Error("a failed respawn must leave the agent suspended, or nothing can bring it back but reset")
	}
	if rec.SessionsByTemplate["coding"] != "codings-session" {
		t.Errorf("archive = %v, want the departing session still filed", rec.SessionsByTemplate)
	}

	// The documented recovery path has to actually work once whatever broke the
	// spawn (here: no tmux server) is fixed, and it has to come back on the
	// archived conversation. codex takes its resume token from the record at
	// launch rather than from argv, so that is what the assertion reads.
	sup.spawnErr = nil
	var sessionAtRespawn string
	sup.onSpawn = func(SpawnRequest) {
		recs, _ := agentstore.Load(agentstore.FilePath(home))
		sessionAtRespawn = recs["leo-x"].SessionID
	}
	if _, err := m.Resume("leo-x"); err != nil {
		t.Fatalf("resume after a failed switch: %v", err)
	}
	if sessionAtRespawn != "codex-rollout" {
		t.Errorf("record held SessionID %q when the supervisor relaunched, want codex-rollout", sessionAtRespawn)
	}
}

// The pin has to be stamped after the departing process is gone. Stamped
// before, a claude flushing its transcript on the way down lands an mtime after
// the pin, and ResumeIDFor would then prefer the conversation the user just
// switched away from — the precise thing the pin exists to prevent.
func TestSwitchTemplateStampsThePinAfterStopping(t *testing.T) {
	home := t.TempDir()
	cfg := switchCfg(home)
	sup := &capturingSupervisor{agents: map[string]ProcessState{"leo-x": {Name: "leo-x", Status: "running"}}}
	var stoppedAt time.Time
	sup.onStop = func(string) {
		time.Sleep(2 * time.Millisecond) // the departing process's last writes
		stoppedAt = time.Now()
	}
	_ = agentstore.Save(home, agentstore.Record{
		Name: "leo-x", Template: "coding", Harness: "claude", Workspace: "/w", SessionID: "codings-session",
	})
	m := New(func() (*config.Config, error) { return cfg, nil }, sup, "", "tok")

	if _, err := m.SwitchTemplate("leo-x", "codex"); err != nil {
		t.Fatalf("SwitchTemplate: %v", err)
	}
	pinnedAt := loadRec(t, home, "leo-x").SessionPinnedAt
	if pinnedAt == nil {
		t.Fatal("no pin recorded")
	}
	if pinnedAt.Before(stoppedAt) {
		t.Errorf("pin stamped at %v, before the departing process stopped at %v", pinnedAt, stoppedAt)
	}
}
