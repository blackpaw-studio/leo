# `leo agent worktree --template` — override the template, keep the source repo

**Date:** 2026-07-15
**Status:** Approved
**Extends:** `2026-07-15-agent-worktree-command-design.md`

## Problem

`leo agent worktree <agent> <branch>` inherits the source agent's template
verbatim. Sometimes you want a worktree on the same repo but built from a
*different* template — e.g. branch off the `chronicle` agent's repo but run it
under a `codex` template instead of `claude`. Today that requires the full
`spawn owner/repo --worktree` form and re-specifying the repo.

## Command

```
leo agent worktree <agent> <branch> --template <name> [existing flags]
```

`--template` is optional. Omitted → today's behavior (inherit the source's
template). Set → the named template builds the new agent, but the git canonical
and worktree placement stay tied to the source agent.

## Semantics

- **Git canonical: always the source's.** Unchanged from the base command —
  `src.CanonicalPath` (worktree source) or `src.Workspace` (otherwise). The
  override template's own `repo`/workspace settings are never used to pick the
  repo.
- **Worktree placement: tied to the source.** The `.worktrees/<agent>/<slug>`
  base workspace is derived from the *source* template, so an override with a
  different pinned `workspace` does not relocate the worktree away from the
  source agent's other worktrees. Agent name stays `<agent>-<branch-slug>`.
- **Template resolution:** `--template` must exist in `cfg.Templates`
  (distinct error from the source-template-missing case). Model, harness,
  harness args, and permission mode all re-resolve from the override template.
- **Env: no inheritance when overriding.** The stored source record's env is a
  flattened blob (`harness + source-template + per-spawn`), so its keys cannot
  be cleanly attributed. When `--template` is set, the new agent inherits
  **none** of the source agent's env — it gets only the override template's own
  env plus any `--env` flags. Without `--template`, env inheritance is
  unchanged (full stored blob, harness keys pruned, `--env` wins).
- **Record:** the persisted record's `Template` reflects the template that
  actually ran (the override when given). `Repo` still copied from the source.

## Implementation

- **Manager (`spawnFromAgent`):** resolve `runTmpl` from `spec.Template` when
  non-empty (error `template %q not found` if absent), else the source
  template. Keep `base` derived from the source template. Set `inheritEnv` to
  `src.Env` only when no override; `nil` when overriding. Pass `runTmpl` to
  `spawnWorktreeCore`; set `spec.Template` to the resolved run-template name so
  the record is accurate.
- **`Spawn` routing guard:** allow `FromAgent` + `Template` together; still
  reject `FromAgent` + `Repo`.
- **Daemon:** no change — `AgentSpawnRequest.Template` already passes through to
  `SpawnSpec.Template`.
- **CLI:** add `--template` flag to `leo agent worktree`; include it in the
  local request and the remote-forward argv.

## Testing

- Manager: override happy path — asserts the new agent's args/model come from
  the override template, the canonical + worktree path match the source, and
  **no** source env is inherited (a source-only env key is absent, an `--env`
  key is present). Unknown override template → error. `FromAgent`+`Repo` still
  rejected. Default (no override) path unchanged — existing tests still green.
- CLI: `--template` forwarded in local request shape and remote argv.
- Docs (`docs/cli/agent.md`, `docs/guides/agents.md`) updated with the flag and
  the env caveat.
- `make test`, `make lint`, `make e2e`.
