package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/bridge"
)

// toldFileName is the told-delegation store, under the config's state dir.
const toldFileName = "delegation-told.json"

// delegationContext serves the delegation policy hooks add to a bridged
// agent's context (see contextFor) and remembers what each session was told.
type delegationContext struct {
	source func() bridge.DelegationState
	told   *toldStore
	now    func() time.Time

	// mu makes one hook's read-decide-record atomic.
	mu sync.Mutex
}

// contextHook is what the SessionStart and UserPromptSubmit hooks send.
type contextHook struct {
	Event     string `json:"hook_event_name"`
	Source    string `json:"source"`
	SessionID string `json:"session_id"`
	AgentID   string `json:"agent_id"`
}

// contextFor is the context the hook's session should be told now ("" for
// none), recording it as told. A failure to persist is logged, not fatal: the
// cost is one repeated injection.
func (d *delegationContext) contextFor(agent string, h contextHook) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	told, wasTold := d.told.Get(agent, h.SessionID)
	var prior *delegationTold
	if wasTold {
		prior = &told
	}
	text, next := contextFor(h.Event, h.Source, d.source(), prior, d.now())
	if !wasTold || next != told {
		if err := d.told.Put(agent, h.SessionID, next); err != nil {
			fmt.Fprintf(os.Stderr, "delegation: %v\n", err)
		}
	}
	return text
}

// newDelegationContext serves source's policy, keeping what sessions were
// told under the config's state directory. Nil when the config cannot be
// read: the context hooks then say nothing.
func (s *Server) newDelegationContext(source func() bridge.DelegationState) *delegationContext {
	cfg, err := s.loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "delegation: no told state: %v\n", err)
		return nil
	}
	return &delegationContext{
		source: source,
		told:   newToldStore(filepath.Join(cfg.StatePath(), toldFileName), time.Now),
		now:    time.Now,
	}
}

// agentContextResponse is what `leo dispatch context` prints from.
type agentContextResponse struct {
	Context string `json:"context"`
}

// handleAPIAgentContext answers a supervised agent's SessionStart and
// UserPromptSubmit hooks (relayed by `leo dispatch context`) with the
// delegation context to add. It speaks only for a bridged launch (parity
// with the mod's prompt.compose), never inside a subagent, and accepts any
// session id with a valid token: a /clear or fork names a new one, which
// then belongs to the launch (see isAgentHookSession). Everything else is a
// 200 with no context, so the hook never retries.
// POST /api/agent/context
func (s *Server) handleAPIAgentContext(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxHookPayloadBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "reading hook payload"})
		return
	}
	var req agentHookRequest
	var hook contextHook
	if err := json.Unmarshal(raw, &req); err != nil || (len(req.Payload) > 0 && json.Unmarshal(req.Payload, &hook) != nil) {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid hook payload"})
		return
	}
	_ = encodeContext(w, s.agentContext(req.Token, hook))
}

func (s *Server) agentContext(token string, hook contextHook) string {
	if s.attention == nil || s.delegationCtx == nil || hook.AgentID != "" || hook.SessionID == "" {
		return ""
	}
	agent, known := s.attention.AgentForToken(token)
	if !known {
		return ""
	}
	if _, _, bridged := s.attention.BoundLaunch(agent); !bridged {
		return ""
	}
	if hook.Event == "SessionStart" {
		s.claimSession(agent, token, hook.SessionID)
	}
	return s.delegationCtx.contextFor(agent, hook)
}

func encodeContext(w http.ResponseWriter, context string) error {
	w.Header().Set("Content-Type", "application/json")
	return json.NewEncoder(w).Encode(agentContextResponse{Context: context})
}

// claimSession records sessionID, durably, as the session the launch holding
// token is in now, so hooks keep being the agent's across a daemon restart
// that adopts the session. A failure is logged, not fatal: the cost is that
// the new session's hooks are dropped until its next SessionStart.
func (s *Server) claimSession(agent, token, sessionID string) {
	home, ok := s.homePath()
	if !ok {
		return
	}
	if err := agentstore.SetClaimedSession(home, agent, token, sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "[%s] recording claimed session: %v\n", agent, err)
	}
}

// sessionClaimedBy returns the session agent's launch holding token last
// announced, "" if none.
func (s *Server) sessionClaimedBy(agent, token string) string {
	home, ok := s.homePath()
	if !ok {
		return ""
	}
	return agentstore.ClaimedSession(home, agent, token)
}

func (s *Server) homePath() (string, bool) {
	cfg, err := s.loadConfig()
	if err != nil {
		return "", false
	}
	return cfg.HomePath, true
}
