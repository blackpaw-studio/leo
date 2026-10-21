package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateIfAbsentWritesPrivateFileInPrivateDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "home", "leo.yaml")

	created, err := CreateIfAbsent(path, &Config{Defaults: DefaultsConfig{Model: "sonnet", MaxTurns: 15}})
	if err != nil || !created {
		t.Fatalf("CreateIfAbsent = %v, %v; want true, nil", created, err)
	}

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0600 {
		t.Errorf("file mode = %o, want 600", got)
	}
	di, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if got := di.Mode().Perm(); got != 0700 {
		t.Errorf("dir mode = %o, want 700", got)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if err := loaded.Validate(); err != nil {
		t.Errorf("written config invalid: %v", err)
	}
}

func TestCreateIfAbsentNeverOverwrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(path, []byte("defaults:\n  model: opus\n"), 0600); err != nil {
		t.Fatal(err)
	}

	created, err := CreateIfAbsent(path, &Config{Defaults: DefaultsConfig{Model: "sonnet"}})
	if err != nil || created {
		t.Fatalf("CreateIfAbsent = %v, %v; want false, nil", created, err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "defaults:\n  model: opus\n" {
		t.Errorf("existing file was modified: %q", data)
	}
}

func TestCreateIfAbsentLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	for range 2 {
		if _, err := CreateIfAbsent(filepath.Join(dir, "leo.yaml"), &Config{}); err != nil {
			t.Fatal(err)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("dir has %d entries, want only leo.yaml: %v", len(entries), entries)
	}
}

func TestFindConfigMissingIsErrConfigNotFound(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	_, err := FindConfig(t.TempDir())
	if !errors.Is(err, ErrConfigNotFound) {
		t.Fatalf("err = %v, want ErrConfigNotFound", err)
	}
}
