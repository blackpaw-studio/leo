package setup

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/prereq"
	"github.com/blackpaw-studio/leo/internal/service"
)

// --- buildConfig ---

func TestBuildConfig_FreshWorkspace(t *testing.T) {
	cfg := buildConfig("/my/workspace", nil)

	if cfg.Defaults.Model != config.DefaultModel {
		t.Errorf("Defaults.Model = %q, want %q", cfg.Defaults.Model, config.DefaultModel)
	}
	if cfg.Defaults.MaxTurns != config.DefaultMaxTurns {
		t.Errorf("Defaults.MaxTurns = %d, want %d", cfg.Defaults.MaxTurns, config.DefaultMaxTurns)
	}
	proc, ok := cfg.Processes["assistant"]
	if !ok {
		t.Fatal("expected default 'assistant' process")
	}
	if proc.Workspace != "/my/workspace" {
		t.Errorf("process workspace = %q, want %q", proc.Workspace, "/my/workspace")
	}
	if !proc.Enabled {
		t.Error("default process should be enabled")
	}
	if proc.RemoteControl == nil || !*proc.RemoteControl {
		t.Error("default process should have remote_control enabled")
	}
	if len(proc.Channels) != 0 {
		t.Errorf("expected empty channels (channel-agnostic default), got %v", proc.Channels)
	}
}

func TestBuildConfig_PreservesExistingConfig(t *testing.T) {
	existing := &config.Config{
		Defaults: config.DefaultsConfig{
			Model:    "opus",
			MaxTurns: 99,
		},
		Processes: map[string]config.ProcessConfig{
			"custom": {
				Workspace: "/custom/ws",
				Channels:  []string{"plugin:telegram@claude-plugins-official"},
				Enabled:   true,
			},
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "0 * * * *", PromptFile: "x.md"},
		},
	}

	cfg := buildConfig("/my/workspace", existing)

	if cfg.Defaults.Model != "opus" {
		t.Errorf("Defaults.Model = %q, want 'opus'", cfg.Defaults.Model)
	}
	if _, ok := cfg.Processes["custom"]; !ok {
		t.Error("expected existing 'custom' process preserved")
	}
	if _, ok := cfg.Tasks["heartbeat"]; !ok {
		t.Error("expected existing 'heartbeat' task preserved")
	}
}

// --- parseUserProfile ---

func TestParseUserProfile_FullFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "USER.md")
	content := `# User Profile

## Name
Alice

## Role
Engineer

## About
Builds things

## Preferences
Dark mode

## Timezone
America/New_York
`
	os.WriteFile(path, []byte(content), 0644)

	got := parseUserProfile(path)
	if got.UserName != "Alice" {
		t.Errorf("UserName = %q, want %q", got.UserName, "Alice")
	}
	if got.Role != "Engineer" {
		t.Errorf("Role = %q, want %q", got.Role, "Engineer")
	}
	if got.About != "Builds things" {
		t.Errorf("About = %q, want %q", got.About, "Builds things")
	}
	if got.Preferences != "Dark mode" {
		t.Errorf("Preferences = %q, want %q", got.Preferences, "Dark mode")
	}
	if got.Timezone != "America/New_York" {
		t.Errorf("Timezone = %q, want %q", got.Timezone, "America/New_York")
	}
}

func TestParseUserProfile_MissingFile(t *testing.T) {
	got := parseUserProfile("/nonexistent/path.md")
	if got.UserName != "" {
		t.Errorf("UserName = %q, want empty for missing file", got.UserName)
	}
}

// --- checkWorkspaceWritable ---

