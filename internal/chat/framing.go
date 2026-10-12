package chat

import (
	"strings"
	"time"
	"unicode/utf8"
)

// PromptPrefix opens every prompt Leo Chat delivers into an agent.
const PromptPrefix = "[Leo Chat]"

// MaxReplyCardRunes caps the quoted text of the message a reply answers.
const MaxReplyCardRunes = 1500

// Delivery is what an inbound message becomes as a prompt.
type Delivery struct {
	Text                string
	Transcript          string
	Voice               bool
	TranscriptionFailed bool
	// Attachments are absolute paths on the daemon host.
	Attachments []string
	// ReplyTo is the message being answered, if any.
	ReplyTo *Message
}

// FramePrompt renders d as the user turn delivered into the agent:
//
//	[Leo Chat] <text or transcript>
//	Attachments: <abs paths>
//	In reply to task <name> run <run_id> (<ts>): <card text>
func FramePrompt(d Delivery) string {
	var b strings.Builder
	b.WriteString(PromptPrefix + " " + body(d))
	if len(d.Attachments) > 0 {
		b.WriteString("\nAttachments: " + strings.Join(d.Attachments, ", "))
	}
	if d.ReplyTo != nil {
		b.WriteString("\n" + replyLine(*d.ReplyTo))
	}
	return b.String()
}

func body(d Delivery) string {
	text := strings.TrimSpace(d.Text)
	if d.Voice {
		spoken := strings.TrimSpace(d.Transcript)
		if d.TranscriptionFailed || spoken == "" {
			spoken = "(voice note: transcription failed)"
		}
		if text != "" {
			return spoken + "\n" + text
		}
		return spoken
	}
	if text == "" && len(d.Attachments) > 0 {
		return "(attachments)"
	}
	return text
}

func replyLine(m Message) string {
	ts := m.TS.UTC().Format(time.RFC3339)
	quoted := clipRunes(strings.TrimSpace(m.Text), MaxReplyCardRunes)
	if m.Task != "" {
		return "In reply to task " + m.Task + " run " + m.RunID + " (" + ts + "): " + quoted
	}
	return "In reply to (" + ts + "): " + quoted
}

// clipRunes shortens s to at most max runes, marking a cut with an ellipsis.
func clipRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	r := []rune(s)
	return string(r[:max-1]) + "…"
}
