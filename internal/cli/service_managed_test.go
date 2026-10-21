package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/service"
)

const managedTestLabel = "studio.blackpaw.leo.daemon"

func managedTestState() managed.State {
	return managed.State{Managed: true, Marker: managed.Marker{Manager: "Leo.app", LaunchdLabel: managedTestLabel, SelfPath: "/app/leo"}}
}

// useTempConfig points the CLI at a throwaway leo.yaml and returns its config.
func useTempConfig(t *testing.T, mutate func(*config.Config)) *config.Config {
	t.Helper()
	home := t.TempDir()
	cfg := &config.Config{HomePath: home, Defaults: config.DefaultsConfig{Model: "sonnet", MaxTurns: 10}}
	if mutate != nil {
		mutate(cfg)
	}
	path := home + "/leo.yaml"
	if err := config.Save(path, cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	loaded, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig: %v", err)
	}
	return loaded
}

// serviceCalls records every service-package seam the CLI could reach.
type serviceCalls struct{ log []string }

func (c *serviceCalls) record(s string) { c.log = append(c.log, s) }

func stubServiceSeams(t *testing.T) *serviceCalls {
	t.Helper()
	c := &serviceCalls{}
	oldStart, oldStop, oldInstall, oldRemove := startServiceFn, stopServiceFn, installDaemonFn, removeDaemonFn
	oldRestartLabel, oldRestartDaemon := restartLabelFn, restartDaemonFn
	oldTeardown := teardownSessionsFn
	t.Cleanup(func() {
		teardownSessionsFn = oldTeardown
		startServiceFn, stopServiceFn, installDaemonFn, removeDaemonFn = oldStart, oldStop, oldInstall, oldRemove
		restartLabelFn, restartDaemonFn = oldRestartLabel, oldRestartDaemon
	})
	startServiceFn = func(service.ServiceConfig) error { c.record("start"); return nil }
	stopServiceFn = func(string) error { c.record("stop"); return nil }
	installDaemonFn = func(service.ServiceConfig) error { c.record("install"); return nil }
	removeDaemonFn = func(string) error { c.record("remove"); return nil }
	restartLabelFn = func(l string) error { c.record("restart-label " + l); return nil }
	restartDaemonFn = func(string) error { c.record("restart-own"); return nil }
	teardownSessionsFn = func(string, string, io.Writer) error { c.record("teardown"); return nil }
	return c
}

func TestManagedServiceCommandsRefuseNamingLeoAppAndTouchNothing(t *testing.T) {
	for _, args := range [][]string{{"start"}, {"start", "--daemon"}, {"stop"}, {"stop", "--daemon"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			useTempConfig(t, nil)
			stubManaged(t, managedTestState())
			calls := stubServiceSeams(t)
			cmd := newServiceCmd()
			cmd.SetArgs(args)
			var buf bytes.Buffer
			cmd.SetOut(&buf)
			cmd.SetErr(&buf)

			err := cmd.Execute()

			if err == nil || !strings.Contains(err.Error(), "Leo.app") || !strings.Contains(err.Error(), managedTestLabel) {
				t.Fatalf("err = %v, want a refusal naming Leo.app and %s", err, managedTestLabel)
			}
			if len(calls.log) != 0 {
				t.Fatalf("service seams called: %v", calls.log)
			}
		})
	}
}

func TestUnmanagedServiceStartStillInstalls(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managed.State{})
	calls := stubServiceSeams(t)
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"start", "--daemon"})
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if len(calls.log) != 1 || calls.log[0] != "install" {
		t.Fatalf("calls = %v", calls.log)
	}
}

func TestManagedRestartKickstartsTheMarkerLabelNeverTheCLIOne(t *testing.T) {
	cfg := useTempConfig(t, nil)
	stubManaged(t, managedTestState())
	calls := stubServiceSeams(t)

	deps := newDaemonRestartDeps(cfg)
	if err := deps.restart(cfg.HomePath); err != nil {
		t.Fatal(err)
	}

	if len(calls.log) != 1 || calls.log[0] != "restart-label "+managedTestLabel {
		t.Fatalf("calls = %v, want one restart of %s", calls.log, managedTestLabel)
	}
}

