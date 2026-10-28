package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestHomeIDStableAndPrivate(t *testing.T) {
	state := filepath.Join(t.TempDir(), "state")
	first, err := HomeID(state)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 32 {
		t.Fatalf("HomeID = %q, want 32 hex chars", first)
	}
	again, err := HomeID(state)
	if err != nil || again != first {
		t.Fatalf("second HomeID = %q, %v; want %q", again, err, first)
	}
	fi, err := os.Stat(filepath.Join(state, "home-id"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("home-id mode = %v, want 0600", fi.Mode().Perm())
	}
}

func TestHomeIDConcurrentFirstReadsAgree(t *testing.T) {
	state := t.TempDir()
	const n = 8
	ids := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := HomeID(state)
			if err != nil {
				t.Error(err)
			}
			ids[i] = id
		}()
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] {
			t.Fatalf("ids disagree: %v", ids)
		}
	}
}

func TestHomeIDRejectsCorruptFile(t *testing.T) {
	state := t.TempDir()
	writeFile(t, state, "home-id", "not-hex\n")
	if _, err := HomeID(state); err == nil {
		t.Fatal("HomeID accepted a corrupt home-id file")
	}
}
