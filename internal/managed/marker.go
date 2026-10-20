// Package managed reads the managed-install marker (state/managed.json) that
// Leo.app writes when it owns the daemon. A valid marker switches leo into
// managed mode: stable persisted paths, the app's tmux, and no self-update.
// Any problem with the marker degrades to unmanaged behaviour plus a warning;
// leo never refuses to run because of it.
package managed

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
)

const (
	// MarkerFile is the marker's name inside the state directory.
	MarkerFile = "managed.json"
	// MarkerVersion is the only marker schema version leo understands.
	MarkerVersion = 1
	// CLILaunchdLabel is the launchd label of the CLI-installed daemon. A
	// marker naming it would route restarts at the wrong job.
	CLILaunchdLabel = "com.blackpaw.leo"
)

var reverseDNS = regexp.MustCompile(`^[A-Za-z0-9-]+(\.[A-Za-z0-9_-]+)+$`)

// Marker is the on-disk managed.json contract between leo and Leo.app.
type Marker struct {
	Version      int    `json:"version"`
	Manager      string `json:"manager"`
	BundleID     string `json:"bundle_id"`
	LaunchdLabel string `json:"launchd_label"`
	SelfPath     string `json:"self_path"`
	TmuxPath     string `json:"tmux_path"`
	// Env is optional: variables the daemon applies to its own process
	// environment at startup, so agents inherit them. They override inherited
	// values.
	Env map[string]string `json:"env,omitempty"`
}

// State is the result of loading the marker. Marker holds expanded paths and
// is only populated when Managed is true.
type State struct {
	Marker
	Managed bool
	Warning string
}

// Deps are the filesystem and platform seams Load reads through.
type Deps struct {
	ReadFile func(string) ([]byte, error)
	Stat     func(string) (fs.FileInfo, error)
	UserHome func() (string, error)
	GOOS     string
}

// DefaultDeps returns Deps backed by the real filesystem.
func DefaultDeps() Deps {
	return Deps{ReadFile: os.ReadFile, Stat: os.Stat, UserHome: os.UserHomeDir, GOOS: runtime.GOOS}
}

// Load reads <stateDir>/managed.json. An absent file is unmanaged without a
// warning; every other failure is unmanaged with one.
func Load(stateDir string, d Deps) State {
	path := filepath.Join(stateDir, MarkerFile)
	data, err := d.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return State{}
	}
	if err != nil {
		return rejected("cannot read %s: %v", path, err)
	}
	marker, err := validate(path, data, d)
	if err != nil {
		return rejected("%s ignored: %v", path, err)
	}
	return State{Marker: marker, Managed: true}
}

func rejected(format string, args ...any) State {
	return State{Warning: fmt.Sprintf(format, args...)}
}

func validate(path string, data []byte, d Deps) (Marker, error) {
	if d.GOOS != "darwin" {
		return Marker{}, fmt.Errorf("managed installs are only supported on darwin (running on %s)", d.GOOS)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return Marker{}, fmt.Errorf("malformed JSON: %w", err)
	}
	if m.Version != MarkerVersion {
		return Marker{}, fmt.Errorf("unsupported version %d (want %d)", m.Version, MarkerVersion)
	}
	if err := checkNotWritableByOthers(path, d); err != nil {
		return Marker{}, err
	}
	if !reverseDNS.MatchString(m.LaunchdLabel) || m.LaunchdLabel == CLILaunchdLabel {
		return Marker{}, fmt.Errorf("launchd_label %q must be reverse-DNS and not %s", m.LaunchdLabel, CLILaunchdLabel)
	}
	home, err := d.UserHome()
	if err != nil {
		return Marker{}, fmt.Errorf("cannot resolve user home to expand paths: %w", err)
	}
	if m.SelfPath, err = existingAbsPath("self_path", m.SelfPath, home, d); err != nil {
		return Marker{}, err
	}
	if m.TmuxPath, err = existingAbsPath("tmux_path", m.TmuxPath, home, d); err != nil {
		return Marker{}, err
	}
	if err := checkTmuxLayout(m.TmuxPath); err != nil {
		return Marker{}, err
	}
	if err := validateEnv(m.Env); err != nil {
		return Marker{}, err
	}
	return m, nil
}

// checkNotWritableByOthers rejects a marker that group or world can rewrite:
// its self_path ends up in hooks that agents execute.
func checkNotWritableByOthers(path string, d Deps) error {
	info, err := d.Stat(path)
	if err != nil {
		return fmt.Errorf("cannot stat marker: %w", err)
	}
	if info.Mode().Perm()&0o022 != 0 {
		return fmt.Errorf("marker is group- or world-writable (%v)", info.Mode().Perm())
	}
	return nil
}

func existingAbsPath(field, raw, home string, d Deps) (string, error) {
	p := expandHome(raw, home)
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("%s %q must be absolute", field, raw)
	}
	if _, err := d.Stat(p); err != nil {
		return "", fmt.Errorf("%s %q does not exist", field, p)
	}
	return p, nil
}

func expandHome(p, home string) string {
	switch {
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

// checkTmuxLayout enforces the <root>/<ver>/tmux layout the version guard
// relies on to find sibling copies.
func checkTmuxLayout(p string) error {
	verDir := filepath.Dir(p)
	if filepath.Base(p) != "tmux" || verDir == "/" || verDir == "." || filepath.Dir(verDir) == verDir {
		return fmt.Errorf("tmux_path %q must look like <root>/<version>/tmux", p)
	}
	return nil
}

// validateEnv rejects entries that cannot be set on a process: keys must be
// non-empty and free of '=' and NUL, values free of NUL.
func validateEnv(env map[string]string) error {
	for k, v := range env {
		if k == "" || strings.ContainsAny(k, "=\x00") {
			return fmt.Errorf("env key %q must be non-empty and contain no '=' or NUL", k)
		}
		if strings.ContainsRune(v, 0) {
			return fmt.Errorf("env value for %q must not contain NUL", k)
		}
	}
	return nil
}

// ApplyEnv sets every entry of env through setenv, in key order.
func ApplyEnv(env map[string]string, setenv func(key, value string) error) error {
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if err := setenv(k, env[k]); err != nil {
			return fmt.Errorf("applying marker env %s: %w", k, err)
		}
	}
	return nil
}

// SelfExecutable returns the resolver for leo's own persisted path: the
// marker's self_path when managed (deliberately not symlink-resolved, so it
// survives app updates), otherwise exe.
func SelfExecutable(st State, exe func() (string, error)) func() (string, error) {
	if !st.Managed {
		return exe
	}
	return func() (string, error) { return st.SelfPath, nil }
}
