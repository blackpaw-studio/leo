package picker

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func names(agents []Agent) []string {
	got := make([]string, len(agents))
	for i, a := range agents {
		got[i] = a.Host + "/" + a.Name
	}
	return got
}

func TestSortModes(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	agents := []Agent{{Name: "zulu", Host: "remote", StartedAt: now.Add(-time.Hour)}, {Name: "beta", Host: LocalHost, StartedAt: now.Add(-3 * time.Hour)}, {Name: "alpha", Host: LocalHost}, {Name: "aardvark", Host: "remote", StartedAt: now.Add(-3 * time.Hour)}}
	last := map[string]time.Time{"remote/zulu": now, "local/beta": now.Add(-time.Minute)}
	cases := []struct {
		name string
		got  []Agent
		want []string
	}{
		{"recent", SortRecent(agents, last), []string{"remote/zulu", "local/beta", "local/alpha", "remote/aardvark"}},
		{"name", SortName(agents, last), []string{"local/alpha", "local/beta", "remote/aardvark", "remote/zulu"}},
		{"uptime", SortUptime(agents, last), []string{"remote/aardvark", "local/beta", "remote/zulu", "local/alpha"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := names(tc.got); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSortKeyKeepsSelectedAgentAndFilterOwnsO(t *testing.T) {
	m := newModelWithOptions(context.Background(), map[string]Backend{LocalHost: &fakeBackend{}}, Options{SortMode: SortModeRecent})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Status: "running"}, {Name: "beta", Status: "running"}}, nil)
	m.list.Select(1)
	m, _ = drive(t, m, keyRunes("o"))
	r, ok := m.selectedRow()
	if !ok || r.ag.Name != "beta" {
		t.Fatalf("selected = %#v, want beta", r)
	}
	m, _ = drive(t, m, keyRunes("/"))
	m, _ = drive(t, m, keyRunes("o"))
	if m.sortMode != SortModeName {
		t.Fatalf("filter consumed o incorrectly: sort = %q", m.sortMode)
	}
}

func TestResultCarriesSortModeOnAttachAndQuit(t *testing.T) {
	m := newModelWithOptions(context.Background(), map[string]Backend{LocalHost: &fakeBackend{}}, Options{SortMode: SortModeUptime})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Status: "running"}}, nil)
	m, _ = drive(t, m, keyRunes("enter"))
	if m.result.SortMode != SortModeUptime {
		t.Fatalf("attach sort = %q", m.result.SortMode)
	}
	m = newModelWithOptions(context.Background(), map[string]Backend{}, Options{SortMode: SortModeName})
	m, _ = drive(t, m, keyRunes("q"))
	if m.result.SortMode != SortModeName {
		t.Fatalf("quit sort = %q", m.result.SortMode)
	}
}
