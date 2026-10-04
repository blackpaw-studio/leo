package outbox

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func newStore(t *testing.T, opts Options) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "outbox")
	return New(dir, opts), dir
}

func entry(id, text string) Entry {
	return Entry{ID: id, Text: text, QueuedAt: time.Unix(1, 0).UTC()}
}

func ids(entries []Entry) string {
	out := make([]string, len(entries))
	for i, e := range entries {
		out[i] = e.ID
	}
	return strings.Join(out, ",")
}

func mustList(t *testing.T, s *Store, agent string) []Entry {
	t.Helper()
	got, err := s.List(agent)
	if err != nil {
		t.Fatalf("List(%s): %v", agent, err)
	}
	return got
}

// An agent's entries come back in the order they were queued, survive a
// new Store over the same directory (a daemon restart), and leave once
// removed; the last removal leaves no file behind.
func TestEntriesPersistInOrderUntilRemoved(t *testing.T) {
	s, dir := newStore(t, Options{})
	for _, e := range []Entry{entry("c1", "one"), {ID: "c2", Text: "two", AsUser: true, From: "beta"}, entry("c3", "three")} {
		if err := s.Append("alpha", e); err != nil {
			t.Fatal(err)
		}
	}
	reopened := New(dir, Options{})
	got := mustList(t, reopened, "alpha")
	if ids(got) != "c1,c2,c3" || !got[1].AsUser || got[1].From != "beta" || got[1].Text != "two" {
		t.Fatalf("after reopening: %+v", got)
	}
	if err := reopened.Remove("alpha", "c2"); err != nil {
		t.Fatal(err)
	}
	if got := ids(mustList(t, s, "alpha")); got != "c1,c3" {
		t.Fatalf("after removing c2: %s", got)
	}
	for _, id := range []string{"c1", "c3", "c3", "missing"} {
		if err := s.Remove("alpha", id); err != nil {
			t.Fatalf("Remove(%s): %v", id, err)
		}
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}
	if got := mustList(t, s, "nobody"); len(got) != 0 {
		t.Fatalf("an agent with no file has entries: %+v", got)
	}
}

// A full outbox refuses the next entry with ErrFull, by count or by bytes,
// and keeps what it holds; an entry too large alone is refused too.
func TestAFullOutboxRefusesInsteadOfDropping(t *testing.T) {
	s, _ := newStore(t, Options{MaxEntries: 2, MaxBytes: 10})
	if err := s.Append("alpha", entry("c1", "12345")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("alpha", entry("c2", "123456")); !errors.Is(err, ErrFull) {
		t.Fatalf("over the byte cap: err=%v, want ErrFull", err)
	}
	if err := s.Append("alpha", entry("c2", "12345")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("alpha", entry("c3", "1")); !errors.Is(err, ErrFull) {
		t.Fatalf("over the count cap: err=%v, want ErrFull", err)
	}
	if got := ids(mustList(t, s, "alpha")); got != "c1,c2" {
		t.Fatalf("a refused append changed the outbox: %s", got)
	}
	if err := s.Append("beta", entry("big", "12345678901")); !errors.Is(err, ErrFull) {
		t.Fatalf("an entry over the cap alone: err=%v, want ErrFull", err)
	}
}

// An id is queued once: appending it again is refused.
func TestAnIDIsQueuedOnce(t *testing.T) {
	s, _ := newStore(t, Options{})
	if err := s.Append("alpha", entry("c1", "one")); err != nil {
		t.Fatal(err)
	}
	if err := s.Append("alpha", entry("c1", "one")); !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err=%v, want ErrDuplicate", err)
	}
}

// Drop hands back everything an agent had queued and forgets it; Rename
// moves an agent's entries to its new name, refusing to merge into a name
// that has its own.
func TestDropAndRename(t *testing.T) {
	s, _ := newStore(t, Options{})
	for _, e := range []Entry{entry("c1", "one"), entry("c2", "two")} {
		if err := s.Append("alpha", e); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Rename("alpha", "gamma"); err != nil {
		t.Fatal(err)
	}
	if got := mustList(t, s, "alpha"); len(got) != 0 {
		t.Fatalf("the old name kept %s", ids(got))
	}
	if got := ids(mustList(t, s, "gamma")); got != "c1,c2" {
		t.Fatalf("the new name has %q", got)
	}
	if err := s.Rename("nobody", "zed"); err != nil {
		t.Fatalf("renaming an agent with nothing queued: %v", err)
	}
	if err := s.Append("delta", entry("d1", "x")); err != nil {
		t.Fatal(err)
	}
	if err := s.Rename("gamma", "delta"); err == nil {
		t.Fatal("a rename merged into another agent's outbox")
	}
	dropped, err := s.Drop("gamma")
	if err != nil || ids(dropped) != "c1,c2" {
		t.Fatalf("Drop = %s, %v", ids(dropped), err)
	}
	if got := mustList(t, s, "gamma"); len(got) != 0 {
		t.Fatalf("dropped entries stayed: %s", ids(got))
	}
}

// Agent names become file names: anything that could leave the directory
// is refused.
func TestAgentNamesCannotEscapeTheDirectory(t *testing.T) {
	s, _ := newStore(t, Options{})
	for _, name := range []string{"", ".", "..", "a/b", "../x", `a\b`, "a\x00b"} {
		if err := s.Append(name, entry("c1", "x")); !errors.Is(err, ErrInvalidAgent) {
			t.Fatalf("Append(%q): err=%v, want ErrInvalidAgent", name, err)
		}
	}
}

// Files are private to the user, and a write never leaves a partial file:
// it lands whole or not at all.
func TestFilesArePrivateAndWrittenWhole(t *testing.T) {
	s, dir := newStore(t, Options{})
	if err := s.Append("alpha", entry("c1", "secret")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "alpha.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v, want 0600", info.Mode().Perm())
	}
	dinfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if dinfo.Mode().Perm() != 0o700 {
		t.Fatalf("dir mode %v, want 0700", dinfo.Mode().Perm())
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "alpha.json" {
			t.Fatalf("a temp file was left behind: %s", e.Name())
		}
	}
}

// A file that does not parse is an error, never silently emptied: the
// next append refuses rather than overwrite what it cannot read.
func TestACorruptFileIsAnErrorNotAnEmptyOutbox(t *testing.T) {
	s, dir := newStore(t, Options{})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.List("alpha"); err == nil {
		t.Fatal("a corrupt file listed as empty")
	}
	if err := s.Append("alpha", entry("c1", "x")); err == nil {
		t.Fatal("an append overwrote a corrupt file")
	}
}

// Concurrent appends to one agent all land.
func TestConcurrentAppendsAllLand(t *testing.T) {
	s, _ := newStore(t, Options{})
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.Append("alpha", entry(string(rune('a'+i)), "x")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := len(mustList(t, s, "alpha")); n != 20 {
		t.Fatalf("%d entries, want 20", n)
	}
}
