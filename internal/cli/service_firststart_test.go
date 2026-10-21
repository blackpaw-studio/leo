package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/managed"
)

// firstStartHome points HOME (and so config.DefaultHome) at a fresh dir with
// no config, and reports whether the marker there is valid.
func firstStartHome(t *testing.T, isManaged bool) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(home)
	old, oldCfg := loadManaged, cfgFile
	cfgFile = ""
	loadManaged = func(string) managed.State {
		if isManaged {
			return managedTestState()
		}
		return managed.State{}
	}
	t.Cleanup(func() { loadManaged, cfgFile = old, oldCfg })
	return filepath.Join(home, ".leo")
}

func TestLoadDaemonConfigManagedCreatesDefaultConfig(t *testing.T) {
	leoHome := firstStartHome(t, true)
	var log bytes.Buffer

	cfg, err := loadDaemonConfig(&log)
	if err != nil {
		t.Fatalf("loadDaemonConfig: %v", err)
	}

	path := filepath.Join(leoHome, "leo.yaml")
	if cfg.HomePath != leoHome {
		t.Errorf("HomePath = %q, want %q", cfg.HomePath, leoHome)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0600 {
		t.Errorf("config file: %v, %v; want 0600", fi, err)
	}
	if fi, err := os.Stat(leoHome); err != nil || fi.Mode().Perm() != 0700 {
		t.Errorf("home dir: %v, %v; want 0700", fi, err)
	}
	if _, ok := cfg.Templates["assistant"]; !ok {
		t.Errorf("default config has no assistant template: %+v", cfg.Templates)
	}
	if !strings.Contains(log.String(), path) {
		t.Errorf("log = %q, want it to name %s", log.String(), path)
	}
}

func TestLoadDaemonConfigUnmanagedKeepsNoConfigError(t *testing.T) {
	leoHome := firstStartHome(t, false)

	_, err := loadDaemonConfig(&bytes.Buffer{})

	if err == nil || !strings.Contains(err.Error(), "no leo.yaml found") {
		t.Fatalf("err = %v, want the 'no leo.yaml found' error", err)
	}
	if _, statErr := os.Stat(leoHome); !os.IsNotExist(statErr) {
		t.Errorf("unmanaged start created %s", leoHome)
	}
}

func TestLoadDaemonConfigManagedLeavesExistingConfigUntouched(t *testing.T) {
	leoHome := firstStartHome(t, true)
	if err := os.MkdirAll(leoHome, 0750); err != nil {
		t.Fatal(err)
	}
	const body = "defaults:\n  model: opus\n"
	path := filepath.Join(leoHome, "leo.yaml")
	if err := os.WriteFile(path, []byte(body), 0640); err != nil {
		t.Fatal(err)
	}
	var log bytes.Buffer

	cfg, err := loadDaemonConfig(&log)
	if err != nil {
		t.Fatalf("loadDaemonConfig: %v", err)
	}

	if cfg.Defaults.Model != "opus" {
		t.Errorf("model = %q, want opus", cfg.Defaults.Model)
	}
	if data, _ := os.ReadFile(path); string(data) != body {
		t.Errorf("config rewritten: %q", data)
	}
	if log.Len() != 0 {
		t.Errorf("logged %q for an existing config", log.String())
	}
}
