package service

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/bridge"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

const testLeoBin = "/opt/leo/bin/leo"

// statefulTmux writes a tmux stub that logs each call (one line per call,
// args joined by spaces) and models one session's life: new-session brings
// it up, kill-session takes it down, has-session and display-message's
// #{pane_dead} report it. show-environment prints showEnv, so adoption can
// recover a launch's bridge key.
func statefulTmux(t *testing.T, showEnv string) (tmuxPath, logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "tmux.log")
	dead := filepath.Join(dir, "dead")
	script := `#!/bin/sh
echo "$@" >> '` + logPath + `'
cmd="$1"
[ "$1" = "-L" ] && cmd="$3"
case "$cmd" in
  has-session) [ -f '` + dead + `' ] && exit 1; exit 0;;
  kill-session) touch '` + dead + `'; exit 0;;
  new-session) rm -f '` + dead + `'; echo '%7'; exit 0;;
  show-environment) [ -n '` + showEnv + `' ] && echo '` + showEnv + `'; exit 0;;
  display-message) [ -f '` + dead + `' ] && echo 1 || echo 0; exit 0;;
esac
exit 0
`
	tmuxPath = filepath.Join(dir, "tmux")
	if err := os.WriteFile(tmuxPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dead, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	return tmuxPath, logPath
}

// newSessionLines returns the logged tmux new-session invocations.
func newSessionLines(logPath string) []string {
	b, _ := os.ReadFile(logPath)
	var out []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.Contains(line, " new-session ") {
			out = append(out, line)
		}
	}
	return out
}

func waitForNewSessions(t *testing.T, logPath string, n int) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		lines := newSessionLines(logPath)
		if len(lines) >= n {
			return lines
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("saw %d new-session calls, want %d; tmux log:\n%s", len(lines), n, b)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type bridgeFixture struct {
	sv       *Supervisor
	hub      *bridge.Hub
	launcher *bridgemod.Launcher
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
}

// startBridged runs superviseProcess for spec on a supervisor wired to a
// bridge whose claude probes as version.
func startBridged(t *testing.T, tmuxPath, version string, connectTimeout time.Duration, spec ProcessSpec) *bridgeFixture {
	t.Helper()
	origPoll, origBackoff := sessionPollInterval, initialBackoff
	sessionPollInterval, initialBackoff = 10*time.Millisecond, time.Hour // a counted restart would stall the test
	t.Cleanup(func() { sessionPollInterval, initialBackoff = origPoll, origBackoff })

	ctx, cancel := context.WithCancel(context.Background())
	sv := NewSupervisor(ctx)
	sv.homePath = t.TempDir()
	sv.tmuxPath = tmuxPath
	hub := bridge.New(bridge.Options{})
	launcher := bridgemod.NewLauncher(bridgemod.LauncherOptions{
		StateDir:   filepath.Join(sv.homePath, "state"),
		LeoVersion: "vtest",
		LeoBin:     testLeoBin,
		Probe:      func(context.Context, string) (string, error) { return version + " (Claude Code)", nil },
		Log:        &bytes.Buffer{},
	})
	sv.SetBridge(hub, launcher, connectTimeout)
	id := newProcIdentity(spec.Name, spec.ClaudeArgs)
	sv.mu.Lock()
	sv.identities[spec.Name] = id
	sv.states[spec.Name] = &ProcessState{Name: spec.Name, Status: "starting", Ephemeral: true}
	sv.mu.Unlock()

	f := &bridgeFixture{sv: sv, hub: hub, launcher: launcher, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(f.done)
		superviseProcess(ctx, tmuxPath, "/fake/claude", spec, sv.homePath, sv, id)
	}()
	t.Cleanup(func() { cancel(); <-f.done; hub.Close() })
	return f
}

func claudeSpec(t *testing.T, name string) ProcessSpec {
	t.Helper()
	return ProcessSpec{
		Name:       name,
		ClaudeArgs: []string{"--session-id", "s-1", "--name", name},
		WorkDir:    t.TempDir(),
		Harness:    "claude",
		Kind:       harness.KindAgent,
	}
}

func writeBrief(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "brief")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// A claude new enough for the mods API launches with the mod loaded and the
// environment the mod reads; the key is routable by agent name.
func TestBridgedClaudeLaunchLoadsTheMod(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, claudeSpec(t, "alpha"))

	line := waitForNewSessions(t, logPath, 1)[0]
	for _, want := range []string{"-e LEO_BRIDGE_AGENT=alpha ", "-e LEO_BRIDGE_BIN=" + testLeoBin + " ", "'--plugin-dir' '"} {
		if !strings.Contains(line, want) {
			t.Errorf("new-session lacks %q:\n%s", want, line)
		}
	}
	plan, ok := f.launcher.Plan(context.Background(), "/fake/claude", "alpha")
	if !ok || !strings.Contains(line, "'--plugin-dir' '"+plan.PluginDir+"' '--session-id'") {
		t.Errorf("--plugin-dir must lead the args and name the materialized mod %q:\n%s", plan.PluginDir, line)
	}
	if key, ok := f.sv.BridgeKey("alpha"); !ok || key != "alpha" {
		t.Fatalf("BridgeKey(alpha) = %q, %v", key, ok)
	}
	// Launch-only: the stored args never carry the plugin dir.
	if args := strings.Join(f.sv.identities["alpha"].Args(), " "); strings.Contains(args, "--plugin-dir") {
		t.Fatalf("--plugin-dir leaked into the stored args: %s", args)
	}
}

