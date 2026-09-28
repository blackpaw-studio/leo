package agent

import (
	"fmt"
	"log"
	"path/filepath"

	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// resolveOpeningPrompt decides how a claude agent's initial-spawn opening
// prompt reaches its launch argv. tmux 3.6a rejects any client command over
// ~16 KiB ("command too long"), and BuildTemplateArgs' literal trailing
// positional would blow well past that for a large prompt — so instead the
// prompt is written to a private per-agent brief file
// (claudeharness.AgentBriefPath) and delivered as a small $(cat ...) command
// substitution word, the same mechanism PR #218 uses for interactive
// dispatch. The returned path is carried on a TYPED field
// (SpawnRequest.OpeningBriefPath / agentstore.Record.OpeningBriefPath) all
// the way to buildClaudeShellCmd, which appends the substitution itself —
// it never rides inside ClaudeArgs, so nothing in that slice (hand-authored
// config, a persisted record, or anything else) can ever smuggle unquoted
// shell text into the launch command.
//
// Called only for the claude harness, and only at initial spawn (Start/
// Restart never call this — see resolveTemplateWiring, which always passes
// an empty prompt to BuildTemplateArgs and never sets OpeningBriefPath on the
// rebuilt record, so a restart/resume never re-sends the opening prompt,
// matching the pre-existing invariant unchanged; Reset instead carries the
// ORIGINAL record's OpeningBriefPath forward unchanged, since Reset replays
// the original spawn's ClaudeArgs verbatim too).
//
// Returns briefPath == "" when prompt is empty (no brief needed at all). An
// oversized prompt (above claudeharness.ArgvPromptLimit) is rejected outright
// with a clear error: unlike interactive dispatch's injectOpening, an
// ephemeral claude agent has no tmux-paste-injection fallback once launched,
// so silently truncating or dropping the prompt would be worse than failing
// the spawn.
func resolveOpeningPrompt(cfg *config.Config, agentName, prompt string) (briefPath string, err error) {
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
	return path, nil
}

// removeOpeningPromptBrief deletes path, if any. Best-effort: a missing file
// is not an error, and a removal failure is logged rather than surfaced,
// matching removeSettingsSpill's contract for the sibling spill file.
//
// Deliberately NOT called from Stop or a non-live Rename: Reset replays the
// original agentstore record's OpeningBriefPath verbatim (see
// resolveOpeningPrompt), so a stop-then-reset (or a rename) must not lose the
// file a later Reset needs. It is removed only on Delete (the agent is gone
// for good) — SweepOpeningPromptBriefs cleans up files a Restart/Start has
// since orphaned by clearing the field without removing the file.
func removeOpeningPromptBrief(path string) {
	if err := claudeharness.RemoveBrief(path); err != nil {
		log.Printf("removing opening-prompt brief %q: %v", path, err)
	}
}

// SweepOpeningPromptBriefs removes agent-briefs files that no agentstore
// record's OpeningBriefPath references — e.g. a Restart/Start cleared the
// field on the record that used to own the file (see resolveOpeningPrompt),
// or the record itself is gone entirely. Best-effort, for daemon startup;
// mirrors SweepSettingsSpills for the sibling settings-spill mechanism.
func SweepOpeningPromptBriefs(homePath string) {
	records, err := agentstore.Load(agentstore.FilePath(homePath))
	if err != nil {
		return // no readable store: never guess which files are orphans
	}
	referenced := make(map[string]struct{}, len(records))
	for _, rec := range records {
		if rec.OpeningBriefPath != "" {
			referenced[rec.OpeningBriefPath] = struct{}{}
		}
	}
	matches, err := filepath.Glob(filepath.Join(homePath, "state", claudeharness.AgentBriefSpillDir, "*.txt"))
	if err != nil {
		return
	}
	for _, path := range matches {
		if _, ok := referenced[path]; ok {
			continue
		}
		removeOpeningPromptBrief(path)
	}
}
