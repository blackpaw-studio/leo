package agent

import (
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/session"
)

// ResumeArgs rewrites stored claude args so a restored or resumed agent rejoins
// a prior session. Any existing `--session-id`/`--resume` pair is stripped
// (defensive: never pass two session-selection flags) before appending
// `--resume <sessionID>`. An empty sessionID returns the args with those flags
// stripped — the caller has chosen a fresh spawn.
func ResumeArgs(args []string, sessionID string) []string {
	cleaned := make([]string, 0, len(args)+2)
	for i := 0; i < len(args); i++ {
		if args[i] == "--session-id" || args[i] == "--resume" {
			if i+1 < len(args) {
				i++ // skip the value too
			}
			continue // drop the flag even if it has no value (never pass a naked flag)
		}
		cleaned = append(cleaned, args[i])
	}
	if sessionID == "" {
		return cleaned
	}
	return append(cleaned, "--resume", sessionID)
}

// ResumeIDFor picks the session id a restart, resume, or daemon-restart restore
// should rejoin.
//
// For claude, the newest transcript in the agent's workspace is preferred over
// the stored id — that catches a session created by /clear that agentstore
// never saw. That scan is workspace-wide and template-blind, though, so a
// record pinned by a template switch (agentstore.Record.SessionPinnedAt) keeps
// its stored id against any transcript written BEFORE the switch: those belong
// to the template just left, and preferring one would silently undo the swap.
// Transcripts written after the switch belong to the template the agent is on
// now and win as usual, which is what keeps the pin from going stale between a
// switch and a restart days later.
//
// Non-claude harnesses have no jsonl to scan: their driver injects resume
// tokens from the stored id at launch, so the stored id is always the answer.
func ResumeIDFor(rec agentstore.Record) string {
	if rec.Harness != "" && rec.Harness != "claude" {
		return rec.SessionID
	}
	latestID, latestAt, err := session.LatestSession(rec.Workspace, 0)
	if err != nil || latestID == "" {
		return rec.SessionID
	}
	if rec.SessionPinnedAt != nil && !latestAt.After(*rec.SessionPinnedAt) {
		return rec.SessionID
	}
	return latestID
}
