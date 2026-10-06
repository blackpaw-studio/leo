package service

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
)

// connectMod connects key's stream as the mod of the claude in the stub
// session does: under the launch token that session's environment carries.
// It retries while the hub has not opened that launch yet (a supervisor
// between launches, or not yet adopting), and returns the launch.
func connectMod(t *testing.T, hub *bridge.Hub, tmuxPath, key string) (*bridge.Stream, string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		launch := sessionEnv(t, tmuxPath, bridgemod.EnvLaunch)
		s, err := hub.Connect(key, launch)
		if err == nil {
			return s, launch
		}
		retry := launch == "" || errors.Is(err, bridge.ErrInvalidLaunch) || errors.Is(err, bridge.ErrForgotten) || errors.Is(err, bridge.ErrStaleLaunch) || errors.Is(err, bridge.ErrNotOpen)
		if !retry || time.Now().After(deadline) {
			t.Fatalf("Connect(%s, %q): %v", key, launch, err)
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

func ackCmd(t *testing.T, hub *bridge.Hub, key, launch string, cmd bridge.Command, ok bool) {
	t.Helper()
	if err := hub.Apply(key, launch, bridge.Report{Type: bridge.ReportAck, ID: cmd.ID, OK: ok, Error: map[bool]string{false: "dropped"}[ok]}); err != nil {
		t.Fatalf("ack %s: %v", cmd.ID, err)
	}
}

func helloAs(t *testing.T, hub *bridge.Hub, key, launch, session string) {
	t.Helper()
	if err := hub.Apply(key, launch, bridge.Report{Type: bridge.ReportHello, SessionID: session, ClaudeVersion: "2.1.289"}); err != nil {
		t.Fatalf("hello: %v", err)
	}
}

// drainCmds returns what s holds right now without waiting for more.
func drainCmds(s *bridge.Stream) []bridge.Command {
	var out []bridge.Command
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		cmd, err := s.Next(ctx)
		cancel()
		if err != nil {
			return out
		}
		out = append(out, cmd)
	}
}

// runsOf counts how many of handed carrying text the mod runs: the first
// command under each id; a repeat id is only re-acked.
func runsOf(handed []bridge.Command, text string) int {
	seen := map[string]bool{}
	runs := 0
	for _, c := range handed {
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if c.Text == text {
			runs++
		}
	}
	return runs
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

func storedRecord(home, name string) agentstore.Record {
	recs, _ := agentstore.Load(agentstore.FilePath(home))
	return recs[name]
}

func storedAck(home, name string) string { return storedRecord(home, name).OpeningAckedID }

func withBackoff(d time.Duration) func(*bridgeTestOpts) {
	return func(o *bridgeTestOpts) { o.backoff = d }
}

func withHome(home string) func(*bridgeTestOpts) {
	return func(o *bridgeTestOpts) { o.home = home }
}

// tmuxCalls counts the logged tmux calls of verb.
func tmuxCalls(logPath, verb string) int {
	n := 0
	for _, line := range strings.Split(loggedLines(logPath), "\n") {
		if strings.Contains(" "+line+" ", " "+verb+" ") {
			n++
		}
	}
	return n
}

// The opening is queued before tmux creates the session, so nothing sent to
// the new agent (a persistent task, a message) can overtake it.
func TestOpeningIsQueuedBeforeTheSessionExists(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	dir := filepath.Dir(tmuxPath)
	// new-session reports in, then holds until the test lets it go.
	script, _ := os.ReadFile(tmuxPath)
	held := strings.Replace(string(script), "new-session)\n",
		"new-session)\n    touch '"+dir+"/started'; while [ ! -f '"+dir+"/go' ]; do sleep 0.01; done\n", 1)
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

// A bridged launch records the opening it queued and the launch it went to
// before the session starts; the mod's ack records the conversation it
// landed in and settles the queued record.
func TestOpeningAckIsPersisted(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)

	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	cmd := nextCmd(t, s)
	if want := bridge.OpeningID("s-1", "the opening"); cmd.ID != want {
		t.Fatalf("opening id = %q, want %q (derived from its conversation and text)", cmd.ID, want)
	}
	rec := storedRecord(home, "alpha")
	if rec.OpeningAckedID != "" || rec.OpeningQueuedLaunch != launch || rec.OpeningQueuedID != cmd.ID {
		t.Fatalf("before the ack: %+v, want the opening queued for launch %s", rec, launch)
	}
	helloAs(t, f.hub, "alpha", launch, "s-1")
	ackCmd(t, f.hub, "alpha", launch, cmd, true)
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == cmd.ID })
	if rec := storedRecord(home, "alpha"); rec.OpeningQueuedLaunch != "" || rec.OpeningQueuedID != "" {
		t.Fatalf("the acked opening is still recorded as queued: %+v", rec)
	}
}

