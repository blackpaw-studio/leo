package managed

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"
)

type fakeFile struct {
	data string
	mode fs.FileMode
}

type fakeFS struct {
	files map[string]fakeFile
	// existing paths that are not the marker (stat targets)
	paths map[string]bool
}

type fakeInfo struct {
	mode fs.FileMode
	name string
}

func (i fakeInfo) Name() string       { return i.name }
func (i fakeInfo) Size() int64        { return 0 }
func (i fakeInfo) Mode() fs.FileMode  { return i.mode }
func (i fakeInfo) ModTime() time.Time { return time.Time{} }
func (i fakeInfo) IsDir() bool        { return false }
func (i fakeInfo) Sys() any           { return nil }

func (f fakeFS) deps(goos string) Deps {
	return Deps{
		ReadFile: func(p string) ([]byte, error) {
			if file, ok := f.files[p]; ok {
				return []byte(file.data), nil
			}
			return nil, fs.ErrNotExist
		},
		Stat: func(p string) (fs.FileInfo, error) {
			if file, ok := f.files[p]; ok {
				return fakeInfo{mode: file.mode, name: p}, nil
			}
			if f.paths[p] {
				return fakeInfo{mode: 0o755, name: p}, nil
			}
			return nil, fs.ErrNotExist
		},
		UserHome: func() (string, error) { return "/Users/test", nil },
		GOOS:     goos,
	}
}

const (
	stateDir   = "/home/state"
	markerPath = "/home/state/managed.json"
	selfPath   = "/Users/test/Library/Application Support/Leo/bin/leo"
	tmuxPath   = "/Users/test/Library/Application Support/Leo/tmux/3.6a/tmux"
)

