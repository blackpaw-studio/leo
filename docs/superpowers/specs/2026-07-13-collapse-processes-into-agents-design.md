# Collapse "processes" into "agents"

**Date:** 2026-07-13
**Status:** Approved (design)

## Problem

Leo exposes two nearly-identical primitives:

- **Processes** (`leo service`, `processes:` in `leo.yaml`) — long-lived supervised
  harness instances, declared statically, auto-started at daemon boot, restart forever.
- **Ephemeral agents** (`leo agent`, `templates:` + `agents.json`) — long-lived
  supervised harness instances, spawned on demand from templates, persisted to the
  agent store, auto-restored on daemon boot.

These are the same thing wearing two hats. They already share the supervision engine:
`superviseProcess()` is one loop parameterized by `ProcessSpec.Kind` (`KindProcess` vs
`KindAgent`), driving the same harness adapters, the same tmux supervision, the same
restart/backoff. The only genuine differences are *where the thing is declared* (config
vs store) and *how it is created* (auto at boot vs imperative spawn) — both of which the
agent path already covers.

Verified facts that make the merge free of new capability:

- **No-repo / fixed workspace:** `ResolveWorkspace` treats a `repo` with no `/` as a plain
  directory under the base workspace (`os.MkdirAll`, no git). A process's `workspace:` is
  exactly this.
- **Stable names:** `SpawnSpec.Name` / `--name` bypasses the derived
  `leo-<template>-<repo>-<branch>` naming, giving a stable name like `rocket`.
- **Channels at boot:** template `channels`/`dev_channels` flow through `BuildTemplateArgs`
  into the harness argv, identical to processes.
- **Autostart is free:** an agentstore record is auto-restored on daemon boot by
  `RestoreAgents()`. A spawned-once long-lived agent already comes back across restarts.

## Decision

Retire the "process" primitive entirely. Everything supervised is an **agent**. A
long-lived assistant is just an agent spawned from a template and never stopped.

**No migration code.** The single user re-spawns their long-lived agents by hand after the
change. Nothing reads the old `processes:` section afterward.

## Scope

### Delete

- `ProcessConfig` struct and the `processes:` map in `internal/config/`.
- `leo process*` CLI commands (list / add / remove / attach / logs / rename).
- `/processes` and `/processes/{name}` web pages and their handlers, plus the process
  branch of the schema-driven config form.
- The process-spawning path in `internal/cli/service.go`: `buildAllProcessSpecs`,
  `resolveProcess`, `processEnviron`, and the process arm of `RunSupervised()`.
- The `ProcessSpec.Kind` distinction — collapse `KindProcess`/`KindAgent` to the single
  agent path. Harness `SupportsKind` and the config validation branches that reference
  `KindProcess` go with it.
- Process references in the setup wizard summary.

### Keep

- **The daemon** and `leo service start/stop/restart`. "Service" now means the daemon
  itself — IPC socket, web UI, cron scheduler, idle-suspend sweep, session dispatch,
  `RestoreAgents()`, opencode global context. None of these depend on `processes:`.
- `templates:` — now the only declarative surface for defining long-lived agents.
- The full agent lifecycle: spawn / suspend / resume / stop / prune, agentstore
  persistence, boot restore.
- `/agents` as the single runtime home; `/templates` for editing blueprints.

### Salvage (small, additive)

- `stale_resume_hours` (currently process-only, controls `--resume` staleness) moves onto
  `TemplateConfig` and the agentstore record, resolved defaults → template → record. This
  is a real knob for long-lived agents that resume often. Optional — cut if it adds noise.

## Non-goals

- No `autostart` flag, no boot-seed list, no config section that re-grows `processes:`
  under a new name. Autostart is already provided by agentstore restore.
- No migration/`leo migrate` command. No auto-rewrite of existing `leo.yaml`.
- No change to tasks, cron, or persistent sessions — none reference `ProcessConfig`.

## New workflow

1. Define a template in `templates:` (workspace, channels, env, harness, model, …).
2. `leo agent spawn --template <t> --name <stable-name>` (with `--repo <plainname>` or the
   template's own workspace for a no-git fixed workspace).
3. The agent persists in `agents.json` and auto-returns on every daemon restart.
4. Manage it with `leo agent suspend|resume|stop|attach|logs|rename`.

## Testing

- **Unit:** config load no longer knows `processes:` (a stray `processes:` key is ignored
  or rejected per the loader's existing unknown-field policy); `Config.Validate()` has no
  `KindProcess` branch; `stale_resume_hours` resolves defaults → template → record.
- **Integration:** spawn a no-repo, `--name`d agent from a channel-bearing template →
  assert channels appear in the harness argv; restart the daemon → assert `RestoreAgents()`
  brings it back under the same name.
- **e2e (build-tag gated, `make e2e`):** spawn → suspend → resume → stop of a long-lived
  named agent. Config/argv changes require running `make e2e` before push.

## Risks

- **Stray `processes:` in a live config.** ~~Risk~~ Resolved: the loader uses
  `yaml.Unmarshal` (`internal/config/config.go:867`), not a strict decoder, so unknown
  top-level keys are silently ignored. A leftover `processes:` block still loads fine; the
  user removes it by hand whenever convenient. No loader change needed.
- **`leo service` semantic shift.** Docs and help text describe `leo service` as "process
  supervisor." Update `CLAUDE.md`, `docs/`, and command help to describe it as the daemon
  lifecycle command.
