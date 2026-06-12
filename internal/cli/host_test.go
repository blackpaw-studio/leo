package cli

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

// --- host list ---

func TestHostEntriesSynthesizesLocalAndDefault(t *testing.T) {
	cfg := &config.Config{
		Client: config.ClientConfig{
			DefaultHost: "beta",
			Hosts: map[string]config.HostConfig{
				"alpha": {SSH: "u@alpha"},
				"beta":  {SSH: "u@beta"},
			},
		},
	}
	got := hostEntries(cfg)
	want := []hostEntry{
		{Name: "localhost", Local: true, Default: false},
		{Name: "alpha", SSH: "u@alpha", Default: false},
		{Name: "beta", SSH: "u@beta", Default: true},
	}
	if len(got) != len(want) {
		t.Fatalf("entries = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestHostEntriesLocalIsDefaultWhenNoHosts(t *testing.T) {
	got := hostEntries(&config.Config{})
	if len(got) != 1 || !got[0].Local || !got[0].Default {
		t.Fatalf("entries = %+v, want a single default local entry", got)
	}
}

func TestHostListJSONCommand(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			DefaultHost: "prod",
			Hosts:       map[string]config.HostConfig{"prod": {SSH: "u@prod"}},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	out, _ := withStubStdio(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "host", "list", "--json"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var entries []hostEntry
	if err := json.Unmarshal(out.Bytes(), &entries); err != nil {
		t.Fatalf("decode json: %v (%s)", err, out.String())
	}
	if len(entries) != 2 || !entries[0].Local || entries[1].Name != "prod" || !entries[1].Default {
		t.Fatalf("entries = %+v", entries)
	}
}

// --- resolveRemoteHost ---

func TestResolveRemoteHostRejections(t *testing.T) {
	home := t.TempDir()
	cfg := &config.Config{
		HomePath: home,
		Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10},
		Client: config.ClientConfig{
			Hosts: map[string]config.HostConfig{"prod": {SSH: "u@prod"}},
		},
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })

	cases := []struct {
		name    string
		host    string
		wantSub string
	}{
		{"unknown host", "ghost", "not defined"},
		{"localhost sentinel", "localhost", "local daemon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := resolveRemoteHost(tc.host)
			if err == nil || !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("err = %v, want substring %q", err, tc.wantSub)
			}
		})
	}

	t.Run("valid remote resolves", func(t *testing.T) {
		_, res, err := resolveRemoteHost("prod")
		if err != nil {
			t.Fatalf("resolveRemoteHost: %v", err)
		}
		if res.Name != "prod" || res.ControlPath == "" {
			t.Fatalf("res = %+v, want named remote with control path", res)
		}
	})
}

// --- forwarder argument construction ---

func TestSSHForwardCmdArgs(t *testing.T) {
	var got []string
	old := forwardExecCommand
	forwardExecCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	t.Cleanup(func() { forwardExecCommand = old })

	f := &hostForwarder{
		res: config.HostResolution{
			Name:        "prod",
			Host:        config.HostConfig{SSH: "u@prod", SSHArgs: []string{"-p", "2222"}},
			ControlPath: "/s/remotes/prod.ctl",
		},
		localSock: "/s/remotes/prod.sock",
	}
	_ = f.sshForwardCmd("/remote/.leo/state/leo.sock")

	want := []string{
		"ssh", "-N", "-T",
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-o", "ControlMaster=auto",
		"-o", "ControlPath=/s/remotes/prod.ctl",
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=3",
		"-p", "2222",
		"-L", "/s/remotes/prod.sock:/remote/.leo/state/leo.sock",
		"u@prod",
	}
	if !equalStrings(got, want) {
		t.Errorf("forward args = %v, want %v", got, want)
	}
}

// --- remote socket path resolution ---

func TestRemoteSockPath(t *testing.T) {
	cases := []struct {
		name    string
		echo    string
		want    string
		wantErr bool
	}{
		{"absolute path", "/home/leo/.leo/state/leo.sock", "/home/leo/.leo/state/leo.sock", false},
		{"trims whitespace", "  /home/leo/.leo/state/leo.sock\n", "/home/leo/.leo/state/leo.sock", false},
		{"rejects relative", "relative/leo.sock", "", true},
		{"rejects empty", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := agentExecCommand
			agentExecCommand = func(name string, args ...string) *exec.Cmd {
				return exec.Command("printf", "%s", tc.echo)
			}
			t.Cleanup(func() { agentExecCommand = old })

			f := &hostForwarder{res: config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}}}
			got, err := f.remoteSockPath()
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("remoteSockPath: %v", err)
			}
			if got != tc.want {
				t.Errorf("path = %q, want %q", got, tc.want)
			}
		})
	}
}

// --- run(): idempotency and first-connect failure ---

func TestForwardRunIdempotentWhenHealthy(t *testing.T) {
	out, _ := withStubStdio(t)

	oldHealthy := forwardHealthy
	forwardHealthy = func(context.Context, string) bool { return true }
	t.Cleanup(func() { forwardHealthy = oldHealthy })

	oldExec := forwardExecCommand
	forwardExecCommand = func(string, ...string) *exec.Cmd {
		t.Fatal("ssh should not be spawned when a healthy forward already exists")
		return nil
	}
	t.Cleanup(func() { forwardExecCommand = oldExec })

	f := &hostForwarder{
		res:       config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}},
		localSock: "/s/remotes/prod.sock",
	}
	if err := f.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if !strings.Contains(out.String(), "/s/remotes/prod.sock") {
		t.Errorf("want socket path printed, got %q", out.String())
	}
}

