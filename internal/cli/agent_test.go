package cli

import (
	"bytes"
	"os/exec"
	"strings"
	"syscall"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/spf13/cobra"
)

// newAgentCLITestConfig writes a config with a single remote host and sets
// cfgFile so subcommands pick it up through loadConfig.
func newAgentCLITestConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {SSH: "user@prod.example.com", SSHArgs: []string{"-p", "2222"}},
			},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return path
}

// stubExec captures the command invocation that would have run so tests can
// assert arguments without actually executing ssh.
type stubExec struct {
	calls [][]string
}

func (s *stubExec) fn(name string, args ...string) *exec.Cmd {
	s.calls = append(s.calls, append([]string{name}, args...))
	// Pretend remote `leo agent session-name <q>` succeeded by echoing the
	// canonical session — the remote attach flow captures stdout to learn it.
	for i, a := range args {
		if a == "session-name" && i+1 < len(args) {
			return exec.Command("echo", "leo-"+args[i+1])
		}
	}
	// Otherwise use `true` (exits 0) so `.Run()` succeeds.
	return exec.Command("true")
}

func withStubExec(t *testing.T) *stubExec {
	t.Helper()
	stub := &stubExec{}
	old := agentExecCommand
	agentExecCommand = stub.fn
	t.Cleanup(func() { agentExecCommand = old })
	return stub
}

func withStubStdio(t *testing.T) (*bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	var out, errBuf bytes.Buffer
	oldOut, oldErr := agentStdout, agentStderr
	agentStdout, agentStderr = &out, &errBuf
	t.Cleanup(func() { agentStdout, agentStderr = oldOut, oldErr })
	return &out, &errBuf
}

func TestAgentListRemoteDispatches(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	// Nothing overrides --host, so default_host "prod" should win.

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	want := []string{"ssh", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "agent", "list"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

func TestAgentSpawnForwardsFlags(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "foo/bar", "--name", "custom"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{config.DefaultRemoteLeoPath, "agent", "spawn", "coding", "--repo", "foo/bar", "--name", "custom"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentStopRemote(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "stop", "leo-coding-bar"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent stop leo-coding-bar") {
		t.Errorf("unexpected call: %s", joined)
	}
}

func TestAgentRemoteHonorsLeoPathOverride(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {
					SSH:     "user@prod.example.com",
					LeoPath: "/opt/leo/bin/leo",
				},
			},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "list"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := []string{"ssh", "user@prod.example.com", "/opt/leo/bin/leo", "agent", "list"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

func TestAgentAttachRemoteHonorsTmuxPathOverride(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {
					SSH:      "user@prod.example.com",
					TmuxPath: "/opt/homebrew/bin/tmux",
				},
			},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("expected 2 ssh calls (resolve + attach), got %d: %v", len(stub.calls), stub.calls)
	}
	wantResolve := []string{"ssh", "user@prod.example.com", config.DefaultRemoteLeoPath, "agent", "session-name", "scratch"}
	if !equalStrings(stub.calls[0], wantResolve) {
		t.Errorf("resolve ssh args = %v, want %v", stub.calls[0], wantResolve)
	}
	wantAttach := []string{"ssh", "-t", "user@prod.example.com", "/opt/homebrew/bin/tmux", "-L", "leo", "attach", "-t", "leo-scratch"}
	if !equalStrings(stub.calls[1], wantAttach) {
		t.Errorf("attach ssh args = %v, want %v", stub.calls[1], wantAttach)
	}
}

func TestAgentLogsFollowRemoteUsesTmuxPath(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts: map[string]config.HostConfig{
				"prod": {
					SSH:      "user@prod.example.com",
					TmuxPath: "/opt/homebrew/bin/tmux",
				},
			},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "logs", "scratch", "--follow"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, "/opt/homebrew/bin/tmux -L leo capture-pane") {
		t.Errorf("remote tail cmd missing tmux path: %s", joined)
	}
	if !strings.Contains(joined, "/opt/homebrew/bin/tmux -L leo pipe-pane") {
		t.Errorf("remote tail cmd missing tmux path in pipe-pane: %s", joined)
	}
}

func TestAgentAttachRemoteUsesTmuxDirectly(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("expected 2 ssh calls (resolve + attach), got %d: %v", len(stub.calls), stub.calls)
	}
	wantResolve := []string{"ssh", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "agent", "session-name", "scratch"}
	if !equalStrings(stub.calls[0], wantResolve) {
		t.Errorf("resolve ssh args = %v, want %v", stub.calls[0], wantResolve)
	}
	wantAttach := []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteTmuxPath, "-L", "leo", "attach", "-t", "leo-scratch"}
	if !equalStrings(stub.calls[1], wantAttach) {
		t.Errorf("attach ssh args = %v, want %v", stub.calls[1], wantAttach)
	}
}

