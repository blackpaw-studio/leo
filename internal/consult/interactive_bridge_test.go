package consult

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// bridgeRig is a TmuxInteractiveRuntime wired to a real bridge hub and a
// launcher whose `claude --version` probe answers claudeVersion. Every tmux
// argv is recorded; new-window answers pane %42.
type bridgeRig struct {
	r   *TmuxInteractiveRuntime
	hub *bridge.Hub
	dir string

	mu           sync.Mutex
	calls        [][]string
	newWindowErr bool
}

const rigPane = "%42"

func newBridgeRig(t *testing.T, claudeVersion string) *bridgeRig {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	cfg := &config.Config{HomePath: dir, Templates: map[string]config.TemplateConfig{
		"claude": {Harness: "claude", Model: "sonnet"},
		"codex":  {Harness: "codex", Model: "gpt-5.5"},
	}}
	g := &bridgeRig{dir: dir, hub: bridge.New(bridge.Options{})}
	t.Cleanup(g.hub.Close)
	g.r = NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
	g.r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		g.mu.Lock()
		g.calls = append(g.calls, append([]string(nil), args...))
		fail := g.newWindowErr
		g.mu.Unlock()
		if slices.Contains(args, "new-window") {
			if fail {
				return exec.Command("false")
			}
			return exec.Command("echo", rigPane)
		}
		return exec.Command("true")
	}
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{
		StateDir:   filepath.Join(dir, "state"),
		LeoVersion: "vtest",
		LeoBin:     "/opt/leo/bin/leo",
		Probe:      func(context.Context, string) (string, error) { return claudeVersion + " (Claude Code)", nil },
		Log:        io.Discard,
	})
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: launcher, ConnectTimeout: 5 * time.Second})
	return g
}

// lastCall returns the most recent tmux argv containing verb.
func (g *bridgeRig) lastCall(verb string) []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	for i := len(g.calls) - 1; i >= 0; i-- {
		if slices.Contains(g.calls[i], verb) {
			return g.calls[i]
		}
	}
	return nil
}

func (g *bridgeRig) callCount(verb string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, c := range g.calls {
		if slices.Contains(c, verb) {
			n++
		}
	}
	return n
}

func (g *bridgeRig) launch(t *testing.T, id, template, prompt string) {
	t.Helper()
	pane, _, err := g.r.Launch(context.Background(), LaunchRequest{ID: id, Template: template, Caller: "orch", Cwd: g.dir, Name: "work", Prompt: prompt})
	if err != nil || pane != rigPane {
		t.Fatalf("Launch = %q, %v", pane, err)
	}
}

// connect opens key's stream and acks the queued opening deliver, leaving a
// connected, idle bridge.
func (g *bridgeRig) connectAndAckOpening(t *testing.T, key string) *bridge.Stream {
	t.Helper()
	stream, err := g.hub.Connect(key)
	if err != nil {
		t.Fatal(err)
	}
	cmd := nextCommand(t, stream)
	if err := g.hub.Apply(key, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	return stream
}

func nextCommand(t *testing.T, stream *bridge.Stream) bridge.Command {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("stream.Next: %v", err)
	}
	return cmd
}

func envArg(argv []string, key string) (string, bool) {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == "-e" && strings.HasPrefix(argv[i+1], key+"=") {
			return strings.TrimPrefix(argv[i+1], key+"="), true
		}
	}
	return "", false
}

func flagArg(argv []string, flag string) (string, bool) {
	i := slices.Index(argv, flag)
	if i < 0 || i+1 >= len(argv) {
		return "", false
	}
	return argv[i+1], true
}

// A claude new enough for mods launches with the leo-bridge mod: the mod's
// --plugin-dir right after the binary, its env, no brief on argv, and the
// opening queued on the bridge as the user's own prompt.
func TestBridgedClaudeDispatchLaunch(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	brief := "do the work; mind $HOME and `quotes`"
	g.launch(t, "d-bridge", "claude", brief)

	launch := g.lastCall("new-window")
	if v, _ := envArg(launch, bridgemod.EnvAgent); v != "dispatch.d-bridge" {
		t.Fatalf("%s = %q in %q", bridgemod.EnvAgent, v, launch)
	}
	if v, _ := envArg(launch, bridgemod.EnvBin); v != "/opt/leo/bin/leo" {
		t.Fatalf("%s = %q", bridgemod.EnvBin, v)
	}
	command := launch[len(launch)-1]
	words := shellCommandWords(command)
	if len(words) < 5 || words[2] != "claude" || words[3] != bridgemod.PluginDirFlag {
		t.Fatalf("command words = %q, want --plugin-dir right after claude", words)
	}
	if _, err := os.Stat(filepath.Join(words[4], ".claude-plugin", "plugin.json")); err != nil {
		t.Fatalf("--plugin-dir %q is not the materialized mod: %v", words[4], err)
	}
	if strings.Contains(command, "$(cat") || strings.Contains(command, brief) {
		t.Fatalf("bridged launch put the brief on argv: %q", command)
	}

	stream, err := g.hub.Connect("dispatch.d-bridge")
	if err != nil {
		t.Fatal(err)
	}
	cmd := nextCommand(t, stream)
	if cmd.Op != bridge.OpDeliver || cmd.Text != brief || !cmd.AsUser {
		t.Fatalf("queued opening = %+v, want deliver of the brief as the user", cmd)
	}
}

