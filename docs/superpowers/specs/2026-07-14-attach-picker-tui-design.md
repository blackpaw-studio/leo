# Attach Picker TUI — Design

Date: 2026-07-14
Status: Approved

## Summary

Replace the bare `promptui.Select` menu shown by `leo attach` (no agent name)
with a stay-open Bubble Tea picker: fuzzy search, all agent states visible
(including suspended), and per-agent lifecycle actions (rename, stop, suspend,
resume). Attach remains the primary action and exits the picker; every other
action runs in place and returns to the list. Remote hosts get full metadata
and full actions by dispatching to the remote `leo` binary over SSH.

## Goals

- Fuzzy filter-as-you-type across all agents (local + remote).
- Show suspended and stopped agents, not just running ones.
- Rename, stop, suspend, resume from inside the picker without exiting.
- Full action support for remote agents via `ssh <host> leo agent <op>`.

## Non-goals

- Logs viewing, reset, and spawn-from-template inside the picker (explicitly
  cut during brainstorming).
- Replacing promptui elsewhere in the CLI.
- A standalone dashboard command; entry point stays `leo attach` /
  `leo agent attach`.

## UX

### Entry

- `leo attach` / `leo agent attach` with no name opens the picker.
- With a name: direct attach, unchanged.
- **Behavior change:** the current single-candidate auto-attach is dropped;
  the picker always opens when no name is given, because it is now also the
  management surface. `leo attach <name>` remains the zero-friction path.

### List

Full-screen (alt-screen) list of all agents: local agents from the daemon
(all statuses) plus one group per configured `client.hosts` entry.

Row format: status glyph, name, template, host, uptime/state age.

```
  ● olympus      infra     local     2d4h
  ◌ blog-writer  writer    local     suspended 3h ago
  ● rocket       assistant hestia    6d1h
```

Glyphs: `●` running, `⟳` starting, `◌` suspended, `✖` stopped.

`/` starts fuzzy filtering (bubbles list built-in) across name, template, and
host. A footer shows keybindings (bubbles help component); a one-line status
bar shows action results and errors.

### Keybindings

| Key | Action |
|-----|--------|
| `Enter` | Attach. Running/starting: exit TUI and attach. Suspended: resume, then exit and attach. Stopped: status-bar message ("stopped — press u to resume"). |
| `s` | Suspend selected agent |
| `u` | Resume selected agent |
| `x` | Stop selected agent (inline `y/n` confirm) |
| `r` | Rename (inline text input pre-filled with current name) |
| `/` | Fuzzy filter |
| `q` / `Esc` / `Ctrl-C` | Quit without attaching |

After any lifecycle action: run it async, show the result in the status bar,
refresh that backend's rows, stay open. Only attach or quit exits.

### Remotes

- Each host is listed via `ssh <host> leo agent list --json` (new flag), so
  remote rows carry real status/template metadata.
- Actions dispatch as `ssh <host> leo agent <op> <name>`.
- Hosts are fetched concurrently with a ~5s per-host timeout; an unreachable
  host renders an error row instead of hanging the picker.
- Fallback: if the remote leo predates `--json`, fall back to the current
  `tmux -L leo list-sessions` enumeration for that host with attach-only rows
  (`AttachOnly: true`).

## Architecture

### New package: `internal/picker`

Bubble Tea model, decoupled from the daemon via injected interfaces:

```go
type Agent struct {
    Name, Template, Host, Status string
    StartedAt                    time.Time
    AttachOnly                   bool
}

type Backend interface { // one per host; "local" wraps the daemon client
    List(ctx context.Context) ([]Agent, error)
    Rename(ctx context.Context, oldName, newName string) error
    Stop(ctx context.Context, name string) error
    Suspend(ctx context.Context, name string) error
    Resume(ctx context.Context, name string) error
}

type Result struct{ Agent *Agent } // nil = quit without attaching

func Run(ctx context.Context, backends map[string]Backend) (Result, error)
```

Files (kept small, per coding style):

- `model.go` — tea model + Update
- `keys.go` — keymap
- `styles.go` — lipgloss styles
- `rows.go` — row building/sorting/glyphs
- `backend_local.go` — wraps `internal/daemon` client
- `backend_ssh.go` — SSH dispatch; reuses existing shell-quoting helpers
  (single-token quoting; see the known ssh argv-flattening gotcha)

### Data flow

1. `runAttachPicker` (in `internal/cli/attach_picker.go`) builds backends:
   local daemon + one SSH backend per `client.hosts` entry.
2. `picker.Run` starts the tea program. Initial load fans out `List()` calls
   concurrently as `tea.Cmd`s; rows stream in as hosts respond.
3. Actions dispatch as async `tea.Cmd`s — the UI never blocks; the acted-on
   row shows a spinner glyph until the result lands, then that backend
   re-`List`s.
4. **Attach happens after the tea program exits.** `Run` returns the chosen
   agent; `runAttachPicker` hands it to the existing `attachChosenSession`
   path so tmux gets a clean terminal. Suspended-agent attach performs
   `Resume` inside the TUI first, then exits and attaches.

### Dependencies

New: `github.com/charmbracelet/bubbletea`, `charmbracelet/bubbles`,
`charmbracelet/lipgloss`. promptui remains for other prompts.

### `leo agent list --json`

New flag on `leo agent list` emitting the `[]agent.Record` slice as JSON.
The SSH backend parses it and maps to `picker.Agent`.

## Error handling

- Action errors render in the status bar (red); the list stays usable.
- Rename validates non-empty + agent-name shape client-side before calling.
- Stop requires inline `y/n` confirmation.
- Daemon down at startup: fail fast with the existing "is leo service
  running?" style error before entering alt-screen.
- Remote host failures degrade to an error row (or tmux fallback), never a
  hang or a picker-wide failure.

## Testing

- The tea model is a pure `Update` function: unit tests drive it with
  key/result messages against a fake `Backend`. Cases: filter narrows rows;
  `s` on a running agent calls Suspend and refreshes; `x` requires confirm;
  rename round-trip; Enter on suspended resumes first; host-fetch failure
  renders an error row.
- SSH backend tested via the existing exec seam pattern (package-level
  `execCommand`-style var).
- No tmux required in unit tests (attach happens post-exit), keeping macOS CI
  green.
- E2E coverage for `leo agent list --json` behind the existing `make e2e`
  build tag.
