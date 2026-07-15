package web

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// deliverConsultReply injects a consult reply into the calling agent's
// session. Unlike handleWebAgentMessage's live fast-path, claude callers
// always go through the readiness-probing injectPrompt: the reply arrives
// minutes after dispatch, when the caller may be mid-turn, and the probe
// waits for the input box instead of pasting blind. Suspended callers are
// resumed first; non-claude callers are routed through their SessionDriver.
func (s *Server) deliverConsultReply(ctx context.Context, name, body string) error {
	if harnessName, handle, ok := s.resolveMessageTarget(name); ok && harnessName != "" && harnessName != "claude" {
		hd, err := harness.Get(harnessName)
		if err != nil {
			return fmt.Errorf("resolving harness %q: %w", harnessName, err)
		}
		drv := hd.Driver()
		if drv == nil {
			return fmt.Errorf("harness %q has no session driver", harnessName)
		}
		_, err = drv.Inject(ctx, handle, body)
		return err
	}

	if _, live := s.processes.States()[name]; !live {
		if s.agentSvc == nil {
			return fmt.Errorf("caller %q is not running and agent service is unavailable", name)
		}
		rec, err := s.agentSvc.Resume(name)
		if err != nil {
			return fmt.Errorf("caller %q is gone: %w", name, err)
		}
		return s.injectPrompt(ctx, agent.SessionName(rec.Name), body)
	}
	return s.injectPrompt(ctx, agent.SessionName(name), body)
}

// handleAPIConsult dispatches a one-off consultant subagent for a calling
// agent. Validation errors return synchronously; the consultant's answer is
// delivered later via deliverConsultReply.
//
// POST /api/consult {"from": "...", "template": "...", "model": "...", "prompt": "..."}
func (s *Server) handleAPIConsult(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From     string `json:"from"`
		Template string `json:"template"`
		Model    string `json:"model,omitempty"`
		Prompt   string `json:"prompt"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: fmt.Sprintf("invalid request: %v", err)})
		return
	}
	if req.From == "" || req.Template == "" || req.Prompt == "" {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: "from, template, and prompt are required"})
		return
	}

	cfg, err := s.loadConfig()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, apiResponse{Error: fmt.Sprintf("loading config: %v", err)})
		return
	}

	// The caller must be a supervised agent: it needs a live (or resumable)
	// session for the reply to land in. Its workspace becomes the
	// consultant's working directory when known. resolved tracks whether the
	// agent record exists at all — distinct from workspace being non-empty,
	// since a suspended agent can have a resolvable record with an empty
	// Workspace field.
	workspace := ""
	resolved := false
	if s.agentSvc != nil {
		if rec, err := s.agentSvc.Resolve(req.From); err == nil {
			resolved = true
			workspace = rec.Workspace
		}
	}
	_, live := s.processes.States()[req.From]
	if !resolved && !live {
		writeJSON(w, http.StatusBadRequest, apiResponse{
			Error: fmt.Sprintf("caller %q is not a supervised agent; consults need a session to reply into", req.From),
		})
		return
	}

	tk, err := s.consults.Dispatch(cfg, consult.Request{
		From:      req.From,
		Template:  req.Template,
		Model:     req.Model,
		Prompt:    req.Prompt,
		Workspace: workspace,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, apiResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, apiResponse{OK: true, Data: map[string]string{
		"id":      tk.ID,
		"harness": tk.Harness,
		"model":   tk.Model,
	}})
}
