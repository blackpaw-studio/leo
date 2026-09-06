// Package attachprefs stores non-critical preferences for the attach picker.
package attachprefs

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type Sort string

const (
	SortRecent Sort = "recent"
	SortName   Sort = "name"
	SortUptime Sort = "uptime"
)

type Preferences struct {
	Sort         Sort                 `json:"sort"`
	LastAttached map[string]time.Time `json:"last_attached"`
}

func defaults() Preferences {
	return Preferences{Sort: SortRecent, LastAttached: map[string]time.Time{}}
}

func validSort(s Sort) bool { return s == SortRecent || s == SortName || s == SortUptime }

// Load returns defaults if preferences cannot be read or validated.
func Load(path string) Preferences {
	b, err := os.ReadFile(path)
	if err != nil {
		return defaults()
	}
	var p Preferences
	if json.Unmarshal(b, &p) != nil || !validSort(p.Sort) {
		return defaults()
	}
	if p.LastAttached == nil {
		p.LastAttached = map[string]time.Time{}
	}
	return p
}

// Save atomically replaces the preferences file with owner-only permissions.
func Save(path string, p Preferences) error {
	if !validSort(p.Sort) {
		p.Sort = SortRecent
	}
	if p.LastAttached == nil {
		p.LastAttached = map[string]time.Time{}
	}
	b, err := json.Marshal(p)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".attach-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(b); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (p Preferences) WithSort(sort Sort) Preferences {
	p.Sort = sort
	p.LastAttached = copyTimes(p.LastAttached)
	return p
}

func (p Preferences) WithLastAttached(key string, at time.Time) Preferences {
	p.LastAttached = copyTimes(p.LastAttached)
	p.LastAttached[key] = at
	return p
}

func copyTimes(in map[string]time.Time) map[string]time.Time {
	out := make(map[string]time.Time, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
