# Dispatch viewer panes: subagents tiled under the caller's TUI

Extends `docs/specs/2026-09-13-subagent-dispatch.md` (headless viewers) and
`docs/specs/2026-09-14-interactive-dispatch.md` (interactive panes).
Companion spec: `2026-09-15-dispatch-settings-menu.md` builds on this one.

## Goal

By default, a dispatched subagent's viewer (headless log tail or interactive
harness TUI) opens as a pane split beneath the caller's pane, and the window
is kept in tmux's `main-horizontal` layout: the orchestrator's TUI on top,
subagents tiled beneath it, every TUI visible at once. Today every viewer is
a separate window (`new-window`) that must be switched to.

## Non-goals

- Changing what runs inside a viewer, how a run is collected, or what
  `leo_wait` returns.
- Any placement for callers that are not in leo's tmux server (oneshot
  tasks, `leo_consult` from outside tmux). They keep the `leo-dispatch`
  session and window behaviour unchanged.
- Shrinking or collapsing finished panes. A pane is open or closed.
- The in-tmux settings menu (companion spec).

## Design

### Config

`defaults.dispatch.viewer` (new; validated in `Config.Validate`):

| Field              | Type   | Default | Constraint            |
|--------------------|--------|---------|-----------------------|
| `placement`        | string | `pane`  | `pane` or `window`    |
| `max_panes`        | int    | `3`     | 1..6                  |
| `main_pane_height` | int    | `60`    | 20..90 (percent)      |

`placement: window` reproduces today's behaviour exactly.

### Session overrides

Per-session overrides live as tmux user options on the caller's session,
not in the daemon or in `leo.yaml`:

- `@leo_viewer_placement` (`pane` | `window`)
- `@leo_viewer_max_panes` (`1`..`6`)

Unset or unparseable values fall back to config. Nothing in this spec
writes them; the companion spec's menu does. They are read with one
`show-options -s -t <session>` call per placement decision, following the
`@leo_primary_pane` precedent in `internal/service/process.go`.

### Placement resolver

One resolver in `internal/consult`, shared by the headless viewer
(`viewer.go`) and the interactive runtime (`interactive_runtime.go`):

```
Resolve(rec, overrides, cfg) -> Placement{Kind: split|window, Target: string}
```

Rules, in order:

1. No `CallerPaneID` on the record, or the caller resolves to the
   `leo-dispatch` fallback session: `window`.
2. Effective placement (override, else config) is `window`: `window`.
3. Live viewer panes for this caller session (records not terminal, or
   interactive and not yet released, with a pane id in the caller's window)
   `>= max_panes`: `window`.
4. Otherwise `split` targeting `CallerPaneID`.

The pane count uses dispatch records, not tmux, so a manually killed pane
still counts until its record is terminal; that is acceptable.

### tmux commands

Split (both runtimes, replacing their `new-window` call only in the
`split` case):

```
tmux -L leo split-window -d -P -F '#{pane_id}' -t <CallerPaneID> \
     [-c <cwd>] [-e K=V ...] <command>
tmux -L leo select-pane -t <new pane> -T '<label>·<hex4>'
tmux -L leo set-option -w -t <CallerPaneID> main-pane-height <N>%
tmux -L leo select-layout -t <CallerPaneID> main-horizontal
```

A pane target resolves to its window for `set-option -w` and
`select-layout`.

`-d` keeps focus on the caller's pane. The pane title carries the same
label the window name carries today so the roster and `leo dispatch list`
stay meaningful.

If `split-window` fails (tmux minimum size, pane gone), the resolver's
caller falls back to `window` in the same call and records that it did so
on the record (`ViewerKind: window`).

Layout re-apply (`select-layout main-horizontal` on the caller's window)
happens after every viewer create and every viewer close in that window.
It is never applied to other windows.

The record gains `ViewerKind` (`split` | `window`) so close paths and the
roster can tell which they are handling. Headless split viewers persist the
pane id in a new `ViewerPaneID` field; window viewers keep `ViewerWindowID`.
`remain-on-exit` is set on the pane as it is on the window today.

### Close rules

Headless, unchanged in policy: close (`kill-pane` for split, `kill-window`
for window) on successful collection; failed, canceled and timed-out
viewers stay for the one-hour grace period and are swept.

Interactive: a pane closes on any of

1. `leo_release {id}` (new MCP tool) or `leo dispatch release <id>` (new
   CLI, over the daemon HTTP API). Allowed on an `idle`, `done`, `failed`
   or `canceled` interactive run. It marks the record `released`, kills the
   pane, and re-applies the layout. Releasing a `running` turn is an error.
2. `leo_cancel` (existing behaviour, plus layout re-apply).
3. The one-hour idle close (existing).
4. The caller's tmux session no longer exists, checked by the existing
   sweep. This covers an orchestrator whose session was stopped.

The dispatch preamble that leo injects for implementers gains one line:
"When the orchestrator has finished with you, it releases this pane."
`internal/templates` CLAUDE.md guidance for orchestrators gains: release
interactive dispatches with `leo_release` once review passes.

### Roster

The status-line roster (`roster_apply.go`) is unaffected in content. It
must not count released runs as live.

### Fallbacks

Unchanged window behaviour for: no caller pane, foreign tmux socket,
`placement: window`, cap reached, split failure. tmux 3.2 (already the
prereq minimum) supports every command used here.

## Testing

- Unit: resolver table tests over the four rules, with overrides present,
  absent and malformed.
- Argv assertions on both runtimes: the exact `split-window`,
  `select-layout` and `kill-pane` argv, and that the `window` fallback argv
  is byte-identical to today's. Mocked exec seams have hidden argv bugs
  before; every tmux call this spec adds is asserted, not just stubbed.
- `leo_release` handler tests: allowed statuses, error on `running`,
  idempotent on already-released.
- e2e (`make e2e`, real tmux): dispatch from a pane, assert the new pane is
  in the caller's window, layout is `main-horizontal`, cap spills the
  fourth run to a window, release kills the pane and the window's pane
  count drops.

## Rollout

Single PR. Default is `pane`, so existing users see the new behaviour on
upgrade; `placement: window` is the one-line opt-out.
