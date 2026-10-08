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
