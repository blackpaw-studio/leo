//go:build darwin

package service

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// fakeRun replaces runCommand with a recorder that answers via reply.
func fakeRun(t *testing.T, reply func(argv []string) (string, error)) *[][]string {
	t.Helper()
	orig := runCommand
	t.Cleanup(func() { runCommand = orig })
	var calls [][]string
	runCommand = func(name string, args ...string) (string, error) {
		argv := append([]string{name}, args...)
		calls = append(calls, argv)
		return reply(argv)
	}
	return &calls
}

func target(label string) string { return fmt.Sprintf("gui/%d/%s", os.Getuid(), label) }

func TestRestartLabelKickstartsExactlyThatLabel(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return "", nil })

	if err := RestartLabel("studio.blackpaw.leo.daemon"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"launchctl", "kickstart", "-k", target("studio.blackpaw.leo.daemon")}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %v, want %v", *calls, want)
	}
}

func TestRestartLabelFailureDoesNotConsultAPlist(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return "Could not find service", errors.New("exit 113") })

	err := RestartLabel("studio.blackpaw.leo.daemon")

	if err == nil {
		t.Fatal("want error")
	}
	if len(*calls) != 1 {
		t.Fatalf("calls = %v, want only the kickstart", *calls)
	}
}

func TestDaemonStatusLabel(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		err   error
		want  string
		loads bool
	}{
		{name: "running", out: "gui/501/x = {\n\tpid = 4242\n}", want: "running (pid 4242)", loads: true},
		{name: "loaded not running", out: "gui/501/x = {\n\tstate = waiting\n}", want: "installed but not running", loads: true},
		{name: "not loaded", err: errors.New("exit 113"), want: "not registered", loads: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := fakeRun(t, func([]string) (string, error) { return tt.out, tt.err })

			got, _ := DaemonStatusLabel("x")
			loaded := LabelLoaded("x")

			if got != tt.want || loaded != tt.loads {
				t.Fatalf("status = %q loaded = %v, want %q %v", got, loaded, tt.want, tt.loads)
			}
			for _, c := range *calls {
				if !reflect.DeepEqual(c, []string{"launchctl", "print", target("x")}) {
					t.Fatalf("argv = %v", c)
				}
			}
		})
	}
}

func TestBootstrapWithRetryBacksOffThenSucceeds(t *testing.T) {
	attempts := 0
	calls := fakeRun(t, func([]string) (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("busy")
		}
		return "", nil
	})
	var slept []time.Duration

	err := bootstrapWithRetry("/p/x.plist", func(d time.Duration) { slept = append(slept, d) })

	if err != nil {
		t.Fatal(err)
	}
	wantArgv := []string{"launchctl", "bootstrap", fmt.Sprintf("gui/%d", os.Getuid()), "/p/x.plist"}
	if len(*calls) != 3 || !reflect.DeepEqual((*calls)[2], wantArgv) {
		t.Fatalf("calls = %v", *calls)
	}
	if !reflect.DeepEqual(slept, []time.Duration{500 * time.Millisecond, time.Second}) {
		t.Fatalf("slept = %v", slept)
	}
}

func TestBootstrapWithRetryGivesUpAfterThreeAttempts(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return "", errors.New("nope") })

	err := bootstrapWithRetry("/p/x.plist", func(time.Duration) {})

	if err == nil || len(*calls) != 3 {
		t.Fatalf("err = %v calls = %d", err, len(*calls))
	}
}

func TestBootoutLabelArgv(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return "", nil })

	if err := BootoutLabel("com.blackpaw.leo"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"launchctl", "bootout", target("com.blackpaw.leo")}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %v", *calls)
	}
}

func TestPlistEnvExtractsEnvironmentVariables(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return `{"PATH":"/usr/bin","HOME":"/h"}`, nil })

	env, err := PlistEnv("/p/x.plist")

	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(env, map[string]string{"PATH": "/usr/bin", "HOME": "/h"}) {
		t.Fatalf("env = %v", env)
	}
	want := [][]string{{"plutil", "-extract", "EnvironmentVariables", "json", "-o", "-", "/p/x.plist"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %v", *calls)
	}
}

func TestPlistEnvMissingKeyIsEmpty(t *testing.T) {
	fakeRun(t, func([]string) (string, error) {
		return "Could not extract value, error: No value at that key path or invalid key path: EnvironmentVariables\n", errors.New("exit 1")
	})

	env, err := PlistEnv("/p/x.plist")

	if err != nil || len(env) != 0 {
		t.Fatalf("env = %v err = %v", env, err)
	}
}

func TestPlistEnvOtherFailuresPropagate(t *testing.T) {
	fakeRun(t, func([]string) (string, error) { return "/p/x.plist: file does not exist", errors.New("exit 1") })

	if _, err := PlistEnv("/p/x.plist"); err == nil {
		t.Fatal("want error")
	}
}

func TestPlistProgramPathArgv(t *testing.T) {
	calls := fakeRun(t, func([]string) (string, error) { return "/opt/homebrew/bin/leo\n", nil })

	got, err := PlistProgramPath("/p/x.plist")

	if err != nil || got != "/opt/homebrew/bin/leo" {
		t.Fatalf("got %q err %v", got, err)
	}
	want := [][]string{{"plutil", "-extract", "ProgramArguments.0", "raw", "-o", "-", "/p/x.plist"}}
	if !reflect.DeepEqual(*calls, want) {
		t.Fatalf("argv = %v", *calls)
	}
}
