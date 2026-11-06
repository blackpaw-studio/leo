# Agent move/copy: fail fast on a missing transcript, `fresh_session`

Status: approved by Evan 2026-11-05.

## Problem

Claude Code deletes transcripts after 30 days. Moving or copying an agent whose
resume transcript is gone creates a job, runs validate (config, git, RuntimeEnv),
and only then fails with `session_missing`. The job's error message repeats the
code and the caller learns nothing actionable.

## Design

1. **Fail fast.** Move and copy requests run the transcript check synchronously,
   before any job exists. It sits in `agentmove.Jobs.Start`, the funnel for the
   daemon route `POST /agents/{name}/move`, the web route
   `POST /api/agent/{name}/move` and the MCP `agent_move` / `agent_copy` tools.
   On failure they answer **409** `{"error":"session_missing","message":"..."}`
   (the web envelope carries the same code). The message is a plain sentence:
   the agent's saved session transcript no longer exists; retry with a fresh
   session. No job is created and no agent is stopped. The in-job
   `checkTranscript` stays as a backstop with the same message.
2. **`fresh_session`.** Optional boolean on move and copy requests: JSON field
   `fresh_session`, CLI flag `--fresh-session`, MCP param `fresh_session`.
   Default false, so existing behavior is unchanged. When true:
   - the transcript check is skipped (early and in-job);
   - the manifest carries no source session id and the arriving record has an
     empty `session_id`, so the destination's first launch has no `--resume`
     and starts a new session. A copy does not fork: no `ForkFrom`, no pinned
     fork id, no transcript snapshot staged or placed;
   - workspace and git state move as usual.
3. **Feature flag.** `agent_move_fresh_session` is advertised in the `/state`
   features list so clients know the field is honored.

## Notes

- `fresh_session` also works when a transcript exists; it just ignores it.
- The manifest gains an optional `fresh_session` field the destination uses to
  skip a copy's transcript placement; the protocol number is unchanged, so a
  fresh copy needs a destination on the same leo version.
