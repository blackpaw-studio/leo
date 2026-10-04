package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// connectWhenOpen connects to key's stream once the current launch has
// opened it (a key between launches refuses with ErrForgotten).
func connectWhenOpen(t *testing.T, hub *bridge.Hub, key string) *bridge.Stream {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		s, err := hub.Connect(key)
		if err == nil {
			return s
		}
		if !errors.Is(err, bridge.ErrForgotten) || time.Now().After(deadline) {
			t.Fatalf("Connect(%s): %v", key, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func nextCmd(t *testing.T, s *bridge.Stream) bridge.Command {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd, err := s.Next(ctx)
	if err != nil {
		t.Fatalf("stream.Next: %v", err)
	}
	return cmd
}

func ackCmd(t *testing.T, hub *bridge.Hub, key string, cmd bridge.Command, ok bool) {
	t.Helper()
	if err := hub.Apply(key, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: ok, Error: map[bool]string{false: "dropped"}[ok]}); err != nil {
		t.Fatalf("ack %s: %v", cmd.ID, err)
	}
}

// endSession makes the stub tmux report the session gone, as a claude exit,
// once the stub has finished bringing it up (it logs new-session first).
func endSession(t *testing.T, tmuxPath string) {
	t.Helper()
	dead := filepath.Join(filepath.Dir(tmuxPath), "dead")
	waitFor(t, "the session to come up", func() bool { _, err := os.Stat(dead); return os.IsNotExist(err) })
	if err := os.WriteFile(dead, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// agentRecord stores name's agentstore record in home, as a spawn does.
func agentRecord(t *testing.T, home string, rec agentstore.Record) {
	t.Helper()
	if err := agentstore.Save(home, rec); err != nil {
		t.Fatal(err)
	}
}

func storedAck(home, name string) string {
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	return recs[name].OpeningAckedID
}

func withBackoff(d time.Duration) func(*bridgeTestOpts) {
	return func(o *bridgeTestOpts) { o.backoff = d }
}

func withHome(home string) func(*bridgeTestOpts) {
	return func(o *bridgeTestOpts) { o.home = home }
}

// The opening is queued before tmux creates the session, so nothing sent to
// the new agent (a persistent task, a message) can overtake it.
func TestOpeningIsQueuedBeforeTheSessionExists(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	dir := filepath.Dir(tmuxPath)
	// new-session reports in, then holds until the test lets it go.
	script, _ := os.ReadFile(tmuxPath)
	held := strings.Replace(string(script), "new-session) rm -f",
		"new-session) touch '"+dir+"/started'; while [ ! -f '"+dir+"/go' ]; do sleep 0.01; done; rm -f", 1)
	if err := os.WriteFile(tmuxPath, []byte(held), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.WriteFile(filepath.Join(dir, "go"), nil, 0o644) })
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)

	waitFor(t, "new-session", func() bool { _, err := os.Stat(filepath.Join(dir, "started")); return err == nil })
	if got := f.hub.State("alpha").Pending; got != 1 {
		t.Fatalf("pending=%d while tmux is still creating the session, want the opening queued already", got)
	}
	_ = os.WriteFile(filepath.Join(dir, "go"), nil, 0o644)
	waitForNewSessions(t, logPath, 1)
}

// The opening counts as delivered only once the mod acks it, and the ack is
// recorded on the agent so it survives a daemon restart.
func TestOpeningAckIsPersisted(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)

	s := connectWhenOpen(t, f.hub, "alpha")
	cmd := nextCmd(t, s)
	if want := bridge.OpeningID("s-1", "the opening"); cmd.ID != want {
		t.Fatalf("opening id = %q, want %q (derived from its conversation and text)", cmd.ID, want)
	}
	if got := storedAck(home, "alpha"); got != "" {
		t.Fatalf("recorded %q before any ack", got)
	}
	ackCmd(t, f.hub, "alpha", cmd, true)
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == cmd.ID })
}

// A launch that dies before the ack has not handled the opening: the next
// launch queues it again under the same id (the mod's dedup turns a repeat
// of one that did run into a bare re-ack), and nothing else the old launch
// had queued reaches the new one.
func TestUnackedOpeningIsRequeuedAfterACrash(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withBackoff(time.Millisecond))
	waitForNewSessions(t, logPath, 1)

	s := connectWhenOpen(t, f.hub, "alpha")
	first := nextCmd(t, s)
	stale := make(chan error, 1)
	go func() { stale <- f.hub.Send(context.Background(), "alpha", bridge.Deliver("for the first claude", false)) }()
	waitFor(t, "the stale message to queue", func() bool { return f.hub.State("alpha").Pending == 2 })
	endSession(t, tmuxPath)

	lines := waitForNewSessions(t, logPath, 2)
	if !strings.Contains(lines[1], "--plugin-dir") || strings.Contains(lines[1], "$(cat") {
		t.Fatalf("a launch whose mod connected relaunches bridged, the opening off argv:\n%s", lines[1])
	}
	if err := <-stale; !errors.Is(err, bridge.ErrForgotten) {
		t.Fatalf("the first launch's queued message: err=%v, want ErrForgotten", err)
	}
	s2 := connectWhenOpen(t, f.hub, "alpha")
	again := nextCmd(t, s2)
	if again.ID != first.ID || again.Text != "the opening" {
		t.Fatalf("relaunch queued %+v, want the opening again under id %s", again, first.ID)
	}
	if got := f.hub.State("alpha").Pending; got != 1 {
		t.Fatalf("relaunch has %d pending commands, want only the opening", got)
	}
}