func markerJSON(overrides map[string]string) string {
	fields := map[string]string{
		"version":       `1`,
		"manager":       `"Leo.app"`,
		"bundle_id":     `"studio.blackpaw.leo.macos"`,
		"launchd_label": `"studio.blackpaw.leo.daemon"`,
		"self_path":     `"~/Library/Application Support/Leo/bin/leo"`,
		"tmux_path":     `"~/Library/Application Support/Leo/tmux/3.6a/tmux"`,
	}
	for k, v := range overrides {
		fields[k] = v
	}
	var parts []string
	for k, v := range fields {
		if v == "" {
			continue
		}
		parts = append(parts, `"`+k+`":`+v)
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func validFS(body string) fakeFS {
	return fakeFS{
		files: map[string]fakeFile{markerPath: {data: body, mode: 0o644}},
		paths: map[string]bool{selfPath: true, tmuxPath: true},
	}
}

func TestLoad(t *testing.T) {
	tests := []struct {
		name        string
		fs          fakeFS
		goos        string
		wantManaged bool
		wantWarn    string // substring; "" means no warning
	}{
		{
			name:        "valid marker expands ~ and is managed",
			fs:          validFS(markerJSON(nil)),
			goos:        "darwin",
			wantManaged: true,
		},
		{
			name:        "unknown JSON fields are ignored",
			fs:          validFS(strings.Replace(markerJSON(nil), "{", `{"future":"x",`, 1)),
			goos:        "darwin",
			wantManaged: true,
		},
		{
			name: "absent file is unmanaged without a warning",
			fs:   fakeFS{},
			goos: "darwin",
		},
		{
			name:     "malformed JSON warns",
			fs:       validFS(`{not json`),
			goos:     "darwin",
			wantWarn: "managed.json",
		},
		{
			name:     "unknown version warns",
			fs:       validFS(markerJSON(map[string]string{"version": "2"})),
			goos:     "darwin",
			wantWarn: "version",
		},
		{
			name:     "relative self_path warns",
			fs:       validFS(markerJSON(map[string]string{"self_path": `"bin/leo"`})),
			goos:     "darwin",
			wantWarn: "self_path",
		},
		{
			name: "missing self_path target warns",
			fs: fakeFS{
				files: map[string]fakeFile{markerPath: {data: markerJSON(nil), mode: 0o644}},
				paths: map[string]bool{tmuxPath: true},
			},
			goos:     "darwin",
			wantWarn: "self_path",
		},
		{
			name: "missing tmux_path target warns",
			fs: fakeFS{
				files: map[string]fakeFile{markerPath: {data: markerJSON(nil), mode: 0o644}},
				paths: map[string]bool{selfPath: true},
			},
			goos:     "darwin",
			wantWarn: "tmux_path",
		},
		{
			name: "tmux_path not shaped <root>/<ver>/tmux warns",
			fs: fakeFS{
				files: map[string]fakeFile{markerPath: {data: markerJSON(map[string]string{"tmux_path": `"/opt/tmux-bin"`}), mode: 0o644}},
				paths: map[string]bool{selfPath: true, "/opt/tmux-bin": true},
			},
			goos:     "darwin",
			wantWarn: "tmux_path",
		},
		{
			name:     "non reverse-DNS label warns",
			fs:       validFS(markerJSON(map[string]string{"launchd_label": `"leo daemon"`})),
			goos:     "darwin",
			wantWarn: "launchd_label",
		},
		{
			name:     "the CLI-install label is refused",
			fs:       validFS(markerJSON(map[string]string{"launchd_label": `"com.blackpaw.leo"`})),
			goos:     "darwin",
			wantWarn: "launchd_label",
		},
		{
			name: "group-writable marker warns",
			fs: fakeFS{
				files: map[string]fakeFile{markerPath: {data: markerJSON(nil), mode: 0o664}},
				paths: map[string]bool{selfPath: true, tmuxPath: true},
			},
			goos:     "darwin",
			wantWarn: "writable",
		},
		{
			name: "world-writable marker warns",
			fs: fakeFS{
				files: map[string]fakeFile{markerPath: {data: markerJSON(nil), mode: 0o646}},
				paths: map[string]bool{selfPath: true, tmuxPath: true},
			},
			goos:     "darwin",
			wantWarn: "writable",
		},
		{
			name:     "non-darwin ignores the marker with a warning",
			fs:       validFS(markerJSON(nil)),
			goos:     "linux",
			wantWarn: "darwin",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Load(stateDir, tc.fs.deps(tc.goos))
			if got.Managed != tc.wantManaged {
				t.Fatalf("Managed = %v, want %v (warning %q)", got.Managed, tc.wantManaged, got.Warning)
			}
			if tc.wantWarn == "" && got.Warning != "" {
				t.Fatalf("unexpected warning %q", got.Warning)
			}
			if tc.wantWarn != "" && !strings.Contains(got.Warning, tc.wantWarn) {
				t.Fatalf("warning %q does not contain %q", got.Warning, tc.wantWarn)
			}
			if !tc.wantManaged && got.SelfPath != "" {
				t.Fatalf("unmanaged state leaked SelfPath %q", got.SelfPath)
			}
		})
	}
}

func TestLoadExpandsHomeInPaths(t *testing.T) {
	got := Load(stateDir, validFS(markerJSON(nil)).deps("darwin"))
	if got.SelfPath != selfPath {
		t.Errorf("SelfPath = %q, want %q", got.SelfPath, selfPath)
	}
	if got.TmuxPath != tmuxPath {
		t.Errorf("TmuxPath = %q, want %q", got.TmuxPath, tmuxPath)
	}
	if got.LaunchdLabel != "studio.blackpaw.leo.daemon" || got.Manager != "Leo.app" || got.BundleID != "studio.blackpaw.leo.macos" {
		t.Errorf("unexpected marker fields: %+v", got.Marker)
	}
}

func TestLoadHomeLookupFailureWarns(t *testing.T) {
	d := validFS(markerJSON(nil)).deps("darwin")
	d.UserHome = func() (string, error) { return "", errors.New("no home") }
	got := Load(stateDir, d)
	if got.Managed || !strings.Contains(got.Warning, "home") {
		t.Fatalf("got %+v, want unmanaged with a home warning", got)
	}
}

func TestSelfExecutable(t *testing.T) {
	exe := func() (string, error) { return "/brew/bin/leo", nil }
	if got, _ := SelfExecutable(State{Managed: true, Marker: Marker{SelfPath: selfPath}}, exe)(); got != selfPath {
		t.Errorf("managed = %q, want %q", got, selfPath)
	}
	if got, _ := SelfExecutable(State{}, exe)(); got != "/brew/bin/leo" {
		t.Errorf("unmanaged = %q, want the os.Executable result", got)
	}
}
