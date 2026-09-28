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
// launch again; SweepRunFiles removes every run file whose id is not in keep.
type runFileCleaner interface {
	ReleaseRunFiles(id string)
	SweepRunFiles(keep map[string]bool)
}

// releaseRunFilesLocked drops a finalized run's files. Callers hold d.mu.
func (d *Dispatcher) releaseRunFilesLocked(id string) {
	if c, ok := d.interactiveRuntime.(runFileCleaner); ok {
		c.ReleaseRunFiles(id)
	}
}

// SweepRunFiles removes run files for every run that is not a live
// interactive dispatch. Best-effort; meant for daemon startup, after
// MarkInterrupted has settled the previous daemon's runs.
func (d *Dispatcher) SweepRunFiles() {
	d.mu.Lock()
	c, ok := d.interactiveRuntime.(runFileCleaner)
	keep := make(map[string]bool)
	for id, s := range d.runs {
		if s.record.Mode == ModeInteractive && !s.record.Status.Terminal() {
			keep[id] = true
		}
	}
	d.mu.Unlock()
	if ok {
		c.SweepRunFiles(keep)
	}
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
// opening-brief file.
func (r *TmuxInteractiveRuntime) ReleaseRunFiles(id string) {
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
// whose run id is not in keep. Agent spill files (<agent>.json) are left
// alone.
func (r *TmuxInteractiveRuntime) SweepRunFiles(keep map[string]bool) {
	home := r.spillHome()
	if home == "" {
		return
	}
	sweepGlob(filepath.Join(home, "state", "settings", dispatchSpillPrefix+"*.json"), dispatchSpillPrefix, ".json", "settings spill", keep)
	sweepGlob(filepath.Join(home, "state", claudeharness.DispatchBriefSpillDir, "*.txt"), "", ".txt", "opening brief", keep)
}

func sweepGlob(pattern, prefix, suffix, what string, keep map[string]bool) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return
	}
	for _, path := range matches {
		id := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(path), prefix), suffix)
		if keep[id] {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "dispatch %s: sweeping %s: %v\n", id, what, err)
		}
	}
}
