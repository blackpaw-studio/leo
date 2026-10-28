package agentstore

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRecordMoveFieldsRoundTrip pins the move marker, its time, the per-peer
// move log and a copy's fork source through Save/Load.
func TestRecordMoveFieldsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	rec := Record{
		Name:            "mover",
		Workspace:       "/tmp/ws",
		Stopped:         true,
		MovedTo:         "laptop",
		MovedAt:         &at,
		MoveLog:         map[string]time.Time{"laptop.local": at},
		ForkFromSession: "src-session",
	}
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	records, err := Load(FilePath(dir))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := records["mover"]
	if got.MovedTo != "laptop" {
		t.Errorf("MovedTo = %q, want laptop", got.MovedTo)
	}
	if got.MovedAt == nil || !got.MovedAt.Equal(at) {
		t.Errorf("MovedAt = %v, want %v", got.MovedAt, at)
	}
	if !got.MoveLog["laptop.local"].Equal(at) {
		t.Errorf("MoveLog = %v, want laptop.local=%v", got.MoveLog, at)
	}
	if got.ForkFromSession != "src-session" {
		t.Errorf("ForkFromSession = %q, want src-session", got.ForkFromSession)
	}
	if !got.Moved() {
		t.Error("Moved() = false for a record with MovedTo set")
	}
}

// TestLegacyRecordDecodesWithoutMoveFields: a record written before move
// existed reads back unmoved, and Save can clear the marker (a move back).
func TestLegacyRecordDecodesWithoutMoveFields(t *testing.T) {
	dir := t.TempDir()
	path := FilePath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0750); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	legacy := `{"old":{"name":"old","template":"coding","workspace":"/tmp/ws","stopped":true}}`
	if err := os.WriteFile(path, []byte(legacy), 0600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	records, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	rec := records["old"]
	if rec.Moved() || rec.MovedAt != nil || rec.MoveLog != nil || rec.ForkFromSession != "" {
		t.Errorf("legacy record decoded with move fields: %+v", rec)
	}

	rec.MovedTo = "laptop"
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rec.MovedTo = ""
	if err := Save(dir, rec); err != nil {
		t.Fatalf("Save: %v", err)
	}
	records, _ = Load(path)
	if records["old"].Moved() {
		t.Error("Save did not clear MovedTo: a move back must be able to unmark the record")
	}
}