// Every launch the bridge does not carry blanks the bridge variables, so a
// dispatch opened in an agent's tmux session never inherits that agent's
// bridge key from the session environment.
func TestUnbridgedDispatchLaunchesBlankTheBridgeEnv(t *testing.T) {
	cases := []struct {
		name, version, template string
		wantBriefWord           bool
	}{
		{name: "claude older than the mods API", version: "2.1.286", template: "claude", wantBriefWord: true},
		{name: "codex", version: "2.1.289", template: "codex"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newBridgeRig(t, tc.version)
			g.launch(t, "d-legacy", tc.template, "brief")
			launch := g.lastCall("new-window")
			for _, k := range []string{bridgemod.EnvAgent, bridgemod.EnvBin} {
				if v, ok := envArg(launch, k); !ok || v != "" {
					t.Fatalf("%s = %q (set %v), want blanked", k, v, ok)
				}
			}
			command := launch[len(launch)-1]
			if strings.Contains(command, bridgemod.PluginDirFlag) {
				t.Fatalf("legacy launch loads the mod: %q", command)
			}
			if got := strings.Contains(command, "$(cat"); got != tc.wantBriefWord {
				t.Fatalf("brief word on argv = %v, want %v: %q", got, tc.wantBriefWord, command)
			}
			if st := g.hub.State("dispatch.d-legacy"); st.Pending != 0 {
				t.Fatalf("legacy launch queued %d bridge commands", st.Pending)
			}
		})
	}
}

func TestDispatchLaunchWithoutABridgeBlanksTheBridgeEnv(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{})
	g.launch(t, "d-nobridge", "claude", "brief")
	launch := g.lastCall("new-window")
	if v, ok := envArg(launch, bridgemod.EnvAgent); !ok || v != "" {
		t.Fatalf("%s = %q (set %v), want blanked", bridgemod.EnvAgent, v, ok)
	}
	if strings.Contains(launch[len(launch)-1], bridgemod.PluginDirFlag) {
		t.Fatal("launch without a bridge loads the mod")
	}
}

func TestFailedBridgedLaunchDropsTheQueuedOpening(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.newWindowErr = true
	if _, _, err := g.r.Launch(context.Background(), LaunchRequest{ID: "d-fail", Template: "claude", Cwd: g.dir, Name: "work", Prompt: "brief"}); err == nil {
		t.Fatal("Launch succeeded with a failing new-window")
	}
	if st := g.hub.State("dispatch.d-fail"); st.Pending != 0 {
		t.Fatalf("failed launch left %d queued commands", st.Pending)
	}
	if g.r.BridgeOwnsReports("d-fail") {
		t.Fatal("failed launch is still registered as bridged")
	}
}

func TestAwaitOpeningKeepsAConnectedBridge(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-ok", "claude", "brief")
	go func() {
		time.Sleep(20 * time.Millisecond)
		_, _ = g.hub.Connect("dispatch.d-ok")
	}()
	handled, paste, err := g.r.AwaitOpening(context.Background(), rigPane)
	if !handled || paste || err != nil {
		t.Fatalf("AwaitOpening = %v, %v, %v; want handled, no paste", handled, paste, err)
	}
	if n := g.callCount("respawn-pane"); n != 0 {
		t.Fatalf("a connected bridge was relaunched %d times", n)
	}
}

