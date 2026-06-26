# Robust Injection Past Claude Startup Dialogs — Design

**Date:** 2026-06-25
**Status:** Approved (design)
**Follows up:** PR #89 (idle-suspend) live-test finding; see memory `project_claude_startup_dialogs_block_injection`.

## Summary

Claude Code shows interactive startup/announcement dialogs (e.g. v2.1.193's *"Try
the new fullscreen renderer? 1. Yes / 2. Not now — Enter to confirm · Esc to
cancel"*). These block the input box, and Leo's tmux message injection fails two
ways:

1. **Mis-classification:** the readiness classifier (`classifyInput`) reads a menu
   line like `❯ 1. Yes, try it` as a content-bearing, ready input box, so the
   injector reports "ready," pastes the message into the menu, and Enter selects a
   menu option — the body is dropped and an unintended option may be chosen.
2. **No dismissal:** the existing per-dialog handlers (`autoResumePrompt`,
   `AcceptDevChannelPrompt`) only match two specific known prompts. A new dialog
   has no matcher, so it sits there blocking input until a human intervenes.

This affects **all** injection paths — idle-suspend auto-wake, persistent-task
delivery, and `leo_send_message` — and recurs whenever a claude update adds a new
dialog.

The fix is two coordinated changes: harden the classifier so a dialog is never read
as a ready input box, and add a *generic* dialog dismisser so unknown dialogs are
cleared without per-dialog code.

## Goals

- Injection delivers reliably even when claude boots/resumes into an announcement
  dialog, without a new code change per claude version.
- Never mis-select a menu option or paste a message into a dialog.
- Don't auto-answer a *consequential* dialog (trust/permission/destructive).

## Non-Goals

- Preventing the dialogs via claude config (version-fragile; revisit only if dialogs
  proliferate). Out of scope.
- Handling dialogs that require a *specific* non-default answer beyond "decline".

## Change 1 — Harden the readiness classifier

File: `internal/tmux/inject.go`, `classifyInput` (and the `inputState` it feeds:
`paneInputState`, `InputHasContent`, and the `injectPrompt` Phase-1 probe).

Today `classifyInput` scans bottom-up for a line beginning with `❯ ` and returns
`inputHasContent` when any non-whitespace follows the glyph. A menu option line
satisfies that.

New rule — when the matched glyph line indicates a dialog rather than a real input
box, return **`inputUnknown`** instead of `inputHasContent`:

- the glyph-line content matches a numbered menu option: regex `^\d+[.)]\s`
  (e.g. `1. Yes, try it`, `2) No`); **or**
- the captured pane contains dialog chrome: both `Enter to confirm` and
  `Esc to cancel` are present.

Rationale for `inputUnknown` (not `inputEmpty`): `inputUnknown` keeps the probe loop
waiting within its existing 60s budget (neither `ready` nor `sawInputBox` is set),
which is exactly what we want while the dismisser (Change 2) clears the dialog. The
existing fall-open after the budget remains the last-resort safety net.

A real input box carrying the probe char (`❯ .`) or genuine typed text (that is not a
numbered option) still classifies as `inputHasContent` — unchanged.

## Change 2 — Generic startup-dialog dismisser

File: `internal/service/process.go`. Generalize `autoResumePrompt` into
`dismissStartupDialog(tmuxPath, sessionName, processName)`, called from the same
`waitForSessionEnd` 5s poll loop it runs in today (line ~789). Because every injected
agent is supervised, this loop runs for the agent's whole life and covers all
injection paths.

Logic, in order:

1. Capture the last N lines of the pane (reuse the existing `capture-pane -S -10`).
2. **Known Enter-prompt (unchanged):** if pane contains `Resume from summary` and
   `Enter to confirm` → send `Enter`, log, done.
3. **Safety denylist:** if the pane (case-insensitive) contains any of `trust`,
   `permission`, `delete`, `overwrite` → do nothing (leave consequential dialogs for
   a human). Return.
4. **Generic decline:** if the pane shows a blocking dialog — a numbered-option menu
   (`^\s*❯?\s*\d+[.)]\s` on some line) **or** dialog chrome (`Esc to cancel`
   present) — send `Esc`, and log what was cleared.
5. Otherwise do nothing.

Best-effort throughout (capture/send errors ignored, retried next 5s poll), matching
the existing handlers. Dev-channel handling (`AcceptDevChannelPrompt`, at spawn,
Enter) is unchanged.

## How the two changes cooperate

1. Resumed/spawned claude comes up at an announcement dialog.
2. The injector's readiness probe captures the pane; `classifyInput` now returns
   `inputUnknown` (dialog, not a ready box) → the injector keeps waiting.
3. Within ≤5s the supervise poll loop's `dismissStartupDialog` sends `Esc`.
4. The dialog clears; claude's real input box renders.
5. Next probe: `classifyInput` returns `inputHasContent` → injector pastes the body
   once and submits. Delivery succeeds.

## Edge cases & risks

- **Trust / permission prompts:** the denylist prevents a blind `Esc` from declining
  trust or a permission grant (which could break or stall an agent). In practice
  Leo's bypass-permissions agents don't see the trust prompt, so this is insurance.
- **Probe chars typed into a live dialog:** the probe types `.` each attempt; a
  numbered menu ignores/filters single chars harmlessly, and the probe's `Ctrl-U`
  plus the post-dismissal fresh input box leave no residue before the real paste.
- **A dialog that ignores `Esc`:** the injector's budget + fall-open still bound the
  failure (message may drop after 60s, logged) — no hang, no mis-select. Not worse
  than today for that (rare) case.
- **`autoResumePrompt` callers / name:** rename to `dismissStartupDialog`; update the
  single call site in `waitForSessionEnd`.

## Testing

- **`classifyInput` table tests** (`internal/tmux/inject_test.go`): empty box →
  `inputEmpty`; box with probe `.` → `inputHasContent`; numbered menu `❯ 1. Yes` →
  `inputUnknown`; pane with `Enter to confirm`+`Esc to cancel` chrome →
  `inputUnknown`; real typed prompt (non-numbered) → `inputHasContent`.
- **`dismissStartupDialog` table tests** (`internal/service/`): via the package
  `execCommand`/exec seam — fullscreen-renderer dialog pane → sends `Esc`;
  `Resume from summary` pane → sends `Enter`; pane containing `trust` → sends
  nothing; plain `❯` prompt → sends nothing.
- **`InjectPrompt` integration-style test**: pane shows a menu for the first N probe
  captures, then a real input box → body pasted exactly once and submitted (extends
  the existing late-session test pattern).

## Key touch points

| Concern | File |
|---|---|
| Classifier hardening (menu/chrome → inputUnknown) | `internal/tmux/inject.go` |
| Classifier tests | `internal/tmux/inject_test.go` |
| `autoResumePrompt` → `dismissStartupDialog` + Esc/denylist | `internal/service/process.go` |
| Dismisser tests | `internal/service/process_test.go` (or sibling) |
