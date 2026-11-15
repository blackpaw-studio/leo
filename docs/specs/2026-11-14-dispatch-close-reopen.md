# Dispatch panes close when done; follow-ups reopen them

Evan, 2026-11-14.

## Problem
- Implement dispatches default to `release_on_finish: false`, so their panes stay open after the result is delivered.
- Orchestrators rarely call `leo_release`, so the panes sit idle until the 1h auto-close.
- That close sends a notification which pastes the last turn's result into the caller's session a second time. This wastes tokens and is confusing.

## Decision
Every interactive dispatch pane closes once its turn's result has reached the caller. A follow-up to a closed run reopens it transparently.

## Behaviour
1. **Close on delivery.**
   - Applies to every role: implement, plan, review* and explore.
   - Once a turn finishes and its complete, untruncated result has been delivered (by notification or `leo_wait`), the pane closes.
   - A truncated result, or a run that is waiting or needs_input, keeps its pane, as today.
   - Closing the pane does not end the run. The run's record, captured harness session id, and cwd or worktree are all kept.
2. **Reopen on follow-up.**
   - Applies when `leo_send_dispatch {id, message}` targets a run whose pane closed this way.
   - Leo relaunches an interactive pane for the same template and environment, in the same cwd or worktree, resuming the captured session (claude `--resume`; codex and opencode use their equivalents, or else return a clear "not resumable" error).
   - The message is delivered as the next turn and returns a `turn_id` as today.
   - The reopened run counts against the concurrency cap like a new launch, and queues if the cap is full.
   - A session that can't be resumed (transcript gone, account mismatch) returns a clear error. It must never silently start a fresh session.
3. **`leo_release` means "done with this run".**
   - It drops the retained worktree or workspace, as release does today, and makes the run terminal. A later follow-up is then rejected.
   - Runs that are closed but never released are swept by a retention TTL (planner: reuse the existing worktree prune or TTL if there is one, otherwise default to 7 days). Uncommitted work is never discarded without the existing safeguards.
4. **Notifications.**
   - A pane close never re-sends a result that was already delivered.
   - Closing because the result was delivered is silent.
   - The idle auto-close, which now applies only to panes still open (waiting, needs_input, truncated), sends a one-line notice with no output if the result was already delivered.
   - When a run is reopened, the reopened turn's result is delivered once, as normal.
5. **Unchanged.**
   - Headless dispatches; they already end.
   - `release_on_finish: false` stays as an explicit opt-out that keeps the pane open.
   - `leo_consult`.
   - The roster/API shows closed-but-resumable runs distinctly, for example `closed` with `resumable: true`, so leoterm can render them. This is an additive field.

## Docs
- docs/configuration/dispatches.md.
- The leo_dispatch / leo_send_dispatch / leo_release tool descriptions.
- The delegation guide text that says implement runs must be `leo_release`d by hand. That text lives in ai-config (`instructions/claude-coding.md`); flag it for a follow-up edit.

## Acceptance
- An implement dispatch's pane closes right after its result notification. No 1h close notice follows.
- `leo_send_dispatch` to that run reopens a pane in the same worktree with the prior conversation, and the fix-round result arrives once.
- `leo_release` then removes the worktree, and a further follow-up is rejected.
- A truncated result keeps the pane until it is collected.
