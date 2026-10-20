package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/managed"
	"github.com/blackpaw-studio/leo/internal/service"
)

const (
	hoPrevLabel = "com.blackpaw.leo.abc123"
	hoAppLabel  = "studio.blackpaw.leo.daemon"
	hoPlist     = "/u/Library/LaunchAgents/com.blackpaw.leo.abc123.plist"
	hoBackup    = hoPlist + ".handed-over"
	hoMarker    = "/home/state/managed.json"
	hoReleasing = hoMarker + ".releasing"
)

// hoFixture drives runHandover with fakes that all record into one ordered
// log shared with the fake drain client, so tests assert the exact sequence.
type hoFixture struct {
	g      *gracefulFixture
	deps   handoverDeps
	failOn map[string]error // op log line -> error
}

func newHoFixture(t *testing.T, busy ...string) *hoFixture {
	t.Helper()
	g := newGracefulFixture(false, busy...)
	if len(busy) > 0 {
		g.drain.doneAt = g.clock.Now().Add(4 * time.Second)
	}
	f := &hoFixture{g: g, failOn: map[string]error{}}
	do := func(line string) error {
		g.drain.record(line)
		return f.failOn[line]
	}
	f.deps = handoverDeps{
		supported:      true,
		home:           "/home",
		prevLabel:      hoPrevLabel,
		plist:          hoPlist,
		markerPath:     hoMarker,
		marker:         managed.State{},
		currentVersion: "v0.60.0",
		daemonRunning:  func() bool { return true },
		jobLoaded:      func(string) bool { return true },
		exists:         func(string) bool { return true },
		executable:     func(string) bool { return true },
		plistEnv: func(p string) (map[string]string, error) {
			err := do("plutil-env " + p)
			return map[string]string{"PATH": "/usr/bin"}, err
		},
		plistProgram: func(p string) (string, error) {
			err := do("plutil-program " + p)
			return "/opt/homebrew/bin/leo", err
		},
		binaryVersion: func(p string) (string, error) {
			err := do("version " + p)
			return "v0.60.0", err
		},
		bootout:   func(l string) error { return do("bootout " + l) },
		bootstrap: func(p string) error { return do("bootstrap " + p) },
		rename:    func(a, b string) error { return do("rename " + a + " " + b) },
		remove:    func(p string) error { return do("remove " + p) },
		waitLock:  func(context.Context) error { return do("wait-lock") },
		waitReady: func(context.Context) error { return do("wait-ready") },
		drain:     g.deps,
	}
	return f
}

func (f *hoFixture) run(t *testing.T, dir handoverDirection, opts handoverOptions) handoverResult {
	t.Helper()
	if opts.drainTimeout == 0 {
		opts.drainTimeout = 10 * time.Second
	}
	return runHandover(context.Background(), dir, opts, f.deps)
}

func (f *hoFixture) wantLog(t *testing.T, want ...string) {
	t.Helper()
	if !reflect.DeepEqual(f.g.log, want) {
		t.Fatalf("calls =\n  %s\nwant\n  %s", strings.Join(f.g.log, "\n  "), strings.Join(want, "\n  "))
	}
}

func toApp(label string) handoverOptions { return handoverOptions{label: label} }

func TestHandoverToAppHappyPathMovesThePlistAsideAfterADrain(t *testing.T) {
	f := newHoFixture(t, "worker")

	res := f.run(t, handoverToApp, toApp(hoAppLabel))

	if !res.OK || res.PreviousLabel != hoPrevLabel || res.PlistBackup != hoBackup {
		t.Fatalf("result = %+v", res)
	}
	if !reflect.DeepEqual(res.Env, map[string]string{"PATH": "/usr/bin"}) {
		t.Fatalf("env = %v", res.Env)
	}
	f.wantLog(t, "plutil-env "+hoPlist,
		"busy", "begin 10s", "commit force=false",
		"bootout "+hoPrevLabel, "rename "+hoPlist+" "+hoBackup, "wait-lock")
}

func TestHandoverToAppWithNothingBusyStillRunsTheCriticalSection(t *testing.T) {
	f := newHoFixture(t)

	res := f.run(t, handoverToApp, toApp(hoAppLabel))

	if !res.OK {
		t.Fatalf("result = %+v", res)
	}
	f.wantLog(t, "plutil-env "+hoPlist, "busy", "bootout "+hoPrevLabel, "rename "+hoPlist+" "+hoBackup, "wait-lock")
}

