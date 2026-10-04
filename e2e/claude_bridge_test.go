//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// TestClaudeBridgeAgent drives one real bridged claude agent through the
// bridge's delivery paths: the opening prompt, a message queued behind a
// busy turn, an interrupt, a message too large for a tmux command, and a
// compaction.
func TestClaudeBridgeAgent(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	const name = "bridge-e2e"
	s.spawn(name, "Reply with exactly: OPENING-OK")
	s.awaitBridge(name, agent.BridgeConnected)
	tr := s.transcript(name)
	tr.await("the opening reply", said("OPENING-OK"))

	t.Run("deliver while busy", func(t *testing.T) {
		tr := tr.with(t)
		if code, body := s.message(name, `Use the Bash tool in the foreground (not in the background) to run exactly: python3 -c "import time; time.sleep(20)"   then reply with exactly: BUSY-DONE`, ""); code != http.StatusOK {
			t.Fatalf("first message: %d %s", code, body)
		}
		tr.await("the busy turn's tool call", ranTool("time.sleep(20)"))
		// The mod accepts a deliver only once the running turn ends, so the
		// second message is answered "queued, not yet accepted".
		code, body := s.message(name, "[message from e2e-peer] Reply with exactly: QUEUED-DONE", "e2e-peer")
		if code != http.StatusAccepted || !strings.Contains(body, "queued") {
			t.Fatalf("message to a busy agent: %d %s, want 202 queued", code, body)
		}
		busy := tr.await("the busy turn's reply", said("BUSY-DONE"))
		queued := tr.await("the queued message's reply", said("QUEUED-DONE"))
		if queued < busy {
			t.Fatalf("queued reply (line %d) came before the busy turn ended (line %d)", queued, busy)
		}
	})

	t.Run("interrupt", func(t *testing.T) {
		tr := tr.with(t)
		if code, body := s.message(name, `Use the Bash tool in the foreground (not in the background) to run exactly: python3 -c "import time; time.sleep(60)"   then reply with exactly: LONG-DONE`, ""); code != http.StatusOK {
			t.Fatalf("message: %d %s", code, body)
		}
		tr.await("the long turn's tool call", ranTool("time.sleep(60)"))
		if code, body := s.call(http.MethodPost, "/web/agent/"+name+"/interrupt", nil); code != http.StatusOK {
			t.Fatalf("interrupt: %d %s", code, body)
		}
		// An idle agent accepts a deliver at once: 200, not 202.
		if code, body := s.message(name, "Reply with exactly: AFTER-INTERRUPT", ""); code != http.StatusOK {
			t.Fatalf("message after the interrupt: %d %s, want it accepted at once", code, body)
		}
		tr.await("the reply after the interrupt", said("AFTER-INTERRUPT"))
		if tr.index(said("LONG-DONE")) >= 0 {
			t.Fatal("the interrupted turn ran to completion")
		}
	})

	t.Run("50KB message", func(t *testing.T) {
		tr := tr.with(t)
		pad := strings.Repeat("This is filler text that carries no instructions. ", 1000)[:50_000]
		code, body := s.message(name, pad+"\n\nIgnore the filler above. Reply with exactly: BIG-MESSAGE-OK", "")
		if code/100 != 2 {
			t.Fatalf("50KB message: %d %s", code, body)
		}
		tr.await("the 50KB message's reply", said("BIG-MESSAGE-OK"))
	})

	t.Run("compact", func(t *testing.T) {
		tr := tr.with(t)
		if code, body := s.call(http.MethodPost, "/web/agent/"+name+"/compact", nil); code/100 != 2 {
			t.Fatalf("compact: %d %s", code, body)
		}
		tr.await("a compaction", compacted)
	})

	// The mod acks a clear only once the session it ran in has ended, so a
	// clear that took must not read as refused.
	t.Run("clear", func(t *testing.T) {
		before := time.Now()
		if code, body := s.call(http.MethodPost, "/web/agent/"+name+"/clear", nil); code/100 != 2 {
			t.Fatalf("clear: %d %s", code, body)
		}
		if code, body := s.message(name, "Reply with exactly: AFTER-CLEAR-OK", ""); code/100 != 2 {
			t.Fatalf("message after the clear: %d %s", code, body)
		}
		s.transcriptAfter(tr, before).with(t).await("the reply in the cleared session", said("AFTER-CLEAR-OK"))
		if tr.index(said("AFTER-CLEAR-OK")) >= 0 {
			t.Fatal("the reply after the clear landed in the session the clear should have ended")
		}
		if out := s.output.String() + s.serviceLog(); strings.Contains(out, "clear of "+name) {
			t.Fatalf("the clear was reported failed:\n%s", out)
		}
	})
}

