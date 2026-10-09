package web

import (
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// locateTmux is the tmux lookup seam for dispatch mode defaulting.
var locateTmux = tmux.Locate

// defaultDispatchMode resolves a dispatch whose mode was omitted: interactive,
// unless it cannot run that way, in which case it is headless and the returned
// note says why. An explicit mode never comes through here, so an explicit
// interactive request on an unsupported setup still fails loudly.
func (s *Server) defaultDispatchMode(cfg *config.Config, template string) (consult.Mode, string) {
	if tmpl, ok := cfg.Templates[template]; ok && cfg.TemplateHarness(tmpl) == "opencode" {
		return consult.ModeHeadless, "ran headless: the opencode harness has no interactive dispatch"
	}
	if _, err := locateTmux(); err != nil {
		return consult.ModeHeadless, "ran headless: tmux is not available for an interactive dispatch"
	}
	if !s.consults.InteractiveAvailable() {
		return consult.ModeHeadless, "ran headless: the interactive runtime is not configured"
	}
	return consult.ModeInteractive, ""
}
