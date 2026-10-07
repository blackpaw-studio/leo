# Bridge roster band replaces the tmux roster line

Status: draft · 2026-10-07

## Problem

The leo-bridge mod reports dispatch counts through `$.ui.status`, which Claude
Code always draws as a yellow `⚠ leo-bridge: …` warning notice. Normal state
reads as an error. Bridged sessions also get the same information twice: the
band above the prompt and the tmux roster status line. The band has no
thinking level.

## Design

### 1. Status line is for problems only

- `statusLine` stops reporting dispatch counts. It returns text only while
  delegation has fallen back (`native agents allowed: <why>`), and is
  otherwise undefined, which clears the line.

### 2. The band is the roster

`ui.render` on `AbovePrompt` draws a section set apart from the spinner and
tip above it: one blank row, a dim header rule, then one line per dispatch in
the snapshot, oldest first, indented two columns and aligned in columns:

```

leo dispatches ──────────────────────────────────────────
  ⟳ impl-auth     opus · high     4:10   182k/12.4k  $1.92   [Cancel]
  ⏸ reviewer      sonnet · med    1:23    41k/3.1k   $0.31   [Cancel]
  ✓ explore-db    haiku           0:41    20k/1.2k   $0.04
```

- The header is dim `leo dispatches ` followed by `─` to the band's width
  (fall back to a fixed 40 when the width is unknown). No counts in it.
- The blank row and header are part of the tree, drawn only when there are
  rows; they never appear on an empty band.

- Columns: glyph, label (`name || role || template || id`, truncated to 16),
  `model · effort` (effort omitted when empty), elapsed, tokens in/out, cost
  (omitted when unknown). Pad each column to the widest value in the band.
- Color: the glyph carries the status color (running default/cyan, idle and
  settling dim, queued dim, done green, failed/canceled/timeout red, stalled
  yellow with ` stalled` after elapsed). Model, tokens and cost are dim. No
  row or band background color.
- Terminal dispatches stay for 10 s after the mod first sees them terminal,
  then drop from the band. A dispatch already terminal in the first snapshot
  after a (re)load is not shown.
- Cancel stays a `Button` on non-terminal rows; behaviour unchanged.
- The 1 s redraw ticker keeps running while any row is running or a terminal
  row is still inside its 10 s window.
- With no rows to show, return `next(e)`.

### 3. Effort in the snapshot

- `bridge.DispatchState` gains `effort` (`json:"effort,omitempty"`), filled
  from the dispatch record's resolved effort (the value the role/profile
  routing resolved, same source as `leo dispatch show`). Empty when none.
- `parseDispatch` reads it as a string.

### 4. tmux roster line hidden for bridged sessions

- When building per-session rosters, the dispatcher skips a tmux session whose
  owning agent currently has an adopted (connected) bridge, and clears that
  session's roster status line if Leo had set it.
- When the bridge disconnects, the next sweep draws the line again.
- `leo-dispatch` and sessions without a bridge (codex, opencode, unbridged
  claude) are unchanged.

## Out of scope

Steering from the band, a separate pane, drawing below the prompt (no mod
slot exists there; `PromptHint` would replace the mode line), changes to the
web UI or `/api/v1`.

## Testing

- Mod (`claude plugin test`): status line empty with dispatches and no
  fallback; fallback text still shown; blank row + header present with rows
  and absent without; row text, indent and column padding; effort
  omitted when empty; stalled marker; terminal row shown then gone after 10 s
  (fake clock); terminal-at-load row not shown; ticker stops after the last
  terminal row expires.
- Go: `DispatchState.Effort` populated from the record; roster sweep skips and
  clears a bridged session's line and restores it after disconnect.
- Live: a bridged claude session dispatches one run; the band shows the row
  with model · effort, no `⚠` status line appears, and the tmux roster line for
  that session is gone. Screenshot at native pixels.