// A mod that never connects is relaunched the legacy way in the same pane:
// no --plugin-dir, the bridge env blanked, the brief back on argv, and the
// queued opening dropped so it can never arrive twice.
func TestAwaitOpeningRelaunchesLegacyWhenTheBridgeNeverConnects(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: 30 * time.Millisecond})
	g.launch(t, "d-slow", "claude", "brief text")

	handled, paste, err := g.r.AwaitOpening(context.Background(), rigPane)
	if !handled || paste || err != nil {
		t.Fatalf("AwaitOpening = %v, %v, %v; want handled, brief on argv", handled, paste, err)
	}
	respawn := g.lastCall("respawn-pane")
	if respawn == nil {
		t.Fatal("no legacy relaunch")
	}
	if !slices.Contains(respawn, "-k") {
		t.Fatalf("respawn does not kill the bridged claude: %q", respawn)
	}
	if v, _ := flagArg(respawn, "-t"); v != rigPane {
		t.Fatalf("respawn target = %q", v)
	}
	if v, _ := flagArg(respawn, "-c"); v != g.dir {
		t.Fatalf("respawn cwd = %q", v)
	}
	for _, k := range []string{bridgemod.EnvAgent, bridgemod.EnvBin} {
		if v, ok := envArg(respawn, k); !ok || v != "" {
			t.Fatalf("respawn %s = %q (set %v), want blanked", k, v, ok)
		}
	}
	if v, _ := envArg(respawn, dispatchIDEnv); v != "d-slow" {
		t.Fatalf("respawn %s = %q", dispatchIDEnv, v)
	}
	command := respawn[len(respawn)-1]
	if strings.Contains(command, bridgemod.PluginDirFlag) {
		t.Fatalf("legacy relaunch loads the mod: %q", command)
	}
	if !strings.HasSuffix(command, claudeBriefArgvWord(dispatchBriefPath(g.dir, "d-slow"))) {
		t.Fatalf("legacy relaunch lacks the brief word: %q", command)
	}
	if !startCommandHasDispatchID(command, "d-slow") {
		t.Fatalf("relaunched pane no longer findable by dispatch id: %q", command)
	}
	if st := g.hub.State("dispatch.d-slow"); st.Pending != 0 {
		t.Fatalf("fallback left %d queued commands", st.Pending)
	}
	if g.r.BridgeOwnsReports("d-slow") {
		t.Fatal("a fallen-back dispatch still takes bridge reports")
	}
	if got := g.r.FrameMessage(rigPane, "next"); got != "next" {
		t.Fatalf("FrameMessage after fallback = %q", got)
	}
}

// An opening too large for argv rides the bridge whole; if the bridge never
// connects, the legacy relaunch carries no brief and the caller must paste.
func TestOversizedOpeningRidesTheBridgeAndNeedsAPasteAfterFallback(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: 30 * time.Millisecond})
	brief := strings.Repeat("x", claudeArgvPromptLimit+1)
	g.launch(t, "d-big", "claude", brief)
	if st := g.hub.State("dispatch.d-big"); st.Pending != 1 {
		t.Fatalf("oversized opening not queued on the bridge (pending %d)", st.Pending)
	}
	handled, paste, err := g.r.AwaitOpening(context.Background(), rigPane)
	if !handled || !paste || err != nil {
		t.Fatalf("AwaitOpening = %v, %v, %v; want handled and a paste", handled, paste, err)
	}
	if command := g.lastCall("respawn-pane"); strings.Contains(command[len(command)-1], "$(cat") {
		t.Fatalf("oversized brief put on argv: %q", command[len(command)-1])
	}
}

func TestAwaitOpeningIgnoresUnbridgedPanes(t *testing.T) {
	g := newBridgeRig(t, "2.1.286")
	g.launch(t, "d-legacy", "claude", "brief")
	if handled, _, _ := g.r.AwaitOpening(context.Background(), rigPane); handled {
		t.Fatal("AwaitOpening handled a legacy pane")
	}
}

// A dispatch released while its bridge is still connecting is never
// relaunched.
func TestAwaitOpeningAfterReleaseDoesNotRelaunch(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: 50 * time.Millisecond})
	g.launch(t, "d-gone", "claude", "brief")
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _, _ = g.r.AwaitOpening(context.Background(), rigPane)
	}()
	g.r.ReleaseRunFiles("d-gone")
	<-done
	if n := g.callCount("respawn-pane"); n != 0 {
		t.Fatalf("released dispatch relaunched %d times", n)
	}
}

