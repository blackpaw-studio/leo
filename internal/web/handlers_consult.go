package web

import (
	"context"
	"fmt"

	"github.com/blackpaw-studio/leo/internal/agent"
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