func TestCheckWorkspaceWritable_Success(t *testing.T) {
	dir := t.TempDir()
	if err := checkWorkspaceWritable(filepath.Join(dir, "new")); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- findExistingConfig ---

func TestFindExistingConfig_None(t *testing.T) {
	dir := t.TempDir()
	cfg, defaultHome := findExistingConfig(dir)
	if cfg != nil {
		t.Error("expected nil config for empty home")
	}
	if defaultHome != filepath.Join(dir, ".leo") {
		t.Errorf("defaultHome = %q, want %q", defaultHome, filepath.Join(dir, ".leo"))
	}
}

func TestFindExistingConfig_Found(t *testing.T) {
	dir := t.TempDir()
	leoHome := filepath.Join(dir, ".leo")
	os.MkdirAll(leoHome, 0750)

	cfg := &config.Config{
		Defaults:  config.DefaultsConfig{Model: "sonnet", MaxTurns: 15},
		Processes: map[string]config.ProcessConfig{"assistant": {Enabled: true}},
	}
	if err := config.Save(filepath.Join(leoHome, "leo.yaml"), cfg); err != nil {
		t.Fatalf("saving config: %v", err)
	}

	got, _ := findExistingConfig(dir)
	if got == nil {
		t.Fatal("expected config to be found")
	}
	if got.Defaults.Model != "sonnet" {
		t.Errorf("Model = %q, want 'sonnet'", got.Defaults.Model)
	}
}

// --- scaffoldWorkspace ---

func TestScaffoldWorkspace_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	leoHome := filepath.Join(dir, ".leo")
	workspace := filepath.Join(leoHome, "workspace")

	cfg := buildConfig(workspace, nil)

	opts := scaffoldOptions{
		workspace: workspace,
		home:      dir,
		leoHome:   leoHome,
		cfg:       cfg,
		userPath:  filepath.Join(workspace, "USER.md"),
		userName:  "Test User",
		role:      "Developer",
		about:     "Writes tests",
		timezone:  "UTC",
	}

	if err := scaffoldWorkspace(opts); err != nil {
		t.Fatalf("scaffoldWorkspace() error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(leoHome, "leo.yaml")); err != nil {
		t.Errorf("expected leo.yaml in leo home: %v", err)
	}

	for _, rel := range []string{
		"USER.md",
		"CLAUDE.md",
		"skills/managing-tasks.md",
		"config/mcp-servers.json",
	} {
		if _, err := os.Stat(filepath.Join(workspace, rel)); err != nil {
			t.Errorf("expected %s in workspace: %v", rel, err)
		}
	}

	claudeData, err := os.ReadFile(filepath.Join(workspace, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if strings.Contains(string(claudeData), "Telegram Messaging Rules") {
		t.Error("CLAUDE.md should not reference telegram-specific rules")
	}
	if !strings.Contains(string(claudeData), "LEO_CHANNELS") {
		t.Error("CLAUDE.md should reference LEO_CHANNELS env var")
	}
}

// --- checkPrerequisites ---

func TestCheckPrerequisites_ClaudeMissing(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult { return prereq.ClaudeResult{OK: false} }
	checkTmuxFn = func() bool { return true }

	if err := checkPrerequisites(); err == nil {
		t.Error("expected error when claude missing")
	}
}

func TestCheckPrerequisites_TmuxMissing(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Path: "/usr/bin/claude", Version: "1.0.0"}
	}
	checkTmuxFn = func() bool { return false }

	if err := checkPrerequisites(); err == nil {
		t.Error("expected error when tmux missing")
	}
}

func TestCheckPrerequisites_AllPresent(t *testing.T) {
	origClaude := checkClaudeFn
	origTmux := checkTmuxFn
	t.Cleanup(func() {
		checkClaudeFn = origClaude
		checkTmuxFn = origTmux
	})

	checkClaudeFn = func() prereq.ClaudeResult {
		return prereq.ClaudeResult{OK: true, Path: "/usr/bin/claude", Version: "1.0.0"}
	}
	checkTmuxFn = func() bool { return true }

	if err := checkPrerequisites(); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// --- installDaemon ---

func TestInstallDaemon_Success(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{"PATH": "/usr/bin"} }
	installDaemonFn = func(sc service.ServiceConfig) error { return nil }
	daemonStatusFn = func() (string, error) { return "running", nil }

	installDaemon("/tmp/workspace", "/tmp/workspace/leo.yaml")
}

func TestInstallDaemon_Failure(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
	})

	osExecutableFn = func() (string, error) { return "/usr/local/bin/leo", nil }
	envCaptureFn = func() map[string]string { return map[string]string{} }
	installDaemonFn = func(sc service.ServiceConfig) error { return fmt.Errorf("install failed") }

	installDaemon("/tmp/workspace", "/tmp/workspace/leo.yaml")
}

