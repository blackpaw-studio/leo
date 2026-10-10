package claude

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"

	"github.com/blackpaw-studio/leo/internal/session"
)

var defaultPluginWarningf = func(format string, args ...any) { log.Printf("leo: warning: "+format, args...) }
var pluginWarningf = defaultPluginWarningf

// InstalledPluginIDs reads the plugins installed in the claude config dir a
// launch with env resolves to (CLAUDE_CONFIG_DIR, else $HOME/.claude).
func InstalledPluginIDs(env map[string]string) []string {
	dir, err := session.ConfigDir(env)
	if err != nil {
		pluginWarningf("resolve Claude config dir: %v", err)
		return nil
	}
	return readInstalledPluginIDs(filepath.Join(dir, "plugins", "installed_plugins.json"), os.ReadFile)
}

func readInstalledPluginIDs(path string, readFile func(string) ([]byte, error)) []string {
	b, err := readFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			pluginWarningf("read Claude plugins %s: %v", path, err)
		}
		return nil
	}
	var v struct {
		Plugins map[string]json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		pluginWarningf("parse Claude plugins %s: %v", path, err)
		return nil
	}
	ids := make([]string, 0, len(v.Plugins))
	for id := range v.Plugins {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
