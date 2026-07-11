package cli

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/session"
)

// Characterization tests: lock buildProcessArgs's argv byte-for-byte across
// the harness refactor. Web is disabled in every case so leomcp.AppendArg
// is a no-op and MergeSystemPrompt passes through (no state-dir writes).
func TestBuildProcessArgsCharacterization(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		proc config.ProcessConfig
		want []string
	}{
		{
			name: "minimal defaults",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{"--model", "opus", "--add-dir", "/tmp/ws"},
		},
		{
			name: "kitchen sink",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model: "opus",
					HarnessOptions: map[string]any{
						"allowed_tools":    []any{"Read", "Bash"},
						"disallowed_tools": []any{"WebFetch"},
					},
				},
			},
			proc: config.ProcessConfig{
				Workspace:   "/tmp/ws",
				Channels:    []string{"plugin:telegram@claude-plugins-official"},
				DevChannels: []string{"plugin:dev@local"},
				AddDirs:     []string{"/tmp/extra"},
				HarnessOptions: map[string]any{
					"remote_control":       true,
					"permission_mode":      "acceptEdits",
					"agent":                "rocket",
					"append_system_prompt": "be terse",
				},
			},
			want: []string{
				"--model", "opus",
				"--channels", "plugin:telegram@claude-plugins-official",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/tmp/extra",
				"--remote-control", "--remote-control-session-name-prefix", "myproc",
				"--permission-mode", "acceptEdits",
				"--agent", "rocket",
				"--allowed-tools", "Read,Bash",
				"--disallowed-tools", "WebFetch",
				"--append-system-prompt", "be terse",
			},
		},
		{
			name: "bypass permissions legacy fallback",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "sonnet",
					HarnessOptions: map[string]any{"bypass_permissions": true},
				},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/tmp/ws",
				"--dangerously-skip-permissions",
			},
		},
		{
			name: "defaults-level option inherited by scope",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"permission_mode": "plan"},
				},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--permission-mode", "plan",
			},
		},
		{
			name: "scope override wins over defaults",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:          "opus",
					HarnessOptions: map[string]any{"permission_mode": "plan"},
				},
			},
			proc: config.ProcessConfig{
				Workspace:      "/tmp/ws",
				HarnessOptions: map[string]any{"permission_mode": "acceptEdits"},
			},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--permission-mode", "acceptEdits",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProcessArgs(tt.cfg, "myproc", tt.proc)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildProcessArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}

func TestResolveSessionStateFreshPinsNewID(t *testing.T) {
	// Empty store + no claude project dir for the workspace → a fresh
	// pinned session ID is minted and persisted.
	home := t.TempDir()
	store := session.NewStore(home)

	st := resolveSessionState(store, "process:x", filepath.Join(home, "no-such-ws"), 0, "")
	if st.Mode != harness.SessionPinned {
		t.Fatalf("Mode = %q, want pinned", st.Mode)
	}
	if st.ID == "" {
		t.Fatal("expected a minted session ID")
	}
	storedID, _, err := store.Get("process:x")
	if err != nil || storedID != st.ID {
		t.Fatalf("store.Get = %q, %v; want %q", storedID, err, st.ID)
	}
}

func TestResolveSessionStateStoredIDResumes(t *testing.T) {
	home := t.TempDir()
	store := session.NewStore(home)
	if err := store.Set("process:x", "stored-id"); err != nil {
		t.Fatal(err)
	}
	st := resolveSessionState(store, "process:x", filepath.Join(home, "no-such-ws"), 0, "")
	if st.Mode != harness.SessionResume || st.ID != "stored-id" {
		t.Fatalf("state = %+v, want resume/stored-id", st)
	}
}