func TestInstallDaemon_NoExecutable(t *testing.T) {
	origExec := osExecutableFn
	origEnv := envCaptureFn
	origInstall := installDaemonFn
	origStatus := daemonStatusFn
	t.Cleanup(func() {
		osExecutableFn = origExec
		envCaptureFn = origEnv
		installDaemonFn = origInstall
		daemonStatusFn = origStatus
	})

	osExecutableFn = func() (string, error) { return "", fmt.Errorf("no executable") }
	envCaptureFn = func() map[string]string { return map[string]string{} }

	var capturedSC service.ServiceConfig
	installDaemonFn = func(sc service.ServiceConfig) error {
		capturedSC = sc
		return nil
	}
	daemonStatusFn = func() (string, error) { return "running", nil }

	installDaemon("/tmp/ws", "/tmp/ws/leo.yaml")
	if capturedSC.LeoPath != "leo" {
		t.Errorf("LeoPath = %q, want %q (fallback)", capturedSC.LeoPath, "leo")
	}
}

// --- client setup ---

func TestPromptSetupMode_DefaultsToServerForFreshInstall(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\n"))
	if got := promptSetupMode(reader, nil); got {
		t.Errorf("fresh install: got client, want server")
	}
}

func TestPromptSetupMode_DefaultsToClientForClientOnlyConfig(t *testing.T) {
	existing := &config.Config{
		Client: config.ClientConfig{
			DefaultHost: "olympus",
			Hosts: map[string]config.HostConfig{
				"olympus": {SSH: "evan@olympus.local"},
			},
		},
	}
	reader := bufio.NewReader(strings.NewReader("\n"))
	if got := promptSetupMode(reader, existing); !got {
		t.Errorf("client-only config: got server, want client")
	}
}

func TestPromptSetupMode_ExplicitChoiceOverridesDefault(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("2\n"))
	if got := promptSetupMode(reader, nil); !got {
		t.Errorf("explicit '2': got server, want client")
	}
}

func TestBuildClientConfig_FreshInstall(t *testing.T) {
	host := config.HostConfig{SSH: "evan@olympus.local"}
	cfg := buildClientConfig(nil, "olympus", host, "olympus")

	if cfg.Client.DefaultHost != "olympus" {
		t.Errorf("DefaultHost = %q, want %q", cfg.Client.DefaultHost, "olympus")
	}
	if got := cfg.Client.Hosts["olympus"].SSH; got != "evan@olympus.local" {
		t.Errorf("Hosts[olympus].SSH = %q, want %q", got, "evan@olympus.local")
	}
	if cfg.Processes != nil {
		t.Errorf("fresh client install should leave Processes nil, got %v", cfg.Processes)
	}
	if cfg.Tasks != nil {
		t.Errorf("fresh client install should leave Tasks nil, got %v", cfg.Tasks)
	}
}

func TestBuildClientConfig_PreservesExistingServerConfig(t *testing.T) {
	existing := &config.Config{
		Defaults: config.DefaultsConfig{Model: "opus"},
		Processes: map[string]config.ProcessConfig{
			"assistant": {Workspace: "/ws"},
		},
		Tasks: map[string]config.TaskConfig{
			"heartbeat": {Schedule: "0 * * * *", PromptFile: "x.md"},
		},
	}
	host := config.HostConfig{SSH: "evan@olympus.local"}
	cfg := buildClientConfig(existing, "olympus", host, "olympus")

	if _, ok := cfg.Processes["assistant"]; !ok {
		t.Error("existing process should be preserved")
	}
	if _, ok := cfg.Tasks["heartbeat"]; !ok {
		t.Error("existing task should be preserved")
	}
	if cfg.Defaults.Model != "opus" {
		t.Errorf("Defaults.Model = %q, want %q", cfg.Defaults.Model, "opus")
	}
	if cfg.Client.DefaultHost != "olympus" {
		t.Errorf("DefaultHost = %q, want %q", cfg.Client.DefaultHost, "olympus")
	}
}

