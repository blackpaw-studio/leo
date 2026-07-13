// Package-file: opencode's tmuxtui profile hooks. The TUI cannot pin a
// session id at launch (no session exists until the first turn runs), so the
// id is discovered post-hoc via `opencode session list --format json` and
// resume rides in as the `-s <id>` flag pair on relaunch.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// execCommand is the process-spawn seam driver tests replace; production
// uses exec.CommandContext. CI has no opencode binary on PATH.
var execCommand = exec.CommandContext

// refreshSessionArgs rewrites the launch argv from the stored session id:
// the `-s <id>` pair may sit anywhere in argv, so it is stripped by scanning
// (mirroring claude's stripResumeArg) rather than assumed positional, then a
// fresh `-s <storedID>` pair is appended when storedID is non-empty.
func refreshSessionArgs(args []string, storedID string) []string {
	base := stripSessionFlag(args)
	if storedID == "" {
		return base
	}
	return append(base, "-s", storedID)
}

// stripSessionFlag removes the `-s <id>` pair from args wherever it appears.
func stripSessionFlag(args []string) []string {
	result := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "-s" && i+1 < len(args) {
			i++ // skip the value too
			continue
		}
		result = append(result, args[i])
	}
	return result
}

// recoverQuickExitArgs: a quick exit while a `-s <id>` resume flag is present
// means the resume itself is poisoned — strip it, clear the stored id, and
// mark no-resume (mirrors claude/codex's ladder step 2). A fresh launch that
// quick-exits just clears.
func recoverQuickExitArgs(args []string) ([]string, harness.QuickExitAction) {
	if hasSessionFlag(args) {
		return stripSessionFlag(args), harness.QuickExitClearAndNoResume
	}
	return args, harness.QuickExitClearSession
}

// hasSessionFlag reports whether a `-s <id>` pair is present in args.
func hasSessionFlag(args []string) bool {
	for i := 0; i < len(args); i++ {
		if args[i] == "-s" && i+1 < len(args) {
			return true
		}
	}
	return false
}

// discoverSessionID wraps latestSessionIDForDir with the tmuxtui discovery
// contract: ctx threads through to the subprocess (a cancelled discovery
// loop kills the `session list` child instead of hanging), and no match is
// reported as ("", nil) so the caller's poll loop keeps trying rather than
// treating it as an error.
func discoverSessionID(ctx context.Context, h harness.SessionHandle, since time.Time) (string, error) {
	id := latestSessionIDForDir(ctx, h.Workspace, since.UnixMilli())
	return id, nil
}

// latestSessionIDForDir runs `opencode session list --format json`, filters
// entries to workspace and to created >= sinceMillis (created is epoch
// MILLISECONDS), and returns the newest matching entry's id — or "" on any
// failure or no match (tolerable; callers poll).
func latestSessionIDForDir(ctx context.Context, workspace string, sinceMillis int64) string {
	cmd := execCommand(ctx, Opencode{}.Binary(), "session", "list", "--format", "json")
	cmd.Dir = workspace
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return ""
	}
	var entries []struct {
		ID        string `json:"id"`
		Created   int64  `json:"created"`
		Directory string `json:"directory"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &entries); err != nil {
		return ""
	}
	var bestID string
	var bestCreated int64 = -1
	for _, e := range entries {
		if e.Created < sinceMillis {
			continue
		}
		if !harness.SamePath(e.Directory, workspace) {
			continue
		}
		if e.Created > bestCreated {
			bestCreated = e.Created
			bestID = e.ID
		}
	}
	return bestID
}
