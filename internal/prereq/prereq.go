package prereq

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/tmux"
)

var (
	lookPath   = exec.LookPath
	runCommand = func(path string, args ...string) ([]byte, error) {
		return exec.Command(path, args...).Output()
	}
	userHomeDir = os.UserHomeDir
	locateTmux  = tmux.Locate
)

// BinaryResult holds the result of checking for a harness CLI binary.
type BinaryResult struct {
	Path    string
	Version string
	OK      bool
}

// CheckBinary checks if the named CLI binary is installed and reachable.
func CheckBinary(name string) BinaryResult {
	path, err := lookPath(name)
	if err != nil {
		return BinaryResult{}
	}

	output, err := runCommand(path, "--version")
	if err != nil {
		return BinaryResult{Path: path, OK: true}
	}

	version := strings.TrimSpace(string(output))
	return BinaryResult{Path: path, Version: version, OK: true}
}

// MinTmuxMajor/MinTmuxMinor is the oldest tmux leo supports: 3.2, the release
// that added `-e` to new-session. Leo passes agent env that way to keep
// credentials out of the pane's start command, so on an older tmux every
// spawn fails and the supervisor retries forever — agents appear to never
// start, with the real cause buried in the service log.
const (
	MinTmuxMajor = 3
	MinTmuxMinor = 2
)

// tmuxVersionRe pulls the numeric version out of `tmux -V` output, which looks
// like "tmux 3.6a", "tmux 3.2", or "tmux next-3.4" on dev builds.
var tmuxVersionRe = regexp.MustCompile(`(\d+)\.(\d+)`)

// tmuxVersionAtLeast reports whether the raw `tmux -V` output is at least
// major.minor. An unparseable version returns true: leo refuses to block
// startup on a guess about an unrecognised build (a dev/master build is
// likely newer, not older).
func tmuxVersionAtLeast(raw string, major, minor int) bool {
	m := tmuxVersionRe.FindStringSubmatch(raw)
	if m == nil {
		return true
	}
	gotMajor, err := strconv.Atoi(m[1])
	if err != nil {
		return true
	}
	gotMinor, err := strconv.Atoi(m[2])
	if err != nil {
		return true
	}
	if gotMajor != major {
		return gotMajor > major
	}
	return gotMinor >= minor
}

// TmuxPath returns the tmux binary leo will actually run, or "" if none.
// Delegates to tmux.Locate so a version check here inspects the same binary
// the supervisor spawns — a separate search order could otherwise validate
// one tmux and run another.
func TmuxPath() string {
	path, err := locateTmux()
	if err != nil {
		return ""
	}
	return path
}

// CheckTmux checks if tmux is installed and reachable.
func CheckTmux() bool {
	return TmuxPath() != ""
}

// TmuxVersion returns the raw `tmux -V` output and whether it satisfies leo's
// minimum. ok is true when tmux is missing (callers report absence
// separately) or when the version cannot be parsed.
func TmuxVersion() (raw string, ok bool) {
	path := TmuxPath()
	if path == "" {
		return "", true
	}
	out, err := runCommand(path, "-V")
	if err != nil {
		return "", true
	}
	raw = strings.TrimSpace(string(out))
	return raw, tmuxVersionAtLeast(raw, MinTmuxMajor, MinTmuxMinor)
}

// FindOpenClaw searches for an OpenClaw installation in common locations.
func FindOpenClaw() string {
	home, _ := userHomeDir()

	ocPath := filepath.Join(home, ".openclaw")
	if _, err := os.Stat(ocPath); err == nil {
		return ocPath
	}

	return ""
}
