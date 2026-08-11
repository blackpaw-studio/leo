package agentstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveEnforces0600Permissions verifies agentstore writes are always
// mode 0600, even when agents.json pre-exists with looser permissions (e.g.
// a file written before this hardening landed). os.WriteFile's perm arg is
// only honored on file creation — an existing file keeps its old mode — so
// write() must explicitly chmod on every save, not just rely on WriteFile's
// create-time perm. Records now persist OPENCODE_CONFIG_CONTENT, which can
// embed LEO_API_TOKEN, so a world/group-readable agents.json is a secret leak.
func TestSaveEnforces0600Permissions(t *testing.T) {
	dir := t.TempDir()
	path := FilePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Simulate a pre-existing file with looser permissions.
	if err := os.WriteFile(path, []byte("{}"), 0644); err != nil {
		t.Fatalf("seeding pre-existing file: %v", err)
	}

	if err := Save(dir, Record{Name: "agent-x"}); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0600 {
		t.Errorf("agents.json mode = %o, want 0600 (even though the file pre-existed at 0644)", got)
	}
}

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

// TestSaveAndLoadSpawnEnv verifies SpawnEnv and InheritedEnv round-trip
// through Save/Load distinctly from each other and from Env, and that a nil
// SpawnEnv/InheritedEnv (the legacy/pre-these-fields shape) round-trips as
// nil rather than an empty map — Manager.Restart's re-resolution logic
// branches on nil vs non-nil to detect legacy records.
func TestSaveAndLoadSpawnEnv(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "state"), 0750)

	_ = Save(dir, Record{
		Name:         "agent-with-spawn-env",
		Env:          map[string]string{"FOO": "merged", "BAR": "merged", "BAZ": "merged"},
		SpawnEnv:     map[string]string{"FOO": "spawn-override"},
		InheritedEnv: map[string]string{"BAZ": "inherited-raw"},
	})
	_ = Save(dir, Record{
		Name: "agent-legacy",
		Env:  map[string]string{"LEGACY": "value"},
		// SpawnEnv/InheritedEnv intentionally omitted — simulates a
		// pre-these-fields record.
	})

	records, err := Load(FilePath(dir))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	withSpawnEnv := records["agent-with-spawn-env"]
	if withSpawnEnv.SpawnEnv["FOO"] != "spawn-override" {
		t.Errorf("SpawnEnv[FOO] = %q, want %q", withSpawnEnv.SpawnEnv["FOO"], "spawn-override")
	}
	if withSpawnEnv.InheritedEnv["BAZ"] != "inherited-raw" {
		t.Errorf("InheritedEnv[BAZ] = %q, want %q", withSpawnEnv.InheritedEnv["BAZ"], "inherited-raw")
	}
	if withSpawnEnv.Env["BAR"] != "merged" {
		t.Errorf("Env[BAR] = %q, want %q (Env must round-trip independently of SpawnEnv/InheritedEnv)", withSpawnEnv.Env["BAR"], "merged")
	}

	legacy := records["agent-legacy"]
	if legacy.SpawnEnv != nil {
		t.Errorf("legacy record's SpawnEnv = %v, want nil", legacy.SpawnEnv)
	}
	if legacy.InheritedEnv != nil {
		t.Errorf("legacy record's InheritedEnv = %v, want nil", legacy.InheritedEnv)
	}
	if legacy.Env["LEGACY"] != "value" {
		t.Errorf("legacy Env[LEGACY] = %q, want %q", legacy.Env["LEGACY"], "value")
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

func TestRecordRoundTripPreservesSuspendFields(t *testing.T) {
	home := t.TempDir()
	in := Record{Name: "leo-x", Workspace: "/w", Suspended: true, IdleSuspendAfter: "24h0m0s"}
	if err := Save(home, in); err != nil {
		t.Fatalf("save: %v", err)
	}
	got, err := Load(FilePath(home))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	rec := got["leo-x"]
	if !rec.Suspended || rec.IdleSuspendAfter != "24h0m0s" {
		t.Fatalf("round-trip lost fields: %+v", rec)
	}
}

// TestSaveAndLoadPerTemplateSessions verifies the per-template session archive
// and the one-shot pin survive a save/load round trip. The archive is what
// makes `leo agent set-template` able to hand a template back its own
// conversation after the agent has been away on another template, so a field
// that silently failed to persist would look exactly like "no prior session"
// — a fresh conversation instead of the one the user expected back.
func TestSaveAndLoadPerTemplateSessions(t *testing.T) {
	dir := t.TempDir()

	rec := Record{
		Name:      "agent-coding-leo",
		Template:  "coding",
		Workspace: "/tmp/workspace",
		SessionID: "live-session",
		SessionsByTemplate: map[string]string{
			"codex":  "codex-rollout-id",
			"review": "review-session-id",
		},
		SessionPinned: true,
	}
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	records, err := Load(FilePath(dir))
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	got, ok := records["agent-coding-leo"]
	if !ok {
		t.Fatal("record not found after save")
	}
	if got.SessionsByTemplate["codex"] != "codex-rollout-id" {
		t.Errorf("SessionsByTemplate[codex] = %q, want %q", got.SessionsByTemplate["codex"], "codex-rollout-id")
	}
	if got.SessionsByTemplate["review"] != "review-session-id" {
		t.Errorf("SessionsByTemplate[review] = %q, want %q", got.SessionsByTemplate["review"], "review-session-id")
	}
	if !got.SessionPinned {
		t.Error("SessionPinned = false after round trip, want true")
	}
	if got.SessionID != "live-session" {
		t.Errorf("SessionID = %q, want %q (the active template's session stays out of the archive)", got.SessionID, "live-session")
	}
}

// TestLegacyRecordHasNoArchive pins the zero-value contract for records
// written before this feature: absent JSON keys must read back as an empty
// archive and an unpinned session, so an upgraded leo treats an untouched
// agent exactly as it did before.
func TestLegacyRecordHasNoArchive(t *testing.T) {
	dir := t.TempDir()
	path := FilePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"legacy-agent":{"name":"legacy-agent","template":"coding","workspace":"/tmp/ws","session_id":"abc"}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatalf("seeding legacy file: %v", err)
	}

	records, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	rec := records["legacy-agent"]
	if len(rec.SessionsByTemplate) != 0 {
		t.Errorf("SessionsByTemplate = %v, want empty for a legacy record", rec.SessionsByTemplate)
	}
	if rec.SessionPinned {
		t.Error("SessionPinned = true for a legacy record, want false")
	}
}
