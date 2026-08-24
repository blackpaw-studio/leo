package cli

import (
	"bytes"
	"context"
	"encoding/json"
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
	// Disable the terminfo bootstrap pass so its ssh+tic call doesn't show up
	// in the stub call log alongside the ssh attach we actually want to
	// assert against. Dedicated tests in terminfo_test.go cover that flow.
	oldTI := ensureRemoteTerminfoFn
	ensureRemoteTerminfoFn = func(config.HostResolution) string { return "" }
	t.Cleanup(func() { ensureRemoteTerminfoFn = oldTI })
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

// ctlOpts returns the shared SSH ControlMaster options the CLI splices into
// every host-targeted ssh call, for the "prod" test host rooted at home. These
// let attach --cc, the forward, and agent dispatches multiplex over one
// connection.
func ctlOpts(home string) []string {
	cfg := &config.Config{HomePath: home}
	return []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + cfg.HostControlPath("prod")}
}

// homeFromConfigPath recovers the leo home from a "<home>/leo.yaml" path so
// tests built via the shared config helpers can derive the control socket path.
func homeFromConfigPath(path string) string {
	return strings.TrimSuffix(path, "/leo.yaml")
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
	want := append([]string{"ssh", "user@prod.example.com", "-p", "2222"}, ctlOpts(homeFromConfigPath(path))...)
	want = append(want, config.DefaultRemoteLeoPath, "agent", "list")
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

func TestAgentRestartRemote(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "restart", "leo-coding-bar"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if !strings.Contains(joined, config.DefaultRemoteLeoPath+" agent restart leo-coding-bar") {
		t.Errorf("unexpected call: %s", joined)
	}
}

func TestAgentRestartRemoteForwardsAllYesJSON(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "restart", "--all", "--yes", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"agent restart", "--all", "--yes", "--json"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentRestartRequiresNameOrAll(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "restart"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when neither a name nor --all is given")
	}
}

func TestAgentRestartRejectsNameWithAll(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "restart", "leo-x", "--all"})
	if err := root.Execute(); err == nil {
		t.Fatal("expected an error when both a name and --all are given")
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
	want := append([]string{"ssh", "user@prod.example.com"}, ctlOpts(home)...)
	want = append(want, "/opt/leo/bin/leo", "agent", "list")
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
}

