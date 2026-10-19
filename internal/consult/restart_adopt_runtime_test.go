package consult

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func callIndex(calls [][]string, want []string) int {
	return slices.IndexFunc(calls, func(c []string) bool { return reflect.DeepEqual(c, want) })
}

// A bridged launch tags its pane with the launch token, after the pane
// exists, so a restarted daemon can re-open the dispatch's bridge key.
func TestBridgedLaunchTagsItsPaneWithTheLaunch(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-tag", "claude", "brief")
	want := tmux.Args("set-option", "-p", "-t", rigPane, adoptLaunchOption, g.launchOf(t, "dispatch.d-tag"))
	g.mu.Lock()
	tag, launched := callIndex(g.calls, want), slices.IndexFunc(g.calls, func(c []string) bool { return slices.Contains(c, "new-window") })
	g.mu.Unlock()
	if tag < 0 || tag < launched {
		t.Fatalf("tag at %d, launch at %d; want %q after the launch", tag, launched, want)
	}
}

func TestUnbridgedLaunchDoesNotTagItsPane(t *testing.T) {
	g := newBridgeRig(t, "2.1.0")
	g.launch(t, "d-legacy", "claude", "brief")
	if n := g.callCount(adoptLaunchOption); n != 0 {
		t.Fatalf("an unbridged launch set %s %d times", adoptLaunchOption, n)
	}
}

// The legacy relaunch drops the tag first: the relaunched claude has no mod,
// so nothing may adopt its bridge.
func TestAwaitOpeningFallbackUntagsThePaneBeforeTheRespawn(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: time.Nanosecond})
	g.launch(t, "d-untag", "claude", "brief")
	if _, _, err := g.r.AwaitOpening(context.Background(), rigPane); err != nil {
		t.Fatal(err)
	}
	want := tmux.Args("set-option", "-p", "-u", "-t", rigPane, adoptLaunchOption)
	g.mu.Lock()
	untag, respawn := callIndex(g.calls, want), slices.IndexFunc(g.calls, func(c []string) bool { return slices.Contains(c, "respawn-pane") })
	g.mu.Unlock()
	if untag < 0 || respawn < 0 || untag > respawn {
		t.Fatalf("untag at %d, respawn at %d; want %q before the respawn", untag, respawn, want)
	}
}

func TestProbeAdoption(t *testing.T) {
	const owned = `export LEO_API_TOKEN="$(cat '/s/token' 2>/dev/null)"; exec 'env' 'LEO_DISPATCH_ID=d-probe' 'claude' '--model' 'opus'`
	const foreign = `exec 'env' 'LEO_DISPATCH_ID=d-other' 'claude'`
	tests := []struct {
		name string
		out  string
		fail string
		want AdoptProbe
		err  bool
	}{
		{"alive and owned with a token", "%2\t0\tother\t" + foreign + "\n%9\t0\ttok_ABC\t" + owned + "\n", "", AdoptProbe{Presence: PanePresentAlive, Owned: true, Launch: "tok_ABC"}, false},
		{"dead", "%9\t1\ttok_ABC\t" + owned + "\n", "", AdoptProbe{Presence: PanePresentDead, Owned: true, Launch: "tok_ABC"}, false},
		{"foreign start command", "%9\t0\ttok_ABC\t" + foreign + "\n", "", AdoptProbe{Presence: PanePresentAlive, Launch: "tok_ABC"}, false},
		{"no token", "%9\t0\t\t" + owned + "\n", "", AdoptProbe{Presence: PanePresentAlive, Owned: true}, false},
		{"absent", "", "can't find pane: %9", AdoptProbe{Presence: PaneAbsent}, false},
		{"tmux error", "", "no server running on /tmp/tmux-501/leo", AdoptProbe{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var argv []string
			r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
			r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
				argv = args
				if tt.fail != "" {
					return exec.Command("sh", "-c", `printf '%s\n' "$0" >&2; exit 1`, tt.fail)
				}
				return exec.Command("printf", "%s", tt.out)
			}
			got, err := r.ProbeAdoption("%9", "d-probe")
			if (err != nil) != tt.err || (!tt.err && got != tt.want) {
				t.Fatalf("ProbeAdoption = %+v, %v; want %+v (error %v)", got, err, tt.want, tt.err)
			}
			want := tmux.Args("list-panes", "-t", "%9", "-F", "#{pane_id}\t#{pane_dead}\t#{"+adoptLaunchOption+"}\t#{pane_start_command}")
			if !reflect.DeepEqual(argv, want) {
				t.Fatalf("argv = %q, want %q", argv, want)
			}
		})
	}
}

