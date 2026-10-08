package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/service"
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
	argv, _ := stubExecRecordingEnv(t)
	return argv
}

// stubExecRecordingEnv is stubExecRecording that also returns the env the
// tmux exec would receive.
func stubExecRecordingEnv(t *testing.T) (argv, env *[]string) {
	t.Helper()
	argv, env = new([]string), new([]string)
	old := agentSyscallExec
	agentSyscallExec = func(_ string, a []string, e []string) error {
		events = append(events, "exec")
		*argv, *env = a, e
		return nil
	}
	t.Cleanup(func() { agentSyscallExec = old })
	return argv, env
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
				return []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", quotedDefaultRemoteLeo, "agent", "attach", "--dispatch-placement", "background", "--", "scratch"}
			},
		},
		"agent attach cc": {
			[]string{"agent", "attach", "--cc", "--dispatch-placement", "background", "scratch"},
			func(home string) []string {
				w := append([]string{"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
				return append(w, quotedDefaultRemoteLeo, "agent", "attach", "--cc", "--dispatch-placement", "background", "--", "scratch")
			},
		},
		"top-level attach non-cc": {
			[]string{"attach", "--dispatch-placement", "window", "scratch"},
			func(string) []string {
				return []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", quotedDefaultRemoteLeo, "attach", "--dispatch-placement", "window", "--", "scratch"}
			},
		},
		"top-level attach cc": {
			[]string{"attach", "--cc", "--dispatch-placement", "window", "scratch"},
			func(home string) []string {
				w := append([]string{"ssh", "-tt", "-e", "none", "user@prod.example.com", "-p", "2222"}, ctlOpts(home)...)
				return append(w, quotedDefaultRemoteLeo, "attach", "--cc", "--dispatch-placement", "window", "--", "scratch")
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

// captureProcessStdout redirects the real os.Stdout while fn runs and returns
// what was written to it: the control-mode stream's channel, which nothing
// before the tmux exec may touch.
func captureProcessStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		var buf bytes.Buffer
		_, _ = buf.ReadFrom(r)
		done <- buf.String()
	}()
	fn()
	os.Stdout = old
	_ = w.Close()
	return <-done
}

// runLocalCCAttach drives `leo agent attach --cc --dispatch-placement
// background scratch` up to the exec seam and returns what reached stdout.
// The remote leo path as the placed attach sends it: the default's $HOME is
// left for the remote shell to expand, the rest single-quoted.
const quotedDefaultRemoteLeo = `"$HOME"/'.local/bin/leo'`

func runLocalCCAttach(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	return runLocalCCAttachWithSpec(t, nil, args...)
}

// runLocalCCAttachWithSpec is runLocalCCAttach where the attach-spec lookup
// fails with specErr when it is non-nil.
func runLocalCCAttachWithSpec(t *testing.T, specErr error, args ...string) (stdout string, err error) {
	t.Helper()
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	stubAgentSession(t, func(_, name string) (string, error) { return "leo-" + name, nil })
	oldSpec := agentAttachSpecFn
	agentAttachSpecFn = func(context.Context, string, string) (daemon.AgentAttachSpecResponse, error) {
		if specErr != nil {
			return daemon.AgentAttachSpecResponse{}, specErr
		}
		return daemon.AgentAttachSpecResponse{Name: "scratch", Harness: "claude"}, nil
	}
	t.Cleanup(func() { agentAttachSpecFn = oldSpec })
	stubExecRecording(t)

	root := newRootCmd()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetArgs(append(args, "agent", "attach", "--cc", "--dispatch-placement", "background", "scratch"))
	stdout = captureProcessStdout(t, func() { err = root.Execute() })
	return stdout + out.String(), err
}

// leoterm parses the control-mode stream byte for byte, so the attach must
// not print anything of its own before tmux takes over stdout, on the happy
// path and when the daemon registration fails.
func TestCCAttachWithPlacementWritesNothingToStdoutBeforeExec(t *testing.T) {
	path := newAgentCLITestConfig(t)
	for name, registerErr := range map[string]error{
		"registered":         nil,
		"daemon unreachable": errors.New("connecting to daemon: dial unix: no such file"),
	} {
		t.Run(name, func(t *testing.T) {
			withStubStdio(t) // agentStdout/agentStderr into buffers
			stdout, stderr := agentStdout.(*bytes.Buffer), agentStderr.(*bytes.Buffer)
			stubPlacementRegistration(t, registerErr)
			got, err := runLocalCCAttach(t, "--config", path, "--host", "localhost")
			if err != nil {
				t.Fatalf("attach: %v", err)
			}
			if got != "" || stdout.Len() != 0 {
				t.Fatalf("stdout = %q / %q, want empty", got, stdout.String())
			}
			// ssh -tt merges stderr into the PTY the control-mode client
			// reads, so the warning must go to the leo log instead.
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing before the exec", stderr.String())
			}
			if registerErr != nil {
				logged, _ := os.ReadFile(service.LogPathFor(homeFromConfigPath(path)))
				if !strings.Contains(string(logged), "--dispatch-placement not registered") {
					t.Errorf("service log = %q, want the warning", logged)
				}
			}
		})
	}
}

func TestCCAttachWithInvalidPlacementFailsOnStderrOnly(t *testing.T) {
	path := newAgentCLITestConfig(t)
	withStubStdio(t)
	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "--cc", "--dispatch-placement", "sideways", "scratch"})
	var err error
	got := captureProcessStdout(t, func() { err = root.Execute() })
	if err == nil || got != "" {
		t.Fatalf("err = %v stdout = %q, want a non-zero failure with empty stdout", err, got)
	}
}

