package consult

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// targetHarness is a dispatcher whose children record their argv, with a
// target agent "leo" (template leo) whose template grants marketing, agent
// leoterm and task digest.
type targetHarness struct {
	d        *Dispatcher
	stateDir string
	leoWS    string
	callerWS string
	mu       sync.Mutex
	argv     [][]string
	logs     []string
	started  []string
}

func newTargetHarness(t *testing.T) *targetHarness {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &targetHarness{stateDir: filepath.Join(root, "state"), leoWS: filepath.Join(root, "agents", "leo"), callerWS: filepath.Join(root, "agents", "mk")}
	for _, dir := range []string{h.stateDir, h.leoWS, h.callerWS} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		h.d.Wait(context.Background(), h.started, 10*time.Second)
	})
	h.d = NewDispatcher(NewFileRecorder(h.stateDir), ctx)
	h.d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		h.mu.Lock()
		h.argv = append(h.argv, append([]string(nil), args...))
		h.mu.Unlock()
		return exec.CommandContext(ctx, "sleep", "30")
	}
	h.d.GitCommand = func(string, ...string) *exec.Cmd { return exec.Command("sh", "-c", "exit 1") }
	h.d.Logf = func(format string, args ...any) {
		h.mu.Lock()
		h.logs = append(h.logs, fmt.Sprintf(format, args...))
		h.mu.Unlock()
	}
	h.d.SetCallerWorkspaces(fakeWorkspaces{
		"leo":         {Workspace: h.leoWS, Harness: "claude", Template: "leo"},
		"mk-1":        {Workspace: h.callerWS, Harness: "claude", Template: "marketing"},
		"leoterm":     {Workspace: h.callerWS, Harness: "claude", Template: "coder"},
		"stranger":    {Workspace: h.callerWS, Harness: "claude", Template: "coder"},
		"task:digest": {Workspace: h.callerWS, Harness: "claude"},
	})
	return h
}

func targetConfig(mode string) *config.Config {
	cfg := confinementConfig(mode)
	cfg.Templates["leo"] = config.TemplateConfig{Harness: "claude", Share: config.ShareConfig{
		ReadableBy: []string{"template:marketing", "agent:leoterm", "task:digest"},
	}}
	cfg.Templates["claude-iso"] = config.TemplateConfig{Harness: "claude", Model: "opus", Isolation: "worktree"}
	// explore routes to the claude template, so nested explore children pass
	// the read-only guard.
	cfg.Delegation = &config.DelegationConfig{
		ActiveProfile: "p",
		Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{
			"explore": {Template: "claude"},
		}}},
	}
	return cfg
}

func (h *targetHarness) start(t *testing.T, mode string, req Request) (Started, error) {
	t.Helper()
	if req.Template == "" {
		req.Template = "claude"
	}
	if req.Role == "" {
		req.Role = "explore"
	}
	if req.Role == "-" {
		req.Role = ""
	}
	req.Prompt = "q"
	if req.Kind == "" {
		req.Kind = "dispatch"
	}
	started, err := h.d.Start(context.Background(), targetConfig(mode), req)
	if err == nil {
		h.started = append(h.started, started.ID)
	}
	return started, err
}

// lastArgv waits for the most recent child's argv.
func (h *targetHarness) lastArgv(t *testing.T, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		if len(h.argv) >= n {
			out := h.argv[n-1]
			h.mu.Unlock()
			return out
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("child %d never launched", n)
	return nil
}

func (h *targetHarness) logLines() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Clone(h.logs)
}

func wantInvalid(t *testing.T, err error, subs ...string) {
	t.Helper()
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a ValidationError", err)
	}
	for _, s := range subs {
		if !strings.Contains(err.Error(), s) {
			t.Fatalf("err %q lacks %q", err, s)
		}
	}
}

// claudeSandboxed reports whether claude argv runs in the read-only sandbox
// for ws: the real argv builder's folded --settings and --setting-sources.
func claudeSandboxed(t *testing.T, argv []string, ws string) bool {
	t.Helper()
	if !slices.Contains(argv, "--setting-sources") {
		return false
	}
	for i, a := range argv {
		if a != "--settings" || i+1 >= len(argv) {
			continue
		}
		var s struct {
			Sandbox struct {
				Enabled    bool `json:"enabled"`
				Filesystem struct {
					DenyWrite []string `json:"denyWrite"`
				} `json:"filesystem"`
			} `json:"sandbox"`
		}
		if err := json.Unmarshal([]byte(argv[i+1]), &s); err != nil {
			t.Fatalf("--settings %q: %v", argv[i+1], err)
		}
		return s.Sandbox.Enabled && slices.Contains(s.Sandbox.Filesystem.DenyWrite, ws)
	}
	return false
}