func TestTestSSHConnectivity_Success(t *testing.T) {
	orig := sshExecFn
	t.Cleanup(func() { sshExecFn = orig })

	var capturedArgs []string
	sshExecFn = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedArgs = append([]string{name}, args...)
		return exec.CommandContext(ctx, "true")
	}

	host := config.HostConfig{SSH: "evan@olympus.local", SSHArgs: []string{"-p", "2222"}}
	if err := testSSHConnectivity(host); err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	want := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=8",
		"-p", "2222",
		"evan@olympus.local", config.DefaultRemoteLeoPath, "version",
	}
	if !reflect.DeepEqual(capturedArgs, want) {
		t.Errorf("args = %v, want %v", capturedArgs, want)
	}
}

func TestTestSSHConnectivity_FailureIncludesStderr(t *testing.T) {
	orig := sshExecFn
	t.Cleanup(func() { sshExecFn = orig })

	sshExecFn = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo 'Permission denied' >&2; exit 1")
	}

	host := config.HostConfig{SSH: "evan@olympus.local"}
	err := testSSHConnectivity(host)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "Permission denied") {
		t.Errorf("error should include stderr, got %q", err.Error())
	}
}

// Hybrid config (hosts + processes) must default to server mode so a
// re-run can't silently clobber a server install with a client-only config.
func TestPromptSetupMode_DefaultsToServerForHybridConfig(t *testing.T) {
	existing := &config.Config{
		Client: config.ClientConfig{
			DefaultHost: "olympus",
			Hosts:       map[string]config.HostConfig{"olympus": {SSH: "evan@olympus.local"}},
		},
		Processes: map[string]config.ProcessConfig{"assistant": {Workspace: "/ws"}},
	}
	reader := bufio.NewReader(strings.NewReader("\n"))
	if got := promptSetupMode(reader, existing); got {
		t.Errorf("hybrid config: got client, want server")
	}
}

// buildClientConfig must not alias maps back into the caller's existing
// config — a shallow struct copy would share the underlying map, causing
// silent cross-mutation.
func TestBuildClientConfig_DoesNotMutateExisting(t *testing.T) {
	existing := &config.Config{
		Processes: map[string]config.ProcessConfig{"assistant": {Workspace: "/ws"}},
		Tasks:     map[string]config.TaskConfig{"heartbeat": {Schedule: "0 * * * *", PromptFile: "x.md"}},
		Client: config.ClientConfig{
			DefaultHost: "olympus",
			Hosts:       map[string]config.HostConfig{"olympus": {SSH: "evan@olympus.local"}},
		},
	}
	buildClientConfig(existing, "new-host", config.HostConfig{SSH: "u@new"}, "new-host")

	if _, ok := existing.Client.Hosts["new-host"]; ok {
		t.Error("buildClientConfig mutated existing.Client.Hosts (aliasing bug)")
	}
	if len(existing.Client.Hosts) != 1 {
		t.Errorf("existing.Client.Hosts size changed: got %d, want 1", len(existing.Client.Hosts))
	}
	if existing.Client.DefaultHost != "olympus" {
		t.Errorf("existing.Client.DefaultHost mutated: got %q", existing.Client.DefaultHost)
	}
}

