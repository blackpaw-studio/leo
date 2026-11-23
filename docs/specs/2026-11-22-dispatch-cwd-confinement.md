# Dispatch cwd confinement

Status: approved 2026-11-22 (Evan). Follow-up before release: cross-workspace read grants.

## Problem

`leo_dispatch` accepts any existing absolute `cwd` (`internal/consult/consult.go`). The child
harness's sandbox is rooted there, so an agent whose own harness config confines it to its
workspace can escape that confinement by dispatching into another agent's workspace (or
anywhere on disk). Harness configs cannot close this: the daemon chooses the child's cwd.

Audit of 1,216 dispatch records (2026-11-22): ~70 root dispatches ran outside the caller's
workspace. Most were legitimate same-repo checkouts (implementer worktrees, sibling Git
worktrees, the caller's scratchpad). ~10 targeted another agent's workspace (e.g. marketing →
leo). One agent dispatched outside `~/.leo` (Evan will move that repo under its workspace).

## Rule

A dispatch's requested `cwd` (the source cwd, before `isolation: worktree`) is **allowed** when,
after resolving symlinks, it is any of:

1. inside the root caller's workspace;
2. inside a Git worktree that shares the workspace repo's Git common dir;
3. inside a Leo dispatch worktree created by a dispatch in the same root caller's tree;
4. inside the root caller's harness scratchpad / temp dir (e.g. Claude's
   `/private/tmp/claude-<uid>/<workspace key>/…`).

"Root caller" is the top-level agent or task: nested dispatches are judged against the root's
workspace, not the parent dispatch's cwd. Continuations (`leo_send_dispatch`) reuse an already
validated cwd and are not re-checked.

Callers with no resolvable workspace (the `leo dispatch` CLI run by a human, API clients such as
leoterm-ios) are exempt.

## Modes

`defaults.dispatch_confinement: off | warn | enforce`, default `warn`.

- `off`: today's behavior.
- `warn`: dispatch proceeds; a warning naming the rule is returned in the dispatch result's
  warnings and logged by the daemon.
- `enforce`: dispatch is refused with an invalid-request error that names the cwd, the caller's
  workspace, and the allowed roots.

No per-template override and no extra-roots allowlist. Exposed in config validation, the
config schema/web form, and docs. Dionysus will run `enforce`.

## Out of scope

- Cross-workspace read grants (`share.readable_by`, read-only roles) — next PR, before release.
- Other path-taking tools (`leo_chat_post{files}`): low impact, separate follow-up.
- Caller-identity spoofing (an agent calling the daemon socket directly with another `from`):
  same class as the #334 parent-id follow-up; record as an accepted risk.

## Acceptance

- Table tests cover each allowed root, symlink escapes (`ln -s ../other inside/`), `..`
  traversal, nested dispatch judged by root, exempt callers, and all three modes.
- `make test`, `make lint`, `make e2e` (isolated env per memory) pass.
- Live check on an isolated test daemon: `warn` returns the warning; `enforce` refuses
  marketing-style cross-agent cwd and allows a same-repo worktree.
