package consult

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

var attachEpoch = time.Unix(1_760_000_000, 0)

// deadPids names the pids the test registry reports as exited.
var deadPids = map[int]bool{}

func newTestAttachPlacements(now *time.Time) *AttachPlacements {
	for pid := range deadPids {
		delete(deadPids, pid)
	}
	return NewAttachPlacements(func() time.Time { return *now }, func(pid int) bool { return !deadPids[pid] })
}

func clientAt(pid int, created time.Time) tmux.Client {
	return tmux.Client{PID: pid, Created: created}
}

func listing(clients ...tmux.Client) func(context.Context, string) ([]tmux.Client, error) {
	return func(context.Context, string) ([]tmux.Client, error) { return clients, nil }
}

func TestAttachPlacementsZeroClientsKeepsBaseOverrides(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "background")
	base := ViewerOverrides{Placement: "window", MaxPanes: 2}
	got := reg.Resolve(context.Background(), "$1", base, &config.Config{}, listing())
	if got != base {
		t.Fatalf("got %+v, want base %+v", got, base)
	}
}

func TestAttachPlacementsListErrorKeepsBaseOverrides(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "background")
	base := ViewerOverrides{Placement: "window"}
	failing := func(context.Context, string) ([]tmux.Client, error) { return nil, errors.New("tmux down") }
	if got := reg.Resolve(context.Background(), "$1", base, &config.Config{}, failing); got != base {
		t.Fatalf("got %+v, want base %+v", got, base)
	}
}

func TestAttachPlacementsMostVisibleWins(t *testing.T) {
	for name, tc := range map[string]struct {
		placements []string
		want       string
	}{
		"pane beats window and background": {[]string{"background", "pane", "window"}, "pane"},
		"window beats background":          {[]string{"background", "window"}, "window"},
		"background alone":                 {[]string{"background"}, "background"},
	} {
		t.Run(name, func(t *testing.T) {
			now := attachEpoch
			reg := newTestAttachPlacements(&now)
			var clients []tmux.Client
			for i, p := range tc.placements {
				reg.Register("$1", 100+i, p)
				clients = append(clients, clientAt(100+i, attachEpoch.Add(time.Second)))
			}
			got := reg.Resolve(context.Background(), "$1", ViewerOverrides{MaxPanes: 2}, &config.Config{}, listing(clients...))
			if got.Placement != tc.want || got.MaxPanes != 2 {
				t.Fatalf("got %+v, want placement %q with MaxPanes kept", got, tc.want)
			}
		})
	}
}

func TestAttachPlacementsUnflaggedClientContributesSessionOverrideOrDefault(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "background")
	clients := listing(clientAt(10, attachEpoch), clientAt(20, attachEpoch))

	cfg := &config.Config{}
	cfg.Defaults.Dispatch.Viewer.Placement = "window"
	if got := reg.Resolve(context.Background(), "$1", ViewerOverrides{}, cfg, clients); got.Placement != "window" {
		t.Fatalf("config default: got %+v, want window", got)
	}
	if got := reg.Resolve(context.Background(), "$1", ViewerOverrides{Placement: "pane"}, cfg, clients); got.Placement != "pane" {
		t.Fatalf("session override: got %+v, want pane", got)
	}
	if got := reg.Resolve(context.Background(), "$1", ViewerOverrides{}, &config.Config{}, clients); got.Placement != "pane" {
		t.Fatalf("built-in default: got %+v, want pane", got)
	}
}

func TestAttachPlacementsIgnoresStaleEntries(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "background")
	base := ViewerOverrides{Placement: "pane"}

	// Same pid, but the client was created well before the registration: the
	// pid was reused by an unrelated client.
	reused := listing(clientAt(10, attachEpoch.Add(-time.Minute)))
	if got := reg.Resolve(context.Background(), "$1", base, &config.Config{}, reused); got.Placement != "pane" {
		t.Fatalf("pid reuse: got %+v, want the unflagged default", got)
	}
	// Registered pid no longer attached: only the unrelated client remains.
	gone := listing(clientAt(11, attachEpoch.Add(time.Second)))
	if got := reg.Resolve(context.Background(), "$1", base, &config.Config{}, gone); got.Placement != "pane" {
		t.Fatalf("gone pid: got %+v, want the unflagged default", got)
	}
}

