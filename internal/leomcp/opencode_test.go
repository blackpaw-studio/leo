package leomcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func agentsPath(t *testing.T) string {
	t.Helper()
	path, err := openCodeAgentsPath()
	if err != nil {
		t.Fatalf("openCodeAgentsPath: %v", err)
	}
	return path
}

func TestEnsureOpenCodeContextCreatesFileWhenAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &config.Config{}
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatalf("EnsureOpenCodeContext: %v", err)
	}

	path := agentsPath(t)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	if !strings.Contains(content, openCodeManagedBeginMarker) || !strings.Contains(content, openCodeManagedEndMarker) {
		t.Errorf("content missing managed markers: %q", content)
	}
	if !strings.Contains(content, LeoNudge(cfg)) {
		t.Errorf("content missing nudge text: %q", content)
	}
}

func TestEnsureOpenCodeContextPreservesSurroundingContentWithMarkers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	path := agentsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "# My global instructions\n\nAlways be nice.\n\n" +
		openCodeManagedBeginMarker + "\nold stale nudge\n" + openCodeManagedEndMarker +
		"\n\n## Trailer\n\nDon't touch this.\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	cfg := &config.Config{}
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatalf("EnsureOpenCodeContext: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	if !strings.Contains(content, "# My global instructions") {
		t.Error("lost content preceding managed block")
	}
	if !strings.Contains(content, "Always be nice.") {
		t.Error("lost content preceding managed block")
	}
	if !strings.Contains(content, "## Trailer") || !strings.Contains(content, "Don't touch this.") {
		t.Error("lost content following managed block")
	}
	if strings.Contains(content, "old stale nudge") {
		t.Error("stale managed content was not replaced")
	}
	if !strings.Contains(content, LeoNudge(cfg)) {
		t.Error("new nudge text missing")
	}
}

func TestEnsureOpenCodeContextAppendsBlockWhenMarkersAbsent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	path := agentsPath(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	existing := "# User's own global AGENTS.md\n\nSome personal notes.\n"
	if err := os.WriteFile(path, []byte(existing), 0o644); err != nil {
		t.Fatalf("write existing: %v", err)
	}

	cfg := &config.Config{}
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatalf("EnsureOpenCodeContext: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	content := string(data)

	if !strings.HasPrefix(content, existing) {
		t.Errorf("existing content was not preserved verbatim at the start: %q", content)
	}
	if !strings.Contains(content, openCodeManagedBeginMarker) || !strings.Contains(content, openCodeManagedEndMarker) {
		t.Errorf("managed block was not appended: %q", content)
	}
}

func TestEnsureOpenCodeContextNoopWhenUnchanged(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	cfg := &config.Config{}
	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatalf("first EnsureOpenCodeContext: %v", err)
	}

	path := agentsPath(t)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	if err := EnsureOpenCodeContext(cfg); err != nil {
		t.Fatalf("second EnsureOpenCodeContext: %v", err)
	}

	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if before.ModTime() != after.ModTime() {
		t.Error("file was rewritten even though content was unchanged")
	}
}

func TestEnsureOpenCodeContextSkipsWhenNudgeEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)

	// LeoNudge returns "" only when cfg is nil.
	if err := EnsureOpenCodeContext(nil); err != nil {
		t.Fatalf("EnsureOpenCodeContext(nil): %v", err)
	}

	path := agentsPath(t)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected no file to be created, stat err = %v", err)
	}
}