// TestClaudeBridgeModReloadMidStream reloads the mod (as saving its
// --plugin-dir does) while a message waits behind a running turn in the
// mod's hands: the message must run exactly once, and the reloaded mod must
// keep delivering.
func TestClaudeBridgeModReloadMidStream(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	const name = "bridge-e2e-reload"
	s.spawn(name, "Reply with exactly: OPENING-OK")
	s.awaitBridge(name, agent.BridgeConnected)
	tr := s.transcript(name)
	tr.await("the opening reply", said("OPENING-OK"))
	streams := s.bridgeStreams(name)
	if len(streams) != 1 {
		t.Fatalf("bridge streams before the reload = %v, want one", streams)
	}

	if code, body := s.message(name, `Use the Bash tool in the foreground (not in the background) to run exactly: python3 -c "import time; time.sleep(25)"   then reply with exactly: RELOAD-BUSY-DONE`, ""); code != http.StatusOK {
		t.Fatalf("busy message: %d %s", code, body)
	}
	tr.await("the busy turn's tool call", ranTool("time.sleep(25)"))
	const queued = "Reply with exactly: RELOAD-QUEUED-OK"
	if code, body := s.message(name, queued, ""); code != http.StatusAccepted {
		t.Fatalf("message to a busy agent: %d %s, want 202 queued", code, body)
	}
	s.reloadMod()
	reloaded := s.awaitReplacedBridgeStream(name, streams)

	busy := tr.await("the busy turn's reply", said("RELOAD-BUSY-DONE"))
	reply := tr.await("the queued message's reply", said("RELOAD-QUEUED-OK"))
	if reply < busy {
		t.Fatalf("queued reply (line %d) came before the busy turn ended (line %d)", reply, busy)
	}
	// Prompts run in order, so a duplicate of the queued message would run
	// before this one does.
	if code, body := s.message(name, "Reply with exactly: AFTER-RELOAD-OK", ""); code/100 != 2 {
		t.Fatalf("message after the reload: %d %s", code, body)
	}
	tr.await("the reply after the reload", said("AFTER-RELOAD-OK"))
	if n := tr.count(prompted(queued)); n != 1 {
		t.Fatalf("the queued message was prompted %d times across the reload, want once", n)
	}
	t.Logf("bridge streams: before the reload %v, after %v", streams, reloaded)
}

