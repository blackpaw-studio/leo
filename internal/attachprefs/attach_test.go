package attachprefs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/picker"
)

func TestLoadSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "attach.json")
	tm := time.Date(2026, 9, 5, 23, 10, 0, 0, time.UTC)
	want := Preferences{
		Sort:         picker.SortModeUptime,
		LastAttached: map[string]time.Time{"local/vitals": tm},
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := Load(path)
	if got.Sort != want.Sort || !got.LastAttached["local/vitals"].Equal(want.LastAttached["local/vitals"]) {
		t.Fatalf("Load = %#v, want %#v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestLoadFallsBackToDefaults(t *testing.T) {
	for _, contents := range []string{"", "{", `{"sort":"bogus"}`} {
		t.Run(contents, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "attach.json")
			if contents != "" {
				if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			got := Load(path)
			if got.Sort != picker.SortModeRecent || len(got.LastAttached) != 0 {
				t.Fatalf("Load = %#v, want defaults", got)
			}
		})
	}
}

func TestWithUpdatesReturnNewValues(t *testing.T) {
	original := Preferences{
		Sort:         picker.SortModeRecent,
		LastAttached: map[string]time.Time{"local/old": {}},
	}
	updated := original.WithSort(picker.SortModeName).WithLastAttached("remote/new", time.Now())
	if original.Sort != picker.SortModeRecent || len(original.LastAttached) != 1 {
		t.Fatalf("original mutated: %#v", original)
	}
	if updated.Sort != picker.SortModeName || len(updated.LastAttached) != 2 {
		t.Fatalf("updated = %#v", updated)
	}
}
