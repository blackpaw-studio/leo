package chat

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"regexp"
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
	entryInjected                   // a harness or plugin injection (task or dispatch notification) or command echo
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

// pluginMessagePrefix opens a prompt a plugin sent on its own (a leo-bridge
// dispatch notification), for transcripts that predate the origin record.
var pluginMessagePrefix = regexp.MustCompile(`^The \S+ plugin sent a message:`)

// promptOrigin is Claude's record of who submitted a user entry.
type promptOrigin struct {
	Kind   string `json:"kind"`
	AsUser bool   `json:"asUser"`
}

// injected reports whether the harness or a plugin submitted the prompt on
// its own. A plugin prompt sent as the user (Leo Chat's own delivery) is a
// real prompt.
func (o *promptOrigin) injected() bool {
	if o == nil {
		return false
	}
	return o.Kind == "task-notification" || (o.Kind == "plugin" && !o.AsUser)
}

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
	Type             string        `json:"type"`
	IsMeta           bool          `json:"isMeta"`
	IsCompactSummary bool          `json:"isCompactSummary"`
	IsSidechain      bool          `json:"isSidechain"`
	Timestamp        string        `json:"timestamp"`
	Origin           *promptOrigin `json:"origin"`
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
		if e.Origin.injected() || isInjected(text) || (e.Origin == nil && pluginMessagePrefix.MatchString(strings.TrimSpace(text))) {
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
	// Reason says why, for the log.
	Reason string
}

// Why a capture was decided as it was.
const (
	reasonNotYet      = "prompt not in the transcript yet"
	reasonExpired     = "prompt never showed up in the transcript within the capture window"
	reasonChatOwns    = "a later chat prompt owns the reply"
	reasonToolPosted  = "the agent answered with leo_chat_post"
	reasonEmpty       = "no reply text"
	reasonTurnEnd     = "final text of the turn"
	reasonSuperseded  = "text before a superseding prompt"
	reasonSupersededE = "superseded by a later prompt with no reply text before it"
)

// Decision names what was done with the capture: pending, posted,
// suppressed (the agent posted itself) or dropped.
func (o CaptureOutcome) Decision() string {
	switch {
	case o.Action == CaptureKeep:
		return "pending"
	case o.Action == CapturePost:
		return "posted"
	case o.Reason == reasonToolPosted:
		return "suppressed"
	default:
		return "dropped"
	}
}

func normalize(s string) string { return strings.Join(strings.Fields(s), " ") }

// ResolveCaptures decides each pending capture (oldest first) at a Stop
// hook, from the transcript entries and the hook's last_assistant_message.
// A capture posts the final assistant text of the turn its prompt opened,
// unless a later chat prompt in the same turn owns the reply, the agent
// called leo_chat_post in that turn, or the reply is empty. A non-chat
// prompt that superseded it (its own Stop never reached capture) ends the
// reply: the text before that prompt posts. A prompt not found yet waits
// CaptureWindow.
//
// A leo_chat_post suppresses a capture only within its own span: from the
// earliest prompt the pending captures matched (chat prompts folded into
// one turn share it, so a post before the second one counts) to the first
// non-chat prompt. A boundary prompt before the capture starts its span
// there, so a post in a superseded or later turn answers that turn instead.
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
	out := make([]CaptureOutcome, len(captures))
	for i, c := range captures {
		toolPosted := starts[i] >= 0 && postedInSpan(entries, turn, starts[i], claimed)
		out[i] = resolveCapture(entries, c, starts[i], claimed, lastAssistant, now, toolPosted)
	}
	return out, mark
}

// isBoundary reports whether entries[i] is a prompt that is not a chat
// delivery: it ends one conversational span and starts the next.
func isBoundary(entries []TranscriptEntry, i int, claimed map[int]bool) bool {
	e := entries[i]
	return e.kind == entryPrompt && !claimed[i] && !isChatPrompt(e.text)
}

// postedInSpan reports whether a leo_chat_post falls in the span of the
// capture whose prompt is entries[start]: from the last boundary before it
// (but not before turn) to the first boundary after it.
func postedInSpan(entries []TranscriptEntry, turn, start int, claimed map[int]bool) bool {
	lo := turn
	for j := start - 1; j >= turn; j-- {
		if isBoundary(entries, j, claimed) {
			lo = j + 1
			break
		}
	}
	hi := len(entries)
	for j := start + 1; j < len(entries); j++ {
		if isBoundary(entries, j, claimed) {
			hi = j
			break
		}
	}
	return slices.ContainsFunc(entries[lo:hi], func(e TranscriptEntry) bool { return e.chatPost })
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

func resolveCapture(entries []TranscriptEntry, c Capture, start int, claimed map[int]bool, lastAssistant string, now time.Time, toolPosted bool) CaptureOutcome {
	if start < 0 {
		if now.Sub(c.DeliveredAt) < CaptureWindow {
			return CaptureOutcome{Capture: c, Action: CaptureKeep, Reason: reasonNotYet}
		}
		return CaptureOutcome{Capture: c, Action: CaptureDrop, Reason: reasonExpired}
	}
	var reply string
	for i, e := range entries[start+1:] {
		switch e.kind {
		case entryPrompt:
			if claimed[start+1+i] || isChatPrompt(e.text) {
				// Another chat prompt in the same turn owns the reply.
				return CaptureOutcome{Capture: c, Action: CaptureDrop, Reason: reasonChatOwns}
			}
			// Superseded: what the agent said before the new prompt is
			// the reply; the rest belongs to the later turn.
			return replyOutcome(c, reply, toolPosted, reasonSuperseded, reasonSupersededE)
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
	return replyOutcome(c, reply, toolPosted, reasonTurnEnd, reasonEmpty)
}

// replyOutcome posts reply (for posted) unless the agent already answered
// with leo_chat_post or said nothing (for empty).
func replyOutcome(c Capture, reply string, toolPosted bool, posted, empty string) CaptureOutcome {
	switch {
	case toolPosted:
		return CaptureOutcome{Capture: c, Action: CaptureDrop, Reason: reasonToolPosted}
	case strings.TrimSpace(reply) == "":
		return CaptureOutcome{Capture: c, Action: CaptureDrop, Reason: empty}
	}
	return CaptureOutcome{Capture: c, Action: CapturePost, Text: strings.TrimSpace(reply), Reason: posted}
}

// isChatPrompt reports whether a prompt is a Leo Chat delivery.
func isChatPrompt(text string) bool {
	return strings.HasPrefix(strings.TrimSpace(text), PromptPrefix)
}