func TestHandoverToAppCLIDaemonNotLoadedOnlyMovesThePlist(t *testing.T) {
	f := newHoFixture(t)
	f.deps.jobLoaded = func(string) bool { return false }
	f.deps.daemonRunning = func() bool { return false }

	res := f.run(t, handoverToApp, toApp(hoAppLabel))

	if !res.OK || res.PlistBackup != hoBackup {
		t.Fatalf("result = %+v", res)
	}
	f.wantLog(t, "plutil-env "+hoPlist, "rename "+hoPlist+" "+hoBackup)
}

func TestHandoverToAppFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name        string
		busy        []string
		tweak       func(*hoFixture)
		failOn      map[string]error
		wantStep    string
		wantErr     string
		wantRolled  *bool
		wantLog     []string
		wantStopped bool
	}{
		{
			name:     "bootout fails: no rollback, drain cancelled",
			busy:     []string{"w"},
			failOn:   map[string]error{"bootout " + hoPrevLabel: boom},
			wantStep: "bootout", wantErr: "bootout_failed",
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "commit force=false", "bootout " + hoPrevLabel, "cancel"},
		},
		{
			name:     "plist move fails: job bootstrapped again",
			busy:     []string{"w"},
			failOn:   map[string]error{"rename " + hoPlist + " " + hoBackup: boom},
			wantStep: "plist", wantErr: "plist_move_failed", wantRolled: ptr(true),
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "commit force=false", "bootout " + hoPrevLabel,
				"rename " + hoPlist + " " + hoBackup, "bootstrap " + hoPlist, "cancel"},
		},
		{
			name:     "plist move fails and so does the re-bootstrap",
			busy:     []string{"w"},
			failOn:   map[string]error{"rename " + hoPlist + " " + hoBackup: boom, "bootstrap " + hoPlist: boom},
			wantStep: "plist", wantErr: "plist_move_failed", wantRolled: ptr(false),
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "commit force=false", "bootout " + hoPrevLabel,
				"rename " + hoPlist + " " + hoBackup, "bootstrap " + hoPlist, "cancel"},
		},
		{
			name:     "lock never frees: plist moved back and job bootstrapped",
			busy:     []string{"w"},
			failOn:   map[string]error{"wait-lock": boom},
			wantStep: "lock", wantErr: "lock_timeout", wantRolled: ptr(true),
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "commit force=false", "bootout " + hoPrevLabel,
				"rename " + hoPlist + " " + hoBackup, "wait-lock",
				"rename " + hoBackup + " " + hoPlist, "bootstrap " + hoPlist, "cancel"},
		},
		{
			name:     "lock never frees and the plist cannot move back: rollback failed, no bootstrap",
			busy:     []string{"w"},
			failOn:   map[string]error{"wait-lock": boom, "rename " + hoBackup + " " + hoPlist: boom},
			wantStep: "lock", wantErr: "lock_timeout", wantRolled: ptr(false),
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "commit force=false", "bootout " + hoPrevLabel,
				"rename " + hoPlist + " " + hoBackup, "wait-lock", "rename " + hoBackup + " " + hoPlist, "cancel"},
		},
		{
			name: "drain timeout: nothing was touched",
			busy: []string{"w"},
			tweak: func(f *hoFixture) {
				f.g.drain.doneAt = time.Time{}
			},
			wantStep: "drain", wantErr: "drain_timeout",
			wantLog: []string{"plutil-env " + hoPlist, "busy", "begin 10s", "cancel"},
		},
		{
			name:     "reading the env fails before anything changes",
			failOn:   map[string]error{"plutil-env " + hoPlist: boom},
			wantStep: "env", wantErr: "env_unreadable",
			wantLog: []string{"plutil-env " + hoPlist},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoFixture(t, tt.busy...)
			for k, v := range tt.failOn {
				f.failOn[k] = v
			}
			if tt.tweak != nil {
				tt.tweak(f)
			}

			res := f.run(t, handoverToApp, toApp(hoAppLabel))

			if res.OK || res.Step != tt.wantStep || res.Error != tt.wantErr {
				t.Fatalf("result = %+v, want step %q error %q", res, tt.wantStep, tt.wantErr)
			}
			if !reflect.DeepEqual(res.RolledBack, tt.wantRolled) {
				t.Fatalf("rolled_back = %v, want %v", fmtBoolPtr(res.RolledBack), fmtBoolPtr(tt.wantRolled))
			}
			f.wantLog(t, tt.wantLog...)
		})
	}
}