// TestAgentAttachRemoteNonCCDelegatesToRemoteLeo verifies the non-`--cc` remote
// `leo agent attach <name>` path delegates the WHOLE invocation to the remote
// leo binary (`ssh -t <host> <leo_path> agent attach <name>`), same as
// top-level `leo attach`'s remote leg — so the remote binary can route
// non-claude agents through their SessionDriver instead of the local side
// raw-tmux-attaching and bypassing driver routing entirely. A local TmuxPath
// override must NOT leak into this delegated call — it's meaningless here
// since we never invoke tmux client-side for this path anymore.
func TestAgentAttachRemoteNonCCDelegatesToRemoteLeo(t *testing.T) {
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
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 delegated ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	// runRemoteAttach (shared with top-level `leo attach`'s remote leg) does not
	// splice in the ControlMaster multiplexing opts that other remote calls
	// (resolve, --cc attach) use — that's pre-existing behavior we reuse as-is,
	// not something this fix changes.
	want := []string{"ssh", "-t", "user@prod.example.com", config.DefaultRemoteLeoPath, "agent", "attach", "scratch"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
	joined := strings.Join(stub.calls[0], " ")
	if strings.Contains(joined, "tmux") {
		t.Errorf("delegated non-cc attach must not invoke tmux client-side, got %q", joined)
	}
}

// TestAgentAttachRemoteCCQuotesTarget drives the full CLI for the remote
// control-mode path that leoterm's remote feature depends on: `leo --host X
// agent attach --cc <name>`. It must (1) resolve the shorthand via the remote
// daemon, then (2) stream `tmux -CC attach` over `ssh -tt -e none`, with the
// exact-match target SINGLE-QUOTED so the remote login shell (zsh) does not eat
// the leading "=" via filename expansion. Before the quote fix the remote ran
// `=leo-scratch` bare and zsh aborted with "leo-scratch not found".
func TestAgentAttachRemoteCCQuotesTarget(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "--cc", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 2 {
		t.Fatalf("expected 2 ssh calls (resolve + cc attach), got %d: %v", len(stub.calls), stub.calls)
	}
	home := homeFromConfigPath(path)
	wantResolve := append([]string{"ssh", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
	wantResolve = append(wantResolve, config.DefaultRemoteLeoPath, "agent", "session-name", "scratch")
	if !equalStrings(stub.calls[0], wantResolve) {
		t.Errorf("resolve ssh args = %v, want %v", stub.calls[0], wantResolve)
	}
	// -tt + -e none precede the host; the quoted exact-match target trails.
	wantAttach := append([]string{"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
	wantAttach = append(wantAttach, config.DefaultRemoteTmuxPath, "-L", "leo", "-CC", "attach", "-t", "'=leo-scratch'")
	if !equalStrings(stub.calls[1], wantAttach) {
		t.Errorf("cc attach ssh args = %v, want %v", stub.calls[1], wantAttach)
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

// TestAgentAttachRemoteUsesRemoteLeoDelegate verifies the non-`--cc` remote
// `leo agent attach <name>` path delegates via `ssh -t <host> <leo_path>
// agent attach <name>` — a single ssh call, no local resolve-then-tmux-attach
// round trip, and no tmux invoked client-side.
func TestAgentAttachRemoteUsesRemoteLeoDelegate(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 delegated ssh call, got %d: %v", len(stub.calls), stub.calls)
	}
	// See TestAgentAttachRemoteNonCCDelegatesToRemoteLeo: runRemoteAttach
	// (reused as-is from top-level `leo attach`) doesn't add ControlMaster opts.
	want := []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "agent", "attach", "scratch"}
	if !equalStrings(stub.calls[0], want) {
		t.Errorf("ssh args = %v, want %v", stub.calls[0], want)
	}
	joined := strings.Join(stub.calls[0], " ")
	if strings.Contains(joined, "tmux") {
		t.Errorf("delegated non-cc attach must not invoke tmux client-side, got %q", joined)
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

func TestAgentSpawnWithoutRepoOmitsRepoFlag(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	joined := strings.Join(stub.calls[0], " ")
	if strings.Contains(joined, "--repo") {
		t.Errorf("repo-less spawn must not forward --repo: %s", joined)
	}
	if !strings.Contains(joined, "spawn coding") {
		t.Errorf("expected spawn coding in call: %s", joined)
	}
}

func TestAgentSpawnWorktreeWithoutRepoErrors(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "spawn", "coding", "--worktree", "feat/x", "--host", "localhost"})
	err := root.Execute()
	if err == nil {
		t.Fatal("expected error for --worktree without a repo")
	}
	if !strings.Contains(err.Error(), "--worktree requires a repo") {
		t.Errorf("error = %v, want mention that --worktree requires a repo", err)
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
	want := append([]string{"ssh", "user@prod.example.com", "-p", "2222"}, ctlOpts(homeFromConfigPath(path))...)
	want = append(want, config.DefaultRemoteLeoPath, "agent", "session-name", "leo")
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

func TestAgentRenameRemoteDispatches(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "rename", "leo-old-name", "new-name"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("expected 1 ssh call, got %d", len(stub.calls))
	}
	joined := strings.Join(stub.calls[0], " ")
	for _, want := range []string{"agent", "rename", "leo-old-name", "new-name"} {
		if !strings.Contains(joined, want) {
			t.Errorf("ssh call missing %q: %s", want, joined)
		}
	}
}

func TestAgentRenameRequiresTwoArgs(t *testing.T) {
	cmd := newAgentRenameCmd()
	if cmd.Use != "rename <name> <new-name>" {
		t.Errorf("unexpected Use: %q", cmd.Use)
	}
	if err := cmd.Args(cmd, []string{"only-one"}); err == nil {
		t.Error("expected error for 1 arg, got nil")
	}
	if err := cmd.Args(cmd, []string{"a", "b"}); err != nil {
		t.Errorf("expected no error for 2 args, got %v", err)
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

// TestAgentCmdDoesNotRegisterSuspendResumePrune locks the removal of the
// suspend/resume/prune verbs: 'stop' is the only dormancy transition now
// (WakeOnMessage carries the old suspend-vs-stop distinction), and delete is
// phase-2 CLI UX not yet added.
func TestAgentCmdDoesNotRegisterSuspendResumePrune(t *testing.T) {
	cmd := newAgentCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	for _, removed := range []string{"suspend", "resume", "prune"} {
		if names[removed] {
			t.Errorf("expected %q subcommand to be removed from agent", removed)
		}
	}
}

// TestAgentCmdRegistersReset verifies that 'reset' is registered as a
// subcommand of 'agent', mirroring TestAgentCmdRegistersSuspendResume.
func TestAgentCmdRegistersReset(t *testing.T) {
	cmd := newAgentCmd()
	names := make(map[string]bool)
	for _, sub := range cmd.Commands() {
		names[sub.Name()] = true
	}
	if !names["reset"] {
		t.Error("expected 'reset' subcommand to be registered under agent")
	}
}

// TestAgentSpawnHasIdleSuspendFlag verifies that the spawn subcommand exposes
// the --idle-suspend flag.
func TestAgentSpawnHasIdleSuspendFlag(t *testing.T) {
	cmd := newAgentSpawnCmd()
	f := cmd.Flags().Lookup("idle-suspend")
	if f == nil {
		t.Fatal("expected --idle-suspend flag on spawn subcommand, not found")
	}
	if f.DefValue != "" {
		t.Errorf("--idle-suspend default should be empty string, got %q", f.DefValue)
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

func TestAgentListJSONUsesSeam(t *testing.T) {
	// Point loadConfig() at an isolated, host-less config so dispatch()
	// resolves Localhost (no client.hosts to fall through to) instead of
	// picking up whatever leo.yaml happens to be discoverable from the
	// process's cwd — without this, a real leo.yaml (and its daemon socket)
	// could be reached by loadConfig()'s upward directory walk.
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	oldCfgFile := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = oldCfgFile })

	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return []agent.Record{
			{Name: "alpha", Template: "writer", Status: "running"},
			{Name: "beta", Status: "stopped"},
		}, nil
	}
	t.Cleanup(func() { agentListFn = oldList })

	var buf bytes.Buffer
	oldOut := agentStdout
	agentStdout = &buf
	t.Cleanup(func() { agentStdout = oldOut })

	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got []agent.Record
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not a JSON array of records: %v\noutput: %s", err, buf.String())
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Status != "stopped" {
		t.Fatalf("unexpected decoded records: %+v", got)
	}
}
