package tmux

import "testing"

// The Codex empty, multiline draft, and trust-dialog fixtures were captured
// from Codex 0.153.4 in a throwaway tmux server. The remaining Codex fixtures
// are synthesized. Every fixture is trimmed to classification-relevant lines.
const (
	codexEmptyCapture = `
› Ask Codex to do anything

  gpt-5.6-luna default · ~/work
`
	codexSingleLineDraftCapture = `
› classify this

  gpt-5.6-luna default · ~/work
`
	codexMultilineDraftCapture = `
› first draft
  second draft
  third draft

  gpt-5.6-luna default · ~/work
`
	codexPlaceholderCapture = `
› Ask Codex to do anything
  gpt-5.6-luna default · ~/work
`
	codexCollapsedPasteCapture = `
› [Pasted text #1 +22 lines]

  gpt-5.6-luna default · ~/work
`
	codexBusyCapture = `
• Working (12s · esc to interrupt)

  gpt-5.6-luna default · ~/work
`
	codexDialogCapture = `
Do you trust the contents of this directory?

› 1. Yes, continue
  2. No, quit

  Press enter to continue
`
	codexFooterOnlyCapture = `
  gpt-5.6-luna default · ~/work
`

	// Claude empty, single-line draft, and busy fixtures were captured from
	// Claude Code 2.1.270 in a throwaway tmux server. Its other fixtures are
	// synthesized from the existing ❯ prompt and dialog chrome contracts.
	claudeEmptyCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────

  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`
	claudeSingleLineDraftCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ classify this draft
────────────────────────────────────────────────────────────────────────────────

  ⏵⏵ auto mode on (shift+tab to cycle)                        ● high · /effort
`
	claudeMultilineDraftCapture = `
❯ first line
  second line
  third line

  ⏵⏵ accept edits on (shift+tab to cycle)
`
	claudePlaceholderCapture = `
❯ Type a message…

  ⏵⏵ accept edits on (shift+tab to cycle)
`
	claudeCollapsedPasteCapture = `
❯ [Pasted text #1 +22 lines]

  ⏵⏵ accept edits on (shift+tab to cycle)
`
	claudeBusyCapture = `
❯ Describe the history of the Roman Empire exhaustively.

✽ Grooving…

────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`
	claudeDialogCapture = `
Claude Code needs permission to use this directory

❯ 1. Trust this folder
  2. Exit

  Enter to confirm · Esc to cancel
`
	claudeFooterOnlyCapture = `
  ⏵⏵ accept edits on (shift+tab to cycle)
`
)

func TestComposerClassifier(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		classify ComposerClassifier
		capture  string
		want     ComposerState
	}{
		{"codex/empty", CodexComposerClassifier, codexEmptyCapture, ComposerEmpty},
		{"codex/single-line-draft", CodexComposerClassifier, codexSingleLineDraftCapture, ComposerDraft},
		{"codex/multiline-draft", CodexComposerClassifier, codexMultilineDraftCapture, ComposerDraft},
		{"codex/placeholder", CodexComposerClassifier, codexPlaceholderCapture, ComposerEmpty},
		{"codex/collapsed-paste", CodexComposerClassifier, codexCollapsedPasteCapture, ComposerDraft},
		{"codex/busy", CodexComposerClassifier, codexBusyCapture, ComposerBusy},
		{"codex/dialog", CodexComposerClassifier, codexDialogCapture, ComposerUnknown},
		{"codex/empty-capture", CodexComposerClassifier, "", ComposerUnknown},
		{"codex/footer-only", CodexComposerClassifier, codexFooterOnlyCapture, ComposerUnknown},
		{"claude/empty", ClaudeComposerClassifier, claudeEmptyCapture, ComposerEmpty},
		{"claude/single-line-draft", ClaudeComposerClassifier, claudeSingleLineDraftCapture, ComposerDraft},
		{"claude/multiline-draft", ClaudeComposerClassifier, claudeMultilineDraftCapture, ComposerDraft},
		{"claude/placeholder", ClaudeComposerClassifier, claudePlaceholderCapture, ComposerEmpty},
		{"claude/collapsed-paste", ClaudeComposerClassifier, claudeCollapsedPasteCapture, ComposerDraft},
		{"claude/busy", ClaudeComposerClassifier, claudeBusyCapture, ComposerBusy},
		{"claude/dialog", ClaudeComposerClassifier, claudeDialogCapture, ComposerUnknown},
		{"claude/empty-capture", ClaudeComposerClassifier, "", ComposerUnknown},
		{"claude/footer-only", ClaudeComposerClassifier, claudeFooterOnlyCapture, ComposerUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.classify(tt.capture); got != tt.want {
				t.Fatalf("classify() = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestComposerStateString(t *testing.T) {
	t.Parallel()
	for state, want := range map[ComposerState]string{
		ComposerBusy:      "busy",
		ComposerEmpty:     "empty",
		ComposerDraft:     "draft",
		ComposerUnknown:   "unknown",
		ComposerState(99): "unknown",
	} {
		if got := state.String(); got != want {
			t.Errorf("ComposerState(%d).String() = %q, want %q", state, got, want)
		}
	}
}
