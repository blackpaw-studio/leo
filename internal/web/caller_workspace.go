package web

import (
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

// CallerWorkspace resolves the root caller of a dispatch to the workspace its
// cwd is confined to: a one-shot task ("task:<name>") to its task workspace,
// otherwise a supervised agent matched by exact name. Fuzzy agent resolution
// is deliberately not used: a near-miss name must not inherit another
// agent's reach.
func (s *Server) CallerWorkspace(cfg *config.Config, caller string) (consult.CallerHome, bool) {
	if name, ok := strings.CutPrefix(caller, "task:"); ok {
		task, ok := cfg.Tasks[name]
		if !ok {
			return consult.CallerHome{}, false
		}
		return consult.CallerHome{Workspace: cfg.TaskWorkspace(task), Harness: cfg.TaskHarness(task)}, true
	}
	if caller == "" || s.agentSvc == nil {
		return consult.CallerHome{}, false
	}
	for _, rec := range s.agentSvc.List() {
		if rec.Name == caller && rec.Workspace != "" {
			return consult.CallerHome{Workspace: rec.Workspace, Harness: cfg.TemplateHarness(cfg.Templates[rec.Template]), Template: rec.Template}, true
		}
	}
	return consult.CallerHome{}, false
}