// A follow-up to a bridged dispatch is armed first, then delivered over the
// bridge as a non-user message; tmux never sees it.
func TestBridgedInjectDeliversOverTheBridge(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-send", "claude", "brief")
	stream := g.connectAndAckOpening(t, "dispatch.d-send")
	before := len(g.calls)

	var armed atomic.Bool
	done := make(chan error, 1)
	go func() {
		done <- g.r.Inject(context.Background(), rigPane, "next step", func() error { armed.Store(true); return nil })
	}()
	cmd := nextCommand(t, stream)
	if !armed.Load() {
		t.Fatal("deliver sent before the turn was armed")
	}
	if cmd.Op != bridge.OpDeliver || cmd.Text != "next step" || cmd.AsUser {
		t.Fatalf("deliver = %+v, want the text as a non-user message", cmd)
	}
	if err := g.hub.Apply("dispatch.d-send", bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Inject = %v", err)
	}
	g.mu.Lock()
	after := g.calls[before:]
	g.mu.Unlock()
	if len(after) != 0 {
		t.Fatalf("bridged inject touched tmux: %q", after)
	}
}

func TestBridgedInjectRejectionIsAnErrorNotAPaste(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-rej", "claude", "brief")
	stream := g.connectAndAckOpening(t, "dispatch.d-rej")
	done := make(chan error, 1)
	go func() { done <- g.r.Inject(context.Background(), rigPane, "next", nil) }()
	cmd := nextCommand(t, stream)
	_ = g.hub.Apply("dispatch.d-rej", bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: false, Error: "composer busy"})
	if err := <-done; !errors.Is(err, bridge.ErrRejected) {
		t.Fatalf("Inject = %v, want ErrRejected", err)
	}
	if n := g.callCount("capture-pane"); n != 0 {
		t.Fatal("a rejected bridge deliver fell back to tmux")
	}
}

func TestBridgedInjectArmFailureSendsNothing(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-arm", "claude", "brief")
	g.connectAndAckOpening(t, "dispatch.d-arm")
	refuse := errors.New("dispatch settled during injection")
	if err := g.r.Inject(context.Background(), rigPane, "next", func() error { return refuse }); !errors.Is(err, refuse) {
		t.Fatalf("Inject = %v, want the arm error", err)
	}
	if st := g.hub.State("dispatch.d-arm"); st.Pending != 0 {
		t.Fatalf("refused inject queued %d commands", st.Pending)
	}
}

// A deliver the mod has not acked by the deadline stays queued: Inject
// reports it accepted-for-delivery rather than failing (a retry would
// deliver it twice) and never pastes it as well.
func TestBridgedInjectUnackedByTheDeadlineStaysQueued(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: time.Second, SendTimeout: 30 * time.Millisecond})
	g.launch(t, "d-queued", "claude", "brief")
	g.connectAndAckOpening(t, "dispatch.d-queued")
	if err := g.r.Inject(context.Background(), rigPane, "next", nil); err != nil {
		t.Fatalf("Inject = %v, want queued", err)
	}
	if st := g.hub.State("dispatch.d-queued"); st.Pending != 1 {
		t.Fatalf("pending = %d, want the deliver still queued", st.Pending)
	}
	if n := g.callCount("capture-pane"); n != 0 {
		t.Fatal("a queued bridge deliver was pasted too")
	}
}

func TestBridgedInjectPastesWhenTheBridgeIsDown(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-down", "claude", "brief")
	_ = g.r.Inject(context.Background(), rigPane, "next", nil)
	if n := g.callCount("capture-pane"); n == 0 {
		t.Fatal("a disconnected bridge did not fall back to tmux")
	}
	if st := g.hub.State("dispatch.d-down"); st.Pending != 1 {
		t.Fatalf("pending = %d, want only the opening", st.Pending)
	}
}

func TestFrameMessageNamesTheOrchestratorOnlyOverALiveBridge(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-frame", "claude", "brief")
	if got := g.r.FrameMessage(rigPane, "hi"); got != "hi" {
		t.Fatalf("FrameMessage before connect = %q", got)
	}
	g.connectAndAckOpening(t, "dispatch.d-frame")
	if got, want := g.r.FrameMessage(rigPane, "hi"), "From orch via leo:\n\nhi"; got != want {
		t.Fatalf("FrameMessage = %q, want %q", got, want)
	}
	if got := g.r.FrameMessage("%7", "hi"); got != "hi" {
		t.Fatalf("FrameMessage of an unknown pane = %q", got)
	}
}

func TestFrameMessageWithoutACallerNamesTheOrchestrator(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	if _, _, err := g.r.Launch(context.Background(), LaunchRequest{ID: "d-anon", Template: "claude", Cwd: g.dir, Name: "work", Prompt: "brief"}); err != nil {
		t.Fatal(err)
	}
	g.connectAndAckOpening(t, "dispatch.d-anon")
	if got, want := g.r.FrameMessage(rigPane, "hi"), "From the orchestrator via leo:\n\nhi"; got != want {
		t.Fatalf("FrameMessage = %q, want %q", got, want)
	}
}

