package agentmove

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// fakeRunner records every command and answers from a script.
type fakeRunner struct {
	calls  []Cmd
	answer func(Cmd) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, c Cmd) ([]byte, error) {
	f.calls = append(f.calls, c)
	if f.answer != nil {
		return f.answer(c)
	}
	return nil, nil
}

func laptop() config.HostResolution {
	return config.HostResolution{Name: "laptop", Host: config.HostConfig{SSH: "user@host"}}
}

func TestSSHArgvRemoteVerb(t *testing.T) {
	r := &fakeRunner{answer: func(Cmd) ([]byte, error) { return []byte(`{"ok":true}`), nil }}
	ep, err := NewSSHEndpoint(laptop(), r)
	if err != nil {
		t.Fatalf("NewSSHEndpoint: %v", err)
	}
	manifest := []byte(`{"agent":{"name":"secret-agent-name"}}`)
	out, err := ep.Run(context.Background(), "preflight", manifest)
	if err != nil || string(out) != `{"ok":true}` {
		t.Fatalf("Run = %q, %v", out, err)
	}
	if len(r.calls) != 1 {
		t.Fatalf("calls = %d", len(r.calls))
	}
	c := r.calls[0]
	want := []string{"-o", "BatchMode=yes", "user@host", "$HOME/.local/bin/leo", "agent", "move-receive", "preflight"}
	if c.Name != "ssh" || !reflect.DeepEqual(c.Args, want) {
		t.Fatalf("argv = %s %q, want ssh %q", c.Name, c.Args, want)
	}
	if !bytes.Equal(c.Stdin, manifest) {
		t.Fatalf("stdin = %q, want the manifest", c.Stdin)
	}
	if strings.Contains(strings.Join(c.Args, " "), "secret-agent-name") {
		t.Fatal("manifest bytes reached ssh argv")
	}
}

func TestSSHArgsWithWhitespaceRefused(t *testing.T) {
	for name, res := range map[string]config.HostResolution{
		"ssh_args":    {Name: "x", Host: config.HostConfig{SSH: "u@h", SSHArgs: []string{"-o", "ProxyCommand=nc %h 22"}}},
		"quote":       {Name: "x", Host: config.HostConfig{SSH: "u@h", SSHArgs: []string{`-oUser="a"`}}},
		"controlpath": {Name: "x", Host: config.HostConfig{SSH: "u@h"}, ControlPath: "/tmp/my dir/x.ctl"},
	} {
		if _, err := NewSSHEndpoint(res, &fakeRunner{}); err == nil || !strings.Contains(err.Error(), "unsafe") {
			t.Errorf("%s: NewSSHEndpoint = %v, want an unsafe refusal", name, err)
		}
	}
}

func TestGitSSHCommandRejectsShellMeta(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "remotes", "x.ctl")
	ok := config.HostResolution{Name: "x", Host: config.HostConfig{SSH: "u@h", SSHArgs: []string{"-p", "2222"}}, ControlPath: ctl}
	got, err := GitSSHCommand(ok)
	if err != nil {
		t.Fatalf("GitSSHCommand: %v", err)
	}
	if want := "ssh -o BatchMode=yes -p 2222 -o ControlMaster=auto -o ControlPath=" + ctl; got != want {
		t.Fatalf("GitSSHCommand = %q, want %q", got, want)
	}
	for _, bad := range []string{"$x", "`x`", "a;b", `a"b`, "a'b", "a b", "a|b", "a\nb"} {
		cases := map[string]config.HostResolution{
			"host":        {Name: "x", Host: config.HostConfig{SSH: "u@" + bad}},
			"ssh_args":    {Name: "x", Host: config.HostConfig{SSH: "u@h", SSHArgs: []string{bad}}},
			"controlpath": {Name: "x", Host: config.HostConfig{SSH: "u@h"}, ControlPath: "/s/" + bad},
		}
		for where, res := range cases {
			if _, err := GitSSHCommand(res); err == nil {
				t.Errorf("%q in %s accepted", bad, where)
			}
			if _, err := NewSSHEndpoint(res, &fakeRunner{}); err == nil {
				t.Errorf("%q in %s accepted by the ssh endpoint", bad, where)
			}
		}
	}
}

func TestPushArgvExact(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "remotes", "laptop.ctl")
	res := config.HostResolution{Name: "laptop", Host: config.HostConfig{SSH: "user@host"}, ControlPath: ctl}
	cmd, err := PushCmd(res, "/Users/me/ws", "/Users/me/canon", "/opt/homebrew/bin/git", []string{"+abc:refs/leo-move/a/head", "+refs/leo-move/a/index:refs/leo-move/a/index"})
	if err != nil {
		t.Fatalf("PushCmd: %v", err)
	}
	want := []string{"-C", "/Users/me/ws", "push", "--no-verify", "--porcelain",
		"--receive-pack=/opt/homebrew/bin/git receive-pack", "user@host:/Users/me/canon",
		"+abc:refs/leo-move/a/head", "+refs/leo-move/a/index:refs/leo-move/a/index"}
	if cmd.Name != "git" || !reflect.DeepEqual(cmd.Args, want) {
		t.Fatalf("push argv = %s %q\nwant git %q", cmd.Name, cmd.Args, want)
	}
	wantEnv := []string{"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o ControlMaster=auto -o ControlPath=" + ctl, "GIT_SSH_VARIANT=ssh"}
	if !reflect.DeepEqual(cmd.Env, wantEnv) {
		t.Fatalf("push env = %q, want %q", cmd.Env, wantEnv)
	}
	if _, err := PushCmd(res, "/ws", "/canon with space", "/git", nil); err == nil {
		t.Fatal("unsafe canonical accepted")
	}
	if _, err := PushCmd(res, "/ws", "/canon", "git; rm", nil); err == nil {
		t.Fatal("unsafe git path accepted")
	}
}