// An agent renamed while live keeps its launch: its opening's ack lands in
// the record under the name the agent has when it lands, not the one it
// had at launch.
func TestOpeningAckFollowsALiveRename(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	cmd := nextCmd(t, s)

	waitFor(t, "the agent to run", func() bool { return f.sv.EphemeralAgents()["alpha"].Status == "running" })
	if err := f.sv.RenameAgent("alpha", "beta"); err != nil {
		t.Fatal(err)
	}
	// agent.Manager.Rename re-keys the record once the supervisor has.
	if err := agentstore.Rename(home, "alpha", "beta", func(r agentstore.Record) agentstore.Record { r.Name = "beta"; return r }); err != nil {
		t.Fatal(err)
	}
	helloAs(t, f.hub, "alpha", launch, "s-1")
	ackCmd(t, f.hub, "alpha", launch, cmd, true)
	waitFor(t, "the ack under the new name", func() bool { return storedAck(home, "beta") == cmd.ID })
	if rec := storedRecord(home, "beta"); rec.OpeningQueuedID != "" {
		t.Fatalf("the renamed record still has the opening queued: %+v", rec)
	}
}

// A launch with no session flag starts a conversation whose id nobody knows
// yet: its opening is queued under its launch, and the ack records the
// session the mod's hello named, so a later --resume of that session knows
// the opening already ran there.
func TestOpeningAckRecordsTheConversationItLandedIn(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	spec.ClaudeArgs = []string{"--name", "alpha"}
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)

	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	cmd := nextCmd(t, s)
	if want := bridge.OpeningID(launch, "the opening"); cmd.ID != want {
		t.Fatalf("opening id = %q, want %q (scoped by the launch)", cmd.ID, want)
	}
	helloAs(t, f.hub, "alpha", launch, "s-new")
	ackCmd(t, f.hub, "alpha", launch, cmd, true)
	want := bridge.OpeningID("s-new", "the opening")
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == want })
}

// A restore that resumes no conversation (NoResume, after a poisoned
// transcript) starts a fresh one, which has not had its opening, whatever
// the record says an earlier conversation got.
func TestNoResumeRestoreDeliversTheOpeningDespiteAnEarlierAck(t *testing.T) {
	for _, stale := range []string{bridge.OpeningID("alpha", "the opening"), bridge.OpeningID("s-1", "the opening")} {
		t.Run(stale, func(t *testing.T) {
			tmuxPath, logPath := statefulTmux(t, "")
			home := t.TempDir()
			agentRecord(t, home, agentstore.Record{Name: "alpha"})
			if err := agentstore.SetOpeningAcked(home, "alpha", stale, ""); err != nil {
				t.Fatal(err)
			}
			spec := claudeSpec(t, "alpha")
			spec.ClaudeArgs = []string{"--name", "alpha"} // what RestoreAgents passes under NoResume
			spec.OpeningBriefPath = writeBrief(t, "the opening")
			f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
			waitForNewSessions(t, logPath, 1)

			s, _ := connectMod(t, f.hub, tmuxPath, "alpha")
			if cmd := nextCmd(t, s); cmd.Text != "the opening" {
				t.Fatalf("a fresh conversation got %+v, want its opening", cmd)
			}
		})
	}
}