func TestUnmanagedRestartUsesTheCLIsOwnJob(t *testing.T) {
	cfg := useTempConfig(t, nil)
	stubManaged(t, managed.State{})
	calls := stubServiceSeams(t)

	if err := newDaemonRestartDeps(cfg).restart(cfg.HomePath); err != nil {
		t.Fatal(err)
	}

	if len(calls.log) != 1 || calls.log[0] != "restart-own" {
		t.Fatalf("calls = %v", calls.log)
	}
}

func TestDaemonStatusForManagedAsksTheMarkerLabel(t *testing.T) {
	cfg := useTempConfig(t, nil)
	stubManaged(t, managedTestState())
	old := daemonStatusLabelFn
	t.Cleanup(func() { daemonStatusLabelFn = old })
	var asked string
	daemonStatusLabelFn = func(l string) (string, error) { asked = l; return "running (pid 7)", nil }

	got, _ := daemonStatusFor(cfg)

	if asked != managedTestLabel || got != "running (pid 7)" {
		t.Fatalf("asked %q got %q", asked, got)
	}
}

func stubJobLoaded(t *testing.T, loaded bool) *[]string {
	t.Helper()
	old := labelLoadedFn
	t.Cleanup(func() { labelLoadedFn = old })
	var asked []string
	labelLoadedFn = func(l string) bool { asked = append(asked, l); return loaded }
	return &asked
}

// restartJSONOut runs the --json restart against a fixture and decodes stdout.
func restartJSONOut(t *testing.T, f *gracefulFixture, opts gracefulOptions) (map[string]any, error) {
	t.Helper()
	cfg := useTempConfig(t, nil)
	if opts.drainTimeout == 0 {
		opts.drainTimeout = 10 * time.Second
	}
	var out bytes.Buffer
	err := runJSON(&out, func() (any, error) {
		return restartJSON(context.Background(), cfg, managedTestState(), opts, f.deps)
	})
	var got map[string]any
	if jerr := json.Unmarshal(out.Bytes(), &got); jerr != nil {
		t.Fatalf("stdout is not one JSON object: %q (%v)", out.String(), jerr)
	}
	return got, err
}

func TestRestartJSONDaemonDownButJobLoadedIsOK(t *testing.T) {
	stubManaged(t, managedTestState())
	asked := stubJobLoaded(t, true)
	f := newGracefulFixture(false)
	f.drain.busyErr = daemon.ErrDaemonUnreachable

	got, err := restartJSONOut(t, f, gracefulOptions{})

	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"ok": true, "drained": false, "restarted": true}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("stdout = %v, want %v", got, want)
	}
	f.wantLog(t, "busy", "restart")
	if !reflect.DeepEqual(*asked, []string{managedTestLabel}) {
		t.Fatalf("job loaded asked of %v", *asked)
	}
}

func TestRestartJSONJobNotLoadedIsNotRegisteredAndRestartsNothing(t *testing.T) {
	stubManaged(t, managedTestState())
	stubJobLoaded(t, false)
	f := newGracefulFixture(false)

	got, err := restartJSONOut(t, f, gracefulOptions{})

	if ExitCode(err) != 1 || got["error"] != "not_registered" {
		t.Fatalf("exit %d stdout %v", ExitCode(err), got)
	}
	if !strings.Contains(got["message"].(string), managedTestLabel) {
		t.Fatalf("message %q should name the label", got["message"])
	}
	f.wantLog(t)
}

func TestRestartJSONDrainTimeoutWithoutForceFailsAndCancels(t *testing.T) {
	stubManaged(t, managedTestState())
	stubJobLoaded(t, true)
	f := newGracefulFixture(false, "worker")

	got, err := restartJSONOut(t, f, gracefulOptions{graceful: true})

	if ExitCode(err) != 1 || got["error"] != "drain_timeout" {
		t.Fatalf("exit %d stdout %v", ExitCode(err), got)
	}
	if f.log[len(f.log)-1] != "cancel" {
		t.Fatalf("calls = %q, want the drain cancelled", f.log)
	}
	for _, c := range f.log {
		if c == "restart" {
			t.Fatalf("restarted despite the timeout: %q", f.log)
		}
	}
}

