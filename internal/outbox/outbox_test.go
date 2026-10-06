package outbox

import (
	"errors"
	"fmt"
	"io/fs"
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

// A rename onto a name that already has messages queued is refused, even
// for an agent with none of its own: they were queued for whoever held the
// name before (a deleted agent whose drop failed), and must reach neither
// the renamed agent nor oblivion. They stay where they are.
func TestARenameNeverInheritsAnotherAgentsMessages(t *testing.T) {
	s, _ := newStore(t, Options{})
	if err := s.Append("delta", entry("d1", "for the old delta")); err != nil {
		t.Fatal(err)
	}
	for _, from := range []string{"empty", "busy"} {
		if from == "busy" {
			if err := s.Append("busy", entry("b1", "x")); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Rename(from, "delta"); !errors.Is(err, ErrNameHasMessages) {
			t.Fatalf("renaming %s onto delta: err=%v, want ErrNameHasMessages", from, err)
		}
	}
	if got := ids(mustList(t, s, "delta")); got != "d1" {
		t.Fatalf("delta holds %q after the refusals", got)
	}
	if got := ids(mustList(t, s, "busy")); got != "b1" {
		t.Fatalf("busy holds %q after the refusal", got)
	}
}

// A name with messages queued is not free for a brand-new agent: they
// were a former holder's, and its first launch would carry them.
func TestCheckEmptyRefusesANameWithMessages(t *testing.T) {
	s, _ := newStore(t, Options{})
	if err := s.CheckEmpty("fresh"); err != nil {
		t.Fatalf("a name with nothing queued: %v", err)
	}
	if err := s.Append("stale", entry("s1", "for the old holder")); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckEmpty("stale"); !errors.Is(err, ErrNameHasMessages) {
		t.Fatalf("a name with messages queued: err=%v, want ErrNameHasMessages", err)
	}
	if got := ids(mustList(t, s, "stale")); got != "s1" {
		t.Fatalf("the check touched the outbox: %q", got)
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

// A corrupt file must not block its agent forever: it is moved aside,
// whole, where it can be recovered by hand, the move is logged loudly, and
// the agent continues with an empty outbox.
func TestACorruptFileIsMovedAsideLoudly(t *testing.T) {
	var logged []string
	dir := filepath.Join(t.TempDir(), "outbox")
	s := New(dir, Options{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "alpha.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := mustList(t, s, "alpha"); len(got) != 0 {
		t.Fatalf("listed %s from a corrupt file", ids(got))
	}

	aside, _ := filepath.Glob(filepath.Join(dir, "alpha.json.corrupt-*"))
	if len(aside) != 1 {
		t.Fatalf("moved aside: %v, want one copy", aside)
	}
	if data, err := os.ReadFile(aside[0]); err != nil || string(data) != "{not json" {
		t.Fatalf("the copy aside holds %q, %v; want the corrupt file whole", data, err)
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "alpha") || !strings.Contains(logged[0], aside[0]) {
		t.Fatalf("logged %q, want the agent and where its file went", logged)
	}
	if err := s.Append("alpha", entry("c1", "x")); err != nil {
		t.Fatalf("appending after the move: %v", err)
	}
	if got := ids(mustList(t, s, "alpha")); got != "c1" {
		t.Fatalf("after the move the outbox holds %q", got)
	}
}

// Only a file that reads but does not decode is corrupt. One that cannot be
// read at all (here a directory where the file should be; a permission or
// disk error alike) is an error, and stays where it is: moving it aside
// would hide messages that are likely intact.
func TestAnUnreadableFileIsAnErrorAndStays(t *testing.T) {
	var logged []string
	dir := filepath.Join(t.TempDir(), "outbox")
	s := New(dir, Options{Logf: func(format string, args ...any) { logged = append(logged, fmt.Sprintf(format, args...)) }})
	path := filepath.Join(dir, "alpha.json")
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := s.List("alpha"); err == nil {
		t.Fatal("an unreadable outbox listed as empty")
	}
	if err := s.Append("alpha", entry("c1", "x")); err == nil {
		t.Fatal("an append went past an unreadable outbox")
	}

	if aside, _ := filepath.Glob(filepath.Join(dir, "alpha.json.corrupt-*")); len(aside) != 0 {
		t.Fatalf("moved an unreadable file aside: %v", aside)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("the unreadable path did not stay: %v, %v", info, err)
	}
	if len(logged) != 0 {
		t.Fatalf("logged %q for an I/O error", logged)
	}
}

// A write that died before its rename leaves its temp file behind; a new
// Store over the directory (the next daemon) sweeps them, and only them.
func TestStrayTempFilesAreSweptOnNew(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "outbox")
	s := New(dir, Options{})
	if err := s.Append("alpha", entry("c1", "x")); err != nil {
		t.Fatal(err)
	}
	strays := []string{".alpha.json.123.tmp", ".beta.json.456.tmp"}
	for _, name := range append(strays, "notes.txt") {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	New(dir, Options{})

	for _, name := range strays {
		if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%s survived the sweep: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Fatalf("the sweep took a file that is not a stray write: %v", err)
	}
	if got := ids(mustList(t, s, "alpha")); got != "c1" {
		t.Fatalf("the sweep touched a live outbox: %q", got)
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