// A non-interactive ssh command has HOME and nothing else: no $TMUX, no
// profile PATH, no --config. The daemon socket must still resolve from the
// default leo home (~/.leo), and the tmux path from tmuxLocate, not PATH.
func TestCCAttachWithPlacementResolvesHomeFromAStrippedEnvironment(t *testing.T) {
	home := t.TempDir()
	leoHome := home + "/.leo"
	if err := os.MkdirAll(leoHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(leoHome+"/leo.yaml", &config.Config{HomePath: leoHome, Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", "")
	t.Setenv("TMUX", "")
	t.Setenv("LEO_HOME", "")
	t.Chdir(home)
	oldCfg := cfgFile
	cfgFile = ""
	t.Cleanup(func() { cfgFile = oldCfg })
	withStubStdio(t)
	regs := stubPlacementRegistration(t, nil)

	stdout, err := runLocalCCAttach(t)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if stdout != "" {
		t.Errorf("stdout = %q", stdout)
	}
	if len(*regs) != 1 || (*regs)[0].homePath != leoHome || (*regs)[0].session != "leo-scratch" {
		t.Fatalf("registrations = %+v, want home %s session leo-scratch", *regs, leoHome)
	}
}

// leoterm's exact form: no --cc, the flag before `--`, the agent after it.
func TestAgentAttachPlacementBeforeDoubleDashLocalExecsTmuxKeepingThePid(t *testing.T) {
	path := newAgentCLITestConfig(t)
	stub := withStubExec(t)
	withStubStdio(t)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	stubAgentSession(t, func(_, name string) (string, error) { return "leo-" + name, nil })
	oldSpec := agentAttachSpecFn
	agentAttachSpecFn = func(context.Context, string, string) (daemon.AgentAttachSpecResponse, error) {
		return daemon.AgentAttachSpecResponse{Name: "scratch", Harness: "claude"}, nil
	}
	t.Cleanup(func() { agentAttachSpecFn = oldSpec })
	events = nil
	regs := stubPlacementRegistration(t, nil)
	argv := stubExecRecording(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "--host", "localhost", "--dispatch-placement", "background", "--", "scratch"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if want := []string{"tmux", "-L", "leo", "attach", "-t", "=leo-scratch"}; !equalStrings(*argv, want) {
		t.Errorf("exec argv = %v, want %v", *argv, want)
	}
	if want := (placementRegistration{homeFromConfigPath(path), "leo-scratch", "background", os.Getpid()}); len(*regs) != 1 || (*regs)[0] != want {
		t.Errorf("registrations = %+v, want [%+v]", *regs, want)
	}
	if !equalStrings(events, []string{"register", "exec"}) {
		t.Errorf("events = %v, want register before exec", events)
	}
	// exec replaces the process (same pid); a child process would break the
	// registered pid, so no command may have been started.
	if len(stub.calls) != 0 {
		t.Errorf("attach started a child process: %v", stub.calls)
	}
}

func TestAgentAttachPlacementBeforeDoubleDashRemoteForwardsQuotedRemoteCommand(t *testing.T) {
	for name, tc := range map[string]struct {
		args  []string
		agent string
	}{
		"default host":             {[]string{"agent", "attach", "--dispatch-placement", "background", "--", "scratch"}, "scratch"},
		"explicit host":            {[]string{"agent", "attach", "--host", "prod", "--dispatch-placement", "background", "--", "scratch"}, "scratch"},
		"name needing quotes":      {[]string{"agent", "attach", "--dispatch-placement", "background", "--", "a b;$(x)'y"}, `'a b;$(x)'\''y'`},
		"name with leading dash":   {[]string{"agent", "attach", "--dispatch-placement", "background", "--", "-rf"}, "-rf"},
		"name with leading equals": {[]string{"agent", "attach", "--dispatch-placement", "background", "--", "=leo"}, "'=leo'"},
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
			want := []string{"ssh", "-t", "user@prod.example.com", "-p", "2222", quotedDefaultRemoteLeo, "agent", "attach", "--dispatch-placement", "background", "--", tc.agent}
			if len(stub.calls) != 1 || !equalStrings(stub.calls[0], want) {
				t.Fatalf("ssh calls = %v\nwant        %v", stub.calls, want)
			}
			if len(*regs) != 0 {
				t.Errorf("client registered against a remote host: %+v", *regs)
			}
		})
	}
}

func TestAttachExecEnsuresAUTF8Locale(t *testing.T) {
	for name, tc := range map[string]struct {
		env  map[string]string
		want string // expected LC_CTYPE entries in the exec env; "" means none
	}{
		"nothing set":       {nil, "LC_CTYPE=UTF-8"},
		"non-utf8 LANG":     {map[string]string{"LANG": "C"}, "LC_CTYPE=UTF-8"},
		"non-utf8 LC_CTYPE": {map[string]string{"LC_CTYPE": "C", "LANG": "C"}, "LC_CTYPE=UTF-8"},
		"utf8 LANG":         {map[string]string{"LANG": "en_US.UTF-8"}, ""},
		"utf8 LC_ALL":       {map[string]string{"LC_ALL": "C.UTF-8", "LANG": "C"}, ""},
		"utf8 LC_CTYPE":     {map[string]string{"LC_CTYPE": "en_US.utf8"}, "LC_CTYPE=en_US.utf8"},
	} {
		for _, cc := range []bool{false, true} {
			t.Run(name, func(t *testing.T) {
				for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
					t.Setenv(k, "")
					os.Unsetenv(k)
				}
				for k, v := range tc.env {
					t.Setenv(k, v)
				}
				stubTmuxLookPath(t, "/usr/bin/tmux", nil)
				stubOutsideTmux(t)
				_, env := stubExecRecordingEnv(t)
				if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", attachOptions{cc: cc}); err != nil {
					t.Fatalf("attach: %v", err)
				}
				var ctype []string
				for _, e := range *env {
					if strings.HasPrefix(e, "LC_CTYPE=") {
						ctype = append(ctype, e)
					}
				}
				want := tc.want
				if want == "" {
					if len(ctype) != 0 {
						t.Fatalf("LC_CTYPE entries = %v, want the environment left alone", ctype)
					}
					return
				}
				if len(ctype) != 1 || ctype[0] != want {
					t.Fatalf("LC_CTYPE entries = %v, want exactly [%s]", ctype, want)
				}
			})
		}
	}
}