// Shell-hook reports give way to the bridge once its stream is up; release
// and kill both forget the bridge.
func TestBridgeOwnsReportsUntilReleasedOrKilled(t *testing.T) {
	for _, end := range []struct {
		name string
		do   func(r *TmuxInteractiveRuntime) error
	}{
		{"release", func(r *TmuxInteractiveRuntime) error { r.ReleaseRunFiles("d-own"); return nil }},
		{"kill", func(r *TmuxInteractiveRuntime) error { return r.Kill(rigPane) }},
	} {
		t.Run(end.name, func(t *testing.T) {
			g := newBridgeRig(t, "2.1.289")
			g.launch(t, "d-own", "claude", "brief")
			if g.r.BridgeOwnsReports("d-own") {
				t.Fatal("bridge owns reports before it ever connected")
			}
			g.connectAndAckOpening(t, "dispatch.d-own")
			if !g.r.BridgeOwnsReports("d-own") {
				t.Fatal("connected bridge does not own reports")
			}
			if err := end.do(g.r); err != nil {
				t.Fatal(err)
			}
			if g.r.BridgeOwnsReports("d-own") {
				t.Fatal("bridge still owns reports after the run ended")
			}
			if g.hub.Connected("dispatch.d-own") {
				t.Fatal("bridge stream survived the run's end")
			}
		})
	}
}

func TestDispatchBridgeKeys(t *testing.T) {
	if got := DispatchBridgeKey("d-0a1b"); got != "dispatch.d-0a1b" || !config.ValidName(got) {
		t.Fatalf("DispatchBridgeKey = %q", got)
	}
	if id, ok := DispatchIDFromBridgeKey("dispatch.d-0a1b"); !ok || id != "d-0a1b" {
		t.Fatalf("DispatchIDFromBridgeKey = %q, %v", id, ok)
	}
	for _, key := range []string{"alpha", "dispatch.", "alpha.dispatch.x"} {
		if _, ok := DispatchIDFromBridgeKey(key); ok {
			t.Fatalf("DispatchIDFromBridgeKey(%q) matched", key)
		}
	}
}

type reportLog struct {
	mu    sync.Mutex
	got   map[string][]HookReport
	usage map[string][]string
	fail  error
}

func (l *reportLog) Report(id string, hr HookReport) error { return l.report(id, hr) }

func (l *reportLog) ApplyBridgeUsage(id string, raw json.RawMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.usage == nil {
		l.usage = map[string][]string{}
	}
	l.usage[id] = append(l.usage[id], string(raw))
}

func (l *reportLog) report(id string, hr HookReport) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.got == nil {
		l.got = map[string][]HookReport{}
	}
	l.got[id] = append(l.got[id], hr)
	return l.fail
}

// The bridge subscriber feeds a bridged dispatch's turn events to the
// dispatcher as the hook reports they stand in for; agents' events, hellos,
// and dispatches the bridge does not own are dropped.
func TestDispatchBridgeSubscriberForwardsOwnedDispatchEvents(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-sub", "claude", "brief")
	var log reportLog
	g.hub.AddSubscriber(g.r.DispatchBridgeSubscriber(&log))

	key := DispatchBridgeKey("d-sub")
	g.connectAndAckOpening(t, key)
	mustApply := func(agent string, r bridge.Report) {
		t.Helper()
		if err := g.hub.Apply(agent, r); err != nil {
			t.Fatal(err)
		}
	}
	mustApply(key, bridge.Report{Type: bridge.ReportHello, SessionID: "s-1", ClaudeVersion: "2.1.289"})
	mustApply(key, bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnStart, Prompt: "brief", EventID: "turn.start:t1"})
	mustApply(key, bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnComplete, Message: "done", EventID: "turn.complete:t1", Usage: json.RawMessage(`{"cost":{"usd":0.1}}`)})
	mustApply("alpha", bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnStart, Prompt: "x"})
	mustApply(DispatchBridgeKey("d-unknown"), bridge.Report{Type: bridge.ReportEvent, Name: bridge.EventTurnStart, Prompt: "x"})

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.got) != 1 || len(log.got["d-sub"]) != 2 {
		t.Fatalf("forwarded = %v, want two reports for d-sub only", log.got)
	}
	first, second := log.got["d-sub"][0], log.got["d-sub"][1]
	if first.EventID != "bridge:turn.start:t1" || !strings.Contains(string(first.Payload), `"UserPromptSubmit"`) || !strings.Contains(string(first.Payload), `"prompt":"brief"`) {
		t.Fatalf("turn.start forwarded as %+v (%s)", first, first.Payload)
	}
	if !strings.Contains(string(second.Payload), `"last_assistant_message":"done"`) {
		t.Fatalf("turn.complete forwarded as %s", second.Payload)
	}
	if got := log.usage["d-sub"]; len(got) != 1 || got[0] != `{"cost":{"usd":0.1}}` {
		t.Fatalf("usage forwarded = %q, want the turn.complete usage once", got)
	}
}

