package tmux

import (
	"os"
	"strings"
	"testing"
)

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
────────────────────────────────────────────────────────────────────────────────
❯ first line
  second line
  third line
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ accept edits on (shift+tab to cycle)
`
	claudePlaceholderCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ Type a message…
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ accept edits on (shift+tab to cycle)
`
	claudeCollapsedPasteCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ [Pasted text #1 +22 lines]
────────────────────────────────────────────────────────────────────────────────
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
	claudeIdleAfterTurn = `
❯ Reply with exactly the word DELTA and nothing else.
⏺ DELTA
✻ Worked for 1s · done 1:51 PM
───────────────────────────────────────────────────────────────── live-claude3 ─
❯ 
────────────────────────────────────────────────────────────────────────────────
  [Haiku 4.5] | 🧠 default | 🌳 ⎇ no git
  Session: 18.0% | ⏱️ 2hr 8m | Weekly: 80.0% | ⏱️ 19hr 8m
  /private/t/leotest-idisp | 𖠰 no git | (no PR) | Ctx Used: 25.0%
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 6 agents
         You've used 80% of your weekly limit · resets 9am (America/New_York)
                                                                          /rc
`
	claudeBusyInLayoutCapture = `
⏺ DELTA
✻ Thinking…
───────────────────────────────────────────────────────────────── live-claude3 ─
❯ 
────────────────────────────────────────────────────────────────────────────────
  [Haiku 4.5] | 🧠 default | 🌳 ⎇ no git
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 6 agents
`
	claudeDraftInLayoutCapture = `
⏺ DELTA
───────────────────────────────────────────────────────────────── live-claude3 ─
❯ draft dispatch
────────────────────────────────────────────────────────────────────────────────
  [Haiku 4.5] | 🧠 default | 🌳 ⎇ no git
  ⏵⏵ bypass permissions on (shift+tab to cycle) · ← 6 agents
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
		{"claude/idle-after-turn", ClaudeComposerClassifier, claudeIdleAfterTurn, ComposerEmpty},
		{"claude/busy-in-layout", ClaudeComposerClassifier, claudeBusyInLayoutCapture, ComposerBusy},
		{"claude/draft-in-layout", ClaudeComposerClassifier, claudeDraftInLayoutCapture, ComposerDraft},
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

func TestComposerClassifierIgnoresDialogKeywordsInProse(t *testing.T) {
	t.Parallel()

	keywords := []string{"permission", "update", "allow", "deny", "approve"}
	fixtures := []struct {
		name     string
		classify ComposerClassifier
		capture  string
	}{
		{
			name:     "codex",
			classify: CodexComposerClassifier,
			capture: `installed Codex lacks a usable {keyword} profile
─ Worked for 1s ─
› Ask Codex to do anything

  gpt-5.6-luna default · ~/work`,
		},
		{
			name:     "claude",
			classify: ClaudeComposerClassifier,
			capture: `ordinary assistant output mentions {keyword} in prose
────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)`,
		},
	}

	for _, fixture := range fixtures {
		for _, keyword := range keywords {
			t.Run(fixture.name+"/"+keyword, func(t *testing.T) {
				capture := strings.ReplaceAll(fixture.capture, "{keyword}", keyword)
				if got := fixture.classify(capture); got != ComposerEmpty {
					t.Fatalf("classify() = %s, want %s", got, ComposerEmpty)
				}
			})
		}
	}
}

func TestIsComposerDialogTitleLine(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		line string
		want bool
	}{
		{"trust", "Trust?", true},
		{"permission", "Permission:", true},
		{"update", "Update", true},
		{"hook-review", "Hook review", true},
		{"approve", "Approve!", true},
		{"allow", "Allow", true},
		{"deny", "Deny", true},
		{"prose", "Permission profile", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isComposerDialog(tt.line); got != tt.want {
				t.Fatalf("isComposerDialog(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

func TestClaudeComposerClassifierIdleAfterTurnCapture(t *testing.T) {
	t.Parallel()

	capture, err := os.ReadFile("testdata/claude_idle_after_turn.txt")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	tests := []struct {
		name    string
		capture string
		want    ComposerState
	}{
		{"raw", string(capture), ComposerEmpty},
		{
			"busy-summary",
			strings.Replace(string(capture), "✻ Sautéed for 1s · done 2:02 PM", "✻ Cogitating… (esc to interrupt)", 1),
			ComposerBusy,
		},
		{
			"draft",
			strings.Replace(string(capture), "❯ ", "❯ draft dispatch", 1),
			ComposerDraft,
		},
		{
			"without-blank-lines",
			withoutBlankLines(string(capture)),
			ComposerEmpty,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ClaudeComposerClassifier(tt.capture); got != tt.want {
				t.Fatalf("ClaudeComposerClassifier() = %s, want %s", got, tt.want)
			}
		})
	}
}

func withoutBlankLines(capture string) string {
	lines := strings.Split(capture, "\n")
	nonBlank := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			nonBlank = append(nonBlank, line)
		}
	}
	return strings.Join(nonBlank, "\n")
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
