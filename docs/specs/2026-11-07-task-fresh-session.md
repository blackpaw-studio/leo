# Per-task fresh sessions

Status: approved by Evan 2026-11-07, with rotate-after-N-runs added
Requested by: assistant agent, for `inbox-calendar-watch`

## Problem

Oneshot tasks always resume the session stored under `task:<name>` in `sessions.json`. For a high-frequency polling task, that context grows without limit. Two problems follow:

- **Cost.** `inbox-calendar-watch` reached 79M cache-read tokens, about $1.90 per run, roughly $90/day.
- **Quality.** By 11/03 its runs had degraded into skipping their work and replying `NO_REPLY`.

The only fix today is to edit `sessions.json` by hand.

## Design

New task field:

```yaml
tasks:
  inbox-calendar-watch:
    session: fresh   # resume (default) | fresh
```

- **`resume`** (default): today's behaviour, unchanged. No migration needed.
- **`fresh`**: each run starts a new session.
  - The runner skips `sessions.Get` in both preview and run (`internal/run/runner.go:132-138` and `:249-256`).
  - It also skips `sessions.Set` (`:312`), so no key is left behind.
  - An existing `task:<name>` key is ignored, not deleted. Switching back to `resume` picks up the old session.
- **Why an enum and not a bool:** it leaves room for later modes, such as rotation, without a second field.
- **Harnesses:** works on all three. Each one already starts fresh when `SessionArgs` gets an empty ID.
- **Rotation:** `session_rotate_after: N` (integer, default 0 = never) applies only with `session: resume`.
  - The runner keeps a count of uses of the stored session (any non-interrupted attempt that returns a session ID counts, successful or not; a firing counts once even across channel-init retries), next to the `task:<name>` entry in the session store.
  - Before a run, if the count is at least N, the run starts fresh. Its new session ID is then stored with a count of 1.
  - Otherwise the run resumes the stored session, and the count goes up by one when the run completes.
  - Interrupted attempts don't count, matching today's skip of `Set`.
  - Any path that clears the session resets the count: retry clears and stale-session recovery.
  - Old `sessions.json` files with no count read as 0.
  - `Validate()` rejects a negative N, and rejects any N > 0 combined with `fresh` or `persistent`.
- **Persistent tasks:** `Validate()` rejects `session: fresh` on `runtime: persistent` with a clear error. Persistent tasks keep their session on an agent that may be shared through `template:`. Clearing it per firing is a separate design.
- **Wiring:**
  - `TaskConfig` field.
  - `Validate()` enum check.
  - Web schema field in the Execution group.
  - Config API.
  - `config-reference.md` and `persistent-tasks.md` (the persistent restriction).

## Tests

- Runner, fresh: no resume argument is passed, even when a key exists, and nothing is written back.
- Runner, resume: unchanged. Existing tests cover it.
- Rotation, N=3: runs 1–3 share one session and run 4 starts fresh. An interrupted run doesn't increment the count. A legacy store entry with no count reads as 0.
- Validate: rejects unknown values, `fresh` combined with `persistent`, a negative rotate value, and rotation combined with `fresh` or `persistent`.
- Config API round-trip.

## Out of scope (follow-ups)

1. **Rotation above a cost/token threshold.** Task history records no usage today (`history.Entry` has no tokens or cost, and `harness.Usage` isn't wired into `internal/run`). This needs a capture path first.
2. **A `leo doctor` / `leo validate` warning when a persisted task session is large.** Same blocker.
3. **Fresh-per-firing for persistent tasks.** For example, `/clear` before injection, implicit agents only.
4. **Defaulting high-frequency tasks to `fresh`.** Rejected: it's implicit, and it breaks the external users who rely on resume.