// A resumed conversation gets its opening only if it is not the one the
// record says received it.
func TestResumeDeliversTheOpeningOnlyToAConversationWithoutIt(t *testing.T) {
	for _, tc := range []struct {
		name, acked string
		want        int
	}{
		{"the conversation that got it", bridge.OpeningID("s-1", "the opening"), 0},
		{"another conversation", bridge.OpeningID("s-0", "the opening"), 1},
		{"no record", "", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmuxPath, logPath := statefulTmux(t, "")
			home := t.TempDir()
			agentRecord(t, home, agentstore.Record{Name: "alpha"})
			if err := agentstore.SetOpeningAcked(home, "alpha", tc.acked, ""); err != nil {
				t.Fatal(err)
			}
			spec := claudeSpec(t, "alpha")
			spec.ClaudeArgs = []string{"--name", "alpha", "--resume", "s-1"}
			spec.OpeningBriefPath = writeBrief(t, "the opening")
			f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
			line := waitForNewSessions(t, logPath, 1)[0]
			if got := f.hub.State("alpha").Pending; got != tc.want {
				t.Fatalf("pending=%d, want %d", got, tc.want)
			}
			if strings.Contains(line, "$(cat") {
				t.Fatalf("a bridged launch put the brief on argv:\n%s", line)
			}
		})
	}
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

	s, _ := connectMod(t, f.hub, tmuxPath, "alpha")
	first := nextCmd(t, s)
	stale := make(chan error, 1)
	go func() {
		stale <- f.hub.Send(context.Background(), "alpha", bridge.Deliver("for the first claude", false))
	}()
	waitFor(t, "the stale message to queue", func() bool { return f.hub.State("alpha").Pending == 2 })
	endSession(t, tmuxPath)

	lines := waitForNewSessions(t, logPath, 2)
	if !strings.Contains(lines[1], "--plugin-dir") || strings.Contains(lines[1], "$(cat") {
		t.Fatalf("a launch whose mod connected relaunches bridged, the opening off argv:\n%s", lines[1])
	}
	if err := <-stale; !errors.Is(err, bridge.ErrForgotten) {
		t.Fatalf("the first launch's queued message: err=%v, want ErrForgotten", err)
	}
	s2, _ := connectMod(t, f.hub, tmuxPath, "alpha")
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
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), false)

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("a rejected opening must relaunch legacy with it on argv:\n%s", lines[1])
	}
}

// The mod's stream can drop between handing over the opening and its
// refusal landing (reports travel apart from the stream): the launch must
// still be abandoned for a legacy relaunch that carries the opening.
func TestRejectedOpeningFallsBackEvenWithTheStreamDown(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec)
	waitForNewSessions(t, logPath, 1)
	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	opening := nextCmd(t, s)
	s.Close()
	ackCmd(t, f.hub, "alpha", launch, opening, false)

	lines := waitForNewSessions(t, logPath, 2)
	if strings.Contains(lines[1], "--plugin-dir") || !strings.Contains(lines[1], claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("a rejected opening must relaunch legacy with it on argv:\n%s", lines[1])
	}
}

// transcripts points the supervisor's opening transcript lookups at dir
// (<conversation>.jsonl), checked every poll, for the rest of the test.
func transcripts(t *testing.T, poll time.Duration) string {
	t.Helper()
	dir := t.TempDir()
	origPath, origPoll := openingTranscriptPath, openingTranscriptPoll
	openingTranscriptPath = func(_, conversation string) (string, error) {
		return filepath.Join(dir, conversation+".jsonl"), nil
	}
	openingTranscriptPoll = poll
	t.Cleanup(func() { openingTranscriptPath, openingTranscriptPoll = origPath, origPoll })
	return dir
}

