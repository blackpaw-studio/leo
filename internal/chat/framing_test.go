package chat

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestFramePromptText(t *testing.T) {
	got := FramePrompt(Delivery{Text: "what's on today?"})
	if got != "[Leo Chat] what's on today?" {
		t.Fatalf("got %q", got)
	}
}

func TestFramePromptVoiceUsesTranscriptAndAttachments(t *testing.T) {
	got := FramePrompt(Delivery{Transcript: "remind me at five", Voice: true, Attachments: []string{"/s/a/voice.m4a"}})
	want := "[Leo Chat] remind me at five\nAttachments: /s/a/voice.m4a"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestFramePromptVoiceTranscriptionFailed(t *testing.T) {
	got := FramePrompt(Delivery{Voice: true, TranscriptionFailed: true, Attachments: []string{"/v.m4a"}})
	if !strings.HasPrefix(got, "[Leo Chat] (voice note: transcription failed)") || !strings.Contains(got, "Attachments: /v.m4a") {
		t.Fatalf("got %q", got)
	}
}

func TestFramePromptReplyToTaskCardIsClipped(t *testing.T) {
	card := Message{Kind: KindTask, Task: "daily-news", RunID: "r-1", Text: strings.Repeat("é", 2000), TS: time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)}
	got := FramePrompt(Delivery{Text: "more on item 2", ReplyTo: &card})
	lines := strings.Split(got, "\n")
	if lines[0] != "[Leo Chat] more on item 2" {
		t.Fatalf("first line %q", lines[0])
	}
	prefix := "In reply to task daily-news run r-1 (2026-10-12T07:00:00Z): "
	if !strings.HasPrefix(lines[1], prefix) {
		t.Fatalf("reply line %q", lines[1])
	}
	if n := utf8.RuneCountInString(strings.TrimPrefix(got[strings.Index(got, prefix):], prefix)); n > MaxReplyCardRunes {
		t.Fatalf("card text not clipped: %d runes", n)
	}
}

func TestFramePromptReplyToPlainMessageQuotesIt(t *testing.T) {
	prev := Message{Kind: KindText, Direction: DirectionOut, Text: "Done.", TS: time.Date(2026, 10, 12, 7, 0, 0, 0, time.UTC)}
	got := FramePrompt(Delivery{Text: "thanks", ReplyTo: &prev})
	if !strings.Contains(got, "\nIn reply to (2026-10-12T07:00:00Z): Done.") {
		t.Fatalf("got %q", got)
	}
}

func TestFramePromptAttachmentsOnly(t *testing.T) {
	got := FramePrompt(Delivery{Attachments: []string{"/a.png", "/b.pdf"}})
	if got != "[Leo Chat] (attachments)\nAttachments: /a.png, /b.pdf" {
		t.Fatalf("got %q", got)
	}
}
