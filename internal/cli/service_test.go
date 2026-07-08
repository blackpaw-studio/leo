package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/service"
	"github.com/blackpaw-studio/leo/internal/session"
)

func TestMergeChannelsIntoEnv(t *testing.T) {
	tests := []struct {
		name    string
		proc    config.ProcessConfig
		wantKey string
		wantVal string
		wantLen int
	}{
		{
			name:    "injects LEO_CHANNELS when channels set",
			proc:    config.ProcessConfig{Channels: []string{"plugin:telegram@claude-plugins-official"}},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official",
			wantLen: 1,
		},
		{
			name: "joins multiple channels comma-separated",
			proc: config.ProcessConfig{
				Channels: []string{"plugin:telegram@claude-plugins-official", "plugin:slack@example"},
			},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official,plugin:slack@example",
			wantLen: 1,
		},
		{
			name:    "no channels yields no LEO_CHANNELS entry",
			proc:    config.ProcessConfig{},
			wantLen: 0,
		},
		{
			name: "preserves existing proc.Env entries",
			proc: config.ProcessConfig{
				Env:      map[string]string{"FOO": "bar"},
				Channels: []string{"plugin:telegram@claude-plugins-official"},
			},
			wantKey: "LEO_CHANNELS",
			wantVal: "plugin:telegram@claude-plugins-official",
			wantLen: 2,
		},
		{
			name: "injects LEO_DEV_CHANNELS when dev_channels set",
			proc: config.ProcessConfig{
				DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
			},
			wantKey: "LEO_DEV_CHANNELS",
			wantVal: "plugin:blackpaw-telegram@blackpaw-plugins",
			wantLen: 1,
		},
		{
			name: "both channels and dev_channels coexist",
			proc: config.ProcessConfig{
				Channels:    []string{"plugin:telegram@claude-plugins-official"},
				DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
			},
			wantKey: "LEO_DEV_CHANNELS",
			wantVal: "plugin:blackpaw-telegram@blackpaw-plugins",
			wantLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mergeChannelsIntoEnv(tt.proc)
			if len(got) != tt.wantLen {
				t.Errorf("got %d keys, want %d: %v", len(got), tt.wantLen, got)
			}
			if tt.wantKey != "" {
				if got[tt.wantKey] != tt.wantVal {
					t.Errorf("got[%q] = %q, want %q", tt.wantKey, got[tt.wantKey], tt.wantVal)
				}
			}
		})
	}
}

func TestProcessEnviron(t *testing.T) {
	proc := config.ProcessConfig{
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
		DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
		Env:         map[string]string{"FOO": "bar"},
	}

	env := processEnviron(proc, nil)

	var sawChannels, sawDevChannels, sawFoo bool
	for _, line := range env {
		if strings.HasPrefix(line, "LEO_CHANNELS=") {
			sawChannels = true
		}
		if strings.HasPrefix(line, "LEO_DEV_CHANNELS=") {
			sawDevChannels = true
		}
		if line == "FOO=bar" {
			sawFoo = true
		}
	}
	if !sawChannels {
		t.Error("processEnviron should contain LEO_CHANNELS")
	}
	if !sawDevChannels {
		t.Error("processEnviron should contain LEO_DEV_CHANNELS")
	}
	if !sawFoo {
		t.Error("processEnviron should contain FOO=bar")
	}
}

func TestBuildProcessArgsIncludesDevChannels(t *testing.T) {
	cfg := &config.Config{}
	proc := config.ProcessConfig{
		Channels:    []string{"plugin:telegram@claude-plugins-official"},
		DevChannels: []string{"plugin:blackpaw-telegram@blackpaw-plugins"},
	}

	args := buildProcessArgs(cfg, "test", proc)

	var sawChan, sawDev bool
	for i, a := range args {
		if a == "--channels" && i+1 < len(args) && args[i+1] == "plugin:telegram@claude-plugins-official" {
			sawChan = true
		}
		if a == "--dangerously-load-development-channels" && i+1 < len(args) && args[i+1] == "plugin:blackpaw-telegram@blackpaw-plugins" {
			sawDev = true
		}
	}
	if !sawChan {
		t.Errorf("missing --channels flag, got args: %v", args)
	}
	if !sawDev {
		t.Errorf("missing --dangerously-load-development-channels flag, got args: %v", args)
	}
}