func TestValidateRef(t *testing.T) {
	for _, good := range []string{"main", "feat/x", "move-wt-copy", "leo-move/agent/head", "v1.2"} {
		if err := ValidateRef(good); err != nil {
			t.Errorf("ValidateRef(%q) = %v", good, err)
		}
	}
	for _, bad := range []string{"", "-x", "a..b", "x.lock", "a/b.lock/c", "a b", "a~b", "a^b", "a:b", "a?b", "a*b", "a[b", `a\b`, "a@{b", "@", "/a", "a/", "a//b", ".a", "a/.b", "a.", "a\nb", "a$b", "a`b", "a;b"} {
		if err := ValidateRef(bad); err == nil {
			t.Errorf("ValidateRef(%q) accepted", bad)
		}
	}
}

func TestLocalEndpointRunsInProcess(t *testing.T) {
	var got []string
	ep := NewLocalEndpoint(func(_ context.Context, verb string, stdin []byte) ([]byte, error) {
		got = append(got, verb+":"+string(stdin))
		return []byte("ok"), nil
	})
	out, err := ep.Run(context.Background(), "commit", []byte("m"))
	if err != nil || string(out) != "ok" || !reflect.DeepEqual(got, []string{"commit:m"}) {
		t.Fatalf("local Run = %q %v, handler saw %v", out, err, got)
	}
	if !ep.Local() || ep.RsyncShell() != "" || ep.Target("/x") != "/x" {
		t.Fatalf("local endpoint = local %v shell %q target %q", ep.Local(), ep.RsyncShell(), ep.Target("/x"))
	}
	ssh, _ := NewSSHEndpoint(config.HostResolution{Name: "laptop", Host: config.HostConfig{SSH: "u@h"}, ControlPath: filepath.Join(t.TempDir(), "c.ctl")}, &fakeRunner{})
	if ssh.Local() || ssh.Target("/x") != "u@h:/x" || !strings.HasPrefix(ssh.RsyncShell(), "ssh -o BatchMode=yes") {
		t.Fatalf("ssh endpoint = local %v shell %q target %q", ssh.Local(), ssh.RsyncShell(), ssh.Target("/x"))
	}
}

func TestSSHRunDecodesRefusal(t *testing.T) {
	refusal := []byte(`{"error":"branch_diverged","message":"main has commits the source lacks"}` + "\n")
	r := &fakeRunner{answer: func(Cmd) ([]byte, error) { return refusal, errors.New("ssh: exit status 1") }}
	ep, _ := NewSSHEndpoint(laptop(), r)
	_, err := ep.Run(context.Background(), VerbGitPrepare, []byte("{}"))
	if !HasCode(err, CodeBranchDiverged) || !Definite(err) {
		t.Fatalf("Run = %v, want a definite branch_diverged refusal", err)
	}
	r.answer = func(Cmd) ([]byte, error) { return nil, errors.New("ssh: exit status 255: connection reset") }
	if _, err := ep.Run(context.Background(), VerbCommit, []byte("{}")); err == nil || Definite(err) {
		t.Fatalf("a dropped connection = %v, want an indefinite error", err)
	}
	r.answer = func(Cmd) ([]byte, error) {
		return []byte(`{"error":"daemon_unreachable","message":"EOF"}`), errors.New("exit status 1")
	}
	if _, err := ep.Run(context.Background(), VerbCommit, []byte("{}")); err == nil || Definite(err) {
		t.Fatalf("an unreachable destination daemon = %v, want an indefinite error", err)
	}
}

func TestEndpointPush(t *testing.T) {
	ssh, _ := NewSSHEndpoint(laptop(), &fakeRunner{})
	c, ok, err := ssh.Push("/Users/me/ws", "/Users/me/canon", "/usr/bin/git", []string{"+a:refs/leo-move/x/head"})
	if err != nil || !ok || c.Name != "git" || c.Args[len(c.Args)-2] != "user@host:/Users/me/canon" {
		t.Fatalf("ssh Push = %v %v %v", c, ok, err)
	}
	local := NewLocalEndpoint(nil)
	if _, ok, err := local.Push("/ws", "/canon", "/usr/bin/git", nil); ok || err != nil {
		t.Fatalf("local Push = %v %v, want skipped (one repository)", ok, err)
	}
}
