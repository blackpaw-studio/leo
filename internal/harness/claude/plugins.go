package claude

import (
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"sort"
)

var defaultPluginWarningf = func(format string, args ...any) { log.Printf("leo: warning: "+format, args...) }
var pluginWarningf = defaultPluginWarningf

func defaultPluginPath(home string) string {
	return filepath.Join(home, ".claude", "plugins", "installed_plugins.json")
}

// InstalledPluginIDsFromHome reads installed plugins from the home used by a launch.
func InstalledPluginIDsFromHome(home string) []string {
	return readInstalledPluginIDs(defaultPluginPath(home), os.ReadFile)
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