func TestRemoteLeoWordRoundTripsThroughAShell(t *testing.T) {
	const home = "/home/some one"
	for _, path := range []string{
		"leo",
		"/opt/Leo Tools/leo",
		"/opt/it's/leo",
		"/opt/$(touch pwned)/`id`/leo",
		"$HOME/.local/bin/leo",
		"$HOME/Leo Tools/it's $(x)/leo",
		"~/bin/le o",
		"~/it's/leo",
	} {
		word := remoteLeoWord(path)
		withHome, err := exec.Command("env", "HOME="+home, "sh", "-c", "printf %s "+word).Output()
		if err != nil {
			t.Fatalf("%q -> %s: %v", path, word, err)
		}
		want := path
		switch {
		case strings.HasPrefix(path, "$HOME/"):
			want = home + strings.TrimPrefix(path, "$HOME")
		case strings.HasPrefix(path, "~/"):
			want = home + strings.TrimPrefix(path, "~")
		}
		if string(withHome) != want {
			t.Errorf("%q -> %s -> %q, want %q", path, word, withHome, want)
		}
	}
	if got := remoteLeoWord("$HOME/.local/bin/leo"); got != quotedDefaultRemoteLeo {
		t.Errorf("default = %s, want %s", got, quotedDefaultRemoteLeo)
	}
	if got := remoteLeoWord("/opt/Leo Tools/leo"); got != "'/opt/Leo Tools/leo'" {
		t.Errorf("spaced = %s", got)
	}
}

