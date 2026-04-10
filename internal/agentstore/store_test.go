package agentstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFilePath(t *testing.T) {
	got := FilePath("/home/user/.leo")
	want := filepath.Join("/home/user/.leo", "state", "agents.json")
	if got != want {
		t.Errorf("FilePath() = %q, want %q", got, want)
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	rec := Record{
		Name:       "agent-coding-leo",
		Template:   "coding",
		Workspace:  "/tmp/workspace",
		ClaudeArgs: []string{"--model", "sonnet"},
		Env:        map[string]string{"FOO": "bar"},
		WebPort:    "8370",
		SpawnedAt:  time.Now().Truncate(time.Second),
	}

	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	records, err := Load(FilePath(dir))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(records) != 1 {
		t.Fatalf("Load() returned %d records, want 1", len(records))
	}

	got := records["agent-coding-leo"]
	if got.Name != rec.Name {
		t.Errorf("Name = %q, want %q", got.Name, rec.Name)
	}
	if got.Template != rec.Template {
		t.Errorf("Template = %q, want %q", got.Template, rec.Template)
	}
	if got.Workspace != rec.Workspace {
		t.Errorf("Workspace = %q, want %q", got.Workspace, rec.Workspace)
	}
	if got.WebPort != rec.WebPort {
		t.Errorf("WebPort = %q, want %q", got.WebPort, rec.WebPort)
	}
	if len(got.ClaudeArgs) != 2 || got.ClaudeArgs[0] != "--model" {
		t.Errorf("ClaudeArgs = %v, want %v", got.ClaudeArgs, rec.ClaudeArgs)
	}
	if got.Env["FOO"] != "bar" {
		t.Errorf("Env[FOO] = %q, want %q", got.Env["FOO"], "bar")
	}
}

func TestSaveMultipleRecords(t *testing.T) {
	dir := t.TempDir()

	rec1 := Record{Name: "agent-a", Template: "coding", Workspace: "/tmp/a"}
	rec2 := Record{Name: "agent-b", Template: "research", Workspace: "/tmp/b"}

	Save(dir, rec1) //nolint:errcheck
	Save(dir, rec2) //nolint:errcheck

	records, err := Load(FilePath(dir))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("Load() returned %d records, want 2", len(records))
	}
	if records["agent-a"].Workspace != "/tmp/a" {
		t.Error("agent-a workspace mismatch")
	}
	if records["agent-b"].Template != "research" {
		t.Error("agent-b template mismatch")
	}
}

func TestSaveOverwritesExisting(t *testing.T) {
	dir := t.TempDir()

	Save(dir, Record{Name: "agent-x", Workspace: "/old"}) //nolint:errcheck
	Save(dir, Record{Name: "agent-x", Workspace: "/new"}) //nolint:errcheck

	records, _ := Load(FilePath(dir))
	if records["agent-x"].Workspace != "/new" {
		t.Errorf("expected overwritten workspace /new, got %q", records["agent-x"].Workspace)
	}
}

func TestRemove(t *testing.T) {
	dir := t.TempDir()

	Save(dir, Record{Name: "agent-keep"})   //nolint:errcheck
	Save(dir, Record{Name: "agent-remove"}) //nolint:errcheck

	Remove(dir, "agent-remove")

	records, _ := Load(FilePath(dir))
	if len(records) != 1 {
		t.Fatalf("expected 1 record after remove, got %d", len(records))
	}
	if _, ok := records["agent-keep"]; !ok {
		t.Error("expected agent-keep to remain")
	}
}

func TestRemoveNonexistent(t *testing.T) {
	dir := t.TempDir()
	// Should not panic on empty/missing file
	Remove(dir, "does-not-exist")
}

func TestLoadMissingFile(t *testing.T) {
	records, err := Load("/nonexistent/path/agents.json")
	if err == nil {
		t.Error("expected error for missing file")
	}
	if records == nil {
		t.Fatal("expected non-nil empty map")
	}
	if len(records) != 0 {
		t.Errorf("expected empty map, got %d entries", len(records))
	}
}

func TestLoadInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state", "agents.json")
	os.MkdirAll(filepath.Dir(path), 0750)
	os.WriteFile(path, []byte("not json"), 0600)

	records, err := Load(path)
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
	if len(records) != 0 {
		t.Errorf("expected empty map on parse error, got %d", len(records))
	}
}

func TestSaveCreatesStateDir(t *testing.T) {
	dir := t.TempDir()
	// Don't pre-create state dir — Save should create it
	err := Save(dir, Record{Name: "agent-test"})
	if err != nil {
		t.Fatalf("Save() should create state dir, got error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "state")); err != nil {
		t.Error("expected state directory to be created")
	}
}