// prompted appends a user prompt of text to conversation's transcript in
// dir, as claude does when it submits it.
func prompted(t *testing.T, dir, conversation, text string) {
	t.Helper()
	line, err := json.Marshal(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}})
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(dir, conversation+".jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(append(line, '\n')); err != nil {
		t.Fatal(err)
	}
}

// An opening a legacy launch carries on argv counts as delivered to its
// conversation once the conversation's transcript shows it was prompted,
// not before (claude can sit at a dialog and never submit it), so a later
// bridged --resume of it does not deliver it again.
func TestArgvOpeningIsRecordedOnceItsTranscriptShowsIt(t *testing.T) {
	dir := transcripts(t, 10*time.Millisecond)
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = writeBrief(t, "the opening\n")
	_ = startBridged(t, tmuxPath, "2.1.286", time.Minute, spec, withHome(home))

	line := waitForNewSessions(t, logPath, 1)[0]
	if !strings.Contains(line, claudeharness.BriefArgvWord(spec.OpeningBriefPath)) {
		t.Fatalf("the legacy launch must carry the opening on argv:\n%s", line)
	}
	time.Sleep(100 * time.Millisecond)
	if got := storedAck(home, "alpha"); got != "" {
		t.Fatalf("recorded %q before the transcript showed the opening", got)
	}
	prompted(t, dir, "s-1", "the opening")
	want := bridge.OpeningID("s-1", "the opening\n")
	waitFor(t, "the argv delivery to be recorded", func() bool { return storedAck(home, "alpha") == want })
}

// A claude that ran the opening and died at once must not get it again
// when it is relaunched into the same conversation; one that died before
// submitting it must.
func TestAQuickExitRelaunchCarriesTheOpeningOnlyIfItNeverRan(t *testing.T) {
	for name, ran := range map[string]bool{"ran": true, "never ran": false} {
		t.Run(name, func(t *testing.T) {
			// Polled never: only the check at the launch's end can see it.
			dir := transcripts(t, time.Hour)
			tmuxPath, logPath := statefulTmux(t, "")
			home := t.TempDir()
			agentRecord(t, home, agentstore.Record{Name: "alpha"})
			spec := claudeSpec(t, "alpha")
			spec.OpeningBriefPath = writeBrief(t, "the opening")
			_ = startBridged(t, tmuxPath, "2.1.286", time.Minute, spec, withHome(home), withBackoff(time.Millisecond))
			waitForNewSessions(t, logPath, 1)
			if ran {
				prompted(t, dir, "s-1", "the opening")
			}
			endSession(t, tmuxPath)

			relaunch := waitForNewSessions(t, logPath, 2)[1]
			carries := strings.Contains(relaunch, claudeharness.BriefArgvWord(spec.OpeningBriefPath))
			if carries == ran {
				t.Fatalf("relaunch carries the opening = %v after it ran = %v:\n%s", carries, ran, relaunch)
			}
		})
	}
}

// A restarted daemon adopting a legacy session the previous one launched
// (its opening on argv) records the opening once the transcript shows it:
// the previous daemon may have died before it could.
func TestAdoptedLegacySessionRecordsTheOpeningItsTranscriptShows(t *testing.T) {
	dir := transcripts(t, 10*time.Millisecond)
	tmuxPath, logPath := statefulTmux(t, "")
	sessionUp(t, tmuxPath)
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	prompted(t, dir, "s-1", "the opening")
	_ = startBridged(t, tmuxPath, "2.1.289", time.Minute, adoptSpec(t, "the opening"), withHome(home))

	want := bridge.OpeningID("s-1", "the opening")
	waitFor(t, "the adopted session's opening to be recorded", func() bool { return storedAck(home, "alpha") == want })
	if n := len(newSessionLines(logPath)); n != 0 {
		t.Fatalf("adoption launched %d sessions", n)
	}
}

