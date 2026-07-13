package codex

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/tmuxtui"
)

func withCodexConfigPath(t *testing.T, path string) {
	t.Helper()
	orig := codexConfigPath
	codexConfigPath = func() (string, error) { return path, nil }
	t.Cleanup(func() { codexConfigPath = orig })
}

func withCodexSessionsDir(t *testing.T, dir string) {
	t.Helper()
	orig := codexSessionsDir
	codexSessionsDir = func() (string, error) { return dir, nil }
	t.Cleanup(func() { codexSessionsDir = orig })
}

func TestRefreshSessionArgs(t *testing.T) {
	base := []string{"-a", "never", "--model", "m"}
	tests := []struct {
		name string
		in   []string
		id   string
		want []string
	}{
		{"fresh no id", base, "", base},
		{"fresh gains resume", base, "u1", []string{"resume", "u1", "-a", "never", "--model", "m"}},
		{"stale resume replaced", []string{"resume", "old", "-a", "never"}, "u2", []string{"resume", "u2", "-a", "never"}},
		{"resume stripped when id cleared", []string{"resume", "old", "-a", "never"}, "", []string{"-a", "never"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := refreshSessionArgs(tt.in, tt.id)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("refreshSessionArgs(%v, %q) = %v, want %v", tt.in, tt.id, got, tt.want)
			}
		})
	}
}

func TestRecoverQuickExitArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantArgs []string
		wantAct  harness.QuickExitAction
	}{
		{"resume strips and clears+no-resume", []string{"resume", "old", "-a", "never"}, []string{"-a", "never"}, harness.QuickExitClearAndNoResume},
		{"no resume clears session", []string{"-a", "never"}, []string{"-a", "never"}, harness.QuickExitClearSession},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotArgs, gotAct := recoverQuickExitArgs(tt.args)
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) || gotAct != tt.wantAct {
				t.Errorf("recoverQuickExitArgs(%v) = (%v, %d), want (%v, %d)", tt.args, gotArgs, gotAct, tt.wantArgs, tt.wantAct)
			}
		})
	}
}

func TestEnsureWorkspaceTrusted(t *testing.T) {
	t.Run("missing file created with block", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		withCodexConfigPath(t, path)

		ws := filepath.Join(dir, "ws")
		if err := ensureWorkspaceTrusted(harness.SessionHandle{Workspace: ws}); err != nil {
			t.Fatalf("ensureWorkspaceTrusted: %v", err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("perm = %o, want 0600", perm)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, `[projects."`+ws+`"]`) {
			t.Errorf("content = %q, missing projects block for %q", content, ws)
		}
		if !strings.Contains(content, `trust_level = "trusted"`) {
			t.Errorf("content = %q, missing trust_level", content)
		}
	})

	t.Run("entry already present is idempotent", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		withCodexConfigPath(t, path)
		ws := filepath.Join(dir, "ws")

		if err := ensureWorkspaceTrusted(harness.SessionHandle{Workspace: ws}); err != nil {
			t.Fatalf("first call: %v", err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := ensureWorkspaceTrusted(harness.SessionHandle{Workspace: ws}); err != nil {
			t.Fatalf("second call: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(before) != string(after) {
			t.Errorf("file changed on idempotent call:\nbefore=%q\nafter=%q", before, after)
		}
	})

	t.Run("existing unrelated content preserved", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.toml")
		unrelated := "[some_other_section]\nfoo = \"bar\"\n"
		if err := os.WriteFile(path, []byte(unrelated), 0o600); err != nil {
			t.Fatal(err)
		}
		withCodexConfigPath(t, path)
		ws := filepath.Join(dir, "ws")

		if err := ensureWorkspaceTrusted(harness.SessionHandle{Workspace: ws}); err != nil {
			t.Fatalf("ensureWorkspaceTrusted: %v", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		content := string(data)
		if !strings.Contains(content, unrelated) {
			t.Errorf("content = %q, unrelated content was not preserved", content)
		}
		if !strings.Contains(content, `[projects."`+ws+`"]`) {
			t.Errorf("content = %q, missing projects block", content)
		}
	})
}

func writeRollout(t *testing.T, path, id, cwd string, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	line := map[string]any{
		"timestamp": mtime.Format(time.RFC3339),
		"type":      "session_meta",
		"payload":   map[string]any{"id": id, "cwd": cwd},
	}
	data, err := json.Marshal(line)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverSessionID(t *testing.T) {
	base := time.Now().Add(-time.Hour)

	t.Run("matching cwd after since returns id", func(t *testing.T) {
		dir := t.TempDir()
		withCodexSessionsDir(t, dir)
		ws := t.TempDir()
		path := filepath.Join(dir, "2026", "07", "12", "rollout-20260712T000000-uuid1.jsonl")
		writeRollout(t, path, "uuid1", ws, base.Add(time.Minute))

		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, base)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "uuid1" {
			t.Errorf("id = %q, want uuid1", id)
		}
	})

	t.Run("matching cwd before since returns empty", func(t *testing.T) {
		dir := t.TempDir()
		withCodexSessionsDir(t, dir)
		ws := t.TempDir()
		path := filepath.Join(dir, "2026", "07", "12", "rollout-20260711T000000-uuid2.jsonl")
		writeRollout(t, path, "uuid2", ws, base.Add(-time.Minute))

		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, base)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty (mtime before since)", id)
		}
	})

	t.Run("non-matching cwd returns empty", func(t *testing.T) {
		dir := t.TempDir()
		withCodexSessionsDir(t, dir)
		ws := t.TempDir()
		other := t.TempDir()
		path := filepath.Join(dir, "2026", "07", "12", "rollout-20260712T000000-uuid3.jsonl")
		writeRollout(t, path, "uuid3", other, base.Add(time.Minute))

		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, base)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty (cwd mismatch)", id)
		}
	})

	t.Run("two matches newest wins", func(t *testing.T) {
		dir := t.TempDir()
		withCodexSessionsDir(t, dir)
		ws := t.TempDir()
		older := filepath.Join(dir, "2026", "07", "12", "rollout-20260712T000000-uuid-old.jsonl")
		newer := filepath.Join(dir, "2026", "07", "12", "rollout-20260712T000100-uuid-new.jsonl")
		writeRollout(t, older, "uuid-old", ws, base.Add(time.Minute))
		writeRollout(t, newer, "uuid-new", ws, base.Add(2*time.Minute))

		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, base)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "uuid-new" {
			t.Errorf("id = %q, want uuid-new (newest wins)", id)
		}
	})

	t.Run("no sessions dir returns empty without error", func(t *testing.T) {
		dir := t.TempDir()
		withCodexSessionsDir(t, filepath.Join(dir, "does-not-exist"))
		ws := t.TempDir()
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, base)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
	})
}

func TestCodexDriverWiring(t *testing.T) {
	d := (Codex{}).Driver()
	if got := d.Style(); got != harness.DriveTmux {
		t.Fatalf("Style() = %q, want %q", got, harness.DriveTmux)
	}
	if _, ok := d.(harness.PreLauncher); !ok {
		t.Fatalf("Driver() does not implement harness.PreLauncher")
	}
	if _, ok := d.(harness.SessionArgsRefresher); !ok {
		t.Fatalf("Driver() does not implement harness.SessionArgsRefresher")
	}
	if _, ok := d.(harness.QuickExitRecovery); !ok {
		t.Fatalf("Driver() does not implement harness.QuickExitRecovery")
	}
	if _, ok := d.(tmuxtui.Driver); !ok {
		t.Fatalf("Driver() is not a tmuxtui.Driver")
	}
}
