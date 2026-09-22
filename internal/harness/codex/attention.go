package codex

import "github.com/blackpaw-studio/leo/internal/harness"

// attentionLaunch backs the driver's harness.AttentionHooker capability.
// Codex ignores hooks.* overrides on the command line, so the hooks live in
// the session's CODEX_HOME (shared with interactive dispatch, reporting via
// the same fixed `leo dispatch report` command); argv is unchanged.
func attentionLaunch(h harness.SessionHandle, args, _ []string) ([]string, error) {
	if err := (Codex{}).PrepareInteractive(CodexHome(h.Env), h.Workspace); err != nil {
		return nil, err
	}
	return args, nil
}
