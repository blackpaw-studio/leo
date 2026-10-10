package session

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// slugReplacer mirrors claude's on-disk slugification of a working directory:
// every '/' and '.' is replaced with '-', character-for-character, no
// collapsing. E.g. "/Users/alice/.leo/workspace" → "-Users-alice--leo-workspace".
var slugReplacer = strings.NewReplacer("/", "-", ".", "-")

// ProjectSlug returns claude's project slug for a working directory. The slug
// is the folder name under ~/.claude/projects/ that holds <session-id>.jsonl
// files for sessions launched with that cwd.
func ProjectSlug(cwd string) string {
	return slugReplacer.Replace(cwd)
}

// ConfigDir returns claude's config directory for a launch environment:
// CLAUDE_CONFIG_DIR from env, then from the daemon's own environment (which a
// spawned claude inherits), else <HOME>/.claude with HOME taken from env and
// then os.UserHomeDir. A nil env is valid.
func ConfigDir(env map[string]string) (string, error) {
	if dir := ExplicitConfigDir(env); dir != "" {
		return dir, nil
	}
	home, err := HomeDir(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude"), nil
}

// ExplicitConfigDir returns CLAUDE_CONFIG_DIR from env, else from the
// daemon's own environment, or "" when claude would use its default dir.
func ExplicitConfigDir(env map[string]string) string {
	if dir := env["CLAUDE_CONFIG_DIR"]; dir != "" {
		return dir
	}
	return os.Getenv("CLAUDE_CONFIG_DIR")
}

// HomeDir returns the home directory a launch with env sees: env["HOME"],
// else os.UserHomeDir.
func HomeDir(env map[string]string) (string, error) {
	if home := env["HOME"]; home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home directory: %w", err)
	}
	return home, nil
}

// JSONLPath returns the absolute path to claude's session jsonl for the given
// cwd + sessionID under the config dir env resolves to.
func JSONLPath(env map[string]string, cwd, sessionID string) (string, error) {
	dir, err := ConfigDir(env)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "projects", ProjectSlug(cwd), sessionID+".jsonl"), nil
}

// LatestSession returns the session ID of the most recently modified
// <session>.jsonl in claude's project directory for cwd. It mirrors what
// `/resume` inside claude would show at the top of the list: the session the
// user was most recently in for this working directory.
//
// Returns ("", zero time, nil) when:
//   - the project directory does not yet exist (brand-new workspace)
//   - no .jsonl files are present
//   - maxAge > 0 and the newest file is older than maxAge
//
// Other filesystem errors are surfaced.
func LatestSession(env map[string]string, cwd string, maxAge time.Duration) (string, time.Time, error) {
	dir, err := ConfigDir(env)
	if err != nil {
		return "", time.Time{}, err
	}
	projDir := filepath.Join(dir, "projects", ProjectSlug(cwd))

	entries, err := os.ReadDir(projDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, fmt.Errorf("reading %s: %w", projDir, err)
	}

	var (
		bestName string
		bestTime time.Time
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(bestTime) {
			bestTime = info.ModTime()
			bestName = e.Name()
		}
	}
	if bestName == "" {
		return "", time.Time{}, nil
	}
	if maxAge > 0 && time.Since(bestTime) > maxAge {
		return "", time.Time{}, nil
	}
	return strings.TrimSuffix(bestName, ".jsonl"), bestTime, nil
}

// IsResumeStale reports whether the session jsonl at JSONLPath(cwd, sessionID)
// has not been written in at least maxAge. Returns (false, 0, nil) if the file
// does not exist — claude will create it, so we should not drop --resume just
// because the file is missing.
func IsResumeStale(env map[string]string, cwd, sessionID string, maxAge time.Duration) (bool, time.Duration, error) {
	if sessionID == "" || maxAge <= 0 {
		return false, 0, nil
	}
	path, err := JSONLPath(env, cwd, sessionID)
	if err != nil {
		return false, 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, 0, nil
		}
		return false, 0, fmt.Errorf("stat %s: %w", path, err)
	}
	age := time.Since(info.ModTime())
	return age > maxAge, age, nil
}
