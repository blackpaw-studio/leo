package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

type placementRegistration struct {
	homePath, session, placement string
	pid                          int
}

// stubPlacementRegistration replaces the daemon registration call, recording
// each registration and the order of events relative to the tmux exec.
func stubPlacementRegistration(t *testing.T, err error) *[]placementRegistration {
	t.Helper()
	var calls []placementRegistration
	old := attachPlacementRegisterFn
	attachPlacementRegisterFn = func(_ context.Context, homePath, session string, pid int, placement string) error {
		calls = append(calls, placementRegistration{homePath, session, placement, pid})
		events = append(events, "register")
		return err
	}
	t.Cleanup(func() { attachPlacementRegisterFn = old })
	return &calls
}

var events []string

func stubExecRecording(t *testing.T) *[]string {
	t.Helper()
	var argv []string
	old := agentSyscallExec
	agentSyscallExec = func(_ string, a []string, _ []string) error {
		events = append(events, "exec")
		argv = a
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })
	return &argv
}

func TestAttachTmuxSessionLocalRegistersPlacementBeforeExec(t *testing.T) {
	for name, tc := range map[string]struct {
		cc   bool
		want []string
	}{
		"plain": {false, []string{"tmux", "-L", "leo", "attach", "-t", "=leo-foo"}},
		"cc":    {true, []string{"tmux", "-L", "leo", "-CC", "attach", "-t", "=leo-foo"}},
	} {
		t.Run(name, func(t *testing.T) {
			events = nil
			stubTmuxLookPath(t, "/usr/bin/tmux", nil)
			stubOutsideTmux(t)
			regs := stubPlacementRegistration(t, nil)
			argv := stubExecRecording(t)

			opts := attachOptions{cc: tc.cc, dispatchPlacement: "background", homePath: "/leo/home"}
			if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", opts); err != nil {
				t.Fatalf("attach: %v", err)
			}
			if !equalStrings(*argv, tc.want) {
				t.Errorf("argv = %v, want %v", *argv, tc.want)
			}
			want := placementRegistration{"/leo/home", "leo-foo", "background", os.Getpid()}
			if len(*regs) != 1 || (*regs)[0] != want {
				t.Errorf("registrations = %+v, want [%+v]", *regs, want)
			}
			if !equalStrings(events, []string{"register", "exec"}) {
				t.Errorf("events = %v, want register before exec", events)
			}
		})
	}
}

func TestAttachTmuxSessionWithoutPlacementDoesNotRegister(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	regs := stubPlacementRegistration(t, nil)
	stubExecRecording(t)
	if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", attachOptions{homePath: "/h"}); err != nil {
		t.Fatalf("attach: %v", err)
	}
	if len(*regs) != 0 {
		t.Fatalf("registered without the flag: %+v", *regs)
	}
}

func TestAttachTmuxSessionWarnsAndAttachesWhenDaemonUnreachable(t *testing.T) {
	events = nil
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	_, stderr := withStubStdio(t)
	stubPlacementRegistration(t, errors.New("connecting to daemon: no such file"))
	argv := stubExecRecording(t)

	opts := attachOptions{dispatchPlacement: "pane", homePath: "/h"}
	if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", opts); err != nil {
		t.Fatalf("attach must still succeed: %v", err)
	}
	if len(*argv) == 0 {
		t.Fatal("tmux was not exec'd")
	}
	if got := stderr.String(); !strings.Contains(got, "warning") || !strings.Contains(got, "--dispatch-placement") {
		t.Errorf("stderr = %q, want a warning naming the flag", got)
	}
}

