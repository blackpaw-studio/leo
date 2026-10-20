package tmux

import (
	"context"
	"errors"
	"io/fs"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"testing/fstest"
)

const managedRoot = "/root/app/tmux"

// fakeVersions answers ServerVersion from a bin -> version table and records
// the order of probes.
type fakeVersions struct {
	answers map[string]string
	probed  []string
}

func (f *fakeVersions) probe(bin string) (string, bool) {
	f.probed = append(f.probed, bin)
	v, ok := f.answers[bin]
	return v, ok
}

// managedOpts builds options over a fake managed root holding a tmux copy in
// each of dirs.
func managedOpts(managedPath string, fv *fakeVersions, dirs ...string) ResolveOptions {
	fsys := fstest.MapFS{}
	for _, d := range dirs {
		fsys[d+"/tmux"] = &fstest.MapFile{Mode: 0o755}
	}
	return ResolveOptions{
		ManagedPath:   managedPath,
		ServerVersion: fv.probe,
		ReadDir:       func(string) ([]fs.DirEntry, error) { return fs.ReadDir(fsys, ".") },
		Exists: func(p string) bool {
			_, err := fs.Stat(fsys, strings.TrimPrefix(p, managedRoot+"/"))
			return err == nil
		},
		Locate: func() (string, error) { return "/usr/bin/tmux", nil },
	}
}

func TestResolve(t *testing.T) {
	pinned := managedRoot + "/3.6a/tmux"
	older := managedRoot + "/3.5/tmux"
	tests := []struct {
		name        string
		managedPath string
		answers     map[string]string
		dirs        []string
		want        Resolution
		wantProbed  []string
	}{
		{
			name: "unmanaged uses Locate for both",
			want: Resolution{Client: "/usr/bin/tmux", Start: "/usr/bin/tmux"},
		},
		{
			name:        "no server running uses tmux_path for both",
			managedPath: pinned,
			dirs:        []string{"3.5", "3.6a"},
			want:        Resolution{Client: pinned, Start: pinned},
			wantProbed:  []string{pinned, older},
		},
		{
			name:        "server matches the pinned version",
			managedPath: pinned,
			answers:     map[string]string{pinned: "3.6a"},
			dirs:        []string{"3.5", "3.6a"},
			want:        Resolution{Client: pinned, Start: pinned},
			wantProbed:  []string{pinned},
		},
		{
			name:        "older server with a copy on disk keeps the server's binary",
			managedPath: pinned,
			answers:     map[string]string{older: "3.5"},
			dirs:        []string{"3.5", "3.6a"},
			want: Resolution{
				Client:  older,
				Start:   pinned,
				Pending: "tmux upgrade pending: server is 3.5, tmux_path is 3.6a; using " + older + " until the server restarts",
			},
			wantProbed: []string{pinned, older},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fv := &fakeVersions{answers: tc.answers}
			got := Resolve(managedOpts(tc.managedPath, fv, tc.dirs...))
			if got.Err != nil {
				t.Fatalf("Err = %v", got.Err)
			}
			if got != tc.want {
				t.Errorf("Resolution = %+v, want %+v", got, tc.want)
			}
			if !reflect.DeepEqual(fv.probed, tc.wantProbed) {
				t.Errorf("probed = %v, want %v", fv.probed, tc.wantProbed)
			}
		})
	}
}

// A server whose version has no copy under the root falls back to tmux_path
// and reports an error naming the missing copy.
func TestResolveServerVersionWithoutCopyIsAnError(t *testing.T) {
	pinned := managedRoot + "/3.6a/tmux"
	missing := managedRoot + "/3.3/tmux"
	// The 3.4 copy answers, and reports a server version nobody has a copy of.
	fv := &fakeVersions{answers: map[string]string{managedRoot + "/3.4/tmux": "3.3"}}
	opts := managedOpts(pinned, fv, "3.4", "3.6a")

	got := Resolve(opts)

	if got.Client != pinned || got.Start != pinned {
		t.Errorf("Client/Start = %q/%q, want both %q", got.Client, got.Start, pinned)
	}
	if got.Err == nil || !strings.Contains(got.Err.Error(), missing) {
		t.Errorf("Err = %v, want it to name the missing %s", got.Err, missing)
	}
	if got.Pending != "" {
		t.Errorf("Pending = %q, want empty", got.Pending)
	}
}

func TestResolveUnmanagedPropagatesLocateError(t *testing.T) {
	want := errors.New("no tmux")
	got := Resolve(ResolveOptions{Locate: func() (string, error) { return "", want }})
	if !errors.Is(got.Err, want) {
		t.Errorf("Err = %v, want %v", got.Err, want)
	}
}

func TestResolveProbesCopiesInSortedOrder(t *testing.T) {
	pinned := managedRoot + "/3.6a/tmux"
	oldest := managedRoot + "/3.2/tmux"
	fv := &fakeVersions{answers: map[string]string{oldest: "3.2", managedRoot + "/3.5/tmux": "3.5"}}

	got := Resolve(managedOpts(pinned, fv, "3.6a", "3.5", "3.2"))

	// ReadDir order is lexical: 3.2, 3.5, 3.6a. The first answer wins.
	if want := []string{pinned, oldest}; !reflect.DeepEqual(fv.probed, want) {
		t.Errorf("probed = %v, want %v", fv.probed, want)
	}
	if got.Client != oldest {
		t.Errorf("Client = %q, want %q", got.Client, oldest)
	}
}

// A version directory without a tmux binary in it is not a candidate, and
// neither is a stray file in the root.
func TestResolveSkipsEntriesWithoutABinary(t *testing.T) {
	pinned := managedRoot + "/3.6a/tmux"
	fsys := fstest.MapFS{"stray": &fstest.MapFile{}, "3.5/other": &fstest.MapFile{}, "3.6a/tmux": &fstest.MapFile{}}
	fv := &fakeVersions{}
	opts := managedOpts(pinned, fv, "3.6a")
	opts.ReadDir = func(string) ([]fs.DirEntry, error) { return fs.ReadDir(fsys, ".") }

	Resolve(opts)

	if want := []string{pinned}; !reflect.DeepEqual(fv.probed, want) {
		t.Errorf("probed = %v, want only the pinned binary %v", fv.probed, want)
	}
}

// The default probe asks the leo server for its version: formats expand
// server-side, so this reports the server's version, not the client's.
func TestServerVersionProbeArgvAndParsing(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	var gotName string
	var gotArgs []string
	execCommand = func(_ context.Context, name string, args ...string) *exec.Cmd {
		gotName, gotArgs = name, args
		return exec.Command("printf", "%s", "3.6a\n")
	}

	v, ok := probeServerVersion("/m/tmux")

	if !ok || v != "3.6a" {
		t.Fatalf("probeServerVersion = %q, %v; want 3.6a, true", v, ok)
	}
	if gotName != "/m/tmux" {
		t.Errorf("binary = %q, want /m/tmux", gotName)
	}
	want := []string{"-L", "leo", "display-message", "-p", "#{version}"}
	if !reflect.DeepEqual(gotArgs, want) {
		t.Errorf("argv = %v, want %v", gotArgs, want)
	}
}

func TestServerVersionProbeFailsWhenNoServer(t *testing.T) {
	orig := execCommand
	defer func() { execCommand = orig }()
	execCommand = func(context.Context, string, ...string) *exec.Cmd { return exec.Command("false") }

	if v, ok := probeServerVersion("/m/tmux"); ok {
		t.Errorf("probeServerVersion = %q, true; want false", v)
	}
}
