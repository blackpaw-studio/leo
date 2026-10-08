# Dispatch UX: no pane chores for Evan

Date: 2026-10-07 · Status: draft

## Problem

Interactive dispatches leak work onto Evan: approval prompts sit in panes the
orchestrator can't see, read-only reviewers can't run tests, a full limiter
rejects instead of queuing, panes clutter the caller's window, and our own
wording invites "go type in the pane". Native subagents have none of this.

## Goals

Evan never has to act inside a dispatch pane. Panes are optional to watch.

## 1. Approvals routed to the orchestrator (claude harness)

- Dispatched claude runs get a `PermissionRequest` command hook (merged into
  the existing dispatch `--settings`, next to the turn hooks). The hook posts
  the request (tool name, tool input summary, run id) to the daemon and
  long-polls for a decision.
- The run's status becomes `needs_input` with `{kind: permission, tool,
  summary, request_id}`. `leo_wait` returns early when any waited id enters
  `needs_input` (like a terminal state), and a notify=true run gets a
  notification.
- Orchestrator answers with `leo_send_dispatch {id, decision: allow|deny,
  reason?}`. `deny` reason is passed back to the subagent as the hook's deny
  message. A plain `message` on a `needs_input` run is rejected with a hint.
- Hook timeout (default 30 min, config `dispatch.approval_timeout`): hook exits
  with no decision, so the normal TUI prompt shows — the pane fallback.
  Decisions arriving after timeout are rejected as stale.
- Codex: out of scope (runs `-a never`; nothing blocks).
- Elicitation/question dialogs: out of scope this round.

## 2. Wording: panes are watch-only

- `internal/mcp/tools.go` leo_dispatch description, `agent-management.md`
  skill, `docs/configuration/dispatches.md`: interactive = "a TUI the user may
  watch"; follow-ups and approvals go through `leo_send_dispatch`; never ask
  the user to type in a pane.
- ai-config delegation guide (`instructions/claude-coding.md`): drop "Evan
  watches and steers"; same rule. Separate commit in that repo.

## 3. Reviewers can run tests without touching the repo

Codex `read-only` can't grant a writable temp dir; `workspace-write` always
makes cwd writable. So:

- New template field `isolation: worktree` (same meaning as the dispatch
  arg; dispatch arg overrides). Set it on `codex-reviewer` with
  `permission_mode: workspace-write`. Writes land in a throwaway worktree at
  committed HEAD; the real tree is untouched.
- Reviews therefore see committed state only — already our rule (implementers
  commit before review). The review prompt preamble states the worktree path
  and that uncommitted changes in the caller tree are not visible.
- `codex-explorer` stays `read-only` (no tests needed; must see uncommitted
  state).

## 4. Interactive dispatches queue

- When the limiter is full, an interactive dispatch is recorded `queued` (no
  pane), and a goroutine blocks on the slot, then resolves the caller window
  and launches — same as headless.
- `leo_cancel` on a queued run finishes it `canceled` with no pane work.
  `leo_send_dispatch` on a queued run is rejected ("queued; wait first").
- `leo_send_dispatch` follow-up "no capacity" was superseded (issue #232):
  follow-ups queue FIFO with new dispatches; see dispatches.md.

## 5. Panes only while working

- When an interactive run's turn ends (idle, done, failed), its pane is moved
  out of the caller's window with `break-pane -d` into a background window in
  the same session named `<label>·<last4>`.
- When an orchestrator turn starts on that run (`leo_send_dispatch`),
  `join-pane -d` returns it to the caller's recorded window (same split
  placement as launch); falls back to leaving it in its window if the caller
  window is gone.
- `needs_input` does not pull the pane back.
- User-typed turns (`steered`) don't move the pane.
- Pane lookup stays keyed by tmux pane id (`%N`, stable across break/join) —
  the moved-pane "composer busy" bug must not recur; test it.

## Testing

- Unit: hook ↔ daemon decision round trip (allow, deny+reason, timeout,
  stale decision); wait returns early on `needs_input`; queued interactive
  launch after slot frees, cancel while queued, send while queued; template
  `isolation` resolution and override.
- tmux argv asserted for break-pane/join-pane (memory: mocked exec seams hide
  argv bugs), plus a send-after-move test.
- Live: isolated test daemon (never restart production), real claude
  implementer hitting a prompt, approved from an orchestrator; reviewer
  running `go test` in its worktree; 7th interactive dispatch queues; pane
  leaves the window on idle and returns on follow-up.

## Order

2 (wording, small) and 3 (template isolation) are independent. 4 → 5 → 1
share `internal/consult/interactive.go` and land sequentially.