// adoptSpec is an agent the previous daemon launched bridged, resumed as a
// restore does.
func adoptSpec(t *testing.T, brief string) ProcessSpec {
	t.Helper()
	spec := claudeSpec(t, "alpha")
	spec.ClaudeArgs = []string{"--name", "alpha", "--resume", "s-1"}
	spec.Adopt = true
	spec.OpeningBriefPath = writeBrief(t, brief)
	return spec
}

// adoptedEnv is the environment of a session the previous daemon launched
// bridged under key alpha and launch launch-old.
const adoptedEnv = "LEO_BRIDGE_AGENT=alpha\nLEO_BRIDGE_LAUNCH=launch-old"

// sessionUp makes the stub tmux report a live session, as one that
// survived the previous daemon.
func sessionUp(t *testing.T, tmuxPath string) {
	t.Helper()
	if err := os.Remove(filepath.Join(filepath.Dir(tmuxPath), "dead")); err != nil {
		t.Fatal(err)
	}
}

// queuedFor records that launch queued the opening under id, as the
// previous daemon did before it died.
func queuedFor(t *testing.T, home, launch, id string) {
	t.Helper()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	if err := agentstore.SetOpeningQueued(home, "alpha", launch, id); err != nil {
		t.Fatal(err)
	}
}

// After a daemon restart, an adopted claude whose launch queued an opening
// that was never acked gets it queued again under the id its launch used,
// even though the restore's args name another scope; nothing is launched.
func TestAdoptRequeuesTheOpeningItsLaunchQueued(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, adoptedEnv)
	sessionUp(t, tmuxPath)
	home := t.TempDir()
	queuedFor(t, home, "launch-old", "open-from-the-launch")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, adoptSpec(t, "the opening"), withHome(home))

	s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
	cmd := nextCmd(t, s)
	if cmd.ID != "open-from-the-launch" || cmd.Text != "the opening" || !cmd.AsUser {
		t.Fatalf("adoption queued %+v, want the opening under its launch's id", cmd)
	}
	helloAs(t, f.hub, "alpha", launch, "s-1")
	ackCmd(t, f.hub, "alpha", launch, cmd, true)
	want := bridge.OpeningID("s-1", "the opening")
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == want })
	if n := len(newSessionLines(logPath)); n != 0 {
		t.Fatalf("adoption launched %d sessions", n)
	}
}

// What another launch queued is not the adopted launch's to deliver: it had
// none queued, or its opening rode argv.
func TestAdoptQueuesNothingAnotherLaunchQueued(t *testing.T) {
	tmuxPath, _ := statefulTmux(t, adoptedEnv)
	sessionUp(t, tmuxPath)
	home := t.TempDir()
	queuedFor(t, home, "launch-other", "open-other")
	f := startBridged(t, tmuxPath, "2.1.289", time.Minute, adoptSpec(t, "the opening"), withHome(home))

	waitFor(t, "adoption", func() bool { _, ok := f.sv.BridgeKey("alpha"); return ok })
	time.Sleep(50 * time.Millisecond)
	if got := f.hub.State("alpha").Pending; got != 0 {
		t.Fatalf("pending=%d, want nothing queued for the adopted launch", got)
	}
}

