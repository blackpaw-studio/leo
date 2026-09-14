# Dispatch roster: live subagent status in the tmux status bar

## Goal

Show every dispatched subagent's state and active time at a glance, the way
Claude Code's native subagent widget does: a counter that runs while the
subagent is working and freezes when it is idle, queued, or finished. The
display lives on a second tmux status line, so window tabs stay plain names
and never get crowded.

## Non-goals

- A vertical roster (`leo dispatch top`), popups, or a web UI page. Later.
- Renaming viewer or TUI windows. Names stay `<label>·<hex4>`.
- Per-second precision. Counters advance on the dispatcher's 5 s sweep.
- Touching the user's tmux config. Leo sets session-scoped options only.

## Data

`Record` gains two fields, persisted with the rest:

- `active_seconds` (float): accumulated time the subagent was working.
- `running_since` (timestamp, nullable): set when work starts, cleared and
  folded into `active_seconds` when it stops.

Work starts when a headless process starts (not while queued) or when an
interactive turn opens (orchestrator or user). Work stops on terminal status
(done, failed, canceled, timeout, closed) and, for interactive runs, on
`idle` and `settling`. Live active time is
`active_seconds + (now - running_since)` while running.

`active_seconds` is added to the API record, `leo_wait` entries, and
`leo dispatch list/show` output next to the existing wall-clock elapsed value.

## Roster rendering

Every sweep, the dispatcher builds one roster per tmux session that hosts
dispatch windows (agent sessions `leo-<name>` and `leo-dispatch`):

```
⟳ reviewer 1:23   ⟳ impl 4:10   ⏸ explorer 0:41   … planner 0:00   ✓ tests 2:10   ✗ lint 0:41
```

- One entry per record whose window lives in that session, oldest first.
- Glyphs: `⟳` running, `⏸` idle (interactive, waiting for input), `…`
  queued, `✓` done or closed, `✗` failed / canceled / timeout.
- Label is the run name or template, sanitised as for window names,
  truncated to 16 characters. Time is `m:ss`, or `h:mm:ss` past an hour.
- Colours via tmux style markup, fixed constants: running default,
  idle amber, done green, failed red.
- Terminal entries follow their window: gone when the viewer window closes on
  collection, otherwise dropped after the same one-hour grace.
- Rendering is a pure function of records and `now` in
  `internal/consult/roster.go`; tmux application lives beside the viewer code.

## tmux mechanics

For a session with a non-empty roster, per sweep, only when the text changed:

```
set-option -t =<session>: @leo_roster "<text>"
```

The first time a session gets a roster, leo also sets the session options
`status 2` and `status-format[1] "#[align=left] #{@leo_roster}"`. When the
roster becomes empty, leo unsets those two session options (`set-option -u`)
so the session falls back to the user's global config, and clears
`@leo_roster`. Sessions that already have `status` ≥ 2 from user config are
left alone except for `status-format[1]`.

tmux redraws the status line when a referenced option changes; if the live
check shows stale counters, leo additionally sets a session
`status-interval 5`.

All tmux failures are logged and never affect the dispatch.

## Error handling

A missing session (caller restarted, `leo-dispatch` killed) drops the roster
for it silently. Records without a resolvable session are not shown.
Persisting the new fields follows the existing record write path.

## Testing

- `internal/consult`: active time accumulates across running → idle →
  running for interactive runs, freezes on idle and terminal states, and is
  not counted while queued; `roster.go` rendering table tests (glyphs, order,
  truncation, time formats, one-hour drop).
- tmux argv asserted through the stubbed seam: option set on change only,
  status options set on first roster, unset on empty roster, exact-match
  `=session:` targets.
- One `make e2e` case: dispatch a trivial run, assert `@leo_roster` on the
  live session contains the label and `status` reads 2; after collection the
  option is gone.
- Live verification before merge on an isolated daemon, then on production:
  counter advances while a codex interactive subagent works, freezes on idle,
  resumes on `leo_send_dispatch`, and the line disappears after the last
  run is collected.