func TestRestartJSONReportsADrainedRestart(t *testing.T) {
	stubManaged(t, managedTestState())
	stubJobLoaded(t, true)
	f := newGracefulFixture(false, "a")
	f.drain.doneAt = f.clock.Now().Add(4 * time.Second)

	got, err := restartJSONOut(t, f, gracefulOptions{graceful: true})

	want := map[string]any{"ok": true, "drained": true, "restarted": true}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("err %v stdout %v, want %v", err, got, want)
	}
}

func TestRestartJSONDaemonNotReadyIsOKWithAWarning(t *testing.T) {
	stubManaged(t, managedTestState())
	stubJobLoaded(t, true)
	f := newGracefulFixture(false)
	f.drain.busyErr = daemon.ErrDaemonUnreachable
	f.restartErr = errDaemonNotReady

	got, err := restartJSONOut(t, f, gracefulOptions{})

	if err != nil || got["ok"] != true || got["restarted"] != true {
		t.Fatalf("err %v stdout %v", err, got)
	}
	if w, _ := got["warnings"].([]any); len(w) != 1 {
		t.Fatalf("warnings = %v", got["warnings"])
	}
}

func TestRestartJSONRestartFailureIsCoded(t *testing.T) {
	stubManaged(t, managedTestState())
	stubJobLoaded(t, true)
	f := newGracefulFixture(false)
	f.drain.busyErr = daemon.ErrDaemonUnreachable
	f.restartErr = errors.New("kickstart blew up")

	got, err := restartJSONOut(t, f, gracefulOptions{})

	if ExitCode(err) != 1 || got["error"] != "restart_failed" {
		t.Fatalf("exit %d stdout %v", ExitCode(err), got)
	}
}

func TestServiceRestartCmdJSONIsWiredToTheJSONRule(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managedTestState())
	stubJobLoaded(t, false)
	calls := stubServiceSeams(t)
	stdout, err := runRoot(t, "service", "restart", "--json")

	if ExitCode(err) != 1 || err.Error() != "" {
		t.Fatalf("err = %v, want a silent exit 1", err)
	}
	var got map[string]any
	if jerr := json.Unmarshal([]byte(stdout), &got); jerr != nil || got["error"] != "not_registered" {
		t.Fatalf("stdout = %q", stdout)
	}
	if len(calls.log) != 0 {
		t.Fatalf("service calls = %v", calls.log)
	}
	if strings.Contains(stdout, "Usage") {
		t.Fatalf("stdout must be only the JSON object, got %q", stdout)
	}
}

func TestServiceRestartCmdJSONBadArgsAreJSONToo(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managedTestState())
	stdout, _ := runRoot(t, "service", "restart", "--json", "--drain-timeout", "0s")

	if !strings.HasPrefix(stdout, `{"error":"invalid_args"`) {
		t.Fatalf("stdout = %q", stdout)
	}
}

// A permanent stop tears the sessions down, and only once the daemon is gone:
// a live daemon would respawn them. Failure to stop tears nothing down.
func TestServiceStopTearsDownSessionsAfterTheDaemonStops(t *testing.T) {
	for _, tc := range []struct {
		args []string
		want []string
	}{
		{[]string{"stop"}, []string{"stop", "teardown"}},
		{[]string{"stop", "--daemon"}, []string{"remove", "teardown"}},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			useTempConfig(t, nil)
			stubManaged(t, managed.State{})
			calls := stubServiceSeams(t)
			cmd := newServiceCmd()
			cmd.SetArgs(tc.args)
			cmd.SetOut(&bytes.Buffer{})

			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if strings.Join(calls.log, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("calls = %v, want %v", calls.log, tc.want)
			}
		})
	}
}

func TestServiceStopFailureLeavesSessionsAlone(t *testing.T) {
	useTempConfig(t, nil)
	stubManaged(t, managed.State{})
	calls := stubServiceSeams(t)
	stopServiceFn = func(string) error { calls.record("stop"); return errors.New("not running") }
	cmd := newServiceCmd()
	cmd.SetArgs([]string{"stop"})
	cmd.SetOut(&bytes.Buffer{})

	if err := cmd.Execute(); err == nil {
		t.Fatal("want the stop error")
	}
	if strings.Join(calls.log, ",") != "stop" {
		t.Fatalf("calls = %v, want only stop", calls.log)
	}
}
