package claude

import (
	"bytes"
	"errors"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestReadInstalledPluginIDs(t *testing.T) {
	got := readInstalledPluginIDs("plugins.json", func(string) ([]byte, error) {
		return []byte(`{"plugins":{"b@market":{},"a@local":{}}}`), nil
	})
	if want := []string{"a@local", "b@market"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestReadInstalledPluginIDsMalformedIsEmpty(t *testing.T) {
	var warnings bytes.Buffer
	pluginWarningf = log.New(&warnings, "", 0).Printf
	t.Cleanup(func() { pluginWarningf = defaultPluginWarningf })
	if got := readInstalledPluginIDs("plugins.json", func(string) ([]byte, error) { return []byte("{"), nil }); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
	if !bytes.Contains(warnings.Bytes(), []byte("parse Claude plugins")) {
		t.Fatalf("warning = %q", warnings.String())
	}
}

func TestReadInstalledPluginIDsAbsentLogsNothing(t *testing.T) {
	var warnings bytes.Buffer
	pluginWarningf = log.New(&warnings, "", 0).Printf
	t.Cleanup(func() { pluginWarningf = defaultPluginWarningf })
	readInstalledPluginIDs("plugins.json", func(string) ([]byte, error) { return nil, os.ErrNotExist })
	if warnings.Len() != 0 {
		t.Fatalf("warning = %q", warnings.String())
	}
}

func TestReadInstalledPluginIDsUnreadableIsEmpty(t *testing.T) {
	if got := readInstalledPluginIDs("plugins.json", func(string) ([]byte, error) { return nil, errors.New("nope") }); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}

func TestInstalledPluginIDsReadsConfigDir(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, configDir := t.TempDir(), t.TempDir()
	write := func(dir string) {
		t.Helper()
		path := filepath.Join(dir, "plugins", "installed_plugins.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(`{"plugins":{"b@x":{}}}`), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(configDir)
	write(filepath.Join(home, ".claude"))
	if got := InstalledPluginIDs(map[string]string{"HOME": home, "CLAUDE_CONFIG_DIR": configDir}); len(got) != 1 || got[0] != "b@x" {
		t.Fatalf("config dir plugins = %v", got)
	}
	if got := InstalledPluginIDs(map[string]string{"HOME": home}); len(got) != 1 {
		t.Fatalf("default plugins = %v", got)
	}
}