func TestHandoverToAppPreflight(t *testing.T) {
	tests := []struct {
		name    string
		label   string
		tweak   func(*hoFixture)
		wantErr string
	}{
		{name: "already managed", label: hoAppLabel, tweak: func(f *hoFixture) { f.deps.marker = managed.State{Managed: true} }, wantErr: "already_managed"},
		{name: "label is not reverse-DNS", label: "nope", wantErr: "invalid_label"},
		{name: "label is the CLI's own", label: "com.blackpaw.leo", wantErr: "invalid_label"},
		{name: "label equals the previous label", label: hoPrevLabel, wantErr: "label_conflict"},
		{name: "plist missing", label: hoAppLabel, tweak: func(f *hoFixture) { f.deps.exists = func(string) bool { return false } }, wantErr: "plist_missing"},
		{name: "daemon running with no launchd job", label: hoAppLabel, tweak: func(f *hoFixture) { f.deps.jobLoaded = func(string) bool { return false } }, wantErr: "no_launchd_job"},
		{name: "unsupported platform", label: hoAppLabel, tweak: func(f *hoFixture) { f.deps.supported = false }, wantErr: "unsupported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoFixture(t)
			if tt.tweak != nil {
				tt.tweak(f)
			}

			res := f.run(t, handoverToApp, toApp(tt.label))

			if res.OK || res.Step != "preflight" || res.Error != tt.wantErr {
				t.Fatalf("result = %+v, want preflight/%s", res, tt.wantErr)
			}
			if len(f.g.log) != 0 {
				t.Fatalf("preflight failure touched %v", f.g.log)
			}
		})
	}
}

func TestHandoverToAppDryRunPlansWithoutChangingAnything(t *testing.T) {
	f := newHoFixture(t, "worker")

	res := f.run(t, handoverToApp, handoverOptions{label: hoAppLabel, dryRun: true})

	if !res.OK || !res.DryRun || res.PreviousLabel != hoPrevLabel {
		t.Fatalf("result = %+v", res)
	}
	wantPlan := []string{
		"drain agents (graceful)",
		"launchctl bootout " + hoPrevLabel,
		"rename " + hoPlist + " " + hoBackup,
		"wait for daemon.lock",
	}
	if !reflect.DeepEqual(res.Plan, wantPlan) {
		t.Fatalf("plan = %q", res.Plan)
	}
	f.wantLog(t, "plutil-env "+hoPlist)
}

func managedHo(f *hoFixture) {
	f.deps.marker = managed.State{Managed: true, Marker: managed.Marker{LaunchdLabel: hoAppLabel}}
}

func TestHandoverToCLIHappyPath(t *testing.T) {
	f := newHoFixture(t, "worker")
	managedHo(f)

	res := f.run(t, handoverToCLI, handoverOptions{})

	if !res.OK || res.PreviousLabel != hoAppLabel || res.PlistBackup != hoBackup {
		t.Fatalf("result = %+v", res)
	}
	f.wantLog(t,
		"plutil-program "+hoBackup, "version /opt/homebrew/bin/leo",
		"busy", "begin 10s", "commit force=false",
		"bootout "+hoAppLabel, "wait-lock",
		"rename "+hoMarker+" "+hoReleasing,
		"rename "+hoBackup+" "+hoPlist, "bootstrap "+hoPlist, "wait-ready",
		"remove "+hoReleasing)
}

func TestHandoverToCLIWarnsWhenTheRestoredBinaryIsOlder(t *testing.T) {
	f := newHoFixture(t)
	managedHo(f)
	f.deps.currentVersion = "v0.60.0"
	f.deps.binaryVersion = func(string) (string, error) { return "leo v0.50.1", nil }

	res := f.run(t, handoverToCLI, handoverOptions{})

	if !res.OK || len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0], "older") {
		t.Fatalf("result = %+v", res)
	}
}

