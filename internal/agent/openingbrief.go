package agent

import (
	"fmt"
	"log"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// resolveOpeningPrompt decides how a claude agent's initial-spawn opening
// prompt reaches its launch argv. tmux 3.6a rejects any client command over
// ~16 KiB ("command too long"), and BuildTemplateArgs' literal trailing
// positional would blow well past that for a large prompt — so instead the
// prompt is written to a private per-agent brief file
// (claudeharness.AgentBriefPath) and delivered as a small $(cat ...) command
// substitution word (claudeharness.BriefArgvWord), the same mechanism PR #218
// uses for interactive dispatch. The word is wrapped with harness.RawArg so
// buildClaudeShellCmd's per-argument shell-quote pass (internal/service/
// process.go) emits it unquoted — a quoted "$(...)" would never expand.
//
// Called only for the claude harness, and only at initial spawn (Start/
// Restart/Reset never call this — see resolveTemplateWiring, which always
// passes an empty prompt to BuildTemplateArgs and never re-adds a trailing
// arg, so a restart/resume never re-sends the opening prompt, matching the
// pre-existing invariant unchanged).
//
// Returns rawArg == "" when prompt is empty (no brief needed at all). An
// oversized prompt (above claudeharness.ArgvPromptLimit) is rejected outright
// with a clear error: unlike interactive dispatch's injectOpening, an
// ephemeral claude agent has no tmux-paste-injection fallback once launched,
// so silently truncating or dropping the prompt would be worse than failing
// the spawn.
func resolveOpeningPrompt(cfg *config.Config, agentName, prompt string) (rawArg string, err error) {
	if prompt == "" {
		return "", nil
	}
	if !claudeharness.DeliversPromptViaArgv(prompt) {
		return "", fmt.Errorf("opening prompt is %d bytes, exceeding the %d byte launch-time limit for a claude agent", len(prompt), claudeharness.ArgvPromptLimit)
	}
	path := claudeharness.AgentBriefPath(cfg.HomePath, agentName)
	if err := claudeharness.WritePrivateBrief(path, prompt); err != nil {
		return "", fmt.Errorf("writing opening-prompt brief: %w", err)
	}
	return harness.RawArg(claudeharness.BriefArgvWord(path)), nil
}

// removeOpeningPromptBrief deletes agentName's opening-prompt brief file, if
// any. Best-effort: called from Stop (the supervise loop that could replay
// the launch argv referencing it is already gone by then) and Delete: a
// missing file is not an error, and a write failure is logged rather than
// surfaced, matching removeSettingsSpill's contract for the sibling spill
// file.
func removeOpeningPromptBrief(homePath, agentName string) {
	path := claudeharness.AgentBriefPath(homePath, agentName)
	if err := claudeharness.RemoveBrief(path); err != nil {
		log.Printf("agent %q: removing opening-prompt brief: %v", agentName, err)
	}
}
