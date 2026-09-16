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

func TestDefaultPluginPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if got := defaultPluginPath(home); got != filepath.Join(home, ".claude", "plugins", "installed_plugins.json") {
		t.Fatalf("path = %q", got)
	}
}

func TestReadInstalledPluginIDsUnreadableIsEmpty(t *testing.T) {
	if got := readInstalledPluginIDs("plugins.json", func(string) ([]byte, error) { return nil, errors.New("nope") }); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
