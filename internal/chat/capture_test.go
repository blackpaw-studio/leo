package chat

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// transcript builds a Claude JSONL transcript.
type transcript []string

func (tr transcript) user(text string) transcript {
	return append(tr, line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": text}}))
}

func (tr transcript) userBlocks(text string) transcript {
	return append(tr, line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": text}}}}))
}

func (tr transcript) meta(text string) transcript {
	return append(tr, line(map[string]any{"type": "user", "isMeta": true, "message": map[string]any{"role": "user", "content": text}}))
}

func (tr transcript) toolResult() transcript {
	return append(tr, line(map[string]any{"type": "user", "message": map[string]any{"role": "user", "content": []any{map[string]any{"type": "tool_result", "content": "ok"}}}}))
}

func (tr transcript) assistant(text string) transcript {
	return append(tr, line(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "text", "text": text}}}}))
}

func (tr transcript) toolUse() transcript {
	return append(tr, line(map[string]any{"type": "assistant", "message": map[string]any{"role": "assistant", "content": []any{map[string]any{"type": "tool_use", "name": "Bash"}}}}))
}

func (tr transcript) write(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "t.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(tr, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func line(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

var captureNow = time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)

func capture(id, prompt string) Capture {
	return Capture{MessageID: id, Agent: "assistant", Prompt: prompt, DeliveredAt: captureNow.Add(-time.Minute)}
}

func resolveOne(t *testing.T, tr transcript, c Capture, last string) CaptureOutcome {
	t.Helper()
	out := ResolveCaptures(ReadTranscript(tr.write(t)), []Capture{c}, last, captureNow)
	if len(out) != 1 {
		t.Fatalf("outcomes = %+v", out)
	}
	return out[0]
}

func TestCapturePostsFinalAssistantMessageOfTheChatTurn(t *testing.T) {
	p := "[Leo Chat] what's the weather"
	tr := transcript{}.user("earlier").assistant("earlier reply").
		user(p).assistant("Let me check.").toolUse().toolResult().assistant("Sunny, 21°C.")
	got := resolveOne(t, tr, capture("m1", p), "")
	if got.Action != CapturePost || got.Text != "Sunny, 21°C." {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCapturePrefersStopPayloadForTheCurrentTurn(t *testing.T) {
	p := "[Leo Chat] hi"
	tr := transcript{}.user(p).assistant("partial")
	got := resolveOne(t, tr, capture("m1", p), "Hello there!")
	if got.Action != CapturePost || got.Text != "Hello there!" {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCaptureMatchesWhitespaceNormalisedAndBlockContent(t *testing.T) {
	p := "[Leo Chat] line one\nline two"
	tr := transcript{}.userBlocks("[Leo Chat]  line one line two").assistant("ok")
	if got := resolveOne(t, tr, capture("m1", p), ""); got.Action != CapturePost {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCaptureSupersededByNonChatPromptPostsNothing(t *testing.T) {
	p := "[Leo Chat] summarise"
	tr := transcript{}.user(p).assistant("working").user("[message from web] deploy done").assistant("noted")
	if got := resolveOne(t, tr, capture("m1", p), "noted"); got.Action != CaptureDrop {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCaptureTaskNotificationMidTurnDoesNotSupersede(t *testing.T) {
	p := "[Leo Chat] run the build"
	tr := transcript{}.user(p).toolUse().toolResult().
		user("<task-notification>\n<task-id>b1</task-id>\n</task-notification>").assistant("Build passed.")
	got := resolveOne(t, tr, capture("m1", p), "Build passed.")
	if got.Action != CapturePost || got.Text != "Build passed." {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCaptureIgnoresMetaEntries(t *testing.T) {
	p := "[Leo Chat] hi"
	tr := transcript{}.user(p).meta("<local-command-caveat>x</local-command-caveat>").assistant("hello")
	if got := resolveOne(t, tr, capture("m1", p), ""); got.Action != CapturePost || got.Text != "hello" {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestCaptureNotYetSubmittedIsKeptThenExpires(t *testing.T) {
	tr := transcript{}.user("something else").assistant("reply")
	c := capture("m1", "[Leo Chat] queued behind a turn")
	if got := resolveOne(t, tr, c, "reply"); got.Action != CaptureKeep {
		t.Fatalf("fresh unmatched capture must wait: %+v", got)
	}
	c.DeliveredAt = captureNow.Add(-CaptureWindow - time.Second)
	if got := resolveOne(t, tr, c, "reply"); got.Action != CaptureDrop {
		t.Fatalf("stale unmatched capture must clear: %+v", got)
	}
}

func TestCaptureSkippedWhenToolPostedOrEmpty(t *testing.T) {
	p := "[Leo Chat] hi"
	c := capture("m1", p)
	c.ToolPosted = true
	if got := resolveOne(t, transcript{}.user(p).assistant("hello"), c, ""); got.Action != CaptureDrop {
		t.Fatalf("leo_chat_post since delivery must suppress the capture: %+v", got)
	}
	if got := resolveOne(t, transcript{}.user(p).toolUse(), capture("m1", p), ""); got.Action != CaptureDrop {
		t.Fatalf("an empty final message posts nothing: %+v", got)
	}
}

func TestCaptureTwoChatPromptsInOneTurnPostOnce(t *testing.T) {
	p1, p2 := "[Leo Chat] first", "[Leo Chat] second"
	tr := transcript{}.user(p1).assistant("thinking").user(p2).assistant("Both done.")
	out := ResolveCaptures(ReadTranscript(tr.write(t)), []Capture{capture("m1", p1), capture("m2", p2)}, "Both done.", captureNow)
	if len(out) != 2 || out[0].Action != CaptureDrop || out[1].Action != CapturePost || out[1].Text != "Both done." {
		t.Fatalf("outcomes = %+v", out)
	}
}

func TestCaptureUsesLastOccurrenceOfARepeatedPrompt(t *testing.T) {
	p := "[Leo Chat] ok"
	tr := transcript{}.user(p).assistant("first answer").user("other").assistant("x").user(p).assistant("second answer")
	if got := resolveOne(t, tr, capture("m1", p), ""); got.Action != CapturePost || got.Text != "second answer" {
		t.Fatalf("outcome = %+v", got)
	}
}

func TestReadTranscriptMissingFileIsEmpty(t *testing.T) {
	if got := ReadTranscript(filepath.Join(t.TempDir(), "missing.jsonl")); len(got) != 0 {
		t.Fatalf("entries = %+v", got)
	}
}
