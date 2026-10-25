package consult

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// dispatchSpillPrefix names an interactive dispatch's private merged-settings
// file: <home>/state/settings/dispatch-<run-id>.json (see
// claudeharness.SettingsSpillPath).
const dispatchSpillPrefix = "dispatch-"

// runFileCleaner is an optional InteractiveRuntime capability for runtimes
// that leave per-run files behind (the claude settings spill, which can hold
// credentials). ReleaseRunFiles removes one run's files once it can never
// launch again; SweepRunFiles removes every run file whose id keep rejects.
type runFileCleaner interface {
	ReleaseRunFiles(id string)
	SweepRunFiles(keep func(id string) bool) (removed int)
}

// releaseRunFilesLocked drops a finalized run's files. Callers hold d.mu.
func (d *Dispatcher) releaseRunFilesLocked(id string) {
	if c, ok := d.interactiveRuntime.(runFileCleaner); ok {
		c.ReleaseRunFiles(id)
	}
}

// SweepSummary reports what SweepRunFiles kept and removed.
type SweepSummary struct {
	Kept, Removed int
}

// SweepRunFiles removes run files for every run that is not a live
// interactive dispatch. Best-effort; meant for daemon startup, after
// MarkInterrupted has settled the previous daemon's runs. Each file's run is
// looked up as the sweep reaches it, under mu: a run is registered before its
// launch writes any file, so a dispatch started while the sweep runs keeps
// its files. Kept counts the live runs whose files were kept.
func (d *Dispatcher) SweepRunFiles() SweepSummary {
	d.mu.Lock()
	c, ok := d.interactiveRuntime.(runFileCleaner)
	d.mu.Unlock()
	if !ok {
		return SweepSummary{}
	}
	kept := map[string]bool{}
	removed := c.SweepRunFiles(func(id string) bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		s := d.runs[id]
		live := s != nil && s.record.Mode == ModeInteractive && !s.record.Status.Terminal()
		if live {
			kept[id] = true
		}
		return live
	})
	return SweepSummary{Kept: len(kept), Removed: removed}
}

// dispatchSpillPath is the settings spill file of run id ("" if unusable).
func dispatchSpillPath(homePath, id string) string {
	return claudeharness.SettingsSpillPath(homePath, dispatchSpillPrefix+id)
}

func (r *TmuxInteractiveRuntime) spillHome() string {
	cfg, err := r.config()
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.HomePath
}

// ReleaseRunFiles removes run id's settings spill file and, if it used
// argv-delivered claude opening (see claudeDeliversPromptViaArgv), its
// opening-brief file. A bridged run's bridge is forgotten too.
func (r *TmuxInteractiveRuntime) ReleaseRunFiles(id string) {
	r.releaseBridge(id)
	home := r.spillHome()
	removeIfExists(dispatchSpillPath(home, id), "settings spill", id)
	removeIfExists(dispatchBriefPath(home, id), "opening brief", id)
}

func removeIfExists(path, what, id string) {
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "dispatch %s: removing %s: %v\n", id, what, err)
	}
}

// SweepRunFiles removes every dispatch settings spill and opening-brief file
// whose run id keep rejects. Agent spill files (<agent>.json) are left
// alone.
func (r *TmuxInteractiveRuntime) SweepRunFiles(keep func(id string) bool) int {
	home := r.spillHome()
	if home == "" {
		return 0
	}
	return sweepGlob(filepath.Join(home, "state", "settings", dispatchSpillPrefix+"*.json"), dispatchSpillPrefix, ".json", "settings spill", keep) +
		sweepGlob(filepath.Join(home, "state", claudeharness.DispatchBriefSpillDir, "*.txt"), "", ".txt", "opening brief", keep)
}

// sweepGlob removes the files matching pattern whose run id keep rejects and
// returns how many it removed. keep is asked once per file, as it is reached.
func sweepGlob(pattern, prefix, suffix, what string, keep func(id string) bool) int {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0
	}
	removed := 0
	for _, path := range matches {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), prefix), suffix)
		if keep(id) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "dispatch %s: sweeping %s: %v\n", id, what, err)
			continue
		}
		removed++
	}
	return removed
}