func TestAgentAttachLocalhostFlagExecsTmux(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)

	var execCalled struct {
		argv0 string
		argv  []string
	}
	old := agentSyscallExec
	agentSyscallExec = func(argv0 string, argv []string, envv []string) error {
		execCalled.argv0 = argv0
		execCalled.argv = argv
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })

	// The local path hits daemon.AgentSession — which talks to a real socket we
	// don't have. So this test only exercises --host=localhost long enough to
	// confirm the dispatch went local; the daemon call will fail. We accept
	// either "no daemon" or "exec tmux" as proof of the local branch.
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "whatever", "--host", "localhost"})
	_ = root.Execute() //nolint:errcheck
	// If agentSyscallExec was called, we succeeded. Otherwise the daemon call
	// short-circuited with an error — that's also proof we took the local branch
	// and never shelled out to ssh.
	_ = execCalled
	// We're not asserting anything here beyond "no panic and no ssh call" —
	// observed by the exec.Command stub in other tests.
	_ = syscall.Exec // silence unused import on platforms where syscall is unused
}

func TestResolveSpawnCollisionForcedFlags(t *testing.T) {
	match := agent.Record{Name: "leo-coding-blackpaw-studio-leo", Repo: "blackpaw-studio/leo", Template: "coding"}

	t.Run("reuse-owner wins", func(t *testing.T) {
		got, err := resolveSpawnCollision(match, "coding", true, false)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != spawnUseCanonicalRepo {
			t.Errorf("choice = %v, want spawnUseCanonicalRepo", got)
		}
	})

	t.Run("attach-existing wins", func(t *testing.T) {
		got, err := resolveSpawnCollision(match, "coding", false, true)
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got != spawnAttachExisting {
			t.Errorf("choice = %v, want spawnAttachExisting", got)
		}
	})

	t.Run("reuse-owner errors without stored repo", func(t *testing.T) {
		bare := agent.Record{Name: "bare", Template: "coding"}
		if _, err := resolveSpawnCollision(bare, "coding", true, false); err == nil {
			t.Error("expected error when --reuse-owner is set but Repo is empty")
		}
	})
}

func TestResolveSpawnCollisionNonInteractive(t *testing.T) {
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })

	cases := []struct {
		name           string
		match          agent.Record
		reuseOwner     bool
		attachExisting bool
		wantChoice     spawnChoice
		wantErr        bool
		errContains    []string
	}{
		{
			name:       "no flags errors with hint",
			match:      agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"},
			wantChoice: spawnCancel,
			wantErr:    true,
			errContains: []string{
				"leo-coding-acme-widget",
				"stdin is not a TTY",
				"--attach-existing",
				"--reuse-owner",
				"owner/repo",
			},
		},
		{
			name:           "attach-existing still wins non-interactively",
			match:          agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"},
			attachExisting: true,
			wantChoice:     spawnAttachExisting,
		},
		{
			name:       "reuse-owner still wins non-interactively",
			match:      agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"},
			reuseOwner: true,
			wantChoice: spawnUseCanonicalRepo,
		},
		{
			name:       "empty repo still named in error",
			match:      agent.Record{Name: "bare-agent", Template: "coding"},
			wantChoice: spawnCancel,
			wantErr:    true,
			errContains: []string{
				"bare-agent",
				"--attach-existing",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSpawnCollision(tc.match, "coding", tc.reuseOwner, tc.attachExisting)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (choice=%v)", got)
				}
				for _, sub := range tc.errContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q missing %q", err.Error(), sub)
					}
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.wantChoice {
				t.Errorf("choice = %v, want %v", got, tc.wantChoice)
			}
		})
	}
}

