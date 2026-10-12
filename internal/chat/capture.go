package chat

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"slices"
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
	// chatPost: an assistant entry calling leo_chat_post.
	chatPost bool
	// at is Claude's entry timestamp; zero when absent.
	at time.Time
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
	Timestamp        string `json:"timestamp"`
	Message          struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"` // tool_use
}

// chatPostTool is leo_chat_post's tool name; Claude prefixes an MCP tool's
// name with mcp__<server>__.
const chatPostTool = "leo_chat_post"

func isChatPost(name string) bool {
	return name == chatPostTool || strings.HasSuffix(name, "__"+chatPostTool)
}

func parseEntry(raw []byte) (TranscriptEntry, bool) {
	var e rawEntry
	if err := json.Unmarshal(raw, &e); err != nil || e.IsMeta || e.IsCompactSummary || e.IsSidechain {
		return TranscriptEntry{}, false
	}
	text, onlyText, posted := contentText(e.Message.Content)
	switch e.Type {
	case "assistant":
		if strings.TrimSpace(text) == "" && !posted {
			return TranscriptEntry{}, false
		}
		return TranscriptEntry{kind: entryAssistant, text: text, chatPost: posted}, true
	case "user":
		// Tool results are user entries too; only typed text is a prompt.
		if !onlyText || strings.TrimSpace(text) == "" {
			return TranscriptEntry{}, false
		}
		if isInjected(text) {
			return TranscriptEntry{kind: entryInjected, text: text}, true
		}
		at, _ := time.Parse(time.RFC3339Nano, e.Timestamp)
		return TranscriptEntry{kind: entryPrompt, text: text, at: at}, true
	}
	return TranscriptEntry{}, false
}

// contentText joins a message's text blocks; onlyText is false when it
// carries any non-text block (tool_use, tool_result, image), and posted
// says one of those is a leo_chat_post call.
func contentText(raw json.RawMessage) (text string, onlyText, posted bool) {
	if len(raw) == 0 {
		return "", false, false
	}
	if raw[0] == '"' {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return "", false, false
		}
		return s, true, false
	}
	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return "", false, false
	}
	parts := make([]string, 0, len(blocks))
	onlyText = true
	for _, b := range blocks {
		if b.Type != "text" {
			onlyText = false
			posted = posted || (b.Type == "tool_use" && isChatPost(b.Name))
			continue
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n"), onlyText, posted
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
// turn owns the reply, the agent called leo_chat_post in that turn, or the
// reply is empty. A prompt not found yet waits CaptureWindow.
//
// The turn starts at the earliest prompt the pending captures matched: a
// capture still pending at this Stop had its prompt in the turn ending now,
// so a post made before a second chat prompt folded into that turn counts.
//
// after is the stamp of the last prompt occurrence an earlier call already
// answered; the returned time advances it. Identical prompts (two "ok"s)
// must answer distinct occurrences, so a capture only claims an unclaimed
// occurrence stamped after both its delivery and that mark.
func ResolveCaptures(entries []TranscriptEntry, captures []Capture, lastAssistant string, now, after time.Time) ([]CaptureOutcome, time.Time) {
	claimed := make(map[int]bool)
	starts := make([]int, len(captures))
	turn := -1
	mark := after
	for i, c := range captures {
		start := findPrompt(entries, c, normalize(c.Prompt), claimed, after)
		starts[i] = start
		if start < 0 {
			continue
		}
		claimed[start] = true
		if turn < 0 || start < turn {
			turn = start
		}
		if entries[start].at.After(mark) {
			mark = entries[start].at
		}
	}
	toolPosted := turn >= 0 && slices.ContainsFunc(entries[turn:], func(e TranscriptEntry) bool { return e.chatPost })
	out := make([]CaptureOutcome, len(captures))
	for i, c := range captures {
		out[i] = resolveCapture(entries, c, starts[i], lastAssistant, now, toolPosted)
	}
	return out, mark
}

// findPrompt returns the index of the unclaimed prompt entry c delivered:
// the earliest one containing prompt stamped at or after its delivery and
// after the mark, so neither an identical prompt from history nor one an
// earlier capture answered matches. A transcript without stamps falls back
// to the latest match.
func findPrompt(entries []TranscriptEntry, c Capture, prompt string, claimed map[int]bool, after time.Time) int {
	if prompt == "" {
		return -1
	}
	latestUnstamped := -1
	for i, e := range entries {
		if claimed[i] || e.kind != entryPrompt || !strings.Contains(normalize(e.text), prompt) {
			continue
		}
		if e.at.IsZero() {
			latestUnstamped = i
			continue
		}
		if !e.at.Before(c.DeliveredAt) && e.at.After(after) {
			return i
		}
	}
	return latestUnstamped
}

func resolveCapture(entries []TranscriptEntry, c Capture, start int, lastAssistant string, now time.Time, toolPosted bool) CaptureOutcome {
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
			if strings.TrimSpace(e.text) != "" {
				reply = e.text
			}
		}
	}
	// The chat turn is the transcript's current one, so the hook's own
	// final message is fresher than a possibly unflushed transcript.
	if strings.TrimSpace(lastAssistant) != "" {
		reply = lastAssistant
	}
	if toolPosted || strings.TrimSpace(reply) == "" {
		return CaptureOutcome{Capture: c, Action: CaptureDrop}
	}
	return CaptureOutcome{Capture: c, Action: CapturePost, Text: strings.TrimSpace(reply)}
}
