package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadWithRevisionHashesTheParsedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	raw := []byte("defaults:\n  model: sonnet\n")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, rev, err := LoadWithRevision(path)
	if err != nil {
		t.Fatalf("LoadWithRevision: %v", err)
	}
	sum := sha256.Sum256(raw)
	if want := "sha256:" + hex.EncodeToString(sum[:]); rev != want {
		t.Errorf("revision = %q, want %q", rev, want)
	}
	if cfg.Defaults.Model != "sonnet" {
		t.Errorf("model = %q, want sonnet", cfg.Defaults.Model)
	}
	if rev != RevisionOf(raw) {
		t.Errorf("RevisionOf disagrees with LoadWithRevision")
	}
}

func TestLoadWithRevisionChangesWhenFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "leo.yaml")
	if err := os.WriteFile(path, []byte("defaults:\n  model: sonnet\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, first, _ := LoadWithRevision(path)
	if err := os.WriteFile(path, []byte("defaults:\n  model: opus\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, second, _ := LoadWithRevision(path)
	if first == second {
		t.Error("revision must change when the file bytes change")
	}
}

func TestLoadWithRevisionMissingFile(t *testing.T) {
	if _, _, err := LoadWithRevision(filepath.Join(t.TempDir(), "nope.yaml")); err == nil {
		t.Fatal("expected an error for a missing file")
	}
}

func TestValidateReturnsStructuredErrors(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"a": {}}}
	err := cfg.Validate()
	var verrs *ValidationErrors
	if !errors.As(err, &verrs) {
		t.Fatalf("Validate error = %T, want *ValidationErrors", err)
	}
	found := false
	for _, m := range verrs.Messages {
		if m == "tasks.a.schedule is required" {
			found = true
		}
	}
	if !found {
		t.Errorf("messages %v lack the schedule error", verrs.Messages)
	}
	if got := err.Error(); got[:len("config validation failed:")] != "config validation failed:" {
		t.Errorf("Error() text changed: %q", got)
	}
}

func TestLoadWithRevisionChangesWhenOnlySharedFileChanges(t *testing.T) {
	dir := t.TempDir()
	leoPath := writeFile(t, dir, "leo.yaml", "templates_file: shared.yaml\n")
	sharedPath := writeFile(t, dir, "shared.yaml", "templates:\n  a:\n    model: opus\n")

	_, first, err := LoadWithRevision(leoPath)
	if err != nil {
		t.Fatal(err)
	}
	_, same, _ := LoadWithRevision(leoPath)
	if first != same {
		t.Fatal("revision must be stable for unchanged files")
	}
	if err := os.WriteFile(sharedPath, []byte("templates:\n  a:\n    model: haiku\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, second, err := LoadWithRevision(leoPath)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Error("revision must change when only the shared file changes")
	}
	if first == RevisionOf([]byte("templates_file: shared.yaml\n")) {
		t.Error("revision with a shared file must not equal the leo.yaml-only digest")
	}
}
