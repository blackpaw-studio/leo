package bridgemod

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// modDirs makes mod dirs under stateDir's mods parent, each name's mtime
// age before now (oldest first as listed), and returns the parent.
func modDirs(t *testing.T, stateDir string, names ...string) string {
	t.Helper()
	parent := filepath.Join(stateDir, "mods", Name)
	now := time.Now()
	for i, name := range names {
		dir := filepath.Join(parent, name)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		at := now.Add(-time.Duration(len(names)-i) * time.Hour)
		if err := os.Chtimes(dir, at, at); err != nil {
			t.Fatal(err)
		}
	}
	return parent
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

func noProcesses(context.Context) ([]string, error) { return []string{"/bin/zsh -l"}, nil }

// A leo upgrade leaves the previous version's mod behind; a stream of them
// would pile up. The current mod and the one before it stay (an agent from
// the last version may still be on it), the rest go.
func TestPruneModsKeepsTheCurrentAndThePreviousMod(t *testing.T) {
	state := t.TempDir()
	parent := modDirs(t, state, "v1-aaaaaaaaaaaa", "v2-bbbbbbbbbbbb", ".tmp-v4-123", "v3-cccccccccccc", "v4-dddddddddddd")
	removed, err := PruneMods(context.Background(), state, filepath.Join(parent, "v4-dddddddddddd"), noProcesses)
	if err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, parent); !slices.Equal(got, []string{".tmp-v4-123", "v3-cccccccccccc", "v4-dddddddddddd"}) {
		t.Fatalf("left %v", got)
	}
	slices.Sort(removed)
	if want := []string{filepath.Join(parent, "v1-aaaaaaaaaaaa"), filepath.Join(parent, "v2-bbbbbbbbbbbb")}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
}

// A claude still running on an old mod loaded it with --plugin-dir; its
// dir stays however old it is.
func TestPruneModsSparesAModAProcessLoaded(t *testing.T) {
	state := t.TempDir()
	parent := modDirs(t, state, "v1-aaaaaaaaaaaa", "v2-bbbbbbbbbbbb", "v3-cccccccccccc", "v4-dddddddddddd")
	running := func(context.Context) ([]string, error) {
		return []string{"/Users/x/.local/bin/claude --plugin-dir " + filepath.Join(parent, "v1-aaaaaaaaaaaa") + " --session-id s1"}, nil
	}
	if _, err := PruneMods(context.Background(), state, filepath.Join(parent, "v4-dddddddddddd"), running); err != nil {
		t.Fatal(err)
	}
	if got := listDir(t, parent); !slices.Equal(got, []string{"v1-aaaaaaaaaaaa", "v3-cccccccccccc", "v4-dddddddddddd"}) {
		t.Fatalf("left %v", got)
	}
}

// Unsure which mods are in use: remove nothing.
func TestPruneModsRemovesNothingWhenProcessesCannotBeListed(t *testing.T) {
	state := t.TempDir()
	parent := modDirs(t, state, "v1-aaaaaaaaaaaa", "v2-bbbbbbbbbbbb", "v3-cccccccccccc")
	for _, list := range []func(context.Context) ([]string, error){
		func(context.Context) ([]string, error) { return nil, errors.New("ps: not found") },
		func(context.Context) ([]string, error) { return nil, nil },
	} {
		if _, err := PruneMods(context.Background(), state, filepath.Join(parent, "v3-cccccccccccc"), list); err == nil {
			t.Fatal("PruneMods reported success without a process list")
		}
		if got := listDir(t, parent); len(got) != 3 {
			t.Fatalf("removed mods without knowing what runs: left %v", got)
		}
	}
}

// The daemon prunes once, at its first bridged launch.
func TestPlanPrunesOldModsOnce(t *testing.T) {
	state := t.TempDir()
	parent := modDirs(t, state, "v0-000000000000", "v1-aaaaaaaaaaaa", "v2-bbbbbbbbbbbb")
	calls := 0
	p := &countingProbe{answers: map[string]string{"/bin/claude": "2.1.289"}}
	l := NewLauncher(LauncherOptions{
		StateDir: state, LeoVersion: "v9", LeoBin: "/opt/leo/bin/leo",
		Probe: p.probe, Log: &bytes.Buffer{},
		Processes: func(ctx context.Context) ([]string, error) {
			calls++
			return noProcesses(ctx)
		},
	})
	plan, ok := l.Plan(context.Background(), "/bin/claude", "leo-alpha")
	if !ok {
		t.Fatal("Plan refused")
	}
	if _, ok := l.Plan(context.Background(), "/bin/claude", "leo-beta"); !ok {
		t.Fatal("Plan refused")
	}
	if calls != 1 {
		t.Fatalf("listed processes %d times, want once per launcher", calls)
	}
	if got, want := listDir(t, parent), []string{"v2-bbbbbbbbbbbb", filepath.Base(plan.PluginDir)}; !slices.Equal(got, want) {
		t.Fatalf("left %v, want %v", got, want)
	}
}

// The real lister sees this test process, whole command line and all.
func TestListProcessesSeesThisProcess(t *testing.T) {
	lines, err := ListProcesses(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	self := os.Args[0]
	if !slices.ContainsFunc(lines, func(l string) bool { return strings.Contains(l, self) }) {
		t.Fatalf("no command line names %s among %d processes", self, len(lines))
	}
}