// The opening is queued under an id derived from the dispatch and its
// text, so any re-queue of it is the same command to the mod's dedup.
func TestBridgedOpeningHasADeterministicID(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-id", "claude", "brief")
	stream, err := g.hub.Connect("dispatch.d-id")
	if err != nil {
		t.Fatal(err)
	}
	if cmd := nextCommand(t, stream); cmd.ID != bridge.OpeningID("dispatch.d-id", "brief") {
		t.Fatalf("opening id = %q, want %q", cmd.ID, bridge.OpeningID("dispatch.d-id", "brief"))
	}
}

// Once a follow-up is queued, the orchestrator's request ending (its HTTP
// client gave up) does not make it a failure: the deliver stays queued and
// will run, so Inject reports it accepted. Reporting an error would close
// the turn as rejected and invite a resend that delivers it twice.
func TestBridgedInjectCallerGoneLeavesTheDeliverQueued(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: time.Second, SendTimeout: 200 * time.Millisecond})
	g.launch(t, "d-gone", "claude", "brief")
	g.connectAndAckOpening(t, "dispatch.d-gone")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_, _ = g.hub.WaitFor(context.Background(), "dispatch.d-gone", func(st bridge.State) bool { return st.Pending == 1 })
		cancel()
	}()
	if err := g.r.Inject(ctx, rigPane, "next", nil); err != nil {
		t.Fatalf("Inject = %v, want the queued deliver reported accepted", err)
	}
	if st := g.hub.State("dispatch.d-gone"); st.Pending != 1 {
		t.Fatalf("pending = %d, want the deliver still queued", st.Pending)
	}
}

// The bridge owns a dispatch's turn reports only while its mod is
// connected: a mod that died hands them back to the shell hooks.
func TestBridgeOwnsReportsOnlyWhileConnected(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.launch(t, "d-conn", "claude", "brief")
	stream := g.connectAndAckOpening(t, "dispatch.d-conn")
	if err := g.hub.Apply("dispatch.d-conn", bridge.Report{Type: bridge.ReportHello, SessionID: "s", ClaudeVersion: "2.1.289"}); err != nil {
		t.Fatal(err)
	}
	if !g.r.BridgeOwnsReports("d-conn") {
		t.Fatal("a connected bridge does not own reports")
	}
	stream.Close()
	if g.r.BridgeOwnsReports("d-conn") {
		t.Fatal("a bridge whose mod is gone still owns reports")
	}
	if _, err := g.hub.Connect("dispatch.d-conn"); err != nil {
		t.Fatal(err)
	}
	if !g.r.BridgeOwnsReports("d-conn") {
		t.Fatal("a reconnected bridge does not own reports")
	}
}

// After a fallback the killed bridged claude's late reports are refused, so
// no orphaned state for its key comes back.
func TestFallbackRefusesTheKilledClaudesLateReports(t *testing.T) {
	g := newBridgeRig(t, "2.1.289")
	g.r.SetBridge(InteractiveBridge{Hub: g.hub, Launcher: g.r.bridge.Launcher, ConnectTimeout: 30 * time.Millisecond})
	g.launch(t, "d-late", "claude", "brief")
	if _, _, err := g.r.AwaitOpening(context.Background(), rigPane); err != nil {
		t.Fatal(err)
	}
	err := g.hub.Apply("dispatch.d-late", bridge.Report{Type: bridge.ReportHello, SessionID: "s-dead", ClaudeVersion: "2.1.289"})
	if !errors.Is(err, bridge.ErrForgotten) {
		t.Fatalf("late hello: err=%v, want ErrForgotten", err)
	}
	if st := g.hub.State("dispatch.d-late"); st.SessionID != "" || st.Connected {
		t.Fatalf("orphaned state after fallback: %+v", st)
	}
}