func TestPlacedRemoteAttachQuotesAConfiguredLeoPath(t *testing.T) {
	path := newAgentCLITestConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.Client.Hosts["prod"]
	host.LeoPath = "/opt/Leo Tools/leo"
	cfg.Client.Hosts["prod"] = host
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	for name, extra := range map[string][]string{"non-cc": nil, "cc": {"--cc"}} {
		t.Run(name, func(t *testing.T) {
			stub := withStubExec(t)
			withStubStdio(t)
			root := newRootCmd()
			root.SetArgs(append(append([]string{"--config", path, "agent", "attach"}, extra...), "--dispatch-placement", "background", "--", "scratch"))
			if err := root.Execute(); err != nil {
				t.Fatalf("execute: %v", err)
			}
			if len(stub.calls) != 1 || !containsSeq(stub.calls[0], "'/opt/Leo Tools/leo'", "agent", "attach") {
				t.Fatalf("ssh calls = %v, want the quoted leo path", stub.calls)
			}
		})
	}
}

func containsSeq(haystack []string, seq ...string) bool {
	for i := 0; i+len(seq) <= len(haystack); i++ {
		if equalStrings(haystack[i:i+len(seq)], seq) {
			return true
		}
	}
	return false
}

func TestNonCCAttachKeepsTheStderrWarningWhenRegistrationFails(t *testing.T) {
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	_, stderr := withStubStdio(t)
	stubPlacementRegistration(t, errors.New("daemon down"))
	stubExecRecording(t)
	home := t.TempDir()
	if err := attachTmuxSession(config.HostResolution{Localhost: true}, "leo-foo", attachOptions{dispatchPlacement: "pane", homePath: home}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "warning") {
		t.Fatalf("stderr = %q, want the warning", stderr.String())
	}
	if _, err := os.Stat(service.LogPathFor(home)); err == nil {
		t.Error("non-cc warning also went to the log file")
	}
}

// The attach-spec lookup failing must not put its warning on the terminal a
// control-mode client is reading; it goes to the service log.
func TestCCAttachSpecLookupFailureWarnsOnlyInTheServiceLog(t *testing.T) {
	path := newAgentCLITestConfig(t)
	_, stderr := withStubStdio(t)
	stubPlacementRegistration(t, nil)
	stdout, err := runLocalCCAttachWithSpec(t, errors.New("daemon busy"), "--config", path, "--host", "localhost")
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	if stdout != "" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q, want both empty", stdout, stderr.String())
	}
	logged, _ := os.ReadFile(service.LogPathFor(homeFromConfigPath(path)))
	if !strings.Contains(string(logged), "driver attach lookup failed") {
		t.Fatalf("service log = %q, want the lookup warning", logged)
	}
}

func TestCCAttachToAStoppedAgentFailsInsteadOfPrompting(t *testing.T) {
	path := newAgentCLITestConfig(t)
	_, stderr := withStubStdio(t)
	stubTmuxLookPath(t, "/usr/bin/tmux", nil)
	stubOutsideTmux(t)
	stubAgentSessionFull(t, func(_, name string) (daemon.AgentSessionResponse, error) {
		return daemon.AgentSessionResponse{Session: "leo-" + name, Name: name, Stopped: true}, nil
	})
	oldTTY := agentIsTTY
	agentIsTTY = func() bool { return true }
	t.Cleanup(func() { agentIsTTY = oldTTY })
	argv := stubExecRecording(t)

	root := newRootCmd()
	root.SetArgs([]string{"--config", path, "agent", "attach", "--host", "localhost", "--cc", "--", "scratch"})
	var err error
	stdout := captureProcessStdout(t, func() { err = root.Execute() })
	if err == nil || stdout != "" || strings.Contains(stderr.String(), "Start it?") || len(*argv) != 0 {
		t.Fatalf("err=%v stdout=%q stderr=%q exec=%v, want a plain failure", err, stdout, stderr.String(), *argv)
	}
}

// A FIFO standing in for service.log with no reader must not hang the attach.
func TestAppendToServiceLogDoesNotBlockOnAReaderlessFIFO(t *testing.T) {
	home := t.TempDir()
	logPath := service.LogPathFor(home)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(logPath, 0o600); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	done := make(chan struct{})
	go func() {
		appendToServiceLog(home, "warning: x\n")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("appendToServiceLog blocked on a FIFO with no reader")
	}
}

func TestNonPlacedRemoteAttachQuotesTheRemoteLeoPath(t *testing.T) {
	path := newAgentCLITestConfig(t)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	host := cfg.Client.Hosts["prod"]
	host.LeoPath = "/opt/Leo Tools/leo"
	cfg.Client.Hosts["prod"] = host
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"agent", "attach", "scratch"}, {"attach", "scratch"}} {
		stub := withStubExec(t)
		withStubStdio(t)
		root := newRootCmd()
		root.SetArgs(append([]string{"--config", path}, args...))
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		if len(stub.calls) != 1 || !containsSeq(stub.calls[0], "'/opt/Leo Tools/leo'") {
			t.Fatalf("%v: ssh calls = %v, want the quoted leo path", args, stub.calls)
		}
	}
}
