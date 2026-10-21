package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// webEnableFixture runs runWebEnable against a temp config, with the restart
// driven through the real C1 routing (newGracefulDepsTo -> restartLabelFn) so
// the managed label's argv is what gets asserted.
type webEnableFixture struct {
	cfg     *config.Config
	calls   *serviceCalls
	running bool
	// web is what the running daemon reports about its listener.
	web      daemon.WebStatus
	gfix     *gracefulFixture
	cfgBytes func() []byte
}

func newWebEnableFixture(t *testing.T, mutate func(*config.Config)) *webEnableFixture {
	t.Helper()
	cfg := useTempConfig(t, mutate)
	stubManaged(t, managedTestState())
	calls := stubServiceSeams(t)
	stubJobLoaded(t, true)
	oldDeps := defaultDaemonRestartDeps
	t.Cleanup(func() { defaultDaemonRestartDeps = oldDeps })
	defaultDaemonRestartDeps = daemonRestartDeps{
		currentPID: func(context.Context, string) (int, bool) { return 0, false },
		restart:    func(string) error { t.Error("unmanaged restart reached"); return nil },
		waitReady:  func(context.Context, string, time.Duration, int) error { return nil },
	}
	f := &webEnableFixture{cfg: cfg, calls: calls, running: true, gfix: newGracefulFixture(false)}
	path := cfgFile
	f.cfgBytes = func() []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return f
}

func (f *webEnableFixture) run(t *testing.T, opts webEnableOptions) (webEnableResult, error) {
	t.Helper()
	if opts.gracefulOptions.drainTimeout == 0 {
		opts.gracefulOptions.drainTimeout = 10 * time.Second
	}
	deps := webEnableDeps{
		daemonWeb: func(string) (daemon.WebStatus, bool) { return f.web, f.running },
		gracefulDeps: func(cfg *config.Config) gracefulDeps {
			d := newGracefulDepsTo(cfg, &bytes.Buffer{})
			d.drain = f.gfix.drain
			d.isInteractive = func() bool { return false }
			return d
		},
		save: saveConfig,
	}
	return runWebEnable(context.Background(), f.cfg, opts, deps)
}

func reloaded(t *testing.T) *config.Config {
	t.Helper()
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestWebEnableTurnsTheListenerOnAndLeavesAStoppedDaemonAlone(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	f.running = false

	res, err := f.run(t, webEnableOptions{})

	if err != nil {
		t.Fatal(err)
	}
	want := webEnableResult{OK: true, Changed: true, Restarted: false, Web: webEnableState{Enabled: true, Bind: "127.0.0.1", Port: 8370}}
	if !reflect.DeepEqual(res, want) {
		t.Fatalf("result = %+v, want %+v", res, want)
	}
	if !reloaded(t).Web.Enabled {
		t.Fatal("web.enabled was not written")
	}
	if len(f.calls.log) != 0 {
		t.Fatalf("restart calls = %v", f.calls.log)
	}
}

func TestWebEnableRestartsTheManagedJobOnceWhenTheDaemonIsRunning(t *testing.T) {
	f := newWebEnableFixture(t, nil)

	res, err := f.run(t, webEnableOptions{})

	if err != nil || !res.OK || !res.Changed || !res.Restarted {
		t.Fatalf("result = %+v err %v", res, err)
	}
	if want := []string{"restart-label " + managedTestLabel}; !reflect.DeepEqual(f.calls.log, want) {
		t.Fatalf("restart calls = %v, want %v", f.calls.log, want)
	}
}

func TestWebEnableAlreadyEnabledAndServingWritesAndRestartsNothing(t *testing.T) {
	f := newWebEnableFixture(t, func(c *config.Config) { c.Web.Enabled = true })
	f.web = daemon.WebStatus{Listening: true, Bind: "127.0.0.1", Port: 8370}
	before := f.cfgBytes()

	res, err := f.run(t, webEnableOptions{})

	if err != nil || !res.OK || res.Changed || res.Restarted {
		t.Fatalf("result = %+v err %v", res, err)
	}
	if !bytes.Equal(before, f.cfgBytes()) {
		t.Fatal("config rewritten for a no-op")
	}
	if len(f.calls.log) != 0 {
		t.Fatalf("restart calls = %v", f.calls.log)
	}
}

// The config can already say enabled while the daemon is not serving it: a
// restart that failed after the save, or a listener enabled by hand. That is
// not a no-op.
func TestWebEnableAlreadyEnabledButNotServedRestartsWithoutRewriting(t *testing.T) {
	tests := []struct {
		name string
		web  daemon.WebStatus
	}{
		{name: "listener off", web: daemon.WebStatus{}},
		{name: "serving another port", web: daemon.WebStatus{Listening: true, Bind: "127.0.0.1", Port: 9999}},
		{name: "serving another bind", web: daemon.WebStatus{Listening: true, Bind: "0.0.0.0", Port: 8370}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newWebEnableFixture(t, func(c *config.Config) { c.Web.Enabled = true })
			f.web = tt.web
			before := f.cfgBytes()

			res, err := f.run(t, webEnableOptions{})

			if err != nil || !res.OK || res.Changed || !res.Restarted {
				t.Fatalf("result = %+v err %v", res, err)
			}
			if !bytes.Equal(before, f.cfgBytes()) {
				t.Fatal("config rewritten although it already matched")
			}
			if want := []string{"restart-label " + managedTestLabel}; !reflect.DeepEqual(f.calls.log, want) {
				t.Fatalf("restart calls = %v, want %v", f.calls.log, want)
			}
		})
	}
}