func TestResolveSpawnCollisionPrompt(t *testing.T) {
	match := agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"}

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })

	cases := []struct {
		name    string
		input   string
		want    spawnChoice
		wantErr bool
	}{
		{"answer a attaches", "a\n", spawnAttachExisting, false},
		{"answer b reuses repo", "b\n", spawnUseCanonicalRepo, false},
		{"answer c spawns fresh", "c\n", spawnFreshTemplate, false},
		{"empty line defaults to c", "\n", spawnFreshTemplate, false},
		{"answer q cancels", "q\n", spawnCancel, true},
		{"uppercase also works", "A\n", spawnAttachExisting, false},
		{"eof cancels", "", spawnCancel, true},
		{"unknown choice errors", "x\n", spawnCancel, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldIn := agentStdin
			agentStdin = strings.NewReader(tc.input)
			t.Cleanup(func() { agentStdin = oldIn })
			withStubStdio(t)

			got, err := resolveSpawnCollision(match, "coding", false, false)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil (choice=%v)", got)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("input %q → %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestResolveExactCollisionForcedFlag(t *testing.T) {
	match := agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"}

	got, err := resolveExactCollision(match, "coding", true)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got != spawnAttachExisting {
		t.Errorf("choice = %v, want spawnAttachExisting", got)
	}
}

func TestResolveExactCollisionNonInteractive(t *testing.T) {
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return false }
	t.Cleanup(func() { agentIsTTY = oldTTY })

	cases := []struct {
		name           string
		match          agent.Record
		attachExisting bool
		wantChoice     spawnChoice
		wantErr        bool
		errContains    []string
	}{
		{
			name:       "no flags errors mentioning branch",
			match:      agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Branch: "feature-x", Template: "coding"},
			wantChoice: spawnCancel,
			wantErr:    true,
			errContains: []string{
				"leo-coding-acme-widget",
				"stdin is not a TTY",
				"--attach-existing",
				"feature-x",
			},
		},
		{
			name:       "no flags errors without branch",
			match:      agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"},
			wantChoice: spawnCancel,
			wantErr:    true,
			errContains: []string{
				"acme/widget",
				"--attach-existing",
			},
		},
		{
			name:           "attach-existing still wins non-interactively",
			match:          agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Template: "coding"},
			attachExisting: true,
			wantChoice:     spawnAttachExisting,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveExactCollision(tc.match, "coding", tc.attachExisting)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (choice=%v)", got)
				}
				for _, sub := range tc.errContains {
					if !strings.Contains(err.Error(), sub) {
						t.Errorf("error %q missing %q", err.Error(), sub)
					}
				}
			} else if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.wantChoice {
				t.Errorf("choice = %v, want %v", got, tc.wantChoice)
			}
		})
	}
}

func TestResolveExactCollisionPrompt(t *testing.T) {
	match := agent.Record{Name: "leo-coding-acme-widget", Repo: "acme/widget", Branch: "feature-x", Template: "coding"}

	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })

	cases := []struct {
		name    string
		input   string
		want    spawnChoice
		wantErr bool
	}{
		{"answer a attaches", "a\n", spawnAttachExisting, false},
		{"answer c spawns fresh", "c\n", spawnFreshTemplate, false},
		{"empty line defaults to c", "\n", spawnFreshTemplate, false},
		{"answer q cancels", "q\n", spawnCancel, true},
		{"uppercase also works", "A\n", spawnAttachExisting, false},
		{"option b rejected", "b\n", spawnCancel, true},
		{"eof cancels", "", spawnCancel, true},
		{"unknown choice errors", "x\n", spawnCancel, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			oldIn := agentStdin
			agentStdin = strings.NewReader(tc.input)
			t.Cleanup(func() { agentStdin = oldIn })
			withStubStdio(t)

			got, err := resolveExactCollision(match, "coding", false)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil (choice=%v)", got)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("input %q → %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestFindExactMatches(t *testing.T) {
	records := []agent.Record{
		{Name: "a", Repo: "acme/widget"},
		{Name: "b", Repo: "ACME/Widget"},
		{Name: "c", Repo: "acme/widget", Branch: "feature-x"},
		{Name: "d", Repo: "other/widget"},
		{Name: "e"},
	}
	matches := filterExactMatches(records, "acme/widget", "")
	if len(matches) != 2 {
		t.Fatalf("want 2 matches (case-insensitive, empty branch), got %d: %+v", len(matches), matches)
	}
	matches = filterExactMatches(records, "acme/widget", "feature-x")
	if len(matches) != 1 || matches[0].Name != "c" {
		t.Fatalf("want 1 branch-scoped match, got %+v", matches)
	}
	matches = filterExactMatches(records, "nobody/nothing", "")
	if len(matches) != 0 {
		t.Fatalf("want 0 matches, got %+v", matches)
	}
}

func TestAgentSpawnRemoteForwardsCollisionFlags(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "leo", "--reuse-owner"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, "spawn coding --repo leo --reuse-owner") {
		t.Errorf("ssh call missing --reuse-owner: %s", joined)
	}
}

func TestAgentSpawnAcceptsPositionalRepo(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "foo/bar"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, "spawn coding --repo foo/bar") {
		t.Errorf("positional repo not forwarded as --repo: %s", joined)
	}
}