func TestForwardRunFailsBeforeHealthy(t *testing.T) {
	withStubStdio(t)

	oldHealthy := forwardHealthy
	forwardHealthy = func(context.Context, string) bool { return false }
	t.Cleanup(func() { forwardHealthy = oldHealthy })

	// remoteSockPath round-trip succeeds...
	oldAgent := agentExecCommand
	agentExecCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "/remote/.leo/state/leo.sock")
	}
	t.Cleanup(func() { agentExecCommand = oldAgent })

	// ...but the ssh forward exits immediately and the socket never comes up.
	oldExec := forwardExecCommand
	forwardExecCommand = func(string, ...string) *exec.Cmd { return exec.Command("false") }
	t.Cleanup(func() { forwardExecCommand = oldExec })

	oldTimeout := forwardConnectTimeout
	forwardConnectTimeout = 2 * time.Second
	t.Cleanup(func() { forwardConnectTimeout = oldTimeout })

	f := &hostForwarder{
		res:       config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}},
		localSock: filepath.Join(t.TempDir(), "prod.sock"),
	}
	err := f.run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "before the socket came up") {
		t.Fatalf("err = %v, want first-connect failure", err)
	}
}

// Each ssh forward stays up briefly then exits, forcing the reconnect loop to
// run several times. The socket path must be announced exactly once across all
// those reconnects — re-emitting it would violate the single-path contract and
// could block a consumer that has stopped draining stdout.
func TestForwardRunAnnouncesOnceAcrossReconnects(t *testing.T) {
	out, _ := withStubStdio(t)

	// False on the first call so the idempotency short-circuit in run() doesn't
	// fire (no pre-existing forward); healthy on every poll thereafter.
	var healthChecks int32
	oldHealthy := forwardHealthy
	forwardHealthy = func(context.Context, string) bool { return atomic.AddInt32(&healthChecks, 1) > 1 }
	t.Cleanup(func() { forwardHealthy = oldHealthy })

	oldAgent := agentExecCommand
	agentExecCommand = func(string, ...string) *exec.Cmd {
		return exec.Command("printf", "%s", "/remote/.leo/state/leo.sock")
	}
	t.Cleanup(func() { agentExecCommand = oldAgent })

	// Tighten the timers so the test exercises real reconnects quickly.
	oldPoll, oldBackoff := forwardHealthPoll, forwardBackoffMin
	forwardHealthPoll = 5 * time.Millisecond
	forwardBackoffMin = 5 * time.Millisecond
	t.Cleanup(func() { forwardHealthPoll, forwardBackoffMin = oldPoll, oldBackoff })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	var spawns int32
	oldExec := forwardExecCommand
	forwardExecCommand = func(string, ...string) *exec.Cmd {
		// Stay up long enough for the health poll to fire, then exit so the
		// loop treats it as a drop and reconnects. Cancel once we've proven a
		// few reconnects each had a chance to re-announce.
		if atomic.AddInt32(&spawns, 1) >= 3 {
			cancel()
		}
		return exec.Command("sleep", "0.03")
	}
	t.Cleanup(func() { forwardExecCommand = oldExec })

	f := &hostForwarder{
		res:       config.HostResolution{Name: "prod", Host: config.HostConfig{SSH: "u@prod"}},
		localSock: filepath.Join(t.TempDir(), "prod.sock"),
	}
	if err := f.run(ctx); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := strings.Count(out.String(), "prod.sock"); got != 1 {
		t.Errorf("announced %d times, want exactly 1; stdout = %q", got, out.String())
	}
	if atomic.LoadInt32(&spawns) < 2 {
		t.Fatalf("expected multiple reconnects, got %d spawns", spawns)
	}
}

// --- stop ---

func TestForwardStopExitsMasterAndRemovesSocket(t *testing.T) {
	withStubStdio(t)
	dir := t.TempDir()
	sock := filepath.Join(dir, "prod.sock")
	if err := os.WriteFile(sock, []byte{}, 0o600); err != nil {
		t.Fatalf("seed socket: %v", err)
	}

	var got []string
	old := agentExecCommand
	agentExecCommand = func(name string, args ...string) *exec.Cmd {
		got = append([]string{name}, args...)
		return exec.Command("true")
	}
	t.Cleanup(func() { agentExecCommand = old })

	f := &hostForwarder{
		res: config.HostResolution{
			Name:        "prod",
			Host:        config.HostConfig{SSH: "u@prod", SSHArgs: []string{"-p", "2222"}},
			ControlPath: filepath.Join(dir, "prod.ctl"),
		},
		localSock: sock,
	}
	if err := f.stop(); err != nil {
		t.Fatalf("stop: %v", err)
	}
	want := []string{"ssh", "-o", "ControlPath=" + filepath.Join(dir, "prod.ctl"), "-O", "exit", "-p", "2222", "u@prod"}
	if !equalStrings(got, want) {
		t.Errorf("stop ssh args = %v, want %v", got, want)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("expected local socket removed, stat err = %v", err)
	}
}
