# Chat asks that don't expire

Status: approved by Evan 2026-10-15

## Problem

`leo_chat_ask` asks expire on a daemon deadline (default 300 s, max 1 h,
`internal/chat/outbound.go`). After expiry an answer is rejected
(`ErrConflict`), and nothing delivers a late answer to the agent. A daemon
restart expires every open ask (`store.ExpireOpen`). Questions the user
gets to later are simply lost.

## Behavior

1. **No deadline by default.** An ask with `timeout_s` omitted (or 0) stays
   `open` until it is answered, withdrawn, or its agent is gone. A positive
   `timeout_s` keeps today's semantics, including the 1 h cap. The per-call
   `wait` still only bounds the call, which returns `pending` as today.
2. **Late answers are delivered.** When an ask is answered and no call is
   blocked waiting on it, the daemon delivers the answer to the asking agent
   as a normal chat message through the existing inbound delivery path. The
   message names the ask (`message_id`) and quotes the question and the
   chosen values or text. A suspended agent auto-resumes as for any other
   message. `leo_chat_ask` resume-by-`message_id` still returns the stored
   answer; a duplicate (answer delivered *and* resumed) is acceptable.
3. **Restart-safe.** The startup sweep no longer expires deadline-free open
   asks. Asks that do have a deadline get their timer re-armed, or expire
   immediately if the deadline has passed (this needs the deadline
   persisted). Resume-by-`message_id` reads state from the store when no
   in-memory waiter exists. It reports an open ask as `pending`, never as
   `timed_out`.
4. **Withdraw.** A new MCP tool, `leo_chat_ask_withdraw {message_id}`, lets
   the asking agent close its own open ask. It is rejected for another
   agent's ask, and a no-op error for an ask that isn't open.
5. **Agent gone.** When an agent is stopped (`leo agent stop`) or removed,
   its open asks close. Idle-suspend, crash-restart, graceful restart, and
   daemon restart do **not** close asks.
6. **Closed reason.** Withdrawn, agent-gone, and timed-out asks all
   transition to the existing `expired` state. They also carry a new
   additive field, `closed_reason`: `timeout` | `withdrawn` | `agent_gone`.
   It is exposed in the store, publish events, `/api/v1` chat payloads, and
   the web UI. No new state value, so existing clients keep working.
7. **Permission prompts are unchanged.** They keep `chat.permission_timeout`
   because a tool call is blocked on them.

## Clients

- Web UI: an open ask with no deadline renders as answerable with no
  countdown. An expired ask shows its `closed_reason`.
- iOS lives outside this repo. A follow-up note goes to the iOS
  project to handle a missing deadline and `closed_reason`.

## Docs

Update `docs/configuration/chat.md` (ask lifecycle, withdraw tool, late
delivery, restart behavior) and the leo_skill / MCP tool descriptions for
`leo_chat_ask`.

## Out of scope

- Reminders or nagging for long-open asks. Existing chat reminders may
  already cover this; no new work.
- Changes to permission-prompt timing.

## Testing

Behavior tests through the chat service's public API, with injected clock
and stubbed delivery:
- a default ask never expires
- a late answer is delivered once, and delivery isn't triggered while a
  waiter is blocked
- after a restart, a deadline-free ask stays open and a past-deadline ask
  expires
- resume after a restart returns the stored answer or `pending`
- withdraw succeeds for the owner and is rejected for others
- stopping the agent closes its asks; suspend doesn't
- `closed_reason` appears in the API payloads

Live check on an isolated test daemon: ask, restart, answer, and confirm
the agent receives the answer.
