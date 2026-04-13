package agent

import (
	"errors"
	"path/filepath"
	"sort"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
)

// stubSupervisor drives Manager.Resolve without wiring real tmux.
type stubSupervisor struct {
	agents map[string]ProcessState
}

func (s *stubSupervisor) SpawnAgent(SpawnRequest) error            { return nil }
func (s *stubSupervisor) StopAgent(string) error                   { return nil }
func (s *stubSupervisor) EphemeralAgents() map[string]ProcessState { return s.agents }

func newResolveManager(t *testing.T, live map[string]ProcessState, stored map[string]agentstore.Record) *Manager {
	t.Helper()
	home := t.TempDir()
	if len(stored) > 0 {
		for _, rec := range stored {
			if err := agentstore.Save(home, rec); err != nil {
				t.Fatalf("seeding agentstore: %v", err)
			}
		}
	}
	loader := func() (*config.Config, error) {
		return &config.Config{HomePath: home}, nil
	}
	return New(loader, &stubSupervisor{agents: live}, "")
}

func TestResolveExactName(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-blackpaw-studio-leo": {Status: "running"}},
		map[string]agentstore.Record{
			"leo-coding-blackpaw-studio-leo": {Name: "leo-coding-blackpaw-studio-leo", Repo: "blackpaw-studio/leo"},
		},
	)
	rec, err := mgr.Resolve("leo-coding-blackpaw-studio-leo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo-coding-blackpaw-studio-leo" {
		t.Errorf("name = %q", rec.Name)
	}
}

func TestResolveExactRepo(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-blackpaw-studio-leo": {Status: "running"}},
		map[string]agentstore.Record{
			"leo-coding-blackpaw-studio-leo": {Name: "leo-coding-blackpaw-studio-leo", Repo: "blackpaw-studio/leo"},
		},
	)
	rec, err := mgr.Resolve("blackpaw-studio/leo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo-coding-blackpaw-studio-leo" {
		t.Errorf("name = %q", rec.Name)
	}
}

func TestResolveRepoShort(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-blackpaw-studio-leo": {Status: "running"}},
		map[string]agentstore.Record{
			"leo-coding-blackpaw-studio-leo": {Name: "leo-coding-blackpaw-studio-leo", Repo: "blackpaw-studio/leo"},
		},
	)
	rec, err := mgr.Resolve("leo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo-coding-blackpaw-studio-leo" {
		t.Errorf("name = %q", rec.Name)
	}
}

func TestResolveSuffixFallbackNoRepo(t *testing.T) {
	// Old record without Repo — suffix tier picks it up.
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-acme-widget": {Status: "running"}},
		nil,
	)
	rec, err := mgr.Resolve("widget")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo-coding-acme-widget" {
		t.Errorf("name = %q", rec.Name)
	}
}

func TestResolveCaseInsensitive(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-blackpaw-studio-leo": {Status: "running"}},
		map[string]agentstore.Record{
			"leo-coding-blackpaw-studio-leo": {Name: "leo-coding-blackpaw-studio-leo", Repo: "blackpaw-studio/leo"},
		},
	)
	rec, err := mgr.Resolve("LEO")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo-coding-blackpaw-studio-leo" {
		t.Errorf("name = %q", rec.Name)
	}
}

func TestResolveNotFound(t *testing.T) {
	mgr := newResolveManager(t, map[string]ProcessState{}, nil)
	_, err := mgr.Resolve("leo")
	var nf *ErrNotFound
	if !errors.As(err, &nf) {
		t.Fatalf("want ErrNotFound, got %T: %v", err, err)
	}
}

func TestResolveAmbiguousRepoShort(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{
			"leo-coding-acme-leo":  {Status: "running"},
			"leo-coding-other-leo": {Status: "running"},
		},
		map[string]agentstore.Record{
			"leo-coding-acme-leo":  {Name: "leo-coding-acme-leo", Repo: "acme/leo"},
			"leo-coding-other-leo": {Name: "leo-coding-other-leo", Repo: "other/leo"},
		},
	)
	_, err := mgr.Resolve("leo")
	var amb *ErrAmbiguous
	if !errors.As(err, &amb) {
		t.Fatalf("want ErrAmbiguous, got %T: %v", err, err)
	}
	sort.Strings(amb.Matches)
	want := []string{"leo-coding-acme-leo", "leo-coding-other-leo"}
	if len(amb.Matches) != 2 || amb.Matches[0] != want[0] || amb.Matches[1] != want[1] {
		t.Errorf("matches = %v, want %v", amb.Matches, want)
	}
}

func TestResolveExactNameBeatsSuffix(t *testing.T) {
	// "leo" literal beats anything ending in "-leo".
	mgr := newResolveManager(t,
		map[string]ProcessState{
			"leo":                 {Status: "running"},
			"leo-coding-acme-leo": {Status: "running"},
		},
		nil,
	)
	rec, err := mgr.Resolve("leo")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Name != "leo" {
		t.Errorf("name = %q, want leo", rec.Name)
	}
}

func TestResolveEmptyQuery(t *testing.T) {
	mgr := newResolveManager(t, map[string]ProcessState{"a": {}}, nil)
	if _, err := mgr.Resolve("   "); err == nil {
		t.Fatal("expected error for empty query")
	}
}

func TestResolveHydratesStoreFields(t *testing.T) {
	mgr := newResolveManager(t,
		map[string]ProcessState{"leo-coding-acme-widget": {Status: "running", Restarts: 3}},
		map[string]agentstore.Record{
			"leo-coding-acme-widget": {
				Name:      "leo-coding-acme-widget",
				Template:  "coding",
				Repo:      "acme/widget",
				Workspace: filepath.Join("/tmp", "widget"),
			},
		},
	)
	rec, err := mgr.Resolve("widget")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if rec.Template != "coding" || rec.Repo != "acme/widget" || rec.Workspace != filepath.Join("/tmp", "widget") {
		t.Errorf("hydrate = %+v", rec)
	}
	if rec.Restarts != 3 {
		t.Errorf("restarts = %d, want 3", rec.Restarts)
	}
}