// An adopted session is live: however long its mod takes to come back (its
// reconnect backoff), and whatever it answers, it is never killed or
// relaunched. Its opening stays queued until the mod takes it.
func TestAdoptedSessionIsNeverKilled(t *testing.T) {
	for _, tc := range []struct {
		name string
		ok   bool
	}{{"mod reconnects late and acks", true}, {"mod refuses the opening", false}} {
		t.Run(tc.name, func(t *testing.T) {
			tmuxPath, logPath := statefulTmux(t, adoptedEnv)
			sessionUp(t, tmuxPath)
			home := t.TempDir()
			queuedFor(t, home, "launch-old", "open-from-the-launch")
			f := startBridged(t, tmuxPath, "2.1.289", 30*time.Millisecond, adoptSpec(t, "the opening"), withHome(home))

			waitFor(t, "adoption", func() bool { _, ok := f.sv.BridgeKey("alpha"); return ok })
			time.Sleep(200 * time.Millisecond) // well past the connect timeout
			if got := f.hub.State("alpha").Pending; got != 1 {
				t.Fatalf("pending=%d past the connect timeout, want the opening still queued", got)
			}
			s, launch := connectMod(t, f.hub, tmuxPath, "alpha")
			ackCmd(t, f.hub, "alpha", launch, nextCmd(t, s), tc.ok)
			time.Sleep(100 * time.Millisecond)
			if n := tmuxCalls(logPath, "kill-session") + len(newSessionLines(logPath)); n != 0 {
				b, _ := os.ReadFile(logPath)
				t.Fatalf("the adopted session was killed or relaunched; tmux log:\n%s", b)
			}
		})
	}
}

// The daemon dies after the mod took the opening but before its ack got
// through. The restarted daemon adopts the surviving session and queues the
// opening again under the same id; the mod, which already ran it, only
// re-acks. The opening runs exactly once and nothing is killed.
func TestDaemonRestartMidOpeningRunsItOnceWithoutAKill(t *testing.T) {
	tmuxPath, logPath := statefulTmux(t, "")
	home := t.TempDir()
	agentRecord(t, home, agentstore.Record{Name: "alpha"})
	brief := writeBrief(t, "the opening")
	spec := claudeSpec(t, "alpha")
	spec.OpeningBriefPath = brief
	before := startBridged(t, tmuxPath, "2.1.289", time.Minute, spec, withHome(home))
	waitForNewSessions(t, logPath, 1)

	// Every command the mod is handed, across both daemons.
	var handed []bridge.Command
	s, launch := connectMod(t, before.hub, tmuxPath, "alpha")
	opening := nextCmd(t, s)
	handed = append(handed, opening)
	before.die() // the ack never reaches it

	sessionsBefore, killsBefore := len(newSessionLines(logPath)), tmuxCalls(logPath, "kill-session")
	restored := spec
	restored.ClaudeArgs = []string{"--name", "alpha", "--resume", "s-1"}
	restored.Adopt = true
	after := startBridged(t, tmuxPath, "2.1.289", 30*time.Millisecond, restored, withHome(home))

	s2, launch2 := connectMod(t, after.hub, tmuxPath, "alpha")
	if launch2 != launch {
		t.Fatalf("the surviving session's mod reconnected as launch %q, want %q", launch2, launch)
	}
	again := nextCmd(t, s2)
	handed = append(handed, again)
	helloAs(t, after.hub, "alpha", launch2, "s-1")
	ackCmd(t, after.hub, "alpha", launch2, again, true)
	waitFor(t, "the ack to be recorded", func() bool { return storedAck(home, "alpha") == opening.ID })

	time.Sleep(100 * time.Millisecond) // past the connect timeout
	handed = append(handed, drainCmds(s2)...)
	// The mod runs a command once per id ($.store outlives the daemon); a
	// repeat under its id is only re-acked. The daemon must hand the opening
	// again (it never saw the ack) under the same id, so it runs once.
	if len(handed) != 2 {
		t.Fatalf("the mod was handed %d commands, want the opening and its re-queue: %+v", len(handed), handed)
	}
	if runs := runsOf(handed, "the opening"); runs != 1 {
		t.Fatalf("the opening ran %d times after the mod's dedup, want exactly once: %+v", runs, handed)
	}
	if n := len(newSessionLines(logPath)) - sessionsBefore; n != 0 {
		t.Fatalf("the restarted daemon launched %d sessions", n)
	}
	if n := tmuxCalls(logPath, "kill-session") - killsBefore; n != 0 {
		t.Fatalf("the restarted daemon killed the session %d times", n)
	}
}
