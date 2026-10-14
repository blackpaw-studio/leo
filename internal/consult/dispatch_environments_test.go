package consult

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func envTestConfig() *config.Config {
	cfg := testConfig()
	cfg.Environments = map[string]map[string]string{
		"tmpl-acct":   {"CLAUDE_CONFIG_DIR": "/tmpl", "TMPL_ONLY": "t"},
		"caller-acct": {"CLAUDE_CONFIG_DIR": "/caller"},
		"other":       {"CLAUDE_CONFIG_DIR": "/other"},
	}
	tmpl := cfg.Templates["claude"]
	tmpl.Environments = []string{"tmpl-acct"}
	cfg.Templates["claude"] = tmpl
	return cfg
}

// startHeadlessEnv starts a headless dispatch that finishes at once and
// returns its record and the child's process environment.
func startHeadlessEnv(t *testing.T, d *Dispatcher, cfg *config.Config, req Request) (Record, map[string]string) {
	t.Helper()
	var captured *exec.Cmd
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		captured = exec.CommandContext(ctx, "sh", "-c", `echo '{"type":"result","result":"ok","is_error":false}'`)
		return captured
	}
	req.Template, req.Prompt, req.Kind, req.Cwd = "claude", "q", "dispatch", t.TempDir()
	started, err := d.Start(context.Background(), cfg, req)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	env := map[string]string{}
	for _, entry := range captured.Env {
		if k, v, ok := strings.Cut(entry, "="); ok {
			env[k] = v
		}
	}
	return rec, env
}

func newEnvDispatcher(t *testing.T) *Dispatcher {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	return NewDispatcher(nil)
}

