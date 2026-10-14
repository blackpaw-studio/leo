package drain

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFileStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	in := PendingResume{
		DrainID:       "d1",
		Agents:        []string{"a", "b"},
		DeferredTasks: []string{"heartbeat"},
		WrittenAt:     time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC),
	}
	if err := store.Write(in); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, "pending-resume.json"))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}

	got, ok, err := store.Take()
	if err != nil || !ok {
		t.Fatalf("Take = ok %v, err %v", ok, err)
	}
	want := in
	want.Version = PendingResumeVersion
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Take = %+v, want %+v", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "pending-resume.json")); !os.IsNotExist(err) {
		t.Errorf("file still present after Take: %v", err)
	}
}

func TestFileStoreWriteEnforces0600OnExistingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending-resume.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := NewFileStore(dir).Write(PendingResume{Agents: []string{"a"}}); err != nil {
		t.Fatalf("Write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("mode = %o, want 0600", got)
	}
}

func TestFileStoreTakeMissingFile(t *testing.T) {
	_, ok, err := NewFileStore(t.TempDir()).Take()
	if ok || err != nil {
		t.Fatalf("Take on missing file = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestFileStoreTakeCorruptFileQuarantines(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending-resume.json")
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewFileStore(dir)
	if _, _, err := store.Take(); err == nil {
		t.Fatal("Take on corrupt file returned nil error")
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "pending-resume.json.corrupt-*"))
	if len(matches) != 1 {
		t.Fatalf("corrupt siblings = %v, want exactly one", matches)
	}
	_, ok, err := store.Take()
	if ok || err != nil {
		t.Fatalf("second Take = ok %v, err %v; want false, nil", ok, err)
	}
}

func TestFileStoreRemoveMissingFile(t *testing.T) {
	if err := NewFileStore(t.TempDir()).Remove(); err != nil {
		t.Fatalf("Remove on missing file: %v", err)
	}
}

func TestFileStoreRemoveDeletesFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(dir)
	if err := store.Write(PendingResume{Agents: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove(); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, ok, _ := store.Take(); ok {
		t.Fatal("file survived Remove")
	}
}