// TestClaudeBridgeDispatchOversizedOpening dispatches a brief larger than
// claude's argv allows; the bridge delivers it as the opening prompt.
func TestClaudeBridgeDispatchOversizedOpening(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	prompt := oversizedBuildLog(claudeharness.ArgvPromptLimit+4096, "BLD-7Q4X9") +
		"\nWhat is the build id on the last line of the build log above? Answer with the id only."
	code, body := s.call(http.MethodPost, "/api/dispatch", map[string]string{"template": bridgeTemplate, "prompt": prompt, "cwd": s.ws, "mode": "interactive"})
	if code/100 != 2 {
		t.Fatalf("dispatch: %d %s", code, body)
	}
	var started struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &started); err != nil || started.Data.ID == "" {
		t.Fatalf("dispatch response %s: %v", body, err)
	}
	id := started.Data.ID
	t.Cleanup(func() { s.call(http.MethodPost, "/api/dispatch/"+id+"/cancel", nil) })

	deadline := time.Now().Add(bridgeTurnTimeout)
	for time.Now().Before(deadline) {
		code, body := s.call(http.MethodGet, "/api/dispatch/wait?id="+url.QueryEscape(id+"#1")+"&timeout=30", nil)
		if code != http.StatusOK {
			t.Fatalf("wait: %d %s", code, body)
		}
		var out struct {
			Data []struct {
				Text      string `json:"text"`
				Outcome   string `json:"outcome"`
				Delivered bool   `json:"delivered"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &out); err != nil || len(out.Data) != 1 {
			t.Fatalf("wait response %s: %v", body, err)
		}
		if e := out.Data[0]; e.Outcome != "" {
			if e.Outcome != "finished" || !e.Delivered || !strings.Contains(e.Text, "BLD-7Q4X9") {
				t.Fatalf("turn 1 = %+v, want it finished naming build BLD-7Q4X9", e)
			}
			return
		}
	}
	t.Fatal("the oversized opening's turn never finished")
}

// TestClaudeBridgeDaemonRestartMidOpening kills the daemon (a crash, or a
// kickstart -k) after an agent's session started but before its mod acked
// the opening. The restarted daemon must adopt the live session, never kill
// it, and queue the opening again: it runs exactly once.
func TestClaudeBridgeDaemonRestartMidOpening(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	const name = "bridge-e2e-adopt"
	const opening = "Reply with exactly: ADOPT-OPENING-OK"
	started := time.Now()
	s.spawn(name, opening)
	pid := s.awaitPanePID(name)
	s.killService()
	if rec := s.stored(name); rec.OpeningQueuedID == "" || rec.OpeningAckedID != "" {
		t.Fatalf("the daemon died after the opening was acked (record %+v): nothing left to adopt mid-opening", rec)
	}

	s.startService()
	s.awaitBridge(name, agent.BridgeConnected)
	// Found by its prompt, not the record's session id: the restore resolves
	// that from the newest transcript in the (shared) workspace, which the
	// adopted claude has not written yet.
	tr := s.transcriptPrompted(opening, started)
	tr.await("the opening reply", said("ADOPT-OPENING-OK"))
	// Prompts run in order, so a second copy of the opening would run
	// before this one does.
	if code, body := s.message(name, "Reply with exactly: AFTER-ADOPT-OK", ""); code/100 != 2 {
		t.Fatalf("message after the adopt: %d %s", code, body)
	}
	tr.await("the reply after the adopt", said("AFTER-ADOPT-OK"))
	if n := s.promptedSince(opening, started); n != 1 {
		t.Fatalf("the opening was prompted %d times across the restart, want once", n)
	}
	if got := s.panePID(name); got != pid {
		t.Fatalf("the adopted session was relaunched: pane pid %s, was %s", got, pid)
	}
	if strings.Contains(s.serviceLog(), "relaunching without") {
		t.Fatal("the adopted session fell back from the bridge")
	}
	if rec := s.stored(name); rec.OpeningQueuedID != "" || rec.OpeningAckedID == "" {
		t.Fatalf("after the adopt the opening is not recorded acked: %+v", rec)
	}
}

// TestClaudeBridgeDaemonRestartCarriesAQueuedMessage kills the daemon
// while a message waits behind an agent's running turn. The message is in
// the agent's outbox, so the restarted daemon queues it again, under its
// own id, for the adopted session: it runs exactly once, whether the old
// daemon's mod still held it or not, and leaves the outbox once acked.
func TestClaudeBridgeDaemonRestartCarriesAQueuedMessage(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{})
	const name = "bridge-e2e-carry"
	started := time.Now()
	s.spawn(name, "Reply with exactly: CARRY-OPENING-OK")
	s.awaitBridge(name, agent.BridgeConnected)
	tr := s.transcript(name)
	tr.await("the opening reply", said("CARRY-OPENING-OK"))
	pid := s.panePID(name)

	if code, body := s.message(name, `Use the Bash tool in the foreground (not in the background) to run exactly: python3 -c "import time; time.sleep(30)"   then reply with exactly: CARRY-BUSY-DONE`, ""); code != http.StatusOK {
		t.Fatalf("busy message: %d %s", code, body)
	}
	tr.await("the busy turn's tool call", ranTool("time.sleep(30)"))
	const queued = "Reply with exactly: CARRY-QUEUED-OK"
	if code, body := s.message(name, queued, ""); code != http.StatusAccepted || !strings.Contains(body, "queued") {
		t.Fatalf("message to a busy agent: %d %s, want 202 queued", code, body)
	}
	s.killService()
	outboxPath := filepath.Join(s.home, "state", "outbox", name+".json")
	if data, err := os.ReadFile(outboxPath); err != nil || !strings.Contains(string(data), "CARRY-QUEUED-OK") {
		t.Fatalf("after the daemon died the queued message is not in the outbox: %q, %v", data, err)
	}

	s.startService()
	s.awaitBridge(name, agent.BridgeConnected)
	busy := tr.await("the busy turn's reply", said("CARRY-BUSY-DONE"))
	carried := tr.await("the queued message's reply", said("CARRY-QUEUED-OK"))
	if carried < busy {
		t.Fatalf("queued reply (line %d) came before the busy turn ended (line %d)", carried, busy)
	}
	// Prompts run in order, so a second copy of the queued message would
	// run before this one does.
	if code, body := s.message(name, "Reply with exactly: AFTER-CARRY-OK", ""); code/100 != 2 {
		t.Fatalf("message after the restart: %d %s", code, body)
	}
	tr.await("the reply after the restart", said("AFTER-CARRY-OK"))
	if n := s.promptedSince(queued, started); n != 1 {
		t.Fatalf("the queued message was prompted %d times across the restart, want once", n)
	}
	if got := s.panePID(name); got != pid {
		t.Fatalf("the adopted session was relaunched: pane pid %s, was %s", got, pid)
	}
	awaitGone(t, outboxPath, 30*time.Second)
}

// awaitGone waits for path to be removed.
func awaitGone(t *testing.T, path string, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	data, _ := os.ReadFile(path)
	t.Fatalf("%s still holds %s", path, data)
}

// TestClaudeBridgeAbsentFallback runs claude with a mod that loads but can
// never reach leo: the agent is relaunched without the bridge, its opening
// prompt back on argv, and messages fall back to the legacy path.
func TestClaudeBridgeAbsentFallback(t *testing.T) {
	s := newBridgeE2E(t, bridgeOptions{breakBridge: true})
	const name = "bridge-e2e-fallback"
	s.spawn(name, "Reply with exactly: FALLBACK-OK")
	tr := s.transcript(name)
	tr.await("the opening reply after the fallback", said("FALLBACK-OK"))

	rec := s.agent(name)
	if rec.Bridge != agent.BridgeAbsent {
		t.Fatalf("bridge = %q, want %q", rec.Bridge, agent.BridgeAbsent)
	}
	if rec.Restarts != 0 {
		t.Fatalf("the fallback counted %d restarts, want 0", rec.Restarts)
	}
	if !strings.Contains(s.serviceLog(), "relaunching without the leo bridge") {
		t.Fatal("the agent was not relaunched without the bridge")
	}
	if code, body := s.message(name, "Reply with exactly: LEGACY-MESSAGE-OK", ""); code != http.StatusOK {
		t.Fatalf("legacy message: %d %s", code, body)
	}
	tr.await("the legacy message's reply", said("LEGACY-MESSAGE-OK"))
}

// oversizedBuildLog is a plausible build log of at least size bytes whose
// last line names buildID. Filler that reads as an attention test makes
// the model balk; a log it is asked a question about does not.
func oversizedBuildLog(size int, buildID string) string {
	var b strings.Builder
	b.WriteString("Here is a build log for context:\n\n")
	for step := 1; b.Len() < size; step++ {
		fmt.Fprintf(&b, "2026-10-03T12:%02d:%02dZ step %05d compile package internal/pkg%03d ok (%d ms)\n", step/60%60, step%60, step, step%997, 40+step%300)
	}
	fmt.Fprintf(&b, "2026-10-03T13:00:00Z build id: %s finished\n", buildID)
	return b.String()
}
