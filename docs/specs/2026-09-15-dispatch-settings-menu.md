# Dispatch settings menu: toggle viewer behaviour from inside tmux

Builds on `docs/specs/2026-09-15-dispatch-viewer-panes.md` (viewer panes,
config defaults, tmux user-option overrides). Ships after it.

## Goal

From any pane in leo's tmux server, `prefix + L` opens a native tmux menu
(`display-menu`) that toggles viewer placement and the pane cap for the
current session, closes finished viewers, and can persist the session's
settings as the `leo.yaml` default. No TUI code; tmux renders the menu.

## Non-goals

- A general settings editor (models, templates, tasks). The web UI is that.
- `display-popup`. If the menu outgrows four entries, a popup running a
  future `leo settings` TUI replaces it; not now.
- Support for `tmux -CC` control mode, which does not render menus. The
  binding is a no-op there.
- Bindings on any tmux server other than leo's (`-L leo`).

## Design

### Key binding

Installed imperatively when the daemon ensures its foreground tmux server
(`internal/tmux/server.go`, next to `EnsureForegroundServer`), and
re-installed on daemon start so a restarted daemon on a surviving server
still has it:

```
tmux -L leo bind-key -T prefix L run-shell \
  "<leo> --config <config> dispatch viewer menu --session '#{session_name}'"
```

`L` is unbound in stock tmux. The binding is not configurable in v1.

### Menu

`leo dispatch viewer menu --session <name>` reads the session's current
overrides (falling back to config) and runs one `display-menu`:

```
tmux -L leo display-menu -t <session> -T ' leo · <session> ' \
  'Viewer placement: panes  → windows' p "run-shell '<leo> dispatch viewer set --session <s> placement=window'" \
  'Max panes: 3'                       m "run-shell '<leo> dispatch viewer set --session <s> max_panes=<next>'" \
  ''                                                                                                      \
  'Close finished viewers'             c "run-shell '<leo> dispatch viewer close-finished --session <s>'" \
  'Save as default'                    s "run-shell '<leo> dispatch viewer save-default --session <s>'"
```

Entry text reflects the current value and what the key will change it to.
Placement toggles between the two values. Max panes cycles 1→2→…→6→1.
The menu closes after one action; pressing `prefix + L` again shows the
updated state.

### Commands

New `leo dispatch viewer` subcommand group (`internal/cli`), all local-only
(they act on leo's tmux server and the local config; remote hosts are out
of scope):

| Command                                  | Effect                                                                 |
|------------------------------------------|------------------------------------------------------------------------|
| `menu --session S`                       | Renders the menu above.                                                |
| `set --session S key=value`              | Validates and writes `@leo_viewer_<key>` on session S.                 |
| `close-finished --session S`             | Daemon HTTP call: for every dispatch whose caller session is S and whose status is terminal, or interactive and idle with no running turn, release/close its viewer and re-apply the layout once. |
| `save-default --session S`               | Reads S's overrides, merges over `defaults.dispatch.viewer`, saves via the existing validate → `config.Save` → reload path (`validateAndSave` + `reloadConfigOrWarn` equivalents exposed through the daemon HTTP API), then clears S's overrides so config is the single source again. |

`set` and `save-default` reject unknown keys and out-of-range values with
the same rules as `Config.Validate`. Errors surface via
`tmux display-message` on the session so a failed action is visible.

`close-finished` reuses the release path from the viewer-panes spec; it is
the only entry that touches the daemon's dispatch state.

### Interaction with the resolver

No change. The resolver already reads the user options this menu writes.
A toggle takes effect on the next dispatch; existing panes are not moved.

## Testing

- Argv assertions: the `bind-key` install line, the `display-menu` argv
  for each combination of current values (placement × cap) so entry text
  and next-values are exact, and the `set-option` argv `set` produces.
- `set` validation table: valid keys/values, rejected ones, error routed
  to `display-message`.
- `close-finished` handler test: terminal headless, idle interactive,
  running interactive (left alone), and a different caller session (left
  alone).
- `save-default` round-trip test on a temp `leo.yaml`: overrides land in
  `defaults.dispatch.viewer`, options cleared, invalid override rejected
  without writing.
- e2e (`make e2e`): install binding on a real server, invoke the menu
  command, assert `show-options` reflects a `set`, and that a subsequent
  dispatch honours it.

## Rollout

Single PR after the viewer-panes PR merges. Documented in
`docs/configuration/` alongside the dispatch docs with the key binding and
the four entries.