// resolveDefaultHost owns the "replace existing default?" decision. It
// must preserve the prior default when the user declines and use the new
// nickname otherwise (including when there is no existing default).
func TestResolveDefaultHost(t *testing.T) {
	tests := []struct {
		name        string
		existing    *config.Config
		answer      string
		wantDefault string
	}{
		{
			name:        "no existing config → new nickname",
			existing:    nil,
			wantDefault: "new-host",
		},
		{
			name:        "existing with no DefaultHost → new nickname",
			existing:    &config.Config{},
			wantDefault: "new-host",
		},
		{
			name: "existing DefaultHost == nickname → nickname (no prompt)",
			existing: &config.Config{
				Client: config.ClientConfig{DefaultHost: "new-host"},
			},
			wantDefault: "new-host",
		},
		{
			name: "accept replacement",
			existing: &config.Config{
				Client: config.ClientConfig{DefaultHost: "olympus"},
			},
			answer:      "y\n",
			wantDefault: "new-host",
		},
		{
			name: "decline replacement",
			existing: &config.Config{
				Client: config.ClientConfig{DefaultHost: "olympus"},
			},
			answer:      "n\n",
			wantDefault: "olympus",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := bufio.NewReader(strings.NewReader(tc.answer))
			got := resolveDefaultHost(reader, tc.existing, "new-host")
			if got != tc.wantDefault {
				t.Errorf("got %q, want %q", got, tc.wantDefault)
			}
		})
	}
}

// promptClientHost should round-trip the SSH port stored in an existing
// config's SSHArgs (["-p", "2222"]) as the default for the port prompt,
// and re-emit it in the returned HostConfig.SSHArgs.
func TestPromptClientHost_PortRoundTrip(t *testing.T) {
	existing := &config.Config{
		Client: config.ClientConfig{
			DefaultHost: "olympus",
			Hosts: map[string]config.HostConfig{
				"olympus": {
					SSH:     "evan@olympus.local",
					SSHArgs: []string{"-p", "2222"},
					LeoPath: "/opt/leo",
				},
			},
		},
	}
	// Blank answers accept all defaults (nickname, ssh, port, leo path).
	reader := bufio.NewReader(strings.NewReader("\n\n\n\n"))
	nickname, host, err := promptClientHost(reader, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if nickname != "olympus" {
		t.Errorf("nickname = %q, want %q", nickname, "olympus")
	}
	if host.SSH != "evan@olympus.local" {
		t.Errorf("SSH = %q, want %q", host.SSH, "evan@olympus.local")
	}
	if host.LeoPath != "/opt/leo" {
		t.Errorf("LeoPath = %q, want %q", host.LeoPath, "/opt/leo")
	}
	want := []string{"-p", "2222"}
	if !reflect.DeepEqual(host.SSHArgs, want) {
		t.Errorf("SSHArgs = %v, want %v", host.SSHArgs, want)
	}
}

// A non-numeric port in existing config must not panic, must not silently
// become 0 (which would drop a real port the user had set), and must warn.
func TestPromptClientHost_InvalidExistingPortIgnored(t *testing.T) {
	existing := &config.Config{
		Client: config.ClientConfig{
			DefaultHost: "olympus",
			Hosts: map[string]config.HostConfig{
				"olympus": {
					SSH:     "evan@olympus.local",
					SSHArgs: []string{"-p", "not-a-port"},
				},
			},
		},
	}
	// Blank answers accept all defaults; with invalid port default = 0,
	// PromptInt returns 0 and no SSHArgs should be emitted.
	reader := bufio.NewReader(strings.NewReader("\n\n\n\n"))
	_, host, err := promptClientHost(reader, existing)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(host.SSHArgs) != 0 {
		t.Errorf("SSHArgs = %v, want empty (invalid existing port should be discarded)", host.SSHArgs)
	}
}

// With a closed stdin (empty reader) and no existing config to provide a
// nickname default, promptClientHost must return io.EOF instead of
// spinning forever on PromptNonEmpty's retry loop.
func TestPromptClientHost_EOFStopsRetryLoop(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		reader := bufio.NewReader(strings.NewReader(""))
		_, _, err := promptClientHost(reader, nil)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected error on closed stdin, got nil")
		}
		if !errors.Is(err, io.EOF) {
			t.Errorf("error should wrap io.EOF, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("promptClientHost did not return within 2s — retry loop is spinning on EOF")
	}
}
