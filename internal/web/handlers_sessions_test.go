package web

import (
	"net/http"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/session"
)

// TestSessionCRUD mirrors TestProviderCRUD's add/edit/delete shape for
// cfg.Sessions. Like providers (and unlike hosts), Config.Validate()
// requires sessions.<name>.workspace to be non-empty, so add seeds a real,
// working default (cfg.DefaultWorkspace()) rather than an obviously-fake
// placeholder — the new session actually boots there until the operator
// picks a workspace of their own via the card's inline form.
func TestSessionCRUD(t *testing.T) {
	s, dir := newTestServer(t)

	// add
	w := postForm(t, s, "/web/session/add", url.Values{"name": {"daily"}})
	if w.Code != http.StatusOK {
		t.Fatalf("add: %d, body=%s", w.Code, readBody(t, w))
	}
	if w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("add: HX-Refresh header = %q, want \"true\"", w.Header().Get("HX-Refresh"))
	}
	cfg := reloadTestConfig(t, dir)
	added, ok := cfg.Sessions["daily"]
	if !ok {
		t.Fatal("session not created")
	}
	if added.Workspace == "" {
		t.Errorf("added session should have non-empty workspace so it round-trips through Validate(): %+v", added)
	}

	// edit. applySection's Apply is a full-form replace (every registered
	// field for the section is set from the submitted form, including back
	// to zero when absent — same as every other schema-driven section), so
	// workspace must be resubmitted here or the save fails Validate()'s
	// "workspace is required" check. The real page's <form> always submits
	// every field since config_form.html renders one input per registered
	// field inside a single form; only a hand-built partial form like an
	// earlier draft of this test can trigger the silent-validation-failure
	// gotcha.
	form := url.Values{"workspace": {added.Workspace}, "idle_timeout": {"30m"}, "model": {"opus"}}
	w = postForm(t, s, "/web/config/session/daily", form)
	body := readBody(t, w)
	if w.Code != http.StatusOK || strings.Contains(body, "flash-error") {
		t.Fatalf("save: %d, body=%s", w.Code, body)
	}
	cfg = reloadTestConfig(t, dir)
	if cfg.Sessions["daily"].IdleTimeout != "30m" || cfg.Sessions["daily"].Model != "opus" || cfg.Sessions["daily"].Workspace != added.Workspace {
		t.Errorf("session not saved: %+v", cfg.Sessions["daily"])
	}

	// delete
	w = deleteRequest(t, s, "/web/session/daily")
	if w.Code != http.StatusOK {
		t.Fatalf("delete: %d, body=%s", w.Code, readBody(t, w))
	}
	if w.Header().Get("HX-Refresh") != "true" {
		t.Errorf("delete: HX-Refresh header = %q, want \"true\"", w.Header().Get("HX-Refresh"))
	}
	cfg = reloadTestConfig(t, dir)
	if _, ok := cfg.Sessions["daily"]; ok {
		t.Error("session not deleted")
	}
}

// TestSessionAddRejectsDuplicate guards handleSessionAdd's existence check.
func TestSessionAddRejectsDuplicate(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/session/add", url.Values{"name": {"daily"}})

	w := postForm(t, s, "/web/session/add", url.Values{"name": {"daily"}})
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash for duplicate name, got: %s", body)
	}

	cfg := reloadTestConfig(t, dir)
	if len(cfg.Sessions) != 1 {
		t.Errorf("duplicate add should not have touched config: %+v", cfg.Sessions)
	}
}

// TestSessionDeleteRejectsDanglingTaskRef guards the documented decision
// that handleSessionDelete does no reference-check of its own: it deletes
// optimistically and lets validateAndSave's Config.Validate() call (via
// ResolveSession's topology-B branch) refuse a delete that would orphan a
// runtime: persistent task's explicit `session:` reference.
func TestSessionDeleteRejectsDanglingTaskRef(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/session/add", url.Values{"name": {"shared"}})

	cfg := reloadTestConfig(t, dir)
	cfg.Tasks["daily-report"] = cfg.Tasks["heartbeat"] // any TaskConfig template
	task := cfg.Tasks["daily-report"]
	task.Runtime = "persistent"
	task.Session = "shared"
	cfg.Tasks["daily-report"] = task
	if err := config.Save(filepath.Join(dir, "leo.yaml"), cfg); err != nil {
		t.Fatalf("seeding dangling ref: %v", err)
	}

	w := deleteRequest(t, s, "/web/session/shared")
	body := readBody(t, w)
	if w.Code != http.StatusOK || !strings.Contains(body, "flash-error") {
		t.Errorf("delete of a referenced session should be refused, got %d: %s", w.Code, body)
	}

	cfg = reloadTestConfig(t, dir)
	if _, ok := cfg.Sessions["shared"]; !ok {
		t.Error("session should still exist after refused delete")
	}
}

// TestSessionReset is this task's TDD anchor for the runtime-action half of
// the page (adapted from the brief's sketch to this codebase's actual
// helpers — postForm instead of postFormWithCookie, no auth cookie since
// newTestServer's stack doesn't require one, and s.execCommand as the seam
// name — srv.execCommand in the brief was a typo for the receiver used
// throughout this file). The daemon isn't running in tests, so
// daemon.IsRunning's guard skips the daemon.ResetSession call entirely;
// this only exercises the tmux kill-session + session-store-clear effects.
func TestSessionReset(t *testing.T) {
	s, dir := newTestServer(t)
	postForm(t, s, "/web/session/add", url.Values{"name": {"daily"}})

	store := session.NewStore(dir)
	if err := store.Set("session:daily", "fake-session-id-123"); err != nil {
		t.Fatalf("seeding stored session id: %v", err)
	}

	// Stub tmux lookup so the execCommand seam below is reached even on a
	// runner (e.g. macOS CI) that has no tmux installed — mirrors the real
	// binary's presence without shelling out to one.
	s.lookTmux = func() (string, error) { return "/usr/bin/tmux", nil }

	var killed []string
	s.execCommand = func(name string, args ...string) *exec.Cmd {
		killed = append(killed, name+" "+strings.Join(args, " "))
		return exec.Command("true")
	}

	w := postForm(t, s, "/web/session/daily/reset", url.Values{})
	if w.Code != http.StatusOK {
		t.Fatalf("reset: %d, body=%s", w.Code, readBody(t, w))
	}

	found := false
	for _, c := range killed {
		if strings.Contains(c, "kill-session") && strings.Contains(c, "leo-session-daily") {
			found = true
		}
	}
	if !found {
		t.Errorf("tmux kill-session not invoked: %v", killed)
	}

	if id, ok, err := store.Get("session:daily"); err != nil || ok {
		t.Errorf("stored session id should be cleared, got id=%q ok=%v err=%v", id, ok, err)
	}
}

// TestSessionResetNotFound guards the handler's existence check.
func TestSessionResetNotFound(t *testing.T) {
	s, _ := newTestServer(t)

	w := postForm(t, s, "/web/session/nonexistent/reset", url.Values{})
	body := readBody(t, w)
	if !strings.Contains(body, "flash-error") || !strings.Contains(body, "not found") {
		t.Errorf("want not-found flash, got: %s", body)
	}
}
