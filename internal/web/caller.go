package web

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

var canonicalPaneID = regexp.MustCompile(`^%[0-9]+$`)

type dispatchCaller struct{ PaneID, SessionID, WindowID, Harness string }

func (s *Server) resolveDispatchCaller(caller, pane string) (dispatchCaller, error) {
	session := ""
	if caller != "" && s.processes != nil {
		state, ok := s.processes.States()[caller]
		if ok && (state.Status == "running" || state.Status == "starting") {
			session = agent.SessionName(caller)
		}
	}
	if pane == "" && session != "" {
		out, err := s.execCommand(findTmuxPath(), tmux.Args("show-options", "-t", tmux.Target(session)+":", "-v", "@leo_primary_pane")...).Output()
		if err != nil {
			return dispatchCaller{}, nil
		}
		pane = strings.TrimSpace(string(out))
	}
	if pane == "" {
		return dispatchCaller{}, nil
	}
	if !canonicalPaneID.MatchString(pane) {
		return dispatchCaller{}, fmt.Errorf("caller_pane_id %q must be a canonical %%N pane id", pane)
	}
	out, err := s.execCommand(findTmuxPath(), tmux.Args("display-message", "-p", "-t", pane, "#{session_name} #{session_id} #{window_id}")...).Output()
	if err != nil {
		return dispatchCaller{}, fmt.Errorf("caller_pane_id %q is not a live pane", pane)
	}
	fields := strings.Fields(string(out))
	if len(fields) != 2 && len(fields) != 3 {
		return dispatchCaller{}, fmt.Errorf("caller_pane_id %q returned invalid tmux identity", pane)
	}
	if session != "" && fields[0] != session {
		return dispatchCaller{}, fmt.Errorf("caller_pane_id %q does not belong to caller session %q", pane, session)
	}
	harness := ""
	if caller != "" {
		if s.resolveHandle != nil {
			harness, _, _ = s.resolveHandle(caller)
		}
		if harness == "" && s.agentSvc != nil {
			harness, _, _ = s.agentSvc.ResolveHandle(caller)
		}
		if harness == "" && session != "" {
			harness = "claude"
		}
	}
	if harness == "" {
		command, err := s.execCommand(findTmuxPath(), tmux.Args("display-message", "-p", "-t", pane, "#{pane_current_command}")...).Output()
		if err == nil {
			switch strings.ToLower(filepath.Base(strings.TrimSpace(string(command)))) {
			case "claude", "codex", "opencode":
				harness = strings.ToLower(filepath.Base(strings.TrimSpace(string(command))))
			}
		}
	}
	windowID := ""
	if len(fields) == 3 {
		windowID = fields[2]
	}
	return dispatchCaller{PaneID: pane, SessionID: fields[1], WindowID: windowID, Harness: harness}, nil
}