func TestHasMCPServers_ValidConfig(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{"test":{"command":"echo"}}}`), 0644)

	if !config.HasMCPServers(f) {
		t.Error("should return true for valid config with servers")
	}
}

func TestHasMCPServers_EmptyServers(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{"mcpServers":{}}`), 0644)

	if config.HasMCPServers(f) {
		t.Error("should return false for empty mcpServers")
	}
}

func TestHasMCPServers_EmptyObject(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "mcp.json")
	os.WriteFile(f, []byte(`{}`), 0644)

	if config.HasMCPServers(f) {
		t.Error("should return false for empty object")
	}
}

func TestHasMCPServers_MissingFile(t *testing.T) {
	if config.HasMCPServers("/nonexistent/mcp.json") {
		t.Error("should return false for missing file")
	}
}

// resolveSessionArgs should prefer the newest jsonl over the stored ID and
// persist the resolved ID back to the store, so subsequent restarts agree
// with what claude has on disk.
func TestResolveSessionArgs_LatestBeatsStored(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	workspace := filepath.Join(t.TempDir(), "leotest-resolve-latest")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir workspace: %v", err)
	}

	projDir := filepath.Join(home, ".claude", "projects", session.ProjectSlug(workspace))
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatalf("mkdir proj: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(projDir) })

	older := filepath.Join(projDir, "sess-old.jsonl")
	newer := filepath.Join(projDir, "sess-new.jsonl")
	for _, p := range []string{older, newer} {
		if err := os.WriteFile(p, []byte("{}\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(older, past, past); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	// Store points at the OLDER session, mimicking a stale entry after /clear.
	storeHome := t.TempDir()
	store := session.NewStore(storeHome)
	if err := store.Set("process:test", "sess-old"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	args := resolveSessionArgs(store, "process:test", workspace, 0, "")
	if len(args) != 2 || args[0] != "--resume" || args[1] != "sess-new" {
		t.Errorf("expected [--resume sess-new], got %v", args)
	}

	got, found, err := store.Get("process:test")
	if err != nil || !found {
		t.Fatalf("store.Get: err=%v found=%v", err, found)
	}
	if got != "sess-new" {
		t.Errorf("store not re-synced: got %q, want sess-new", got)
	}
}

// When no jsonl exists yet (brand-new process whose user hasn't sent a
// message), resolveSessionArgs should honor the stored ID with --resume so
// claude reuses the pre-issued session.
func TestResolveSessionArgs_NoJSONLUsesStored(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "leotest-resolve-stored")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	storeHome := t.TempDir()
	store := session.NewStore(storeHome)
	if err := store.Set("process:test", "sess-preissued"); err != nil {
		t.Fatalf("store.Set: %v", err)
	}

	args := resolveSessionArgs(store, "process:test", workspace, 0, "")
	if len(args) != 2 || args[0] != "--resume" || args[1] != "sess-preissued" {
		t.Errorf("expected [--resume sess-preissued], got %v", args)
	}
}

// With neither a jsonl nor a stored ID, resolveSessionArgs should mint a
// fresh session via --session-id and persist it.
func TestResolveSessionArgs_BrandNewMintsID(t *testing.T) {
	workspace := filepath.Join(t.TempDir(), "leotest-resolve-new")
	if err := os.MkdirAll(workspace, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	storeHome := t.TempDir()
	store := session.NewStore(storeHome)

	args := resolveSessionArgs(store, "process:test", workspace, 0, "")
	if len(args) != 2 || args[0] != "--session-id" || args[1] == "" {
		t.Errorf("expected [--session-id <id>], got %v", args)
	}

	got, found, err := store.Get("process:test")
	if err != nil || !found {
		t.Fatalf("store.Get: err=%v found=%v", err, found)
	}
	if got != args[1] {
		t.Errorf("store out of sync: got %q, want %q", got, args[1])
	}
}

func TestBuildProcessArgsInjectsMessagingAwareness(t *testing.T) {
	// HomePath set so AppendArg's EnsureConfig writes under a temp dir.
	cfg := &config.Config{HomePath: t.TempDir(), Web: config.WebConfig{Enabled: true}}
	args := buildProcessArgs(cfg, "assistant", config.ProcessConfig{})

	found := false
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--append-system-prompt" && strings.Contains(args[i+1], "leo_send_message") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected messaging awareness in process args; got %v", args)
	}
}

// TestSupervisableUnits verifies the supervisor's "is there anything to run"
// accounting counts persistent-task sessions, not just processes — so a home
// with only persistent tasks (no enabled processes) is still startable.
func TestSupervisableUnits(t *testing.T) {
	t.Run("session-only home reports a session", func(t *testing.T) {
		cfg := &config.Config{
			HomePath: t.TempDir(),
			Tasks: map[string]config.TaskConfig{
				"pinger": {Runtime: "persistent", Enabled: true, Workspace: "/tmp/p"},
			},
		}
		procs, sessions := supervisableUnits(cfg, "claude", "tok")
		if procs != 0 || sessions != 1 {
			t.Fatalf("procs=%d sessions=%d, want 0,1", procs, sessions)
		}
	})

	t.Run("empty home reports nothing", func(t *testing.T) {
		cfg := &config.Config{HomePath: t.TempDir()}
		procs, sessions := supervisableUnits(cfg, "claude", "tok")
		if procs != 0 || sessions != 0 {
			t.Fatalf("procs=%d sessions=%d, want 0,0", procs, sessions)
		}
	})
}

func TestBuildAllProcessSpecsProviderEnv(t *testing.T) {
	t.Setenv("LEO_TEST_GLM_KEY", "sk-glm")
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_GLM_KEY"},
		},
		Processes: map[string]config.ProcessConfig{
			"bot":   {Enabled: true, Provider: "glm"},
			"plain": {Enabled: true},
		},
		HomePath: t.TempDir(),
	}
	specs := buildAllProcessSpecs(cfg, "/usr/bin/claude", "")
	byName := map[string]service.ProcessSpec{}
	for _, s := range specs {
		byName[s.Name] = s
	}
	if got := byName["bot"].Env["ANTHROPIC_BASE_URL"]; got != "https://x.example" {
		t.Errorf("bot ANTHROPIC_BASE_URL = %q", got)
	}
	if got := byName["bot"].Env["ANTHROPIC_AUTH_TOKEN"]; got != "sk-glm" {
		t.Errorf("bot ANTHROPIC_AUTH_TOKEN = %q", got)
	}
	if _, ok := byName["plain"].Env["ANTHROPIC_BASE_URL"]; ok {
		t.Error("plain process must not get provider env")
	}
}

func TestBuildAllProcessSpecsSkipsUnresolvableProvider(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{
			"glm": {BaseURL: "https://x.example", APIKeyEnv: "LEO_TEST_DEFINITELY_UNSET_KEY"},
		},
		Processes: map[string]config.ProcessConfig{
			"bot":   {Enabled: true, Provider: "glm"},
			"plain": {Enabled: true},
		},
		HomePath: t.TempDir(),
	}
	specs := buildAllProcessSpecs(cfg, "/usr/bin/claude", "")
	if len(specs) != 1 || specs[0].Name != "plain" {
		t.Fatalf("expected only plain to survive, got %+v", specs)
	}
}
