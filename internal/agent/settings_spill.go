package agent

import (
	"log"
	"os"

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