func TestDispatchInheritsCallerEnvironmentsOverTemplate(t *testing.T) {
	d := newEnvDispatcher(t)
	rec, env := startHeadlessEnv(t, d, envTestConfig(), Request{CallerEnvironments: []string{"caller-acct"}})
	if env["CLAUDE_CONFIG_DIR"] != "/caller" || env["TMPL_ONLY"] != "t" {
		t.Fatalf("child env CLAUDE_CONFIG_DIR=%q TMPL_ONLY=%q; want caller account over template", env["CLAUDE_CONFIG_DIR"], env["TMPL_ONLY"])
	}
	if !reflect.DeepEqual(rec.Environments, []string{"tmpl-acct", "caller-acct"}) || rec.EnvironmentsSource != EnvironmentsInherited {
		t.Fatalf("record environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestDispatchWithoutCallerEnvironmentsUsesTemplate(t *testing.T) {
	d := newEnvDispatcher(t)
	rec, env := startHeadlessEnv(t, d, envTestConfig(), Request{})
	if env["CLAUDE_CONFIG_DIR"] != "/tmpl" {
		t.Fatalf("CLAUDE_CONFIG_DIR=%q, want /tmpl", env["CLAUDE_CONFIG_DIR"])
	}
	if !reflect.DeepEqual(rec.Environments, []string{"tmpl-acct"}) || rec.EnvironmentsSource != EnvironmentsTemplate {
		t.Fatalf("record environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestDispatchOverrideReplacesInheritedLayer(t *testing.T) {
	d := newEnvDispatcher(t)
	rec, env := startHeadlessEnv(t, d, envTestConfig(), Request{CallerEnvironments: []string{"caller-acct"}, Environments: []string{"other"}})
	if env["CLAUDE_CONFIG_DIR"] != "/other" {
		t.Fatalf("CLAUDE_CONFIG_DIR=%q, want the override's /other", env["CLAUDE_CONFIG_DIR"])
	}
	if !reflect.DeepEqual(rec.Environments, []string{"tmpl-acct", "other"}) || rec.EnvironmentsSource != EnvironmentsOverride {
		t.Fatalf("record environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestDispatchEmptyOverrideInheritsNothing(t *testing.T) {
	d := newEnvDispatcher(t)
	rec, env := startHeadlessEnv(t, d, envTestConfig(), Request{CallerEnvironments: []string{"caller-acct"}, Environments: []string{}})
	if env["CLAUDE_CONFIG_DIR"] != "/tmpl" {
		t.Fatalf("CLAUDE_CONFIG_DIR=%q, want template's /tmpl", env["CLAUDE_CONFIG_DIR"])
	}
	if !reflect.DeepEqual(rec.Environments, []string{"tmpl-acct"}) || rec.EnvironmentsSource != EnvironmentsOverride {
		t.Fatalf("record environments=%v source=%q", rec.Environments, rec.EnvironmentsSource)
	}
}

func TestDispatchUnknownOverrideIsRejected(t *testing.T) {
	d := newEnvDispatcher(t)
	_, err := d.Start(context.Background(), envTestConfig(), Request{Template: "claude", Prompt: "q", Kind: "dispatch", Cwd: t.TempDir(), Environments: []string{"nope"}})
	var invalid *ValidationError
	if !errors.As(err, &invalid) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("want an invalid-request error naming nope, got %v", err)
	}
}

func TestNestedDispatchInheritsItsParentsEnvironments(t *testing.T) {
	d := newEnvDispatcher(t)
	cfg := envTestConfig()
	var mu sync.Mutex
	var cmds []*exec.Cmd
	// One seam for both runs (it is read from the parent's goroutine): the
	// parent keeps running so it is live as a parent; the child finishes.
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		mu.Lock()
		defer mu.Unlock()
		cmd := exec.CommandContext(ctx, "sh", "-c", `echo '{"type":"result","result":"ok","is_error":false}'`)
		if len(cmds) == 0 {
			cmd = exec.CommandContext(ctx, "sleep", "30")
		}
		cmds = append(cmds, cmd)
		return cmd
	}
	start := func(req Request) Record {
		t.Helper()
		req.Template, req.Prompt, req.Kind, req.Cwd = "claude", "q", "dispatch", t.TempDir()
		started, err := d.Start(context.Background(), cfg, req)
		if err != nil {
			t.Fatal(err)
		}
		if req.ParentDispatchID != "" {
			d.Wait(context.Background(), []string{started.ID}, 3*time.Second)
		}
		rec, err := d.Get(started.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rec
	}
	parent := start(Request{CallerEnvironments: []string{"caller-acct"}})
	// The child's own caller (its MCP server's agent name) has other
	// environments; the parent run's list is what it inherits.
	child := start(Request{ParentDispatchID: parent.ID, CallerEnvironments: []string{"other"}})
	mu.Lock()
	childEnv := envMap(cmds[1].Env)
	mu.Unlock()
	if childEnv["CLAUDE_CONFIG_DIR"] != "/caller" {
		t.Fatalf("CLAUDE_CONFIG_DIR=%q, want the parent's /caller", childEnv["CLAUDE_CONFIG_DIR"])
	}
	if !reflect.DeepEqual(child.Environments, []string{"tmpl-acct", "caller-acct"}) || child.EnvironmentsSource != EnvironmentsInherited {
		t.Fatalf("child environments=%v source=%q", child.Environments, child.EnvironmentsSource)
	}
}

func TestHeadlessContinuationKeepsTheInheritedEnvironments(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	var cmds []*exec.Cmd
	d.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		cmd := exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
		cmds = append(cmds, cmd)
		return cmd
	}
	cfg := envTestConfig()
	cfg.Web.Enabled = true
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "first", Cwd: t.TempDir(), Kind: "dispatch", CallerEnvironments: []string{"caller-acct"}})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	if len(cmds) != 2 {
		t.Fatalf("launched %d processes, want 2", len(cmds))
	}
	var resumed string
	for _, entry := range cmds[1].Env {
		if v, ok := strings.CutPrefix(entry, "CLAUDE_CONFIG_DIR="); ok {
			resumed = v
		}
	}
	if resumed != "/caller" {
		t.Fatalf("resumed CLAUDE_CONFIG_DIR=%q, want the inherited /caller", resumed)
	}
}