// A bridged launch that ends before its mod ever connected (claude choking
// on --plugin-dir, say) must not crash-loop bridged: the next launch is
// legacy, the opening back on argv.
func TestBridgedLaunchThatDiesBeforeConnectingRelaunchesLegacy(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	_ = startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withBackoff(time.Millisecond))
	waitForNewSessions(t, logPath, 1)
	endSession(t, tmuxPath)

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], "-e LEO_BRIDGE_AGENT= ") {
		t.Fatalf("relaunch after a bridged launch that never connected must be legacy:\n%s", lines[1])
	}
	if !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("the legacy relaunch must carry the unacked opening on argv:\n%s", lines[1])
	}
}

// A mod that refuses the opening has not run it: the launch falls back to
// legacy with the opening on argv.
func TestRejectedOpeningFallsBackToLegacy(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)
	waitForNewSessions(t, logPath, 1)
	s := connectWhenOpen(t, f.hub, "alpha")
	ackCmd(t, f.hub, "alpha", nextCmd(t, s), false)

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("a rejected opening must relaunch legacy with it on argv:\n%s", lines[1])
	}
}

// A launch that starts a fresh conversation (a spawn or reset) has not run
// its opening, whatever an earlier conversation's record says.
func TestFreshConversationClearsAStaleAck(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	if err := agentstore.SetOpeningAcked(home, "alpha", "open-earlier-conversation"); err != nil {
		t.Fatal(err)
	}
	spec := claudeSpec(t, "alpha") // --session-id s-1: a fresh conversation
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)
	if got := f.hub.State("alpha").Pending; got != 1 {
		t.Fatalf("pending=%d, want the opening queued", got)
	}
	if got := storedAck(home, "alpha"); got != "" {
		t.Fatalf("stale ack %q kept for a fresh conversation", got)
	}
}

// adoptSpec is an agent the previous daemon launched bridged under key,
// resumed as a restore does.
func adoptSpec(t *testing.T, brief string) ProcessSpec {
	t.Helper()
	spec := claudeSpec(t, "alpha")
	spec.ClaudeArgs = []string{"--name", "alpha", "--resume", "s-1"}
	spec.Adopt = true
	spec.OpeningBriefPath = writeBrief(t, brief)
	return spec
}

// sessionUp makes the stub tmux report a live session, as one that
// survived the previous daemon.
func sessionUp(t *testing.T, tmuxPath string) {
	t.Helper()
	if err := os.Remove(filepath.Join(filepath.Dir(tmuxPath), "dead")); err != nil {
		t.Fatal(err)
	}
}

// After a daemon restart, an adopted claude whose opening was never acked
// gets it queued again under the same id, and the adoption waits for the
// mod like a launch does.
func TestAdoptRequeuesAnUnackedOpening(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha")
	sessionUp(t, tmuxPath)
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, adoptSpec(t, "the opening"), withHome(home))

	s := connectWhenOpen(t, f.hub, "alpha")
	cmd := nextCmd(t, s)
	if want := bridge.OpeningID("s-1", "the opening"); cmd.ID != want || cmd.Text != "the opening" {
		t.Fatalf("adoption queued %+v, want the opening under %s", cmd, want)
	}
	ackCmd(t, f.hub, "alpha", cmd, true)
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == cmd.ID })
	if n := len(newSessionLines(logPath)); n != 0 {
		t.Fatalf("adoption launched %d sessions", n)
	}
}

func TestAdoptSkipsAnAckedOpening(t *testing.T) {
	tmuxPath, _ := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha")
	sessionUp(t, tmuxPath)
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	_ = agentstore.SetOpeningAcked(home, "alpha", bridge.OpeningID("s-1", "the opening"))
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, adoptSpec(t, "the opening"), withHome(home))

	waitFor(t, "adoption", func() bool { _, ok := f.sv.BridgeKey("alpha"); return ok })
	time.Sleep(50 * time.Millisecond)
	if got := f.hub.State("alpha").Pending; got != 0 {
		t.Fatalf("an acked opening was queued again (pending=%d)", got)
	}
}

// An adopted claude whose mod never reconnects, with its opening unacked,
// falls back like a launch: killed and relaunched legacy, opening on argv.
func TestAdoptedSessionWhoseModNeverConnectsFallsBack(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "LEO_BRIDGE_AGENT=alpha")
	sessionUp(t, tmuxPath)
	spec := adoptSpec(t, "the opening")
	_ = startBridged(t, tmuxPath, "2.1.289", 50*time.Millisecond, spec)

	lines := waitForNewSessions(t, logPath, 1)
	if strings.Contains(lines[0], "--plugin-dir") || !strings.Contains(lines[0], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("fallback from the adopted session must be legacy with the opening on argv:\n%s", lines[0])
	}
}
