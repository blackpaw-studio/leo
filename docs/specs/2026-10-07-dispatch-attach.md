# Dispatch attach: a contract for leoterm

Date: 2026-10-07 · Status: implemented

leoterm (a client of the `/api/v1` observe API) shows dispatches and can attach
to an interactive dispatch's TUI. This spec is the contract between Leo and
that client. Every change is additive; `SnapshotVersion` stays 1. A client
checks the `features` list on the SSE `hello` before relying on a field.

| Feature | Meaning |
|---|---|
| `dispatch_attach` | `dispatches[].attachable` and `tmux_target` exist; `leo dispatch attach` works |
| `dispatch_removed` | the `dispatch_removed` SSE event exists |
| `state_seq` | `GET /api/v1/state` carries `meta.seq` |
| `attach_dispatch_placement` | `leo agent attach --dispatch-placement` steers where dispatch viewers open (see 2026-10-08-attach-dispatch-placement.md) |
| `dispatch_placement_live` | live interactive viewers follow their session's effective placement; `ViewerKind` can be `background` (see 2026-10-08-attach-dispatch-placement.md, "Live placement") |

## 1. Viewer placement `background`

`defaults.dispatch.viewer.placement` (and the per-session
`@leo_viewer_placement` override) accepts `pane | window | background`. The
default stays `pane`.

With `background`, every dispatch viewer opens as a detached window in the
`leo-dispatch` session and never touches the caller's tmux session: interactive
dispatches (nested ones too) and headless viewer windows alike. The recorded
`ViewerKind` is `window` (since `dispatch_placement_live`, an interactive
viewer in `leo-dispatch` is recorded as `background`), and the window is
attachable (section 2).

## 2. `attachable` and `tmux_target`

Each `dispatches[]` entry in `/api/v1/state`, and each `dispatch_changed`
payload, gains:

| Field | Type | Meaning |
|---|---|---|
| `attachable` | bool | `leo dispatch attach <id>` can show this dispatch now |
| `tmux_target` | string, omitted when empty | the interactive dispatch's tmux pane id (`%N`) on Leo's tmux server (`tmux -L leo`) |

`attachable` is true only for an interactive dispatch that has a pane, whose
`ViewerKind` is `window` or `background` (the TUI is alone in its own window),
and whose status
is not terminal (`done`, `failed`, `timeout`, `canceled`, `closed`, `released`)
or `settling`. A `split` pane (shares the caller's window) and a `hidden` pane
(parked) are never attachable. `tmux_target` is reported whenever the pane
exists, attachable or not. Both fields are derived from the recorded placement,
which the pane reconciler keeps current, so a move republishes
`dispatch_changed`. Go callers use `consult.Attachable(Record)`.

## 3. `leo [--host H] dispatch attach <id>`

1. Fetch the record (`GET /api/dispatch/<id>`) and check `consult.Attachable`.
   An unknown id, a headless run, an ended one (terminal, `closed`,
   `settling` aside), and a split or hidden pane each print one line to stderr
   and exit 1 (`SilenceUsage`/`SilenceErrors`; no tmux is touched).
2. Probe live that the pane's window has exactly one pane
   (`display-message -p -t %N '#{window_id} #{window_panes}'`); the record can
   be stale.
3. `new-session -d -s _watch-<id>-<rand>` (never a `leo-` prefix, which marks
   supervised agents), `link-window` the dispatch's window into it, `kill-window`
   the placeholder window `new-session` made, `set-option status off`.
4. `tmux -L leo attach -r -t =_watch-…` (`-r`: keystrokes never reach the
   dispatch; popup variant inside tmux). Panes are never moved or broken out.
5. Outside tmux the client runs as a child process (stdio inherited, its exit
   status becomes leo's), never an exec, so leo always gets to clean up. The
   watch session is killed after the client returns and on any failure before
   it, including a tmux that cannot start (such as stdin not a TTY), which
   would never arm the hook.

`destroy-unattached` is armed by a `client-attached` hook, not set up front.
Verified against tmux 3.6a on an isolated `TMUX_TMPDIR` socket: setting
`destroy-unattached on` on a session that has no client destroys it at once,
before the attach could start. With the hook, detaching destroys the session
(the dispatch window survives in its own session), and the dispatch's window
closing destroys it and the client exits 0.

Remote: `ssh -tt <host> <leo> dispatch attach '<id>'`, the id a single
shell-quoted token. The remote leo prints its own one-line reason; only its
exit status is propagated.

Known behavior: startup-dialog auto-dismiss skips sessions with an attached
client (`session_attached`). The watch session is a different session, so the
dispatch's own session still reads 0 attached.

## 4. `dispatch_removed` and `meta.seq`

`dispatch_changed` announces every change to an entry of `dispatches[]`; until
now nothing announced the entry leaving `/state` when its `DispatchLinger`
(60 s) expired, so a client that never refetched kept finished dispatches
forever.

- SSE `dispatch_removed`, payload `{id, seq, at}`: published once, by the 1 s
  dispatch tick, when a dispatch that was listed in `/state` no longer is: its
  linger expired, or its record left the store. A dispatch that was never
  listed (finished long before the daemon started) never produces one. Clients
  drop the entry; removing an unknown id is a no-op.
- `GET /api/v1/state` gains `meta: {seq}`: the event bus's last `seq` read
  *before* the snapshot is built, the same counter every SSE payload and the
  `hello` frame carry. The snapshot reflects every event with `seq <= meta.seq`
  and may already reflect later ones. The bus has no replay, so a client must
  subscribe first: open the SSE stream, then `GET /state`, and apply only
  events with `seq > meta.seq` (event payloads carry whole entities, so
  re-applying one is harmless). If the stream's `hello.seq` is greater than
  `meta.seq`, events may have fallen between the two, so refetch `/state`.
  `meta.seq` is `0` when the daemon has no event source.

Advertised as the `dispatch_removed` and `state_seq` hello features.

## 5. Nested dispatches: `parent_dispatch_id` and the root caller

A subagent's own MCP server knows its `LEO_DISPATCH_ID`, and `leo_dispatch`
sends it as `parent_dispatch_id` on `POST /api/dispatch`. `consult.Start`
verifies it names a known dispatch (an unknown or malformed id is dropped, the
dispatch just has no recorded parent) and then:

- stores it on the record as `ParentDispatchID`. It works for headless parents
  too, which have no bridge key to derive it from;
- sets the child's `Caller` to the parent's, so every dispatch in a tree has the
  root agent as its `caller_agent`, at every depth (a parent with no caller
  leaves the child's own). This is attribution only: viewer placement still
  uses the requester's own caller, so a nested interactive dispatch with no
  caller pane opens in the `leo-dispatch` session, never in the root agent's.

`dispatches[].parent_dispatch_id` is the *immediate* parent, as before. Records
written before this change still derive it from `caller_bridge_key`
(`dispatch.<id>`), which stays as the fallback. Attention holds are unchanged:
only a root agent's direct dispatches count toward its `outstanding.dispatches`.
