# Per-attach dispatch placement

Status: draft, awaiting approval. Requested by leoterm on Evan's behalf, 2026-10-08.

## Problem

Viewer placement is set per daemon (`defaults.dispatch.viewer.placement`) or per session (`@leo_viewer_placement`). leoterm and a plain terminal attach to the same agent session. leoterm wants `background`, and the terminal wants `pane`. Neither existing knob can tell them apart.

## Shape

1. `leo agent attach` and `leo attach` take `--dispatch-placement pane|window|background`. The flag works with and without `--cc`, locally and remotely.
2. Before it execs tmux, the attach registers `{session, pid: os.Getpid(), placement, registered_at}` with the daemon over IPC. exec keeps the PID, so the registered pid is the tmux client's `#{client_pid}`. Nothing unregisters explicitly. An entry counts only while a live client in that session has that pid and `client_created >= registered_at` (guards against PID reuse). Entries that fail this check are pruned lazily.
3. Remote paths with the flag set always go through the remote leo, so the registration happens on the daemon's host:
   - non-cc (already the case today): `ssh -t host leo agent attach --dispatch-placement X <name>`
   - cc (new): `ssh -tt -e none host leo agent attach --cc --dispatch-placement X <name>`. The remote leo has no `$TMUX`, so it execs `tmux -CC` and does not take the popup path.

   Without the flag, the remote cc path keeps its current raw `ssh … tmux -CC attach`.
4. The popup path (`--cc` inside an outer tmux) and `asChild` reject the flag with a clear error. Neither is a leoterm or terminal use case.
5. Resolution happens at dispatch start, before today's `ResolveViewerPlacement`. Run `list-clients -t <root caller session> -F '#{client_pid} #{client_created}'`. Each client contributes its registered placement, or the session/config default if it has none. The most visible wins: pane > window > background. With zero clients, keep today's behavior exactly. The winner then feeds the existing coordinator, so the max-panes and caller-pane fallbacks still apply.
6. Nested dispatches resolve the client set against the root caller's session. Today they use the requester's own caller.
7. Feature flag: `attach_dispatch_placement` in the SSE `hello` (`internal/observe/event.go`). leoterm gates on it.

## Impact on leoterm

leoterm currently runs `ssh -tt -e none host tmux -CC -L leo attach`. To opt in, it switches to `ssh -tt -e none host leo agent attach --cc --dispatch-placement background <agent>`. The control-mode stream is unchanged, since leo execs the same tmux.

## Rejected alternatives

- Infer from `client_flags` control-mode: this would also catch iTerm `-CC` users.
- Tag through TERM or the env of the tmux client: fragile, and reading another process's env on macOS is not reliable.
- Explicit register/unregister calls: these leak on crash. PID-plus-liveness needs no cleanup.

## Tests

- Resolver: zero clients → today's result. Mixed flags → most visible wins. Unflagged client → default. Stale pid or `client_created` before `registered_at` → ignored.
- Attach argv: remote cc with the flag routes through remote leo, and the flag is forwarded. Popup path with the flag → error.
- Nested: the root session's clients drive placement.

## Live placement (follow-up, v0.42)

Status: implemented. Resolution used to happen once, at dispatch start. Now a
flip of a session's effective placement re-places its live interactive viewers.

1. **Detect.** `Dispatcher.PollPlacement` runs once a second, only while some
   interactive viewer is live. It issues one `list-clients -F '#{client_pid}
   #{client_created} #{session_id} #{session_name}'` across all sessions
   (`tmux.ListAllClients`) and, per root caller session, computes
   `AttachPlacements.Effective(clients, listedAt, fallback)`: the most visible
   placement, `""` for no clients. Polling, not tmux hooks: hooks are global to
   the leo tmux server, which production and test daemons share.
2. **Flip.** Only `background` <-> visible counts. The new state must hold for 2
   polls (debounce). `""` is never a flip, and `pane` <-> `window` moves nothing.
   Each run carries the placement it was last steered to; a flip steers every run
   in the root's tree (queued and launching ones included) and bumps the root's
   generation.
3. **Launch race.** A launch reads the generation before resolving its
   placement; if a flip committed while it resolved, the flip's placement wins
   over the (possibly older) resolved one. A flip landing after the decision is
   corrected by the reconcile the pane's publish triggers.
4. **Move.** Through the per-run pane-op worker, so it orders with publish, kill,
   hide and show. Going background: `break-pane` (split) or `move-window` (pane
   alone in its window; never the last window of a session) into `leo-dispatch`.
   Coming back: `join-pane` below the live caller pane when it is in the root
   caller's session and `Coordinator.Decide` allows a split, else `move-window`
   into the root caller's session (record `hidden` when idle, else `window`).
   Pane ids survive. Parents move before children.
5. **Reconciler.** While the session is background the desired placement is
   `background`: orchestrator turns do not rejoin a pane and idle panes are not
   hidden. A user-typed `needs_input` turn still moves nothing.
6. **Pinning.** Before a move the pane's live session is read
   (`list-panes -a`, ignoring `_watch-*` attach links). A pane in neither the
   caller's session nor `leo-dispatch`, or linked into several real sessions,
   is pinned and never touched again.
7. **Record.** `ViewerKind` gains `background` (a window of its own in
   `leo-dispatch`); `leo dispatch attach` treats it like `window`.
8. **Feature flag.** `dispatch_placement_live` in the SSE `hello`.
9. **Phase 2.** Headless watch-viewer windows are keyed by window id, not pane
   id, and are not moved yet.
