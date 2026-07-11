package service

import (
	"context"
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

// TestSessionSpecsFromConfigExplicitCodexHarness verifies that an explicit
// session pointed at codex resolves Harness and carries the RAW
// harness_options map through untouched — no claude decode happens (a
// codex-shaped options map like {"sandbox": ...} would fail
// claudeSessionOptions, so if this test passes the claude decode path was
// correctly skipped).
func TestSessionSpecsFromConfigExplicitCodexHarness(t *testing.T) {
	cfg := &config.Config{
		Sessions: map[string]config.SessionConfig{
			"chat": {
				Workspace: "/tmp/chat",
				Harness:   "codex",
				HarnessOptions: map[string]any{
					"sandbox": "workspace-write",
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
	spec := specs[0]
	if spec.Harness != "codex" {
		t.Fatalf("Harness = %q, want %q", spec.Harness, "codex")
	}
	if got := spec.HarnessOptions["sandbox"]; got != "workspace-write" {
		t.Fatalf("HarnessOptions[sandbox] = %v, want %v", got, "workspace-write")
	}
	// The claude-only fields must stay zero: no claudeSessionOptions decode
	// was attempted against a codex-shaped options map.
	if spec.PermissionMode != "" || spec.Agent != "" {
		t.Fatalf("expected claude fields to stay zero for a codex session, got %+v", spec)
	}
}

// TestSessionSpecsFromConfigImplicitOpencodeHarness mirrors the explicit
// case above for an implicit (Topology A) session on opencode, and confirms
// the no-cascade quirk still applies: the task's own harness_options reach
// the spec, but defaults.harness_options do not leak in (defaults.harness
// here is claude, a different harness than the task's opencode — the
// cascade gate in cfg.TaskHarnessOptions would already exclude it, but this
// locks the whole path in for the non-claude branch, not just the option
// content).
func TestSessionSpecsFromConfigImplicitOpencodeHarness(t *testing.T) {
	cfg := &config.Config{
		Tasks: map[string]config.TaskConfig{
			"chat": {
				Runtime:   "persistent",
				Workspace: "/tmp/chat",
				Harness:   "opencode",
				HarnessOptions: map[string]any{
					"permission": map[string]any{"bash": "allow"},
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
	spec := specs[0]
	if spec.Harness != "opencode" {
		t.Fatalf("Harness = %q, want %q", spec.Harness, "opencode")
	}
	if spec.HarnessOptions == nil {
		t.Fatalf("expected raw HarnessOptions to reach the implicit spec")
	}
	if spec.PermissionMode != "" {
		t.Fatalf("expected claude fields to stay zero for an opencode session, got %+v", spec)
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

// TestSuperviseSessionCodexRegistersNothing verifies that a codex session has
// no resident tmux process to supervise: SuperviseSession returns
// immediately (nil error) without spawning a restart-loop goroutine. The
// session still exists as stored state + driver dispatch, reached by the
// daemon's harness-aware injector, not by anything this call registers.
func TestSuperviseSessionCodexRegistersNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	spec := SessionSpec{Name: "cx", Workdir: t.TempDir(), Harness: "codex"}
	if err := SuperviseSession(ctx, "unused-tmux-path", "unused-claude-path", spec, t.TempDir(), nil); err != nil {
		t.Fatalf("SuperviseSession(codex) = %v, want nil", err)
	}
}

// TestSuperviseSessionUnknownHarnessErrors guards the default branch: an
// unrecognized harness must fail loudly rather than silently doing nothing.
func TestSuperviseSessionUnknownHarnessErrors(t *testing.T) {
	spec := SessionSpec{Name: "x", Workdir: t.TempDir(), Harness: "bogus"}
	if err := SuperviseSession(context.Background(), "unused", "unused", spec, t.TempDir(), nil); err == nil {
		t.Fatal("expected an error for an unsupported harness")
	}
}

// TestBuildSessionDispatchCodexFillsTurnArgs verifies the codex branch
// decodes the session's raw harness_options and renders TurnArgs via the
// adapter's Args(), and that the handle's routing fields (TmuxSession,
// IDs) are populated.
func TestBuildSessionDispatchCodexFillsTurnArgs(t *testing.T) {
	home := t.TempDir()
	specs := []SessionSpec{
		{
			Name:           "cx",
			Workdir:        "/tmp/cx",
			Model:          "gpt-5.3-codex",
			Harness:        "codex",
			HarnessOptions: map[string]any{"sandbox": "workspace-write"},
		},
	}
	dispatch := BuildSessionDispatch(specs, home)
	d, ok := dispatch[SessionTmuxName("cx")]
	if !ok {
		t.Fatalf("expected a dispatch entry for %q", SessionTmuxName("cx"))
	}
	if d.Harness != "codex" {
		t.Fatalf("Harness = %q, want %q", d.Harness, "codex")
	}
	if d.Handle.IDs == nil {
		t.Fatalf("expected a non-nil IDs store")
	}
	joined := strings.Join(d.Handle.TurnArgs, " ")
	if !strings.Contains(joined, "--sandbox workspace-write") || !strings.Contains(joined, "--model gpt-5.3-codex") {
		t.Fatalf("TurnArgs = %v, want sandbox+model rendered", d.Handle.TurnArgs)
	}
}

// TestBuildSessionDispatchSkipsClaude verifies claude sessions never appear
// in the dispatch table — the injector's default tmux path must handle them
// via a map miss, not a claude branch here.
func TestBuildSessionDispatchSkipsClaude(t *testing.T) {
	specs := []SessionSpec{
		{Name: "chat", Workdir: "/tmp/chat", Harness: "claude"},
		{Name: "old", Workdir: "/tmp/old"}, // Harness == "" (pre-field record)
	}
	dispatch := BuildSessionDispatch(specs, t.TempDir())
	if len(dispatch) != 0 {
		t.Fatalf("expected no dispatch entries for claude sessions, got %+v", dispatch)
	}
}

// TestBuildSessionDispatchOpencodeNoTurnArgs verifies the opencode branch
// builds a dispatch entry without TurnArgs — ServerDriver.Inject resolves
// the server itself via LoadServerState(HomePath, TmuxSession).
func TestBuildSessionDispatchOpencodeNoTurnArgs(t *testing.T) {
	home := t.TempDir()
	specs := []SessionSpec{{Name: "oc", Workdir: "/tmp/oc", Harness: "opencode"}}
	dispatch := BuildSessionDispatch(specs, home)
	d, ok := dispatch[SessionTmuxName("oc")]
	if !ok {
		t.Fatalf("expected a dispatch entry for %q", SessionTmuxName("oc"))
	}
	if len(d.Handle.TurnArgs) != 0 {
		t.Fatalf("expected no TurnArgs for opencode, got %v", d.Handle.TurnArgs)
	}
	if d.Handle.HomePath != home || d.Handle.TmuxSession != SessionTmuxName("oc") {
		t.Fatalf("handle routing fields wrong: %+v", d.Handle)
	}
}