func TestHandoverToCLIFailures(t *testing.T) {
	boom := errors.New("boom")
	drainLog := []string{"plutil-program " + hoBackup, "version /opt/homebrew/bin/leo", "busy", "begin 10s", "commit force=false"}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	tests := []struct {
		name        string
		failOn      map[string]error
		wantStep    string
		wantErr     string
		wantRolled  *bool
		wantStopped bool
		wantLog     []string
	}{
		{
			name: "managed bootout fails: still running, drain cancelled", failOn: map[string]error{"bootout " + hoAppLabel: boom},
			wantStep: "bootout", wantErr: "bootout_failed",
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "cancel"}),
		},
		{
			name: "lock never frees: managed job is down", failOn: map[string]error{"wait-lock": boom},
			wantStep: "lock", wantErr: "lock_timeout", wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "cancel"}),
		},
		{
			name: "marker rename fails", failOn: map[string]error{"rename " + hoMarker + " " + hoReleasing: boom},
			wantStep: "marker", wantErr: "marker_move_failed", wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "rename " + hoMarker + " " + hoReleasing, "cancel"}),
		},
		{
			name: "restoring the plist fails: marker comes back", failOn: map[string]error{"rename " + hoBackup + " " + hoPlist: boom},
			wantStep: "plist", wantErr: "plist_restore_failed", wantRolled: ptr(true), wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "rename " + hoMarker + " " + hoReleasing,
				"rename " + hoBackup + " " + hoPlist, "rename " + hoReleasing + " " + hoMarker, "cancel"}),
		},
		{
			name: "bootstrap fails: CLI label booted out, backup and marker restored", failOn: map[string]error{"bootstrap " + hoPlist: boom},
			wantStep: "bootstrap", wantErr: "bootstrap_failed", wantRolled: ptr(true), wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "rename " + hoMarker + " " + hoReleasing,
				"rename " + hoBackup + " " + hoPlist, "bootstrap " + hoPlist,
				"bootout " + hoPrevLabel, "rename " + hoPlist + " " + hoBackup, "rename " + hoReleasing + " " + hoMarker, "cancel"}),
		},
		{
			name: "daemon never ready: rolled back the same way", failOn: map[string]error{"wait-ready": boom},
			wantStep: "ready", wantErr: "not_ready", wantRolled: ptr(true), wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "rename " + hoMarker + " " + hoReleasing,
				"rename " + hoBackup + " " + hoPlist, "bootstrap " + hoPlist, "wait-ready",
				"bootout " + hoPrevLabel, "rename " + hoPlist + " " + hoBackup, "rename " + hoReleasing + " " + hoMarker, "cancel"}),
		},
		{
			name: "rollback itself fails", failOn: map[string]error{"wait-ready": boom, "bootout " + hoPrevLabel: boom},
			wantStep: "ready", wantErr: "not_ready", wantRolled: ptr(false), wantStopped: true,
			wantLog: cat(drainLog, []string{"bootout " + hoAppLabel, "wait-lock", "rename " + hoMarker + " " + hoReleasing,
				"rename " + hoBackup + " " + hoPlist, "bootstrap " + hoPlist, "wait-ready",
				"bootout " + hoPrevLabel, "rename " + hoPlist + " " + hoBackup, "rename " + hoReleasing + " " + hoMarker, "cancel"}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoFixture(t, "worker")
			managedHo(f)
			for k, v := range tt.failOn {
				f.failOn[k] = v
			}

			res := f.run(t, handoverToCLI, handoverOptions{})

			if res.OK || res.Step != tt.wantStep || res.Error != tt.wantErr {
				t.Fatalf("result = %+v, want step %q error %q", res, tt.wantStep, tt.wantErr)
			}
			if !reflect.DeepEqual(res.RolledBack, tt.wantRolled) {
				t.Fatalf("rolled_back = %v, want %v", fmtBoolPtr(res.RolledBack), fmtBoolPtr(tt.wantRolled))
			}
			if res.ManagedStopped != tt.wantStopped {
				t.Fatalf("managed_stopped = %v, want %v", res.ManagedStopped, tt.wantStopped)
			}
			f.wantLog(t, tt.wantLog...)
		})
	}
}

func TestHandoverToCLIRemovingTheReleasingMarkerFailsOnlyAsAWarning(t *testing.T) {
	f := newHoFixture(t)
	managedHo(f)
	f.failOn["remove "+hoReleasing] = errors.New("busy")

	res := f.run(t, handoverToCLI, handoverOptions{})

	if !res.OK || len(res.Warnings) != 1 {
		t.Fatalf("result = %+v", res)
	}
}

