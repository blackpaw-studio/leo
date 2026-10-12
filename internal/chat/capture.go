package chat

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"time"
)

// CaptureWindow is how long a capture whose prompt has not shown up in the
// agent's transcript waits (queued behind a running turn) before it is
// dropped.
const CaptureWindow = 30 * time.Minute

// maxTranscriptTail bounds how much of a transcript's end is read at a
// Stop hook: the chat turn is always near the end.
const maxTranscriptTail = 8 << 20

// entryKind classifies a transcript entry for capture.
type entryKind int

const (
	entryPrompt    entryKind = iota // a submitted user prompt
	entryInjected                   // a harness injection (task-notification) or command echo
	entryAssistant                  // assistant text
)

// TranscriptEntry is one capture-relevant line of a Claude transcript.
type TranscriptEntry struct {
	kind entryKind
	text string
}

// harnessInjectionMarkers are the envelopes of prompts Claude Code submits
// on its own, which fold into the running turn (leo #216).
var harnessInjectionMarkers = []string{"task-notification"}

// commandEchoPrefixes start the user entries Claude writes for slash
// commands and their output; they are not conversational prompts.
var commandEchoPrefixes = []string{"<command-name>", "<command-message>", "<local-command-", "<system-reminder>"}

// ReadTranscript reads the capture-relevant entries from the tail of the
// Claude transcript at path. A missing or unreadable file reads as empty.
func ReadTranscript(path string) []TranscriptEntry {
	f, err := os.Open(path) // #nosec G304 -- the agent's own transcript, from its hook payload
	if err != nil {
		return nil
	}
	defer f.Close()
	skipFirst := false
	if info, err := f.Stat(); err == nil && info.Size() > maxTranscriptTail {
		if _, err := f.Seek(info.Size()-maxTranscriptTail, io.SeekStart); err != nil {
			return nil
		}
		skipFirst = true // the first line is cut
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 16<<20)
	var out []TranscriptEntry
	for sc.Scan() {
		if skipFirst {
			skipFirst = false
			continue
		}
		if e, ok := parseEntry(sc.Bytes()); ok {
			out = append(out, e)
		}
	}
	return out
}

type rawEntry struct {
	Type             string `json:"type"`
	IsMeta           bool   `json:"isMeta"`
	IsCompactSummary bool   `json:"isCompactSummary"`
	IsSidechain      bool   `json:"isSidechain"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func parseEntry(raw []byte) (TranscriptEntry, bool) {
	var e rawEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.IsMeta || e.IsCompactSummary || e.IsSidechain {
		return TranscriptEntry{}, false
	}
	text, onlyText := contentText(e.Message.Content)
	switch e.Type {
	case "assistant":
		if strings.TrimSpace(text) == "" {
			return TranscriptEntry{}, false
		}
		return TranscriptEntry{kind: entryAssistant, text: text}, true
	case "user":
		// Tool results are user entries too; only typed text is a prompt.
		if !onlyText || strings.TrimSpace(text) == "" {
			return TranscriptEntry{}, false
		}
		if isInjected(text) {
			return TranscriptEntry{kind: entryInjected, text: text}, true
		}
		return TranscriptEntry{kind: entryPrompt, text: text}, true
	}
	return TranscriptEntry{}, false
}

// contentText joins a message's text blocks; onlyText is false when it
// carries any non-text block (tool_use, tool_result, image).
func contentText(raw json.RawMessage) (text string, onlyText bool) {
	if len(raw) == 0 {
		return "", false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false
		}
		return s, true
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false
	}
	parts := make([]string, 0, len(blocks))
	onlyText = true
	for _, b := range blocks {
		if b.Type != "text" {
			onlyText = false
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), onlyText
}

func isInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, m := range harnessInjectionMarkers {
		if strings.HasPrefix(t, "<"+m+">") && strings.HasSuffix(t, "</"+m+">") {
			return true
		}
	}
	for _, p := range commandEchoPrefixes {
		if strings.HasPrefix(t, p) {
			return true
		}
	}
	return false
}

// CaptureAction is what a Stop hook does with one pending capture.
type CaptureAction int

const (
	// CaptureKeep: the prompt has not shown up yet; wait for a later Stop.
	CaptureKeep CaptureAction = iota
	// CaptureDrop: clear it without posting.
	CaptureDrop
	// CapturePost: post Text as the agent's reply, then clear it.
	CapturePost
)

// CaptureOutcome is the decision for one capture.
type CaptureOutcome struct {
	Capture Capture
	Action  CaptureAction
	Text    string
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// ResolveCaptures decides each pending capture (oldest first) at a Stop
// hook, from the transcript entries and the hook's last_assistant_message.
// A capture posts the final assistant text of the turn its prompt opened,
// unless a non-chat prompt superseded it, a later chat prompt in the same
// turn owns the reply, the agent already posted with leo_chat_post, or the
// reply is empty. A prompt not found yet waits CaptureWindow.
func ResolveCaptures(entries []TranscriptEntry, captures []Capture, lastAssistant string, now time.Time) []CaptureOutcome {
	prompts := make([]string, len(captures))
	for i, c := range captures {
		prompts[i] = normalize(c.Prompt)
	}
	// Identical prompts (two "ok"s) must answer distinct occurrences:
	// oldest first, each capture claims the latest occurrence not yet
	// claimed, so a second "ok" that has not run yet waits for its own.
	claimed := make(map[int]bool)
	out := make([]CaptureOutcome, len(captures))
	for i, c := range captures {
		out[i] = resolveCapture(entries, c, prompts[i], lastAssistant, now, claimed)
	}
	return out
}

func resolveCapture(entries []TranscriptEntry, c Capture, prompt string, lastAssistant string, now time.Time, claimed map[int]bool) CaptureOutcome {
	start := -1
	for i := len(entries) - 1; i >= 0; i-- {
		if !claimed[i] && entries[i].kind == entryPrompt && prompt != "" && strings.Contains(normalize(entries[i].text), prompt) {
			start = i
			claimed[i] = true
			break
		}
	}
	if start < 0 {
		if now.Sub(c.DeliveredAt) < CaptureWindow {
			return CaptureOutcome{Capture: c, Action: CaptureKeep}
		}
		return CaptureOutcome{Capture: c, Action: CaptureDrop}
	}
	var reply string
	for _, e := range entries[start+1:] {
		switch e.kind {
		case entryPrompt:
			// Another chat prompt in the same turn owns the reply; any
			// other prompt superseded this one. Either way, nothing here.
			return CaptureOutcome{Capture: c, Action: CaptureDrop}
		case entryAssistant:
			reply = e.text
		}
	}
	// The chat turn is the transcript's current one, so the hook's own
	// final message is fresher than a possibly unflushed transcript.
	if strings.TrimSpace(lastAssistant) != "" {
		reply = lastAssistant
	}
	if c.ToolPosted || strings.TrimSpace(reply) == "" {
		return CaptureOutcome{Capture: c, Action: CaptureDrop}
	}
	return CaptureOutcome{Capture: c, Action: CapturePost, Text: strings.TrimSpace(reply)}
}