func TestWebEnableRerunAfterAFailedRestartRestartsAgain(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	restartLabelFn = func(string) error { return os.ErrPermission }
	if _, err := f.run(t, webEnableOptions{}); err == nil {
		t.Fatal("first run should fail its restart")
	}
	f.calls = stubServiceSeams(t)
	f.cfg = reloaded(t) // a rerun is a new process: it reads the saved file

	res, err := f.run(t, webEnableOptions{gracefulOptions: gracefulOptions{force: true}})

	if err != nil || !res.OK || res.Changed || !res.Restarted {
		t.Fatalf("rerun result = %+v err %v (the saved config must not turn it into a no-op)", res, err)
	}
	if want := []string{"restart-label " + managedTestLabel}; !reflect.DeepEqual(f.calls.log, want) {
		t.Fatalf("restart calls = %v, want %v", f.calls.log, want)
	}
}

func TestWebEnableInvalidBindLeavesTheFileUntouched(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	before := f.cfgBytes()

	_, err := f.run(t, webEnableOptions{bind: "not-an-ip"})

	var je *jsonError
	if !asJSONError(err, &je) || je.Code != "invalid_config" {
		t.Fatalf("err = %v, want invalid_config", err)
	}
	if !bytes.Equal(before, f.cfgBytes()) {
		t.Fatal("config changed despite the invalid bind")
	}
	if len(f.calls.log) != 0 {
		t.Fatalf("restart calls = %v", f.calls.log)
	}
}

func TestWebEnableNonLoopbackBindNeedsAnAllowedHost(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	f.running = false
	before := f.cfgBytes()

	_, err := f.run(t, webEnableOptions{bind: "192.168.1.20"})

	var je *jsonError
	if !asJSONError(err, &je) || je.Code != "invalid_config" || !strings.Contains(je.Message, "allowed_hosts") {
		t.Fatalf("err = %v", err)
	}
	if !bytes.Equal(before, f.cfgBytes()) {
		t.Fatal("config changed despite the failed validation")
	}

	res, err := f.run(t, webEnableOptions{bind: "192.168.1.20", allowHosts: []string{"leo.local", "192.168.1.20", "leo.local"}})

	if err != nil || !res.OK || res.Web.Bind != "192.168.1.20" {
		t.Fatalf("result = %+v err %v", res, err)
	}
	got := reloaded(t)
	if got.Web.Bind != "192.168.1.20" || !reflect.DeepEqual(got.Web.AllowedHosts, []string{"leo.local", "192.168.1.20"}) {
		t.Fatalf("web = %+v", got.Web)
	}
}

func TestWebEnableDaemonOutsideLaunchdIsSavedWithAWarning(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	stubJobLoaded(t, false)

	res, err := f.run(t, webEnableOptions{})

	if err != nil || !res.OK || !res.Changed || res.Restarted || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v err %v", res, err)
	}
	if !reloaded(t).Web.Enabled {
		t.Fatal("web.enabled was not written")
	}
}

func TestWebEnableRestartFailureIsCodedAndKeepsTheSavedConfig(t *testing.T) {
	f := newWebEnableFixture(t, nil)
	f.calls = stubServiceSeams(t)
	restartLabelFn = func(string) error { return os.ErrPermission }

	_, err := f.run(t, webEnableOptions{})

	var je *jsonError
	if !asJSONError(err, &je) || je.Code != "restart_failed" {
		t.Fatalf("err = %v", err)
	}
	if !reloaded(t).Web.Enabled {
		t.Fatal("config should stay saved")
	}
}

func TestWebEnableResultJSONShape(t *testing.T) {
	f := newWebEnableFixture(t, nil)

	res, _ := f.run(t, webEnableOptions{})
	b, _ := json.Marshal(res)

	const want = `{"ok":true,"changed":true,"restarted":true,"web":{"enabled":true,"bind":"127.0.0.1","port":8370}}`
	if string(b) != want {
		t.Fatalf("json = %s, want %s", b, want)
	}
}