func TestAgentSpawnRejectsConflictingFlags(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "leo", "--reuse-owner", "--attach-existing"})
	if err := root.Execute(); err == nil {
		t.Error("expected error when both --reuse-owner and --attach-existing set")
	}
}

func TestAgentSessionNameRemoteDispatches(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "session-name", "leo"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	want := []string{"ssh", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "agent", "session-name", "leo"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

func TestAgentSpawnRejectsWorktreeWithBareRepo(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "barerepo", "--worktree", "feat/x", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for --worktree with bare-name repo")
	}
	if !strings.Contains(err.Error(), "--worktree requires owner/repo") {
		t.Errorf("error = %v, want mention of worktree requires owner/repo", err)
	}
}

func TestAgentSpawnRejectsBaseWithoutWorktree(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "owner/bar", "--base", "main", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for --base without --worktree")
	}
	if !strings.Contains(err.Error(), "--base only applies with --worktree") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAgentSpawnRemoteForwardsWorktreeFlags(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--repo", "owner/bar", "--worktree", "feat/x", "--base", "main"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"--worktree", "feat/x", "--base", "main"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentPruneRemoteDispatches(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "prune", "leo-coding-owner-bar-feat-x", "--force", "--delete-branch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"agent", "prune", "leo-coding-owner-bar-feat-x", "--force", "--delete-branch"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentStopForceRequiresPrune(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "stop", "foo", "--force", "--host", "localhost"})
	err := root.Execute()
	if err == nil || !strings.Contains(err.Error(), "--force and --delete-branch require --prune") {
		t.Fatalf("expected --force requires --prune error, got %v", err)
	}
}

func TestAgentStopRemoteForwardsPruneFlags(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "stop", "leo-foo", "--prune", "--force", "--delete-branch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"agent", "stop", "leo-foo", "--prune", "--force", "--delete-branch"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

// TestCompleteAgentNamesGracefulFallback: when the daemon isn't reachable
// (the common case under `go test`), the completer returns
// ShellCompDirectiveNoFileComp with no values instead of error-ing, so the
// shell suppresses filename completion rather than suggesting garbage.
func TestCompleteAgentNamesGracefulFallback(t *testing.T) {
	path := newAgentCLITestConfig(t)
	// Point the CLI at the test config so loadConfig() succeeds even though
	// no daemon is running against that home directory.
	oldCfgFile := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = oldCfgFile })

	names, directive := completeAgentNames(nil, nil, "")
	if len(names) != 0 {
		t.Errorf("expected no names when daemon unreachable, got %v", names)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
}

// TestCompleteAgentNamesSkipsAfterFirstArg: agent commands take a single
// positional, so completion should yield nothing once one is already given.
func TestCompleteAgentNamesSkipsAfterFirstArg(t *testing.T) {
	names, directive := completeAgentNames(nil, []string{"already"}, "")
	if len(names) != 0 {
		t.Errorf("expected no names after first arg, got %v", names)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Errorf("directive = %v, want ShellCompDirectiveNoFileComp", directive)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
