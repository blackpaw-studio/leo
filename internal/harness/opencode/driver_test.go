package opencode

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func withExecCommand(t *testing.T, fn func(ctx context.Context, name string, args ...string) *exec.Cmd) {
	t.Helper()
	orig := execCommand
	execCommand = fn
	t.Cleanup(func() { execCommand = orig })
}

func TestRefreshSessionArgs(t *testing.T) {
	base := []string{"--model", "lmstudio/qwen/qwen3.6-35b-a3b"}
	tests := []struct {
		name string
		in   []string
		id   string
		want []string
	}{
		{"fresh no id", base, "", base},
		{"fresh gains -s", base, "ses_1", []string{"--model", "lmstudio/qwen/qwen3.6-35b-a3b", "-s", "ses_1"}},
		{"stale -s replaced", []string{"--model", "m", "-s", "old"}, "ses_2", []string{"--model", "m", "-s", "ses_2"}},
		{"-s stripped when id cleared", []string{"--model", "m", "-s", "old"}, "", []string{"--model", "m"}},
		{"no model, id added", nil, "ses_3", []string{"-s", "ses_3"}},
		{"-s pair anywhere is found and replaced", []string{"-s", "old", "--model", "m"}, "ses_4", []string{"--model", "m", "-s", "ses_4"}},
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
		{"-s present strips and clears+no-resume", []string{"--model", "m", "-s", "old"}, []string{"--model", "m"}, harness.QuickExitClearAndNoResume},
		{"-s absent clears session", []string{"--model", "m"}, []string{"--model", "m"}, harness.QuickExitClearSession},
		{"empty args, no -s, clears session", nil, nil, harness.QuickExitClearSession},
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

// sessionListFixture builds an execCommand replacement that ignores name/args
// and shells out `cat` against a temp JSON file holding entries, so
// discoverSessionID exercises the real execCommand→JSON-decode path without
// depending on a real opencode binary.
func sessionListFixture(t *testing.T, entries []map[string]any) func(ctx context.Context, name string, args ...string) *exec.Cmd {
	t.Helper()
	data, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session_list.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "cat", path)
	}
}

func TestDiscoverSessionID(t *testing.T) {
	// created is epoch MILLISECONDS. since straddles the two entries below.
	since := time.UnixMilli(1_700_000_000_000)

	t.Run("entry at/after since in workspace matches", func(t *testing.T) {
		ws := t.TempDir()
		withExecCommand(t, sessionListFixture(t, []map[string]any{
			{"id": "ses_new", "created": since.UnixMilli(), "directory": ws},
		}))
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, since)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "ses_new" {
			t.Errorf("id = %q, want ses_new", id)
		}
	})

	t.Run("entry strictly before since is excluded", func(t *testing.T) {
		ws := t.TempDir()
		withExecCommand(t, sessionListFixture(t, []map[string]any{
			{"id": "ses_old", "created": since.UnixMilli() - 1, "directory": ws},
		}))
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, since)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty (created before since)", id)
		}
	})

	t.Run("non-matching directory excluded", func(t *testing.T) {
		ws := t.TempDir()
		other := t.TempDir()
		withExecCommand(t, sessionListFixture(t, []map[string]any{
			{"id": "ses_other", "created": since.UnixMilli() + 1000, "directory": other},
		}))
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, since)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty (directory mismatch)", id)
		}
	})

	t.Run("two matches newest wins", func(t *testing.T) {
		ws := t.TempDir()
		withExecCommand(t, sessionListFixture(t, []map[string]any{
			{"id": "ses_older", "created": since.UnixMilli() + 1000, "directory": ws},
			{"id": "ses_newer", "created": since.UnixMilli() + 5000, "directory": ws},
		}))
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, since)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "ses_newer" {
			t.Errorf("id = %q, want ses_newer", id)
		}
	})

	t.Run("subprocess failure yields empty, no error", func(t *testing.T) {
		ws := t.TempDir()
		withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sh", "-c", "exit 1")
		})
		id, err := discoverSessionID(context.Background(), harness.SessionHandle{Workspace: ws}, since)
		if err != nil {
			t.Fatalf("discoverSessionID: %v", err)
		}
		if id != "" {
			t.Errorf("id = %q, want empty", id)
		}
	})

	t.Run("cancelled ctx propagates to the subprocess instead of hanging", func(t *testing.T) {
		ws := t.TempDir()
		var mu sync.Mutex
		started := make(chan struct{})
		withExecCommand(t, func(ctx context.Context, name string, args ...string) *exec.Cmd {
			mu.Lock()
			defer mu.Unlock()
			close(started)
			return exec.CommandContext(ctx, "sleep", "5")
		})

		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = discoverSessionID(ctx, harness.SessionHandle{Workspace: ws}, since)
		}()

		<-started
		cancel()

		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("discoverSessionID did not return promptly after ctx cancellation; the subprocess was not killed")
		}
	})
}

func TestOpencodeSessionArgsWithoutModel(t *testing.T) {
	spec := harness.LaunchSpec{Kind: harness.KindAgent, Options: Options{}}
	args, err := (Opencode{}).Args(spec)
	if err != nil {
		t.Fatalf("Args: %v", err)
	}
	if args != nil {
		t.Errorf("args = %#v, want nil", args)
	}
}
