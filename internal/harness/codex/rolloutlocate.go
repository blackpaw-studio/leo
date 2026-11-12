package codex

import (
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// RolloutPath is the rollout file of session id under codex home, or "" when
// there is none (or id is empty).
func RolloutPath(home, id string) string {
	if id == "" {
		return ""
	}
	rels, err := (Codex{}).SessionFiles(home, "", []string{id})
	if err != nil || len(rels) == 0 {
		return ""
	}
	return filepath.Join(home, rels[0])
}

// LatestRolloutFor finds the newest rollout modified at/after since whose
// recorded cwd is workspace, looking only in the date directories from the
// day before since onward (codex files a rollout under the day it started, so
// older directories hold older sessions). matches counts every rollout that
// qualified; path and id are the newest's. The day of slack covers a rollout
// started before midnight and a directory dated in another zone.
func LatestRolloutFor(home, workspace string, since time.Time) (path, id string, matches int) {
	cutoff := since.AddDate(0, 0, -1)
	best, matches := scanRollouts(filepath.Join(home, "sessions"), workspace, since, func(rel []string) bool {
		return dateDirBefore(rel, cutoff)
	})
	return best.path, best.id, matches
}

type rolloutHit struct {
	path, id string
	mod      time.Time
}

// scanRollouts walks root for rollouts modified at/after since whose cwd is
// workspace, returning the newest and how many matched. skipDir, when set, is
// asked about each directory (by its path parts below root) and prunes it.
func scanRollouts(root, workspace string, since time.Time, skipDir func(rel []string) bool) (best rolloutHit, matches int) {
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // unreadable entries are skipped, not fatal
		}
		if d.IsDir() {
			if skipDir != nil && path != root {
				if rel, relErr := filepath.Rel(root, path); relErr == nil && skipDir(strings.Split(rel, string(filepath.Separator))) {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since) {
			return nil
		}
		id, cwd, ok := readRolloutMeta(path)
		if !ok || !harness.SamePath(cwd, workspace) {
			return nil
		}
		matches++
		if info.ModTime().After(best.mod) {
			best = rolloutHit{path: path, id: id, mod: info.ModTime()}
		}
		return nil
	})
	return best, matches
}

// dateDirBefore reports whether the YYYY, YYYY/MM or YYYY/MM/DD directory rel
// lies wholly before cutoff's day. Names that are not dates are kept.
func dateDirBefore(rel []string, cutoff time.Time) bool {
	want := []int{cutoff.Year(), int(cutoff.Month()), cutoff.Day()}
	for i := 0; i < len(rel) && i < len(want); i++ {
		n, err := strconv.Atoi(rel[i])
		if err != nil {
			return false
		}
		switch {
		case n < want[i]:
			return true
		case n > want[i]:
			return false
		}
	}
	return false
}