// Below the mods API the launch is exactly the legacy one, and the bridge
// variables are blanked so an inherited value cannot point a mod anywhere.
func TestOldClaudeLaunchesLegacy(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	f := startBridged(t, tmuxPath, "2.1.286", time.Minute, claudeSpec(t, "alpha"))

	line := waitForNewSessions(t, logPath, 1)[0]
	if strings.Contains(line, "--plugin-dir") {
		t.Fatalf("legacy launch carries --plugin-dir:\n%s", line)
	}
	for _, want := range []string{"-e LEO_BRIDGE_AGENT= ", "-e LEO_BRIDGE_BIN= "} {
		if !strings.Contains(line, want) {
			t.Errorf("legacy launch does not blank %q:\n%s", want, line)
		}
	}
	if _, ok := f.sv.BridgeKey("alpha"); ok {
		t.Fatal("a legacy launch must not have a bridge key")
	}
}

func TestNonClaudeHarnessNeverBridges(t *testing.T) {
	testFakeDriver = &fakeHookDriver{}
	defer func() { testFakeDriver = nil }()
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.Harness = "fakehook"
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)
	line := waitForNewSessions(t, logPath, 1)[0]
	if strings.Contains(line, "--plugin-dir") || !strings.Contains(line, "-e LEO_BRIDGE_AGENT= ") {
		t.Fatalf("non-claude launch must be legacy with blanked bridge env:\n%s", line)
	}
	if _, ok := f.sv.BridgeKey("alpha"); ok {
		t.Fatal("non-claude launch has a bridge key")
	}
}

// The opening prompt goes over the bridge as the agent's first user prompt,
// never on argv.
func TestBridgedOpeningPromptIsQueuedNotOnArgv(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	brief := "do the thing\nwith 'quotes' and $(no expansion)"
	spec.OpeningBriefPath = writeBrief(t, brief)
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)

	line := waitForNewSessions(t, logPath, 1)[0]
	if strings.Contains(line, "$(cat") {
		t.Fatalf("bridged launch still puts the brief on argv:\n%s", line)
	}
	stream, err := f.hub.Connect("alpha")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := stream.Next(ctx)
	if err != nil {
		t.Fatalf("no opening command queued: %v", err)
	}
	if cmd.Op != bridge.OpDeliver || cmd.Text != brief || !cmd.AsUser {
		t.Fatalf("opening command = %+v, want a user deliver of the brief verbatim", cmd)
	}
}

