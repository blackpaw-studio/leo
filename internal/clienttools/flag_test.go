package clienttools

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestFirstRegistrationPersistsEverRegistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "client-tools.json")
	ever := NewEverRegistered(NewFileFlagStore(path), nil)
	if ever.Registered() {
		t.Fatal("a fresh store reports a registration")
	}

	ever.Record([]string{"browser_navigate", "browser_click"})

	if !ever.Registered() {
		t.Fatal("Registered() false after Record")
	}
	reopened := NewEverRegistered(NewFileFlagStore(path), nil)
	if !reopened.Registered() {
		t.Fatal("flag did not survive a new store over the same path")
	}
	if got, want := reopened.Names(), []string{"browser_click", "browser_navigate"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("persisted names = %v, want %v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestEverRegisteredAccumulatesNamesAndSkipsUnchangedWrites(t *testing.T) {
	store := &countingStore{}
	ever := NewEverRegistered(store, nil)
	ever.Record([]string{"b", "a"})
	ever.Record([]string{"a"})
	ever.Record([]string{"c", "a"})
	if got, want := ever.Names(), []string{"a", "b", "c"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %v, want %v", got, want)
	}
	if store.saves != 2 {
		t.Fatalf("saves = %d, want 2 (the repeat adds nothing)", store.saves)
	}
}

func TestFailedFlagWriteIsLoggedNotFatal(t *testing.T) {
	var logged []string
	ever := NewEverRegistered(&countingStore{err: errors.New("disk full")}, func(format string, args ...any) {
		logged = append(logged, format)
	})
	ever.Record([]string{"a"})
	if !ever.Registered() {
		t.Fatal("a failed write must not forget the registration in memory")
	}
	if len(logged) != 1 || !strings.Contains(logged[0], "client-tools") {
		t.Fatalf("logged = %v", logged)
	}
}

func TestCorruptFlagFileReadsAsNeverRegistered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "client-tools.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logged int
	ever := NewEverRegistered(NewFileFlagStore(path), func(string, ...any) { logged++ })
	if ever.Registered() || logged != 1 {
		t.Fatalf("registered=%v logged=%d", ever.Registered(), logged)
	}
}

type countingStore struct {
	saves int
	state FlagState
	err   error
}

func (s *countingStore) Load() (FlagState, error) { return s.state, nil }

func (s *countingStore) Save(st FlagState) error {
	s.saves++
	if s.err != nil {
		return s.err
	}
	s.state = st
	return nil
}
