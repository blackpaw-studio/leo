package service

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

func stubResolve(t *testing.T, res tmux.Resolution) *tmux.ResolveOptions {
	t.Helper()
	orig := resolveTmuxFn
	t.Cleanup(func() { resolveTmuxFn = orig })
	got := new(tmux.ResolveOptions)
	resolveTmuxFn = func(opts tmux.ResolveOptions) tmux.Resolution {
		*got = opts
		return res
	}
	return got
}

// The daemon feeds Resolve the marker's tmux_path and uses Client for every
// tmux call, including the foreground server it supervises and restarts.
func TestResolveDaemonTmuxUsesClientAndPassesManagedPath(t *testing.T) {
	seen := stubResolve(t, tmux.Resolution{Client: "/root/3.5/tmux", Start: "/root/3.6a/tmux", Pending: "tmux upgrade pending: x"})
	var log bytes.Buffer

	got, err := resolveDaemonTmux("/root/3.6a/tmux", &log)

	if err != nil {
		t.Fatal(err)
	}
	if got != "/root/3.5/tmux" {
		t.Errorf("tmux = %q, want Client /root/3.5/tmux", got)
	}
	if seen.ManagedPath != "/root/3.6a/tmux" {
		t.Errorf("ManagedPath = %q, want the marker's tmux_path", seen.ManagedPath)
	}
	if want := "tmux upgrade pending: x\n"; !strings.Contains(log.String(), want) {
		t.Errorf("log = %q, want it to contain %q", log.String(), want)
	}
}

func TestResolveDaemonTmuxLogsErrorAndContinuesWithManagedPath(t *testing.T) {
	stubResolve(t, tmux.Resolution{Client: "/root/3.6a/tmux", Start: "/root/3.6a/tmux", Err: errors.New("server is 3.4, copy missing")})
	var log bytes.Buffer

	got, err := resolveDaemonTmux("/root/3.6a/tmux", &log)

	if err != nil || got != "/root/3.6a/tmux" {
		t.Fatalf("got %q, %v; want the managed path and no error", got, err)
	}
	if !strings.Contains(log.String(), "server is 3.4, copy missing") {
		t.Errorf("log = %q, want the error", log.String())
	}
}

func TestResolveDaemonTmuxFailsWhenNoTmuxCanBeFound(t *testing.T) {
	want := errors.New("tmux not found")
	stubResolve(t, tmux.Resolution{Err: want})

	_, err := resolveDaemonTmux("", &bytes.Buffer{})

	if !errors.Is(err, want) {
		t.Errorf("error = %v, want %v", err, want)
	}
}

func TestResolveDaemonTmuxLeavesUnmanagedPathEmpty(t *testing.T) {
	seen := stubResolve(t, tmux.Resolution{Client: "/usr/bin/tmux", Start: "/usr/bin/tmux"})

	got, err := resolveDaemonTmux("", &bytes.Buffer{})

	if err != nil || got != "/usr/bin/tmux" {
		t.Fatalf("got %q, %v", got, err)
	}
	if !reflect.DeepEqual(*seen, tmux.ResolveOptions{}) {
		t.Errorf("options = %+v, want zero value (unmanaged)", *seen)
	}
}