func TestAttachPlacementsToleratesSecondGranularityClientCreated(t *testing.T) {
	now := attachEpoch.Add(900 * time.Millisecond)
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "background")
	// tmux truncated client_created to the whole second, below registered_at.
	got := reg.Resolve(context.Background(), "$1", ViewerOverrides{Placement: "pane"}, &config.Config{}, listing(clientAt(10, attachEpoch)))
	if got.Placement != "background" {
		t.Fatalf("got %+v, want the registered background", got)
	}
}

func TestAttachPlacementsPrunesEntriesWhoseProcessIsGone(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("leo-a", 10, "background")
	reg.Register("leo-a", 11, "window")
	live := listing(clientAt(99, attachEpoch))

	// Both processes are alive (11 may be about to exec tmux): both kept.
	reg.Resolve(context.Background(), "$1", ViewerOverrides{}, &config.Config{}, live)
	if reg.Len() != 2 {
		t.Fatalf("live entries pruned: len=%d", reg.Len())
	}
	deadPids[10] = true
	reg.Resolve(context.Background(), "$1", ViewerOverrides{}, &config.Config{}, live)
	if reg.Len() != 1 {
		t.Fatalf("dead entry kept: len=%d", reg.Len())
	}
}

func TestAttachPlacementsPrunesReusedPidSeenInTheListing(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("leo-a", 10, "background")
	reg.Resolve(context.Background(), "$1", ViewerOverrides{}, &config.Config{}, listing(clientAt(10, attachEpoch.Add(-time.Hour))))
	if reg.Len() != 0 {
		t.Fatalf("reused-pid entry kept: len=%d", reg.Len())
	}
}

func TestAttachPlacementsMatchesByPidWhateverSessionNameWasRegistered(t *testing.T) {
	// The attach registers the tmux session name; dispatches know the session
	// id. The client list of the id is what scopes the match.
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("leo-agent-foo", 10, "background")
	got := reg.Resolve(context.Background(), "$4", ViewerOverrides{Placement: "pane"}, &config.Config{}, listing(clientAt(10, attachEpoch)))
	if got.Placement != "background" {
		t.Fatalf("got %+v", got)
	}
}

func TestAttachPlacementsReRegisterReplacesSamePid(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	reg.Register("$1", 10, "pane")
	reg.Register("$1", 10, "background")
	got := reg.Resolve(context.Background(), "$1", ViewerOverrides{}, &config.Config{}, listing(clientAt(10, attachEpoch)))
	if got.Placement != "background" || reg.Len() != 1 {
		t.Fatalf("got %+v len=%d", got, reg.Len())
	}
}

func TestAttachPlacementsRegisterRejectsBadInput(t *testing.T) {
	now := attachEpoch
	reg := newTestAttachPlacements(&now)
	for name, call := range map[string]func() error{
		"bad placement": func() error { return reg.Register("$1", 10, "sideways") },
		"empty session": func() error { return reg.Register("", 10, "pane") },
		"zero pid":      func() error { return reg.Register("$1", 0, "pane") },
	} {
		if call() == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if reg.Len() != 0 {
		t.Fatalf("len=%d, want 0", reg.Len())
	}
}

func TestNilAttachPlacementsLeavesOverridesAlone(t *testing.T) {
	var reg *AttachPlacements
	base := ViewerOverrides{Placement: "window"}
	if got := reg.Resolve(context.Background(), "$1", base, &config.Config{}, listing(clientAt(1, attachEpoch))); got != base {
		t.Fatalf("got %+v", got)
	}
}