func TestAttachTmuxSessionRejectsPlacementOnPopupAndChildPaths(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	regs := stubPlacementRegistration(t, nil)
	stub := withStubExec(t)
	argv := stubExecRecording(t)

	t.Run("popup inside tmux", func(t *testing.T) {
		old := tmuxEnv
		tmuxEnv = func() string { return "/tmp/tmux-501/default,1234,0" }
		t.Cleanup(func() { tmuxEnv = old })
		err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", attachOptions{dispatchPlacement: "pane", homePath: "/h"})
		if err == nil || !strings.Contains(err.Error(), "--dispatch-placement") || !strings.Contains(err.Error(), "tmux") {
			t.Fatalf("err = %v, want a --dispatch-placement refusal mentioning tmux", err)
		}
	})
	t.Run("as child", func(t *testing.T) {
		stubOutsideTmux(t)
		err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", attachOptions{dispatchPlacement: "pane", homePath: "/h", asChild: true})
		if err == nil || !strings.Contains(err.Error(), "--dispatch-placement") {
			t.Fatalf("err = %v, want a --dispatch-placement refusal", err)
		}
	})
	if len(*regs) != 0 || len(stub.calls) != 0 || len(*argv) != 0 {
		t.Fatalf("refused attach still acted: regs=%+v calls=%v exec=%v", *regs, stub.calls, *argv)
	}
}

func TestAgentAttachRemoteForwardsPlacementThroughRemoteLeo(t *testing.T) {
	for name, tc := range map[string]struct {
		args []string
		want func(home string) []string
	}{
		"agent attach non-cc": {
			[]string{"agent", "attach", "--dispatch-placement", "background", "scratch"},
			func(string) []string {
				return []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "agent", "attach", "--dispatch-placement", "background", "scratch"}
			},
		},
		"agent attach cc": {
			[]string{"agent", "attach", "--cc", "--dispatch-placement", "background", "scratch"},
			func(home string) []string {
				w := append([]string{"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
				return append(w, config.DefaultRemoteLeoPath, "agent", "attach", "--cc", "--dispatch-placement", "background", "scratch")
			},
		},
		"top-level attach non-cc": {
			[]string{"attach", "--dispatch-placement", "window", "scratch"},
			func(string) []string {
				return []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", config.DefaultRemoteLeoPath, "attach", "--dispatch-placement", "window", "scratch"}
			},
		},
		"top-level attach cc": {
			[]string{"attach", "--cc", "--dispatch-placement", "window", "scratch"},
			func(home string) []string {
				w := append([]string{"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
				return append(w, config.DefaultRemoteLeoPath, "attach", "--cc", "--dispatch-placement", "window", "scratch")
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			path := newAgentCLITestConfig(t)
			stub := withStubExec(t)
			withStubStdio(t)
			regs := stubPlacementRegistration(t, nil)

			root := newRootCmd()
			root.SetArgs(append([]string{"--config", path}, tc.args...))
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(stub.calls) != 1 {
				t.Fatalf("expected one ssh call through the remote leo, got %d: %v", len(stub.calls), stub.calls)
			}
			if want := tc.want(homeFromConfigPath(path)); !equalStrings(stub.calls[0], want) {
				t.Errorf("ssh args = %v\nwant       %v", stub.calls[0], want)
			}
			if len(*regs) != 0 {
				t.Errorf("the client must not register against a remote host: %+v", *regs)
			}
		})
	}
}

func TestAttachRejectsInvalidDispatchPlacement(t *testing.T) {
	for _, args := range [][]string{
		{"agent", "attach", "--dispatch-placement", "sideways", "scratch"},
		{"attach", "--dispatch-placement", "sideways", "scratch"},
	} {
		path := newAgentCLITestConfig(t)
		stub := withStubExec(t)
		withStubStdio(t)
		root := newRootCmd()
		var errOut bytes.Buffer
		root.SetErr(&errOut)
		root.SetArgs(append([]string{"--config", path}, args...))
		err := root.Execute()
		if err == nil || !strings.Contains(err.Error(), "--dispatch-placement") || !strings.Contains(err.Error(), "pane, window or background") {
			t.Errorf("%v: err = %v", args, err)
		}
		if len(stub.calls) != 0 {
			t.Errorf("%v: invalid flag still reached ssh: %v", args, stub.calls)
		}
	}
}
