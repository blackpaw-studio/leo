# `leo agent move` / `leo agent copy`: on-demand agent handoff and fork between hosts

Status: approved by Evan 2026-10-27; open question resolved (see "Decisions").
Copied from ai-config (commit 579bf73), and this copy is the source of truth.
Companion: `2026-10-27-multi-machine-config.md` in ai-config (paused 2026-10-27 pending a
Leo-native sync brainstorm). This feature does not depend on it: the destination preflight
refuses when something is missing.

## Goal
`leo agent move <agent> --to <host>` stops an agent on this machine and resumes the
same conversation, in the same workspace state, on another Leo host. Moves happen
on demand, back and forth, and send only what changed since the last move.
Harness-generic: Claude must work in v1. Codex and opencode get adapters where their
session storage allows resuming on another host. A harness with no adapter refuses
the move with a clear error.

## Non-goals
- Live sync or background replication.
- Anything specific to one person's setup (claude-mem, custom memory stores). Those
  plug in through hooks.
- Hosts with a different home path or username. Leo refuses the move.

## Invariant: one live home per agent
After a move, the source record is marked dormant with `moved_to: <host>`. Leo refuses
to start, wake or deliver to it there (persistent-task delivery included) and points
at the destination. `leo agent move <agent> --to <source>` from the destination brings
it back and clears the marker. Because only one copy is ever live, the pushing side
is always authoritative.

## Harness adapters
Each harness provides a small adapter: (a) the paths holding this agent's conversation
and project memory, given its workspace, session ID and env; (b) how to resume that
session on start (already exists for Leo's own resume). The workspace transfer, hooks
and record handling are shared.
- **Claude (v1, required):** paths as below.
- **Codex (if feasible):** session rollouts under `$CODEX_HOME/sessions/**/rollout-*-<id>.jsonl`;
  resume by session ID. The planner verifies Codex can resume a rollout copied from
  another host.
- **opencode (if feasible):** session storage under its data dir. The planner verifies
  whether a single session can be extracted and resumed elsewhere; if it is a shared
  database that can't be split per session, opencode stays unsupported.

## What Leo moves (Claude adapter shown)
1. **Workspace.** rsync over SSH of the agent's workspace, including `.git`,
   uncommitted and untracked files, with `--delete` (the source is authoritative).
   Only changed files are sent. Template-level excludes are allowed
   (`move.exclude`, for example `node_modules`, build output).
2. **Conversation.** The agent's Claude session transcript(s) for that workspace
   (`<claude config dir>/projects/<encoded workspace path>/<session>.jsonl` plus any
   sidecar dirs), sent with rsync `--update` (a newer file on the destination is never
   overwritten), no delete.
3. **Project memory.** `<claude config dir>/projects/<encoded path>/memory/`, sent with
   rsync `--update`, no delete.
4. **Agent record.** Name, template, repo, workspace, environments, session ID and other
   state needed to resume. The destination creates or updates the record and then
   `start`s it, which resumes the session.

The Claude config dir is resolved per agent from its env (`CLAUDE_CONFIG_DIR`) on both
ends. If the destination lacks that dir, the move fails before stopping anything.

## Hooks (the extension point)
`leo.yaml` (or the shared templates file, per template):
```yaml
move:
  hooks:
    pre_send:    [~/bin/claude-mem-export.sh]   # runs on source after stop
    post_receive: [~/bin/claude-mem-import.sh]  # runs on destination before start
```
Each hook gets env: `LEO_AGENT`, `LEO_WORKSPACE`, `LEO_SESSION_ID`, `LEO_MOVE_FROM`,
`LEO_MOVE_TO`, `LEO_MOVE_DIR` (a staging dir that Leo rsyncs from source to
destination between the two phases), and `LEO_MOVE_LAST` (timestamp of the previous
move of this agent between this pair of hosts, empty on the first). A nonzero hook
aborts the move. Evan's claude-mem export/import is a hook script in his own config,
not Leo code.

## `leo agent copy`: fork instead of hand off
`leo agent copy <agent> [--to <host>] [--name <new>]` creates an independent second agent
that starts from the same conversation and workspace state. Both stay live and diverge
from then on. Nothing ever syncs back.
- **Shared with move:** preflight, adapters, transfer and hooks (`LEO_MOVE_MODE=copy` is
  passed to hooks). `--to` defaults to the local host. The source keeps running, with
  no stop and no `moved_to` marker. If the source is mid-turn, the copy takes the
  transcript as of the last completed turn.
- **New identity:** the copy gets a new agent name (default `<agent>-copy`, or `-copy-2`
  and so on if that is taken) and a new workspace path (`<workspace>-<newname>` on the
  destination; never the source's path, even on another host, so a later move can't
  collide). Its conversation is forked, not shared: Claude resumes with `--fork-session`,
  and Codex uses `codex fork`, so each agent writes its own session from then on.
  A harness that can't fork refuses the copy.
- **Git:** the copied workspace is put on a new branch `<current-branch>-<newname>`
  (uncommitted changes carried over as-is), so the two agents never push to the same
  branch. `--branch <name>` overrides it.
- **Memory:** project memory is keyed by workspace path, so the copy gets a new memory
  dir, seeded from the source's. It's an independent snapshot after that.
- **Persistent tasks:** an agent backing a `runtime: persistent` task can be copied,
  but the copy is not wired to the task.

## Flow and failure handling
1. Preflight, with nothing stopped yet: destination reachable via `client.hosts`, same
   home path, template handling per open question 1, Claude config dir exists, the agent is not
   `moved_to` elsewhere, and the agent is idle (no turn running). Pass `--force` to stop
   it mid-turn.
2. Stop the source agent.
3. Run the `pre_send` hooks, then transfer (workspace, conversation, memory, staging dir).
4. Over SSH on the destination: `post_receive` hooks, then create/update the record and
   start it.
5. Only after the destination start succeeds, mark the source `moved_to`.
Any failure before step 5 leaves the source dormant but unmarked, so `leo agent start`
locally recovers it, and the error says so. Re-running the move is safe because every
transfer is incremental and idempotent.

## Surfaces
CLI `leo agent move` / `leo agent copy`, MCP tools `leo_agent_move` / `leo_agent_copy` (so an agent or Rocket can trigger a
move from chat), and `moved_to` shown in `leo agent list` and the web UI.

## Acceptance
Move a running Claude agent (and, for each adapter shipped, a Codex/opencode agent) with uncommitted changes from Dionysus to the laptop: it
resumes the same conversation there with identical workspace state, and the Dionysus
copy refuses to start. Work on the laptop, then move back: only changed files transfer,
and the conversation includes the laptop turns. A failing `pre_send` hook leaves the
agent recoverable on the source. Copy an agent to the laptop: both agents run at once,
each on its own branch and its own session ID, and a turn in one never shows up in
the other.

## Decisions
1. **Template or environment missing/different on the destination** (decided by Evan
   2026-10-27): **refuse.** `move`/`copy` preflight on the destination and fail with a
   clear error when the destination lacks the agent's template or any environment it
   uses. Fix gaps on the destination itself (shared templates file, or by hand). No
   template snapshot, no `--with-template`, no sync in this feature. When the destination
   has a template of the same name, the destination's version is used as-is. Cross-host
   sync of templates and environments is a separate future feature (to be brainstormed
   in the leo repo).
