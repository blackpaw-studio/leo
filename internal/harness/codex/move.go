package codex

import (
	"context"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
)

var (
	_ harness.SessionMover      = Codex{}
	_ harness.Forker            = Codex{}
	_ harness.TranscriptLocator = Codex{}
)

// SessionRoot is $CODEX_HOME for env.
func (Codex) SessionRoot(env map[string]string) string { return CodexHome(env) }

// Transcript finds id's rollout under <root>/sessions.
func (c Codex) Transcript(root, _, id string) (string, bool) {
	rels, err := c.SessionFiles(root, "", []string{id})
	return filepath.Join(root, "sessions"), err == nil && len(rels) > 0
}

// SessionFiles finds each id's rollout under sessions/ by file name
// (rollout-<timestamp>-<id>.jsonl, possibly compressed). The global state
// DBs stay behind: codex read-repairs its index from a rollout it finds by
// file name.
func (Codex) SessionFiles(root, _ string, ids []string) ([]string, error) {
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id != "" {
			want[id] = true
		}
	}
	var out []string
	err := filepath.WalkDir(filepath.Join(root, "sessions"), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // a missing or unreadable dir has no rollouts to send
		}
		if d.IsDir() || !want[rolloutID(d.Name())] {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	sort.Strings(out)
	return out, err
}

// rolloutID is the session id in a rollout file name, "" for other files.
// Ids are UUIDs, so the id is the last five dash-separated fields; the test
// fixtures' short ids are the last field alone.
func rolloutID(name string) string {
	if !strings.HasPrefix(name, "rollout-") {
		return ""
	}
	base, _, ok := strings.Cut(name, ".jsonl")
	if !ok {
		return ""
	}
	fields := strings.Split(base, "-")
	if len(fields) >= 5 && isUUID(strings.Join(fields[len(fields)-5:], "-")) {
		return strings.Join(fields[len(fields)-5:], "-")
	}
	return fields[len(fields)-1]
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
				return false
			}
		}
	}
	return true
}

// PrepareSend: a rollout travels as a file, so nothing is staged.
func (Codex) PrepareSend(context.Context, harness.MoveContext, string) error { return nil }

// FinishReceive: codex finds a received rollout by file name on resume.
func (Codex) FinishReceive(context.Context, harness.MoveContext, string) error { return nil }

// ForkArgs prefixes `fork <src>`, replacing any session prefix. codex picks
// the fork's id itself; it is discovered after the first turn and the
// refresher then swaps the prefix for `resume <id>`.
func (Codex) ForkArgs(args []string, src, _ string) []string {
	return append([]string{"fork", src}, stripSessionPrefix(args)...)
}

// stripSessionPrefix drops a leading `resume <id>` or `fork <id>`.
func stripSessionPrefix(args []string) []string {
	if len(args) >= 2 && (args[0] == "resume" || args[0] == "fork") {
		return append([]string{}, args[2:]...)
	}
	return append([]string{}, args...)
}
