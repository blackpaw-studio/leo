package consult

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"syscall"
)

// Actions a prune report assigns to a worktree. The first four are
// assessWorktree's verdicts; the rest are decided here.
const (
	PruneRemove        = string(verdictRemove)
	PruneKeepDirty     = string(verdictDirty)
	PruneKeepUnmerged  = string(verdictUnmerged)
	PruneKeepUnverif   = string(verdictUnverifiable)
	PruneMissingRepo   = string(verdictMissingRepo)
	PruneKeepNotTerm   = "keep:not-terminal"
	PruneOrphan        = "orphan"
	pruneDefaultWorker = 8
)

// PruneOptions configures PruneWorktrees.
type PruneOptions struct {
	// DryRun reports what would be removed and changes nothing.
	DryRun bool
	// GitCommand builds git invocations; nil uses exec.Command.
	GitCommand func(name string, args ...string) *exec.Cmd
	// Workers bounds the concurrent assessments; 0 uses a default.
	Workers int
}

// PruneItem is one worktree on disk and what prune did, or would do, to it.
type PruneItem struct {
	ID         string `json:"id"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
	Bytes      int64  `json:"size_bytes"`
	Status     string `json:"status,omitempty"`
	Worktree   string `json:"worktree"`
	Branch     string `json:"branch,omitempty"`
	Repository string `json:"repository,omitempty"`
	// Removed and BranchDeleted report a real run's outcome for a "remove"
	// item; Error says why one was not removed.
	Removed       bool   `json:"removed,omitempty"`
	BranchDeleted bool   `json:"branch_deleted,omitempty"`
	Error         string `json:"error,omitempty"`
}

// PruneTotal is a count and the disk space it holds.
type PruneTotal struct {
	Count int   `json:"count"`
	Bytes int64 `json:"bytes"`
}

// PruneReport is the outcome of one PruneWorktrees run.
type PruneReport struct {
	DryRun bool        `json:"dry_run"`
	Items  []PruneItem `json:"items"`
	// Totals is per action, over every listed item.
	Totals map[string]PruneTotal `json:"totals"`
	// Reclaimable is what a real run would remove (the "remove" items);
	// Reclaimed is what this run actually removed (zero for a dry run).
	Reclaimable        PruneTotal `json:"reclaimable"`
	Reclaimed          PruneTotal `json:"reclaimed"`
	PrunedRepositories []string   `json:"pruned_repositories,omitempty"`
}

// PruneWorktrees reconciles the dispatch worktrees under stateDir. It looks
// only at terminal dispatches whose worktree is still on disk, applies the
// same assessWorktree rules cleanup does, and (unless DryRun) removes each
// safe worktree and its merged branch, then runs `git worktree prune` in each
// repository it touched. Running dispatches are never touched; a dispatch
// whose repository or record is gone is reported, never deleted.
func PruneWorktrees(stateDir string, opts PruneOptions) (PruneReport, error) {
	records, err := Load(stateDir)
	if err != nil {
		return PruneReport{}, fmt.Errorf("loading dispatch records: %w", err)
	}
	newCmd := opts.GitCommand
	if newCmd == nil {
		newCmd = exec.Command
	}
	git := func(args ...string) ([]byte, error) { return execGit(newCmd, args...) }

	items, err := pruneCandidates(stateDir, records)
	if err != nil {
		return PruneReport{}, err
	}
	pruneAssess(git, items, opts.Workers)
	if !opts.DryRun {
		pruneApply(git, stateDir, items)
	}
	return summarizePrune(opts.DryRun, items, git, !opts.DryRun), nil
}

// pruneEntry is an item plus what applying it needs.
type pruneEntry struct {
	item PruneItem
	rec  Record
	// orphan marks a directory with no record: reported only.
	orphan bool
}

func pruneCandidates(stateDir string, records []Record) ([]*pruneEntry, error) {
	known := map[string]bool{}
	var entries []*pruneEntry
	for _, rec := range records {
		known[rec.ID] = true
		if rec.Isolation != "worktree" || rec.Worktree == "" {
			continue
		}
		if _, err := os.Lstat(rec.Worktree); err != nil {
			continue
		}
		entries = append(entries, &pruneEntry{rec: rec, item: PruneItem{
			ID: rec.ID, Status: string(rec.Status), Worktree: rec.Worktree, Branch: rec.Branch, Repository: rec.RepositoryRoot,
		}})
	}
	dir := filepath.Join(stateDir, worktreesDirName)
	listing, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("listing %s: %w", dir, err)
	}
	for _, e := range listing {
		if known[e.Name()] {
			continue
		}
		entries = append(entries, &pruneEntry{orphan: true, item: PruneItem{
			ID: e.Name(), Action: PruneOrphan, Reason: "no dispatch record; left alone", Worktree: filepath.Join(dir, e.Name()),
		}})
	}
	return entries, nil
}

// pruneDecide is the one decision both the dry run and the apply pass make,
// from the record as it stands now.
func pruneDecide(git gitFunc, rec Record) (action, reason string) {
	switch {
	case !rec.Status.Terminal():
		return PruneKeepNotTerm, fmt.Sprintf("dispatch is %s", rec.Status)
	case rec.Mode == ModeInteractive && rec.PaneID != "":
		return PruneKeepNotTerm, fmt.Sprintf("pane %s is still recorded", rec.PaneID)
	case rec.Mode != ModeInteractive && rec.Error == "daemon restarted":
		return PruneKeepUnverif, "headless run was interrupted by a daemon restart; its writers are unknown"
	}
	a := assessWorktree(git, rec)
	return string(a.Verdict), a.Reason
}

func pruneAssess(git gitFunc, entries []*pruneEntry, workers int) {
	if workers <= 0 {
		workers = pruneDefaultWorker
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, workers)
	for _, e := range entries {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			if !e.orphan {
				e.item.Action, e.item.Reason = pruneDecide(git, e.rec)
			}
			e.item.Bytes = diskUsage(e.item.Worktree)
		}()
	}
	wg.Wait()
}

// pruneApply removes the "remove" entries one at a time, re-reading and
// re-assessing each record first: assessment of a large state dir takes long
// enough for a dispatch to come back to life.
func pruneApply(git gitFunc, stateDir string, entries []*pruneEntry) {
	for _, e := range entries {
		if e.orphan || e.item.Action != PruneRemove {
			continue
		}
		rec, err := LoadOne(stateDir, e.item.ID)
		if err != nil {
			e.item.Error = fmt.Sprintf("re-reading record: %v", err)
			continue
		}
		if action, reason := pruneDecide(git, rec); action != PruneRemove {
			e.item.Action, e.item.Reason = action, "changed since assessment: "+reason
			continue
		}
		if err := removeWorktreeCheckout(git, rec); err != nil {
			e.item.Error = err.Error()
			continue
		}
		tip, deleted := deleteMergedBranch(git, rec)
		rec.WorktreeState = WorktreeRemoved
		if tip != "" {
			rec.BranchTip = tip
		}
		e.item.Removed, e.item.BranchDeleted = true, deleted
		if err := writeRecord(Dir(stateDir), rec); err != nil {
			e.item.Error = fmt.Sprintf("worktree removed but its record was not updated: %v", err)
		}
	}
}

func summarizePrune(dryRun bool, entries []*pruneEntry, git gitFunc, pruneRepos bool) PruneReport {
	report := PruneReport{DryRun: dryRun, Items: make([]PruneItem, 0, len(entries)), Totals: map[string]PruneTotal{}}
	repos := map[string]bool{}
	for _, e := range entries {
		it := e.item
		report.Items = append(report.Items, it)
		t := report.Totals[it.Action]
		report.Totals[it.Action] = PruneTotal{Count: t.Count + 1, Bytes: t.Bytes + it.Bytes}
		if it.Action == PruneRemove {
			report.Reclaimable.Count++
			report.Reclaimable.Bytes += it.Bytes
		}
		if it.Removed {
			report.Reclaimed.Count++
			report.Reclaimed.Bytes += it.Bytes
		}
		if !e.orphan && it.Action != PruneMissingRepo && it.Repository != "" {
			repos[it.Repository] = true
		}
	}
	sort.SliceStable(report.Items, func(i, j int) bool {
		a, b := report.Items[i], report.Items[j]
		if a.Action != b.Action {
			return a.Action < b.Action
		}
		if a.Bytes != b.Bytes {
			return a.Bytes > b.Bytes
		}
		return a.ID < b.ID
	})
	if pruneRepos {
		for _, repo := range slices.Sorted(maps.Keys(repos)) {
			if pruneRepositoryWorktrees(git, repo) == nil {
				report.PrunedRepositories = append(report.PrunedRepositories, repo)
			}
		}
	}
	return report
}

// diskUsage is the disk space under root, in allocated bytes, counting each
// hard-linked file once and ignoring what cannot be read.
func diskUsage(root string) int64 {
	var total int64
	seen := map[string]bool{}
	_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			total += info.Size()
			return nil
		}
		if st.Nlink > 1 && !info.IsDir() {
			// Dev and Ino differ in width across platforms; a string key
			// avoids converting them.
			key := fmt.Sprint(st.Dev, ":", st.Ino)
			if seen[key] {
				return nil
			}
			seen[key] = true
		}
		total += st.Blocks * 512
		return nil
	})
	return total
}