// If the mod never connects, nothing has run yet: the agent is relaunched
// the legacy way at once, its opening prompt back on argv, without counting
// a restart or waiting out a backoff.
func TestBridgedOpeningFallsBackToLegacyWhenTheModNeverConnects(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", 50*time.Millisecond, spec)

	lines := waitForNewSessions(t, logPath, 2)
	if !strings.Contains(lines[0], "--plugin-dir") || strings.Contains(lines[0], "$(cat") {
		t.Fatalf("first launch should be bridged without the brief on argv:\n%s", lines[0])
	}
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], "-e LEO_BRIDGE_AGENT= ") {
		t.Fatalf("relaunch must be legacy:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("legacy relaunch must carry the opening brief on argv:\n%s", lines[1])
	}
	if st := f.hub.State("alpha"); st.Pending != 0 {
		t.Fatalf("the queued opening must be forgotten, pending=%d", st.Pending)
	}
	f.sv.mu.RLock()
	restarts := f.sv.states["alpha"].Restarts
	f.sv.mu.RUnlock()
	if restarts != 0 {
		t.Fatalf("fallback counted %d restarts, want 0", restarts)
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(newSessionLines(logPath)); n != 2 {
		t.Fatalf("%d launches, want exactly the bridged one and its legacy fallback", n)
	}
	if _, ok := f.sv.BridgeKey("alpha"); ok {
		t.Fatal("the legacy relaunch must clear the bridge key")
	}
}

// The bridged claude may already have written its --session-id transcript
// (startup hook output, the mod's own log lines), and claude refuses a
// --session-id whose transcript exists. The legacy relaunch then resumes
// that session instead of crash-looping into a counted restart.
func TestBridgeFallbackResumesASessionTheBridgedLaunchWrote(t *testing.T) {
	for _, tc := range []struct {
		name    string
		written bool
		want    string
	}{
		{"transcript written", true, "'--resume' 's-1'"},
		{"no transcript", false, "'--session-id' 's-1'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			orig := bridgedSessionWritten
			var asked []string
			var mu sync.Mutex
			bridgedSessionWritten = func(cwd, id string) bool {
				mu.Lock()
				asked = append(asked, cwd+"|"+id)
				mu.Unlock()
				return tc.written
			}
			t.Cleanup(func() { bridgedSessionWritten = orig })
			tmuxPath, logPath := statefulTmux(t, "")
			spec := claudeSpec(t, "alpha")
			spec.OpeningBriefPath = writeBrief(t, "the opening")
			f := startBridged(t, tmuxPath, "2.1.289", 50*time.Millisecond, spec)

			lines := waitForNewSessions(t, logPath, 2)
			if !strings.Contains(lines[1], tc.want) || strings.Count(lines[1], "'s-1'") != 1 {
				t.Fatalf("legacy relaunch should carry %s once:\n%s", tc.want, lines[1])
			}
			mu.Lock()
			gotAsked := append([]string(nil), asked...)
			mu.Unlock()
			if len(gotAsked) == 0 || gotAsked[0] != spec.WorkDir+"|s-1" {
				t.Fatalf("transcript lookups = %v, want the workspace and s-1", gotAsked)
			}
			f.sv.mu.RLock()
			idn := f.sv.identities["alpha"]
			f.sv.mu.RUnlock()
			if args := strings.Join(idn.Args(), " "); !strings.Contains(args, strings.ReplaceAll(tc.want, "'", "")) {
				t.Fatalf("stored args %q do not carry %s", args, tc.want)
			}
			f.sv.mu.RLock()
			restarts := f.sv.states["alpha"].Restarts
			f.sv.mu.RUnlock()
			if restarts != 0 {
				t.Fatalf("fallback counted %d restarts, want 0", restarts)
			}
		})
	}
}