func TestStartTargetGrantedViaEachPrefix(t *testing.T) {
	for _, tc := range []struct{ caller, grant string }{
		{"mk-1", "template:marketing"},
		{"leoterm", "agent:leoterm"},
		{"task:digest", "task:digest"},
	} {
		t.Run(tc.grant, func(t *testing.T) {
			h := newTargetHarness(t)
			started, err := h.start(t, "enforce", Request{Caller: tc.caller, TargetAgent: "leo"})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			if started.Cwd != h.leoWS {
				t.Fatalf("cwd = %q, want the target workspace %q", started.Cwd, h.leoWS)
			}
			rec, err := h.d.Get(started.ID)
			if err != nil {
				t.Fatal(err)
			}
			if rec.TargetAgent != "leo" || rec.TargetGrant != tc.grant || !slices.Contains(rec.TargetWorkspaces, h.leoWS) {
				t.Fatalf("record target = %q %q %v", rec.TargetAgent, rec.TargetGrant, rec.TargetWorkspaces)
			}
			if !claudeSandboxed(t, h.lastArgv(t, 1), h.leoWS) {
				t.Fatalf("headless argv not sandboxed: %q", h.lastArgv(t, 1))
			}
			logs := h.logLines()
			want := fmt.Sprintf("dispatch %s: read grant caller=%s target=leo role=explore grant=%s", started.ID, tc.caller, tc.grant)
			if len(logs) != 1 || logs[0] != want {
				t.Fatalf("logs = %q, want exactly %q", logs, want)
			}
		})
	}
}

func TestStartTargetWithoutGrantRefusedInEveryMode(t *testing.T) {
	for _, mode := range []string{"off", "warn", "enforce"} {
		t.Run(mode, func(t *testing.T) {
			h := newTargetHarness(t)
			_, err := h.start(t, mode, Request{Caller: "stranger", TargetAgent: "leo"})
			wantInvalid(t, err, `"leo"`, `"stranger"`, "templates.leo.share.readable_by")
			if recs, _ := Load(h.stateDir); len(recs) != 0 {
				t.Fatalf("refused start recorded %d records", len(recs))
			}
		})
	}
}

func TestStartTargetRefusesInvalidCombinations(t *testing.T) {
	cases := []struct {
		name string
		req  Request
		want string
	}{
		{"write role", Request{Caller: "mk-1", TargetAgent: "leo", Role: "code"}, "read-only role"},
		{"template dispatch", Request{Caller: "mk-1", TargetAgent: "leo", Role: "-"}, "role"},
		{"explicit cwd", Request{Caller: "mk-1", TargetAgent: "leo", Cwd: "/tmp"}, "cwd"},
		{"explicit isolation", Request{Caller: "mk-1", TargetAgent: "leo", Isolation: "worktree"}, "isolation"},
		{"consult", Request{Caller: "mk-1", TargetAgent: "leo", Kind: "consult"}, "dispatch"},
		{"remote target", Request{Caller: "mk-1", TargetAgent: "leo@other"}, "target_agent"},
		{"task target", Request{Caller: "mk-1", TargetAgent: "task:digest"}, "target_agent"},
		{"unknown target", Request{Caller: "mk-1", TargetAgent: "ghost"}, `"ghost"`},
		{"opencode", Request{Caller: "mk-1", TargetAgent: "leo", Template: "opencode"}, `harness "opencode" has no enforceable read-only sandbox`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTargetHarness(t)
			_, err := h.start(t, "off", tc.req)
			wantInvalid(t, err, tc.want)
		})
	}
}

func TestStartTargetSkipsTemplateDefaultIsolation(t *testing.T) {
	h := newTargetHarness(t)
	started, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo", Template: "claude-iso"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	rec, _ := h.d.Get(started.ID)
	if rec.Isolation != "" || rec.Worktree != "" || started.Cwd != h.leoWS {
		t.Fatalf("granted run isolated: isolation=%q worktree=%q cwd=%q", rec.Isolation, rec.Worktree, started.Cwd)
	}
}

func TestStartTargetExemptCallersRecordTheirGrant(t *testing.T) {
	for _, tc := range []struct{ caller, grant string }{
		{"", "caller:none"},
		{"leoterm-ios", "api_client:leoterm-ios"},
	} {
		t.Run(tc.grant, func(t *testing.T) {
			h := newTargetHarness(t)
			started, err := h.start(t, "enforce", Request{Caller: tc.caller, TargetAgent: "leo"})
			if err != nil {
				t.Fatalf("Start: %v", err)
			}
			rec, _ := h.d.Get(started.ID)
			if rec.TargetGrant != tc.grant {
				t.Fatalf("grant = %q, want %q", rec.TargetGrant, tc.grant)
			}
			if !claudeSandboxed(t, h.lastArgv(t, 1), h.leoWS) {
				t.Fatal("exempt caller's run not sandboxed")
			}
		})
	}
}

