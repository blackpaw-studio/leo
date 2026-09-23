package agent

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agentstore"

	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// removeSettingsSpill deletes a deleted agent's private merged-settings file
// (claudeharness.SettingsSpillPath), which can hold credentials.
func removeSettingsSpill(homePath, name string) {
	path := claudeharness.SettingsSpillPath(homePath, name)
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("agent %q: removing settings spill: %v", name, err)
	}
}

// dispatchSpillPrefix marks interactive-dispatch spill files, which the
// dispatcher sweeps itself (consult.Dispatcher.SweepRunFiles).
const dispatchSpillPrefix = "dispatch-"

// SweepSettingsSpills removes agent settings spill files whose agent has no
// record — e.g. the old name of an agent renamed while live. Best-effort,
// for daemon startup.
func SweepSettingsSpills(homePath string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return // no readable store: never guess which files are orphans
	}
	matches, err := filepath.Glob(filepath.Join(homePath, "state", "settings", "*.json"))
	if err != nil {
		return
	}
	for _, path := range matches {
		name := strings.TrimSuffix(filepath.Base(path), ".json")
		if _, known := records[name]; known || strings.HasPrefix(name, dispatchSpillPrefix) || strings.HasPrefix(name, ".") {
			continue
		}
		removeSettingsSpill(homePath, name)
	}
}
