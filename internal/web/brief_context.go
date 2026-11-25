package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/brief"
)

// briefToldFileName is the told-brief store, under the config's state dir.
const briefToldFileName = "brief-told.json"

// briefContext serves what the SessionStart and UserPromptSubmit hooks tell
// an agent about its brief, and remembers which instruction each session
// heard. It is built once, so the hooks never re-read leo.yaml.
type briefContext struct {
	home string
	told *toldStore
	now  func() time.Time
	mu   sync.Mutex // one hook's read-decide-record
}

// briefCtx is the hooks' brief context, built on first use; nil when the
// config cannot be read.
func (s *Server) briefCtx() *briefContext {
	s.briefCtxOnce.Do(func() {
		cfg, err := s.loadConfig()
		if err != nil {
			fmt.Fprintf(os.Stderr, "brief: no told state: %v\n", err)
			return
		}
		s.briefContext = &briefContext{
			home: cfg.HomePath,
			told: newToldStore(filepath.Join(cfg.StatePath(), briefToldFileName), time.Now),
			now:  time.Now,
		}
	})
	return s.briefContext
}

func instructionHash() string {
	sum := sha256.Sum256([]byte(brief.Instruction))
	return hex.EncodeToString(sum[:])
}

// briefContextFor is the brief context the hook's session should be told now
// ("" for none). SessionStart always carries the instruction, since the
// model's context was just rebuilt, plus the stored brief after a clear,
// compact or resume. UserPromptSubmit carries the instruction only when this
// session has not heard its current text (briefs switched on mid-session, or
// the text changed), plus the staleness nudge when one is due. Everything is
// silent while briefs are off, and for a launch whose MCP server is not
// currently offering leo_brief (it never reported, or reported it withdrawn):
// the guidance names a tool that session does not have.
func (s *Server) briefContextFor(agent, token string, h contextHook) string {
	if !s.briefEnabled() || !s.briefOffers.offered(agent, token) {
		return ""
	}
	bc := s.briefCtx()
	if bc == nil {
		return ""
	}
	switch h.Event {
	case "SessionStart":
		return bc.sessionStart(agent, h)
	case "UserPromptSubmit":
		return bc.prompt(s, agent, h)
	}
	return ""
}

func (b *briefContext) sessionStart(agent string, h contextHook) string {
	b.markTold(agent, h.SessionID)
	parts := []string{brief.Instruction}
	switch h.Source {
	case "clear", "compact", "resume":
		if stored := agentstore.BriefOf(b.home, agent); stored != nil && !stored.IsZero() {
			parts = append(parts, brief.Restored(stored.Clamped(), h.Source))
		}
	}
	return strings.Join(parts, "\n\n")
}

func (b *briefContext) prompt(s *Server, agent string, h contextHook) string {
	var parts []string
	b.mu.Lock()
	told, wasTold := b.told.Get(agent, h.SessionID)
	if !wasTold || told.Hash != instructionHash() {
		parts = append(parts, brief.Instruction)
		b.recordTold(agent, h.SessionID)
	} else if b.now().Sub(told.At) >= toldRestamp {
		b.recordTold(agent, h.SessionID) // keep a live session's entry from ageing out
	}
	b.mu.Unlock()
	if s.briefs.Nudge(agent) {
		parts = append(parts, brief.NudgeText(agentstore.BriefOf(b.home, agent) != nil))
	}
	return strings.Join(parts, "\n\n")
}

func (b *briefContext) markTold(agent, session string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recordTold(agent, session)
}

// recordTold remembers the instruction as heard; a failure to persist costs
// one repeated instruction, so it is logged, not fatal.
func (b *briefContext) recordTold(agent, session string) {
	t := delegationTold{Enabled: true, Hash: instructionHash(), At: b.now()}
	if err := b.told.Put(agent, session, t); err != nil {
		fmt.Fprintf(os.Stderr, "brief: %v\n", err)
	}
}