func TestStartNestedUnderGrantInheritsTarget(t *testing.T) {
	h := newTargetHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	h.lastArgv(t, 1)
	// Enforce allows the child into the target workspace (not mk-1's own).
	child, err := h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID, Cwd: h.leoWS})
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	rec, _ := h.d.Get(child.ID)
	if rec.TargetAgent != "leo" || rec.TargetGrant != "template:marketing" || !slices.Contains(rec.TargetWorkspaces, h.leoWS) {
		t.Fatalf("child target = %q %q %v", rec.TargetAgent, rec.TargetGrant, rec.TargetWorkspaces)
	}
	if !claudeSandboxed(t, h.lastArgv(t, 2), h.leoWS) {
		t.Fatalf("nested argv not sandboxed: %q", h.lastArgv(t, 2))
	}
	if n := len(h.logLines()); n != 1 {
		t.Fatalf("log lines = %d, want 1 (inherited grants are not new starts)", n)
	}
	// Repeating the same target is the inherited grant, not a new one.
	if _, err := h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID, TargetAgent: "leo"}); err != nil {
		t.Fatalf("nested same target: %v", err)
	}
}

func TestStartNestedUnderGrantDefaultsToTargetWorkspace(t *testing.T) {
	h := newTargetHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	child, err := h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID})
	if err != nil {
		t.Fatalf("nested: %v", err)
	}
	if child.Cwd != h.leoWS {
		t.Fatalf("nested cwd = %q, want the target workspace", child.Cwd)
	}
}

func TestStartNestedUnderGrantRefusesADifferentTarget(t *testing.T) {
	h := newTargetHarness(t)
	h.d.SetCallerWorkspaces(fakeWorkspaces{
		"leo":   {Workspace: h.leoWS, Harness: "claude", Template: "leo"},
		"other": {Workspace: h.callerWS, Harness: "claude", Template: "leo"},
		"mk-1":  {Workspace: h.callerWS, Harness: "claude", Template: "marketing"},
	})
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	_, err = h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID, TargetAgent: "other"})
	wantInvalid(t, err, `"other"`, `"leo"`)
}

func TestStartNestedUnderGrantRefusesIsolation(t *testing.T) {
	h := newTargetHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	_, err = h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID, Isolation: "worktree"})
	wantInvalid(t, err, "isolation")
}

func TestStartTargetCodexRunsReadOnly(t *testing.T) {
	h := newTargetHarness(t)
	if _, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo", Template: "codex"}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	argv := strings.Join(h.lastArgv(t, 1), " ")
	if !strings.Contains(argv, "--sandbox read-only") || strings.Contains(argv, "writable_roots") {
		t.Fatalf("codex argv = %s, want --sandbox read-only and no writable_roots", argv)
	}
}

func TestConsultUnderGrantInheritsTheSandbox(t *testing.T) {
	h := newTargetHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	h.lastArgv(t, 1)
	consult, err := h.start(t, "enforce", Request{Caller: "dispatch", ParentDispatchID: root.ID, Kind: "consult", Role: "-", Cwd: h.leoWS})
	if err != nil {
		t.Fatalf("consult: %v", err)
	}
	rec, _ := h.d.Get(consult.ID)
	if rec.TargetAgent != "leo" || !claudeSandboxed(t, h.lastArgv(t, 2), h.leoWS) {
		t.Fatalf("consult under a grant not sandboxed: target=%q argv=%q", rec.TargetAgent, h.lastArgv(t, 2))
	}
}

// A consult under a granted tree is not refused for its template's default
// worktree isolation: like the granted run, it reads the target in place.
func TestConsultUnderGrantSkipsTemplateDefaultIsolation(t *testing.T) {
	h := newTargetHarness(t)
	root, err := h.start(t, "enforce", Request{Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("root: %v", err)
	}
	cfg := targetConfig("enforce")
	cfg.Delegation.Profiles["p"].Roles["plan"] = config.RoleTarget{Template: "claude-iso"}
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err = h.d.Consult(ctx, cfg, Request{Template: "claude-iso", Prompt: "q", Caller: "dispatch", ParentDispatchID: root.ID, Cwd: h.leoWS})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("consult err = %v, want it to start and time out", err)
	}
}

func TestStartTargetWarnsWhenCodexPermissionModeIsOverridden(t *testing.T) {
	h := newTargetHarness(t)
	cfg := targetConfig("enforce")
	cfg.Templates["codex-ww"] = config.TemplateConfig{Harness: "codex", Model: "gpt-5.3-codex", HarnessOptions: map[string]any{"permission_mode": "workspace-write"}}
	started, err := h.d.Start(context.Background(), cfg, Request{Template: "codex-ww", Role: "explore", Prompt: "q", Kind: "dispatch", Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	h.started = append(h.started, started.ID)
	if !hasWarning(started.Warnings, `permission_mode "workspace-write" overridden`) {
		t.Fatalf("warnings = %v, want the codex override named", started.Warnings)
	}
	if argv := strings.Join(h.lastArgv(t, 1), " "); !strings.Contains(argv, "--sandbox read-only") || strings.Contains(argv, "workspace-write") {
		t.Fatalf("codex argv = %s", argv)
	}
}