// Adopting re-opens the dispatch's key for the launch its pane was tagged
// with: that launch's mod may connect again, no other may, and its reports
// keep driving the dispatch.
func TestAdoptBridgeReopensTheKeyForTheTaggedLaunch(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	if err := g.r.AdoptBridge("d-adopt", "%9", "orch", "tok_ABC"); err != nil {
		t.Fatal(err)
	}
	g.hub.AwaitAdoption(nil)
	if _, err := g.hub.Connect("dispatch.d-adopt", "tok_OTHER"); !errors.Is(err, bridge.ErrStaleLaunch) {
		t.Fatalf("a foreign launch connected: %v", err)
	}
	if _, err := g.hub.Connect("dispatch.d-adopt", "tok_ABC"); err != nil {
		t.Fatalf("the tagged launch cannot reconnect: %v", err)
	}
	if !g.r.BridgeOwnsReports("d-adopt") {
		t.Fatal("an adopted bridge does not own its reports")
	}
	if got := g.r.FrameMessage("%9", "next"); got != bridge.Framed("orch", "next") {
		t.Fatalf("FrameMessage = %q, want a bridged follow-up", got)
	}
	if err := g.r.Kill("%9"); err != nil {
		t.Fatal(err)
	}
	if g.r.BridgeOwnsReports("d-adopt") {
		t.Fatal("killing the adopted pane kept its bridge")
	}
}

func TestAdoptBridgeWithoutAHubFails(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	if err := r.AdoptBridge("d-adopt", "%9", "orch", "tok_ABC"); err == nil {
		t.Fatal("adopted a bridge with no hub")
	}
}

func TestAdoptPaneRestoresClassifierAndPlacement(t *testing.T) {
	r := NewInteractiveRuntime("", nil, nil, "tmux", "leo")
	r.AdoptPane("%9", "codex", "window")
	r.AdoptPane("%8", "claude", "split")
	if r.ViewerKind("%9") != "window" || r.ViewerKind("%8") != "split" {
		t.Fatalf("placements = %q, %q", r.ViewerKind("%9"), r.ViewerKind("%8"))
	}
	pointer := func(c tmux.ComposerClassifier) uintptr { return reflect.ValueOf(c).Pointer() }
	if pointer(r.classifier("%9")) != pointer(tmux.CodexComposerClassifier) || pointer(r.classifier("%8")) != pointer(tmux.ClaudeComposerClassifier) {
		t.Fatal("adopted panes lost their harness's composer classifier")
	}
}

// An adopted turn is already running: it takes a slot even over the cap,
// and the line moves only once enough slots are released.
func TestSlotLimiterAdoptTakesASlotOverTheCap(t *testing.T) {
	l := newSlotLimiter(1)
	if !l.TryAcquire() {
		t.Fatal("first slot refused")
	}
	l.Adopt()
	if l.InUse() != 2 {
		t.Fatalf("in use = %d, want 2", l.InUse())
	}
	w := l.Enqueue()
	ready := func() bool {
		select {
		case <-w.Ready():
			return true
		default:
			return false
		}
	}
	l.Release()
	if ready() {
		t.Fatal("granted while still at the cap")
	}
	l.Release()
	if !ready() {
		t.Fatal("not granted once a slot freed")
	}
}

type helloLog struct {
	reportLog
	hellos []string
}

func (l *helloLog) NoteBridgeHello(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.hellos = append(l.hellos, id)
}

// The mod of an adopted dispatch says hello when it reconnects, which is
// what tells the dispatcher the run is live.
func TestDispatchBridgeSubscriberPassesOnAnAdoptedRunsHello(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	var log helloLog
	g.hub.AddSubscriber(g.r.DispatchBridgeSubscriber(&log))
	if err := g.r.AdoptBridge("d-adopt", "%9", "orch", "tok_ABC"); err != nil {
		t.Fatal(err)
	}
	if err := g.hub.Apply("dispatch.d-adopt", "tok_ABC", bridge.Report{Type: bridge.ReportHello, SessionID: "s1", ClaudeVersion: "2.1.289"}); err != nil {
		t.Fatal(err)
	}
	log.mu.Lock()
	defer log.mu.Unlock()
	if !reflect.DeepEqual(log.hellos, []string{"d-adopt"}) || len(log.got) != 0 {
		t.Fatalf("hellos=%v reports=%v; want one hello and no report", log.hellos, log.got)
	}
}
