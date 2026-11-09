package chat

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The leoterm incident of 2026-10-25: a voice message (chat id
// 01M5STEK3WPE06PNEG9QXBJ8M5) was answered only in the terminal. The
// fixture is the trimmed, sanitized transcript around it: the chat prompt
// at 15:21:48Z, a terminal answer and Stop at 15:22:33Z, then a leo-bridge
// dispatch notification opening a turn that ends at 15:25:45Z.
const (
	replayFixture = "testdata/capture_leoterm_voice.jsonl"
	replayPrompt  = "[Leo Chat] Which bug should we fix first?\n" +
		"Attachments: /home/user/.leo/state/chat/attachments/_unassigned/01M5STEK0R060V0HFHTARQFP3P/Voice note.m4a"
)

var replayCapture = Capture{
	MessageID: "01M5STEK3WPE06PNEG9QXBJ8M5", Agent: "leoterm", Prompt: replayPrompt,
	// The message's own stamp: delivery came after it, before the prompt.
	DeliveredAt: time.Date(2026, 10, 25, 15, 21, 46, 364e6, time.UTC),
}

// replayAsOf writes the fixture's lines stamped at or before stop (and the
// unstamped ones among them) and returns the entries a Stop hook then reads,
// with the final assistant text as its last_assistant_message.
func replayAsOf(t *testing.T, stop time.Time) ([]TranscriptEntry, string) {
	t.Helper()
	f, err := os.Open(replayFixture)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var kept []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		var e struct {
			Timestamp string `json:"timestamp"`
		}
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if at, err := time.Parse(time.RFC3339Nano, e.Timestamp); err == nil && at.After(stop) {
			break
		}
		kept = append(kept, sc.Text())
	}
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(kept, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	entries := ReadTranscript(p)
	last := ""
	for _, e := range entries {
		if e.kind == entryAssistant && strings.TrimSpace(e.text) != "" {
			last = e.text
		}
	}
	return entries, last
}

func TestCaptureReplayLeotermVoiceAtItsOwnStop(t *testing.T) {
	stop := time.Date(2026, 10, 25, 15, 22, 33, 442e6, time.UTC)
	entries, last := replayAsOf(t, stop)
	out, _ := ResolveCaptures(entries, []Capture{replayCapture}, last, stop, time.Time{})
	if out[0].Action != CapturePost || !strings.HasPrefix(out[0].Text, "The login bug, since it blocks new users.") {
		t.Fatalf("outcome = %+v, want the terminal answer posted", out[0])
	}
}

// The chat turn's own Stop never reached capture (its hook was dropped as
// another session's after a /clear). At the next Stop, the leo-bridge
// notification that opened the later turn must not supersede the capture.
func TestCaptureReplayLeotermVoiceAfterABridgeNotification(t *testing.T) {
	stop := time.Date(2026, 10, 25, 15, 25, 45, 845e6, time.UTC)
	entries, last := replayAsOf(t, stop)
	out, _ := ResolveCaptures(entries, []Capture{replayCapture}, last, stop, time.Time{})
	if out[0].Action != CapturePost || !strings.HasPrefix(out[0].Text, "**My recommendation:**") {
		t.Fatalf("outcome = %+v, want the reply posted past the bridge notification", out[0])
	}
}
