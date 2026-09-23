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
	codexApproveDialogCapture = `
Approve this action?

› Approve
  Deny

  Press Enter to continue
`
	codexAllowDialogCapture = `
Allow network access?

› Allow
  Deny

  Press Enter to continue
`
	codexDenyDialogCapture = `
Deny this action?

› Deny
  Allow

  Press Enter to continue
`
	codexUpdateAvailableDialogCapture = `
Update available

› Install update
  Not now

  Press Enter to continue
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
	claudeUpdateDialogCapture = `
Update available

❯ 1. Install update
  2. Not now

  Enter to confirm · Esc to cancel
`
	claudeHookReviewDialogCapture = `
Hook review

❯ 1. Approve hook
  2. Deny hook

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
		{"codex/approve-dialog", CodexComposerClassifier, codexApproveDialogCapture, ComposerUnknown},
		{"codex/allow-dialog", CodexComposerClassifier, codexAllowDialogCapture, ComposerUnknown},
		{"codex/deny-dialog", CodexComposerClassifier, codexDenyDialogCapture, ComposerUnknown},
		{"codex/update-available-dialog", CodexComposerClassifier, codexUpdateAvailableDialogCapture, ComposerUnknown},
		{"codex/empty-capture", CodexComposerClassifier, "", ComposerUnknown},
		{"codex/footer-only", CodexComposerClassifier, codexFooterOnlyCapture, ComposerUnknown},
		{"claude/empty", ClaudeComposerClassifier, claudeEmptyCapture, ComposerEmpty},
		{"claude/single-line-draft", ClaudeComposerClassifier, claudeSingleLineDraftCapture, ComposerDraft},
		{"claude/multiline-draft", ClaudeComposerClassifier, claudeMultilineDraftCapture, ComposerDraft},
		{"claude/placeholder", ClaudeComposerClassifier, claudePlaceholderCapture, ComposerEmpty},
		{"claude/collapsed-paste", ClaudeComposerClassifier, claudeCollapsedPasteCapture, ComposerDraft},
		{"claude/busy", ClaudeComposerClassifier, claudeBusyCapture, ComposerBusy},
		{"claude/dialog", ClaudeComposerClassifier, claudeDialogCapture, ComposerUnknown},
		{"claude/update-dialog", ClaudeComposerClassifier, claudeUpdateDialogCapture, ComposerUnknown},
		{"claude/hook-review-dialog", ClaudeComposerClassifier, claudeHookReviewDialogCapture, ComposerUnknown},
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

func TestCodexComposerClassifierIgnoresExactPermissionProseCapture(t *testing.T) {
	t.Parallel()

	capture := `• I inspected the test suite and found the relevant behavior:
  - TestCodexSandboxCanWriteManagedWorktree: adds negative control and explicit-root write; skipped because
    installed Codex lacks a usable permission profile.
─ Worked for 14m 15s ─
› Ask Codex to do anything
  gpt-5.6-sol default · ~/leo-worktrees/worktree · dispatch-worktree · Context 65% used · weekly 83% left …`
	if got := CodexComposerClassifier(capture); got != ComposerEmpty {
		t.Fatalf("CodexComposerClassifier() = %s, want %s", got, ComposerEmpty)
	}
}

func TestComposerClassifierIgnoresProseHeadingsAndNumberedTranscript(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		capture string
		want    ComposerState
	}{
		{"Update:\n› Ask Codex to do anything\n  gpt-5.6-sol default · ~/work", ComposerEmpty},
		{"1. Allow running the command\n› Ask Codex to do anything\n  gpt-5.6-sol default · ~/work", ComposerEmpty},
		{"Update:\n› prose heading\n  with an indented continuation", ComposerDraft},
		{"› 1. Allow running the command\n  gpt-5.6-sol default · ~/work", ComposerDraft},
		{"› 1. Historical option\n  2. Historical option\n› Ask Codex to do anything\n  gpt-5.6-sol default · ~/work", ComposerEmpty},
	} {
		if got := CodexComposerClassifier(tt.capture); got != tt.want {
			t.Fatalf("CodexComposerClassifier() = %s, want %s for %q", got, tt.want, tt.capture)
		}
	}
}

func TestClaudeComposerClassifierIgnoresHistoricalMenu(t *testing.T) {
	t.Parallel()

	capture := `❯ 1. Historical option
  2. Historical option
────────────────────────────────────────────────────────────────────────────────
❯
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)`
	if got := ClaudeComposerClassifier(capture); got != ComposerEmpty {
		t.Fatalf("ClaudeComposerClassifier() = %s, want %s", got, ComposerEmpty)
	}
}

func TestComposerDialogTitleRequiresMenuBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		capture string
		want    ComposerState
	}{
		{
			"title-only prose",
			"Update:\n› Ask Codex to do anything\n  gpt-5.6-sol default · ~/work",
			ComposerEmpty,
		},
		{
			"title with numbered menu",
			"Update:\n› 1. Install update\n  2. Not now",
			ComposerUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodexComposerClassifier(tt.capture); got != tt.want {
				t.Fatalf("CodexComposerClassifier() = %s, want %s", got, tt.want)
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

// Real captures of idle Claude dispatch panes that were reported busy because
// transcript prose above the composer said "running" (issue #211).
func TestClaudeComposerClassifierIgnoresBusyWordsInTranscriptCaptures(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{
		"testdata/claude_idle_prose_running_397.txt",
		"testdata/claude_idle_prose_running_411.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			capture, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := ClaudeComposerClassifier(string(capture)); got != ComposerEmpty {
				t.Fatalf("ClaudeComposerClassifier() = %s, want %s", got, ComposerEmpty)
			}
		})
	}
}

func TestComposerClassifierIgnoresBusyWordsInProse(t *testing.T) {
	t.Parallel()

	const claudeBox = `
────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)
`
	tests := []struct {
		name     string
		classify ComposerClassifier
		capture  string
		want     ComposerState
	}{
		{
			"claude/prose-running-above-done-summary",
			ClaudeComposerClassifier,
			"⏺ The daemon kept running while I was working.\n\n✻ Worked for 3s · done 6:08 PM" + claudeBox,
			ComposerEmpty,
		},
		{
			"claude/prose-working-directly-above-box",
			ClaudeComposerClassifier,
			"⏺ Done.\n  Still working: nothing; the thinking step is running fine." + claudeBox,
			ComposerEmpty,
		},
		{
			"claude/spinner-above-todo-list",
			ClaudeComposerClassifier,
			"⏺ Starting.\n\n✢ Running tests… (esc to interrupt)\n  ⎿  ☐ write test\n     ☐ fix bug\n" + claudeBox,
			ComposerBusy,
		},
		{
			"codex/prose-running-above-composer",
			CodexComposerClassifier,
			"• The tests are running in parallel and still working.\n\n› Ask Codex to do anything\n\n  gpt-5.6-luna default · ~/work\n",
			ComposerEmpty,
		},
		{
			"codex/prose-running-in-history",
			CodexComposerClassifier,
			"• Running the suite now.\n\n• All green.\n\n› Ask Codex to do anything\n\n  gpt-5.6-luna default · ~/work\n",
			ComposerEmpty,
		},
		{
			"codex/live-status-above-composer",
			CodexComposerClassifier,
			"• Running the suite now.\n\n• Working (12s • esc to interrupt)\n\n› Ask Codex to do anything\n\n  gpt-5.6-luna default · ~/work\n",
			ComposerBusy,
		},
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

// Synthesized from Codex's live status layout: queued ↳ rows with their edit
// hint, a wrapped queued message, and a status line wrapped by a narrow pane.
func TestCodexComposerClassifierStatusRegionCaptures(t *testing.T) {
	t.Parallel()

	for _, fixture := range []string{
		"testdata/codex_busy_queued_hint.txt",
		"testdata/codex_busy_wrapped_queued.txt",
		"testdata/codex_busy_wrapped_status.txt",
	} {
		t.Run(fixture, func(t *testing.T) {
			t.Parallel()
			capture, err := os.ReadFile(fixture)
			if err != nil {
				t.Fatalf("ReadFile() error = %v", err)
			}
			if got := CodexComposerClassifier(string(capture)); got != ComposerBusy {
				t.Fatalf("CodexComposerClassifier() = %s, want %s", got, ComposerBusy)
			}
		})
	}
}

func TestClaudeComposerClassifierSpinnerFrames(t *testing.T) {
	t.Parallel()

	const box = `
────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle)
`
	const todos = "\n  ⎿  ☐ write test\n     ☐ fix bug"
	tests := []struct {
		name   string
		status string
		want   ComposerState
	}{
		{"frame-·", "· Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-✢", "✢ Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-✳", "✳ Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-✶", "✶ Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-✻", "✻ Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-✽", "✽ Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-*", "* Cogitating… (esc to interrupt)" + todos, ComposerBusy},
		{"frame-·-token-counter", "· Pondering (5s · ↑ 200 tokens)", ComposerBusy},
		{"token-counter-without-interrupt-hint", "✻ Pondering… (5s · ↑ 200 tokens)", ComposerBusy},
		{"token-counter-without-ellipsis", "✻ Pondering (5s · ↑ 200 tokens)", ComposerBusy},
		{"queued-prompt-below-spinner", "✻ Pondering… (esc to interrupt)\n\n❯ also check the docs", ComposerBusy},
		{"done-summary", "✻ Worked for 3s · done 6:08 PM", ComposerEmpty},
		{"transcript-·-bullet", "⏺ Summary:\n  · the daemon is running\n· plain dot line", ComposerEmpty},
		{"transcript-*-bullet", "⏺ Summary:\n* working on nothing", ComposerEmpty},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			capture := "⏺ Starting.\n\n" + tt.status + "\n" + box
			if got := ClaudeComposerClassifier(capture); got != tt.want {
				t.Fatalf("ClaudeComposerClassifier() = %s, want %s", got, tt.want)
			}
		})
	}
}
