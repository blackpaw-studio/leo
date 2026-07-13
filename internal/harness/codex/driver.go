// Package-file: codex's tmuxtui profile hooks. The TUI cannot pin a session
// id at launch, so the id is discovered post-hoc from rollout files and
// resume rides in as the `resume <id>` subcommand prefix on relaunch.
package codex

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// codexConfigPath/codexSessionsDir are seams tests replace with temp dirs.
var codexConfigPath = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

var codexSessionsDir = func() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "sessions"), nil
}

// ensureWorkspaceTrusted idempotently registers h.Workspace as trusted in
// ~/.codex/config.toml so the TUI skips its trust dialog (which the dialog
// policy correctly refuses to auto-answer — it contains "trust"). This is
// the same write codex itself performs when the user answers "Yes"; inline
// -c overrides do NOT skip the dialog (verified 2026-07-12).
func ensureWorkspaceTrusted(h harness.SessionHandle) error {
	path, err := codexConfigPath()
	if err != nil {
		return fmt.Errorf("codex: resolving config path: %w", err)
	}
	header := fmt.Sprintf("[projects.%q]", h.Workspace)
	existing, err := os.ReadFile(path) // #nosec G304 -- fixed well-known path
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("codex: reading %s: %w", path, err)
	}
	if strings.Contains(string(existing), header) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("codex: creating %s: %w", filepath.Dir(path), err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) // #nosec G304
	if err != nil {
		return fmt.Errorf("codex: opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "\n%s\ntrust_level = \"trusted\"\n", header); err != nil {
		return fmt.Errorf("codex: writing trust entry: %w", err)
	}
	return nil
}

// refreshSessionArgs rewrites the launch argv from the stored session id:
// prefix `resume <id>` (codex resumes via a subcommand, flags stay valid
// after it — verified 2026-07-12), replacing any stale resume prefix.
func refreshSessionArgs(args []string, storedID string) []string {
	base := args
	if len(base) >= 2 && base[0] == "resume" {
		base = base[2:]
	}
	if storedID == "" {
		return append([]string{}, base...)
	}
	return append([]string{"resume", storedID}, base...)
}

// recoverQuickExitArgs: a quick exit while resuming means the resume itself
// is poisoned — strip it, clear the stored id, and mark no-resume (mirrors
// claude's ladder step 2). A fresh launch that quick-exits just clears.
func recoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction) {
	if len(args) >= 2 && args[0] == "resume" {
		return append([]string{}, args[2:]...), harness.QuickExitClearAndNoResume
	}
	return args, harness.QuickExitClearSession
}

// rolloutMeta is the shape of a rollout file's first line.
type rolloutMeta struct {
	Payload struct {
		ID  string `json:"id"`
		Cwd string `json:"cwd"`
	} `json:"payload"`
}

// discoverSessionID finds the newest rollout created at/after `since` whose
// recorded cwd is h.Workspace. Rollouts only exist once the FIRST turn runs
// (verified: TUI launch alone creates none), so callers poll. Two agents
// sharing a workspace can race here — newest wins and a warning is logged;
// the residual ambiguity is accepted (see spec Risks).
func discoverSessionID(_ context.Context, h harness.SessionHandle, since time.Time) (string, error) {
	root, err := codexSessionsDir()
	if err != nil {
		return "", err
	}
	var bestID string
	var bestMod time.Time
	matches := 0
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		id, cwd, ok := readRolloutMeta(path)
		if !ok || !harness.SamePath(cwd, h.Workspace) {
			return nil
		}
		matches++
		if info.ModTime().After(bestMod) {
			bestMod = info.ModTime()
			bestID = id
		}
		return nil
	})
	if matches > 1 {
		log.Printf("codex: %d rollouts match workspace %s since %s; using newest (%s)", matches, h.Workspace, since.Format(time.RFC3339), bestID)
	}
	return bestID, nil
}

// readRolloutMeta parses a rollout file's first line for the session id and
// cwd. ok=false on any parse trouble — discovery just keeps polling.
func readRolloutMeta(path string) (id, cwd string, ok bool) {
	f, err := os.Open(path) // #nosec G304 -- path enumerated from codex's own sessions dir
	if err != nil {
		return "", "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	if !sc.Scan() {
		return "", "", false
	}
	var meta rolloutMeta
	if json.Unmarshal(sc.Bytes(), &meta) != nil {
		return "", "", false
	}
	return meta.Payload.ID, meta.Payload.Cwd, meta.Payload.ID != "" && meta.Payload.Cwd != ""
}
