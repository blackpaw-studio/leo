package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/session"
)

// newSessionTestHome writes a config and populates a session store with two
// entries. Returns the HomePath; loadConfig is wired to the written leo.yaml.
func newSessionTestHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	store := session.NewStore(home)
	if err := store.Set("task:heartbeat", "sess-heartbeat"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	if err := store.Set("service:dm", "sess-dm"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}
	return home
}

// withSessionTTY stubs the TTY check for session commands.
func withSessionTTY(t *testing.T, isTTY bool) {
	t.Helper()
	old := sessionIsTTY
	sessionIsTTY = func() bool { return isTTY }
	t.Cleanup(func() { sessionIsTTY = old })
}

// withSessionInput stubs the interactive reader for confirm prompts.
func withSessionInput(t *testing.T, input string) {
	t.Helper()
	old := sessionStdin
	sessionStdin = bufio.NewReader(strings.NewReader(input))
	t.Cleanup(func() { sessionStdin = old })
}

// runSessionListJSON runs `session list --json` and returns decoded payload.
func runSessionListJSON(t *testing.T) []sessionListEntry {
	t.Helper()
	oldStdout := sessionStdout
	var buf bytes.Buffer
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	sessionStdout = w

	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(&buf, r)
		close(done)
	}()

	cmd := newSessionListCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set json: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	_ = w.Close()
	<-done
	sessionStdout = oldStdout

	var out []sessionListEntry
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("decode json: %v; raw: %s", err, buf.String())
	}
	return out
}

func TestSessionList_JSONIncludesEntries(t *testing.T) {
	newSessionTestHome(t)
	entries := runSessionListJSON(t)
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %+v", len(entries), entries)
	}
	keys := map[string]string{}
	for _, e := range entries {
		keys[e.Key] = e.SessionID
	}
	if keys["service:dm"] != "sess-dm" || keys["task:heartbeat"] != "sess-heartbeat" {
		t.Errorf("unexpected entries: %+v", entries)
	}
}

func TestSessionList_JSONEmpty(t *testing.T) {
	home := t.TempDir()
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet"}}
	if err := config.Save(cfgPath, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = cfgPath
	t.Cleanup(func() { cfgFile = oldCfgFile })

	entries := runSessionListJSON(t)
	if len(entries) != 0 {
		t.Errorf("want 0 entries on empty store, got %d", len(entries))
	}
}

func TestSessionClear_SingleRequiresConfirmNonTTY(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, false)

	cmd := newSessionClearCmd()
	err := cmd.RunE(cmd, []string{"task:heartbeat"})
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("expected non-interactive error; got %v", err)
	}

	// Entry should still exist.
	store := session.NewStore(home)
	_, found, _ := store.Get("task:heartbeat")
	if !found {
		t.Error("session should still exist after aborted clear")
	}
}

func TestSessionClear_SingleWithYes(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, false)

	cmd := newSessionClearCmd()
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
	if err := cmd.RunE(cmd, []string{"task:heartbeat"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}

	store := session.NewStore(home)
	_, found, _ := store.Get("task:heartbeat")
	if found {
		t.Error("session should be cleared")
	}
}

func TestSessionClear_SingleConfirmYes(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, true)
	withSessionInput(t, "y\n")

	cmd := newSessionClearCmd()
	if err := cmd.RunE(cmd, []string{"task:heartbeat"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	store := session.NewStore(home)
	_, found, _ := store.Get("task:heartbeat")
	if found {
		t.Error("session should be cleared after 'y'")
	}
}

func TestSessionClear_SingleConfirmNo(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, true)
	withSessionInput(t, "n\n")

	cmd := newSessionClearCmd()
	if err := cmd.RunE(cmd, []string{"task:heartbeat"}); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	store := session.NewStore(home)
	_, found, _ := store.Get("task:heartbeat")
	if !found {
		t.Error("session should still exist after 'n'")
	}
}

func TestSessionClear_AllRequiresConfirmNonTTY(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, false)

	cmd := newSessionClearCmd()
	if err := cmd.Flags().Set("all", "true"); err != nil {
		t.Fatalf("set all: %v", err)
	}
	err := cmd.RunE(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "non-interactive") {
		t.Fatalf("expected non-interactive error; got %v", err)
	}
	store := session.NewStore(home)
	entries, _ := store.List()
	if len(entries) == 0 {
		t.Error("entries should remain after aborted --all clear")
	}
}

func TestSessionClear_AllWithYes(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, false)

	cmd := newSessionClearCmd()
	if err := cmd.Flags().Set("all", "true"); err != nil {
		t.Fatalf("set all: %v", err)
	}
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	store := session.NewStore(home)
	entries, _ := store.List()
	if len(entries) != 0 {
		t.Errorf("all entries should be cleared; got %d", len(entries))
	}
}

func TestSessionClear_AllConfirmNo(t *testing.T) {
	home := newSessionTestHome(t)
	withSessionTTY(t, true)
	withSessionInput(t, "n\n")

	cmd := newSessionClearCmd()
	if err := cmd.Flags().Set("all", "true"); err != nil {
		t.Fatalf("set all: %v", err)
	}
	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("RunE: %v", err)
	}
	store := session.NewStore(home)
	entries, _ := store.List()
	if len(entries) != 2 {
		t.Errorf("entries should remain after 'n'; got %d", len(entries))
	}
}

func TestSessionClear_UnknownKeyErrors(t *testing.T) {
	newSessionTestHome(t)
	withSessionTTY(t, false)

	cmd := newSessionClearCmd()
	if err := cmd.Flags().Set("yes", "true"); err != nil {
		t.Fatalf("set yes: %v", err)
	}
	err := cmd.RunE(cmd, []string{"does-not-exist"})
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error; got %v", err)
	}
}
