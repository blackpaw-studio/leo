package cli

import (
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	codexharness "github.com/blackpaw-studio/leo/internal/harness/codex"
	opencodeharness "github.com/blackpaw-studio/leo/internal/harness/opencode"
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
			got := buildProcessArgs(tt.cfg, "myproc", tt.proc, "")
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

// TestResolveProcessLaunchCodexFillsLeoMCPBridge locks the type-switch's
// codex branch: when the leo MCP gate passes (web enabled + a non-empty
// webToken), the codex options carry a LeoMCP bridge referencing the env-var
// *names* the supervisor already exports — no literal token needed.
func TestResolveProcessLaunchCodexFillsLeoMCPBridge(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	proc := config.ProcessConfig{Workspace: "/tmp/ws"}

	h, spec, err := resolveProcessLaunch(cfg, "myproc", proc, "tok")
	if err != nil {
		t.Fatalf("resolveProcessLaunch: %v", err)
	}
	if h.Name() != "codex" {
		t.Fatalf("resolved harness = %q, want codex", h.Name())
	}
	opts, ok := spec.Options.(codexharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want codexharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled when the leo MCP gate passes")
	}
	wantEnvVars := []string{"LEO_PROCESS_NAME", "LEO_WEB_PORT", "LEO_API_TOKEN"}
	if !reflect.DeepEqual(opts.LeoMCP.EnvVars, wantEnvVars) {
		t.Errorf("LeoMCP.EnvVars = %v, want %v", opts.LeoMCP.EnvVars, wantEnvVars)
	}
	if opts.LeoMCP.ApprovalMode != "approve" {
		t.Errorf("LeoMCP.ApprovalMode = %q, want approve", opts.LeoMCP.ApprovalMode)
	}

	// Args() now renders the turn-prefix argv for KindProcess (TurnDriver,
	// Plan 4 Task 5): the leo MCP bridge config lands in the prefix, ready
	// for TurnArgs to be appended per-turn.
	args, err := h.Args(spec)
	if err != nil {
		t.Fatalf("Args(): %v", err)
	}
	joined := strings.Join(args, "\x00")
	if !strings.Contains(joined, "mcp_servers.leo.command=\"leo\"") {
		t.Errorf("Args() = %v, want the leo MCP bridge config", args)
	}
}

// TestResolveProcessLaunchCodexNoLeoMCPWithoutToken confirms the gate: no
// webToken (the single-process foreground path has none) means no bridge.
func TestResolveProcessLaunchCodexNoLeoMCPWithoutToken(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true},
		Defaults: config.DefaultsConfig{Harness: "codex"},
	}
	_, spec, err := resolveProcessLaunch(cfg, "myproc", config.ProcessConfig{Workspace: "/tmp/ws"}, "")
	if err != nil {
		t.Fatalf("resolveProcessLaunch: %v", err)
	}
	opts, ok := spec.Options.(codexharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want codexharness.Options", spec.Options)
	}
	if opts.LeoMCP != nil {
		t.Errorf("expected nil LeoMCP bridge without a webToken, got %+v", opts.LeoMCP)
	}
}

// TestResolveProcessLaunchOpencodeFillsLeoMCPBridge locks the opencode
// branch: unlike codex's env-var-name whitelist, opencode's bridge needs the
// literal LEO_* values inline.
func TestResolveProcessLaunchOpencodeFillsLeoMCPBridge(t *testing.T) {
	cfg := &config.Config{
		HomePath: t.TempDir(),
		Web:      config.WebConfig{Enabled: true, Port: 4141},
		Defaults: config.DefaultsConfig{Harness: "opencode"},
	}
	h, spec, err := resolveProcessLaunch(cfg, "myproc", config.ProcessConfig{Workspace: "/tmp/ws"}, "sekrit-token")
	if err != nil {
		t.Fatalf("resolveProcessLaunch: %v", err)
	}
	if h.Name() != "opencode" {
		t.Fatalf("resolved harness = %q, want opencode", h.Name())
	}
	opts, ok := spec.Options.(opencodeharness.Options)
	if !ok {
		t.Fatalf("spec.Options = %T, want opencodeharness.Options", spec.Options)
	}
	if opts.LeoMCP == nil {
		t.Fatal("expected LeoMCP bridge to be filled when the leo MCP gate passes")
	}
	wantEnv := map[string]string{
		"LEO_PROCESS_NAME": "myproc",
		"LEO_WEB_PORT":     "4141",
		"LEO_API_TOKEN":    "sekrit-token",
	}
	if !reflect.DeepEqual(opts.LeoMCP.Env, wantEnv) {
		t.Errorf("LeoMCP.Env = %v, want %v", opts.LeoMCP.Env, wantEnv)
	}
	if opts.ServerPort == 0 {
		t.Error("expected resolveProcessLaunch to provision a ServerPort (Plan 4 Task 6 ServerDriver)")
	}
	if opts.ServerPassword == "" {
		t.Error("expected resolveProcessLaunch to provision a ServerPassword (Plan 4 Task 6 ServerDriver)")
	}

	// Args() now renders the `opencode serve` argv for KindProcess
	// (ServerDriver, Plan 4 Task 6).
	args, err := h.Args(spec)
	if err != nil {
		t.Fatalf("Args(): %v", err)
	}
	want := []string{"serve", "--port", strconv.Itoa(opts.ServerPort), "--hostname", "127.0.0.1"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("Args() = %#v, want %#v", args, want)
	}
}

// TestResolveProcessLaunchKindIsProcess locks that supervised processes
// always resolve KindProcess, regardless of harness.
func TestResolveProcessLaunchKindIsProcess(t *testing.T) {
	cfg := &config.Config{HomePath: t.TempDir()}
	_, spec, err := resolveProcessLaunch(cfg, "myproc", config.ProcessConfig{Workspace: "/tmp/ws"}, "")
	if err != nil {
		t.Fatalf("resolveProcessLaunch: %v", err)
	}
	if spec.Kind != harness.KindProcess {
		t.Errorf("spec.Kind = %q, want %q", spec.Kind, harness.KindProcess)
	}
}