func TestHandoverToCLIPreflight(t *testing.T) {
	tests := []struct {
		name    string
		tweak   func(*hoFixture)
		wantErr string
	}{
		{name: "not managed", tweak: func(f *hoFixture) { f.deps.marker = managed.State{} }, wantErr: "not_managed"},
		{name: "no backup", tweak: func(f *hoFixture) { f.deps.exists = func(p string) bool { return p != hoBackup } }, wantErr: "backup_missing"},
		{name: "backup's program is gone", tweak: func(f *hoFixture) { f.deps.executable = func(string) bool { return false } }, wantErr: "backup_unusable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newHoFixture(t)
			managedHo(f)
			tt.tweak(f)

			res := f.run(t, handoverToCLI, handoverOptions{})

			if res.OK || res.Step != "preflight" || res.Error != tt.wantErr {
				t.Fatalf("result = %+v, want preflight/%s", res, tt.wantErr)
			}
			for _, c := range f.g.log {
				if strings.HasPrefix(c, "bootout") || strings.HasPrefix(c, "rename") || c == "busy" {
					t.Fatalf("preflight failure changed state: %v", f.g.log)
				}
			}
		})
	}
}

func TestHandoverToCLIDryRunPlansWithoutChangingAnything(t *testing.T) {
	f := newHoFixture(t, "worker")
	managedHo(f)

	res := f.run(t, handoverToCLI, handoverOptions{dryRun: true})

	if !res.OK || !res.DryRun || len(res.Plan) == 0 {
		t.Fatalf("result = %+v", res)
	}
	for _, c := range f.g.log {
		if strings.HasPrefix(c, "bootout") || strings.HasPrefix(c, "bootstrap") || strings.HasPrefix(c, "rename") || c == "busy" {
			t.Fatalf("dry run changed state: %v", f.g.log)
		}
	}
}

func ptr[T any](v T) *T { return &v }

func fmtBoolPtr(b *bool) string {
	if b == nil {
		return "nil"
	}
	return fmt.Sprint(*b)
}

func TestWaitForLockReleasePollsUntilTheLockFrees(t *testing.T) {
	clock := &gracefulClock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	attempts, released := 0, 0
	acquire := func() (func() error, error) {
		attempts++
		if attempts < 3 {
			return nil, &service.LockHeldError{Home: "/h", PID: 42}
		}
		return func() error { released++; return nil }, nil
	}

	err := waitForLockRelease(context.Background(), acquire, clock, time.Minute, 500*time.Millisecond)

	if err != nil || attempts != 3 || released != 1 {
		t.Fatalf("err %v attempts %d released %d", err, attempts, released)
	}
	if got := clock.Now().Sub(time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)); got != time.Second {
		t.Fatalf("waited %s, want 1s (two polls)", got)
	}
}

func TestWaitForLockReleaseTimesOutNamingTheHolder(t *testing.T) {
	clock := &gracefulClock{now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)}
	acquire := func() (func() error, error) { return nil, &service.LockHeldError{Home: "/h", PID: 42} }

	err := waitForLockRelease(context.Background(), acquire, clock, 2*time.Second, time.Second)

	if err == nil || !strings.Contains(err.Error(), "pid 42") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitForLockReleaseOtherErrorsFailAtOnce(t *testing.T) {
	clock := &gracefulClock{now: time.Now()}
	boom := errors.New("EACCES")
	calls := 0
	acquire := func() (func() error, error) { calls++; return nil, boom }

	err := waitForLockRelease(context.Background(), acquire, clock, time.Minute, time.Second)

	if !errors.Is(err, boom) || calls != 1 {
		t.Fatalf("err %v calls %d", err, calls)
	}
}

func TestVersionOlder(t *testing.T) {
	tests := []struct {
		restored, current string
		want              bool
	}{
		{"leo v0.50.1", "v0.60.0", true},
		{"leo v0.60.0", "v0.60.0", false},
		{"leo v0.61.0", "v0.60.0", false},
		{"leo v0.60.0-20-gabc", "v0.60.0-25-gdef", false},
		{"leo dev", "v0.60.0", false},
		{"", "v0.60.0", false},
		{"leo v0.50.1", "dev", false},
	}
	for _, tt := range tests {
		if got := versionOlder(tt.restored, tt.current); got != tt.want {
			t.Errorf("versionOlder(%q, %q) = %v, want %v", tt.restored, tt.current, got, tt.want)
		}
	}
}

func TestHandoverCmdRequiresExactlyOneDirection(t *testing.T) {
	for _, args := range [][]string{{}, {"--to-app", "--to-cli"}, {"--to-app"}} {
		cmd := newServiceHandoverCmd()
		cmd.SetArgs(args)
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v: want an error", args)
		}
	}
}