// A bridge that connects in time keeps the bridged launch.
func TestBridgedOpeningKeepsALaunchWhoseModConnects(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", 300*time.Millisecond, spec)
	waitForNewSessions(t, logPath, 1)
	if _, err := f.hub.Connect("alpha"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	if n := len(newSessionLines(logPath)); n != 1 {
		t.Fatalf("%d launches; a connected bridge must not be relaunched", n)
	}
}

// Without an opening prompt there is nothing to rescue: a mod that never
// connects leaves the agent running (call sites fall back to tmux).
func TestBridgedLaunchWithoutOpeningIsNotRelaunched(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	_ = startBridged(t, tmuxPath, "2.1.289", 50*time.Millisecond, claudeSpec(t, "alpha"))
	waitForNewSessions(t, logPath, 1)
	time.Sleep(300 * time.Millisecond)
	if n := len(newSessionLines(logPath)); n != 1 {
		t.Fatalf("%d launches, want 1", n)
	}
}

// An opening too large for argv can only reach a legacy claude by paste.
func TestLegacyLaunchPastesAnOversizedOpening(t *testing.T) {
	var (
		mu     sync.Mutex
		pasted []string
	)
	orig := pasteOpeningPrompt
	pasteOpeningPrompt = func(_ context.Context, _ string, session, body string) error {
		mu.Lock()
		defer mu.Unlock()
		pasted = append(pasted, session+"\x00"+body)
		return nil
	}
	defer func() { pasteOpeningPrompt = orig }()

	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	big := strings.Repeat("x", claudeharness.ArgvPromptLimit+1)
	spec.OpeningBriefPath = writeBrief(t, big)
	_ = startBridged(t, tmuxPath, "2.1.286", time.Minute, spec)

	line := waitForNewSessions(t, logPath, 1)[0]
	if strings.Contains(line, "$(cat") {
		t.Fatalf("an oversized brief must not go on argv:\n%s", line)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		mu.Lock()
		got := append([]string(nil), pasted...)
		mu.Unlock()
		if len(got) == 1 {
			if got[0] != "leo-alpha\x00"+big {
				t.Fatalf("pasted %d bytes into the wrong place or altered", len(got[0]))
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pasted %d times, want 1", len(got))
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// After a daemon restart the surviving claude keeps the key it was launched
// with; adoption reads it back from the session environment.
func TestAdoptRecoversTheBridgeKey(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha.0a1b2c")
	origHas := tmuxHasSession
	tmuxHasSession = func(_, _ string) bool { return true }
	// Registered before startBridged so it runs after the supervisor stops.
	t.Cleanup(func() { tmuxHasSession = origHas })
	spec := claudeSpec(t, "alpha")
	spec.Adopt = true
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)

	deadline := time.Now().Add(5 * time.Second)
	for {
		if key, ok := f.sv.BridgeKey("alpha"); ok {
			if key != "alpha.0a1b2c" {
				t.Fatalf("BridgeKey = %q, want the launch-time key", key)
			}
			break
		}
		if time.Now().After(deadline) {
			b, _ := os.ReadFile(logPath)
			t.Fatalf("adoption never recovered the key; tmux log:\n%s", b)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if n := len(newSessionLines(logPath)); n != 0 {
		t.Fatalf("adoption launched %d sessions", n)
	}
}

func TestAdoptIgnoresAnUnsetOrInvalidKey(t *testing.T) {
	for _, env := range []string{"", "-LEO_BRIDGE_AGENT", "LEO_BRIDGE_AGENT=", "LEO_BRIDGE_AGENT=bad key"} {
		t.Run(env, func(t *testing.T) {
			tmuxPath, _ := statefulTmux(t, env)
			if key, ok := tmuxSessionBridgeKey(tmuxPath, "leo-alpha"); ok {
				t.Fatalf("recovered key %q from %q", key, env)
			}
		})
	}
}

// A later agent may take a renamed agent's old name while the renamed one's
// claude still connects under that name: the newcomer gets a distinct key.
func TestBridgeKeysStayUniqueAcrossRenames(t *testing.T) {
	sv := NewSupervisor(context.Background())
	renamed := newProcIdentity("beta", nil)
	renamed.setBridgeKey("alpha")
	newcomer := newProcIdentity("alpha", nil)
	sv.identities["beta"] = renamed
	sv.identities["alpha"] = newcomer

	key := sv.allocBridgeKey("alpha", newcomer)
	if key == "alpha" || !strings.HasPrefix(key, "alpha.") {
		t.Fatalf("allocBridgeKey = %q, want alpha.<suffix>", key)
	}
	fresh := newProcIdentity("gamma", nil)
	sv.identities["gamma"] = fresh
	if got := sv.allocBridgeKey("gamma", fresh); got != "gamma" {
		t.Fatalf("an uncontested name keys as itself, got %q", got)
	}
	// A relaunch of the same identity keeps its key, so commands queued
	// for it while its claude was down still reach the next one.
	if got := sv.allocBridgeKey("beta", renamed); got != "alpha" {
		t.Fatalf("relaunch of the renamed identity keyed %q, want its own alpha", got)
	}
	newcomer.setBridgeKey(key)
	if again := sv.allocBridgeKey("alpha", newcomer); again != key {
		t.Fatalf("relaunch key = %q, want %q", again, key)
	}
}

// Stopping an agent forgets its bridge: queued commands are dropped and
// their senders released.
func TestStopForgetsTheBridge(t *testing.T) {
	tmuxPath, _ := statefulTmux(t, "")
	sv := NewSupervisor(context.Background())
	sv.tmuxPath = tmuxPath
	hub := bridge.New(bridge.Options{})
	defer hub.Close()
	sv.SetBridge(hub, nil, time.Minute)
	id := newProcIdentity("alpha", nil)
	id.setBridgeKey("alpha.0a1b2c")
	sv.identities["alpha"] = id
	sv.states["alpha"] = &ProcessState{Name: "alpha", Status: "running", Ephemeral: true}
	sent := make(chan error, 1)
	go func() { sent <- hub.Send(context.Background(), "alpha.0a1b2c", bridge.Deliver("hi", false)) }()
	if _, err := hub.WaitFor(context.Background(), "alpha.0a1b2c", func(s bridge.State) bool { return s.Pending == 1 }); err != nil {
		t.Fatal(err)
	}

	if err := sv.stopAgentProcess("alpha"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-sent:
		if !errors.Is(err, bridge.ErrForgotten) {
			t.Fatalf("Send err=%v, want ErrForgotten", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not release the queued sender")
	}
}

func TestBridgeKeyForSession(t *testing.T) {
	sv := NewSupervisor(context.Background())
	id := newProcIdentity("alpha", nil)
	id.setBridgeKey("alpha")
	sv.identities["alpha"] = id
	if key, ok := sv.BridgeKeyForSession(id.SessionName()); !ok || key != "alpha" {
		t.Fatalf("BridgeKeyForSession = %q, %v", key, ok)
	}
	if _, ok := sv.BridgeKeyForSession("leo-nobody"); ok {
		t.Fatal("resolved an unknown session")
	}
}

// Daemon boot wires one hub into the supervisor and plans launches with the
// running leo binary and a mod materialized under the leo home's state dir.
func TestWireBridge(t *testing.T) {
	home := t.TempDir()
	sv := NewSupervisor(context.Background())
	probe := func(context.Context, string) (string, error) { return "2.1.289 (Claude Code)", nil }
	hub, launcher := wireBridge(sv, home, "v9.9.9", probe)
	defer hub.Close()
	if sv.BridgeRouter() == nil || sv.BridgeRouter().Hub != hub {
		t.Fatal("supervisor not wired to the returned hub")
	}
	plan, ok := launcher.Plan(context.Background(), "/fake/claude", "alpha")
	if !ok {
		t.Fatal("wired launcher refused a capable claude")
	}
	if !strings.HasPrefix(plan.PluginDir, filepath.Join(home, "state", "mods", "leo-bridge", "v9.9.9-")) {
		t.Fatalf("mod materialized at %q, want under the home's state dir", plan.PluginDir)
	}
	exe, _ := os.Executable()
	if plan.Env[bridgemod.EnvBin] != exe {
		t.Fatalf("LEO_BRIDGE_BIN = %q, want the running executable %q", plan.Env[bridgemod.EnvBin], exe)
	}
	// An unversioned dev build still gets a mod directory.
	_, dev := wireBridge(NewSupervisor(context.Background()), t.TempDir(), "", probe)
	if _, ok := dev.Plan(context.Background(), "/fake/claude", "alpha"); !ok {
		t.Fatal("a build without a version must still bridge")
	}
}
