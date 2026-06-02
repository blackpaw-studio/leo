# Rename Spawned Agents — Design

**Date:** 2026-06-01
**Status:** Approved, pending implementation plan

## Goal

Let a user rename an ephemeral agent — a **true identity rename**, not a cosmetic
label — from both the CLI and the web UI. Renaming a *running* agent must happen
**with zero process restart**: the agent's claude session keeps running
uninterrupted while its name changes everywhere.

## Background: name is the whole identity

An ephemeral agent has no separate ID. Its `Name` (e.g.
`leo-mytemplate-owner-repo-branch`) is simultaneously:

- the key in `agentstore` (`~/.leo/state/agents.json`, a `map[string]Record`)
- the tmux session name, via `agent.SessionName(name)` → `leo-<name>`
- the key in the supervisor's in-memory `states` and `cancels` maps
- the value of the `--name` flag baked into the persisted `ClaudeArgs`
  (`BuildTemplateArgs` appends `--name <agentName>`)

What does **not** depend on the name: the workspace directory, `CanonicalPath`,
and the git branch — these are stored explicitly in the record and never need to
match the name. Rename is therefore identity-only; nothing moves on disk.

## Key enabler for zero-restart

Each running agent has a `superviseProcess` goroutine. Today it captures the
session name **once**:

```go
sessionName := agent.SessionName(spec.Name)
...
waitForSessionEnd(ctx, tmuxPath, sessionName, spec, startTime)
```

`waitForSessionEnd` **polls** `tmux has-session -t sessionName` every 5 seconds.
That poll is the saving grace: if the watcher reads the session name from a
shared, lock-guarded handle on every poll instead of a captured local, then a
live `tmux rename-session leo-old → leo-new` is absorbed within one tick and
never trips a false "session ended" → restart. A literal naive
`tmux rename-session` against today's code would desync the watcher (it would
keep polling `leo-old`, see it gone, and spin up a fresh `leo-old`).

## Components

### 1. `procIdentity` — shared mutable identity handle (`internal/service`)

```go
type procIdentity struct {
    mu   sync.RWMutex
    name string
    args []string
}

func (p *procIdentity) Name() string         // RLock
func (p *procIdentity) SessionName() string   // agent.SessionName(p.Name())
func (p *procIdentity) Args() []string        // RLock, returns a copy
func (p *procIdentity) setArgs(args []string) // Lock — used by strip-resume path
func (p *procIdentity) rename(newName string) // Lock — sets name AND rewrites
                                              // the --name value inside args
```

One per supervised process, constructed in the spawn path and stored in a new
`Supervisor.identities map[string]*procIdentity` keyed by current name (re-keyed
on rename). It is the single source of truth for mutable per-process identity.

### 2. `superviseProcess` changes (`internal/service/process.go`)

The only delicate edit. Thread `id *procIdentity` in:

- `waitForSessionEnd(ctx, tmuxPath, id, spec, startTime)` reads `id.SessionName()`
  **on each poll iteration** → live renames are absorbed.
- At the top of each restart-loop iteration, snapshot `name := id.Name()`,
  `sessionName := id.SessionName()`, `currentArgs := id.Args()` and use the
  snapshot for that iteration's `kill-session` / `new-session` and name-keyed
  state-file/exit-code ops.
- The quick-exit strip-resume path writes mutated args back via `id.setArgs(...)`.
- `sv.setState`/`incrementRestarts` use the iteration snapshot. These name-keyed
  side files flip to the new name at the next iteration boundary (harmless,
  best-effort). The **supervisor map keys re-key immediately** (component 3),
  independently of the goroutine.

### 3. `Supervisor.RenameAgent(old, new string) error`

New method on the `agent.Supervisor` interface (`internal/agent/manager.go`),
implemented by `*service.Supervisor`.

Under `s.mu`:

1. Require `states[old]` exists, is `Ephemeral`, and `Status == "running"`.
   A non-running (mid-restart) agent returns a **retryable** error — this closes
   the small window between the iteration snapshot and re-entering the poll.
2. Require `new` is free across `states`, `reservations`, and `identities`.
3. Under the identity's write-lock: run `tmux rename-session -t leo-old leo-new`.
   On failure, abort with nothing mutated. On success, `id.rename(new)` (sets
   name + rewrites `--name` in args).
4. Re-key `states[new]`/`cancels[new]`/`identities[new]` from the old keys, set
   `states[new].Name = new`, delete the old keys.

The tmux rename + `id.rename` happen under the identity write-lock, atomic with
respect to the watcher's RLock: a given poll observes either (old name, old
session) or (new name, new session), never a crossed state. The tmux exec is a
sub-process of a few milliseconds; acceptable to hold the lock across it.

### 4. `agent.Manager.Rename(query, rawNew string) (Record, error)`

New method on `*agent.Manager` and the `daemon.AgentManager` interface.

1. `rec, err := m.Resolve(query)` — fuzzy-resolve the existing agent.
2. `newName := NormalizeAgentName(rawNew)`.
3. Reject if `newName == rec.Name` ("name unchanged") or if `newName` collides
   with an existing agentstore record.
4. If the agent is live (`EphemeralAgents()[rec.Name]` present): call
   `sup.RenameAgent(rec.Name, newName)`. If it is a stopped worktree agent (no
   live supervisor state): skip the supervisor entirely.
5. Persist via `agentstore.Rename` (component 5): re-key the file, set
   `Record.Name = newName`, rewrite the `--name <old>` → `--name <new>` value in
   `Record.ClaudeArgs`.
6. Return the updated `Record`.

### 5. `agentstore.Rename(homePath, old, new string, mutate func(Record) Record) error`

Atomic, under `storeMu`: load all records, error if `old` missing or `new`
already present, apply `mutate` to the record, store under the new key, delete
the old key, write the file once. The `mutate` callback sets `Name` and rewrites
the `--name` flag in `ClaudeArgs`.

### 6. `NormalizeAgentName(raw string) (string, error)` (`internal/agent`)

- trim surrounding whitespace, lowercase
- strip any leading `leo-`, then re-add exactly one `leo-` prefix (preserves the
  invariant that stored name == tmux session name)
- after the prefix, allow only `[a-z0-9-]`; reject dots, colons, whitespace,
  slashes, and empty input (tmux-hostile / ambiguous)
- cap total length at 64
- collapse repeated/trailing dashes for a clean slug

### 7. Daemon endpoint (`internal/daemon`)

- `mux.HandleFunc("POST /agents/{name}/rename", s.handleAgentRename)`
- `handleAgentRename`: 503 if no agent manager; resolve `{name}` to the canonical
  record via the existing `resolveAgentOrError`; decode `{"new_name":"..."}`;
  call `mgr.Rename(rec.Name, newName)`; return the updated `Record` as JSON, or a
  `409`/`400` on collision/validation error.
- Add `Rename(query, newName string) (agent.Record, error)` to the
  `daemon.AgentManager` interface.

### 8. CLI: `leo agent rename <old-query> <new-name>` (`internal/cli/agent.go`)

- Cobra subcommand under the existing `agent` command, mirroring `stop`.
- Local mode: `POST` to the daemon socket `/agents/{old}/rename`.
- Remote mode: SSH-dispatch `leo agent rename` to the host that owns the agent,
  exactly as `stop`/`prune` already dispatch.
- Print a confirmation line (`renamed <old> → <new>`).

### 9. Web UI (`internal/web`)

- `POST /api/agent/{name}/rename` (JSON) and `POST /web/agent/{name}/rename`
  (form → returns the refreshed agents partial), following the existing
  stop-handler pair.
- `templates/partials/agents.html`: an inline ✎ rename affordance — a button
  that reveals a small text input + submit, `hx-post` to the web rename route,
  `hx-target` the agents partial so the list re-renders with the new name.

## Testing

Table-driven, run with `-race`:

- `agentstore.Rename`: successful re-key + mutate; collision on existing `new`;
  missing `old`; `--name` rewrite in `ClaudeArgs`.
- `Supervisor.RenameAgent`: maps (`states`/`cancels`/`identities`) re-keyed,
  using the package's `exec`/tmux test seam; rejects non-running, non-ephemeral,
  and colliding names; tmux-rename failure leaves state untouched.
- `procIdentity` + `waitForSessionEnd`: the watcher follows a live name change
  (fake tmux exec returns success for the new session name, failure for the old).
- `agent.Manager.Rename`: running path (calls `sup.RenameAgent` + store re-key +
  `--name` rewrite) and stopped path (store-only) via a fake `Supervisor`;
  `NormalizeAgentName` normalization/validation; unchanged-name and collision
  errors.
- Daemon `handleAgentRename`: happy path and validation error.
- CLI: subcommand arg validation (table) and remote-dispatch argument shape.

## Decisions & risks

- **Identity-only:** workspace dir, `CanonicalPath`, and git branch are
  unaffected — nothing moves on disk.
- **Mid-restart safety:** rename of a non-running agent is rejected with a
  retryable error rather than raced.
- **Config (non-ephemeral) processes are not renamable** — `RenameAgent` guards
  on `Ephemeral`, mirroring `StopAgent`.
- **`--name` claude flag** is rewritten in both the live identity handle and the
  persisted record, so it is coherent immediately and after a daemon restart.
- **Blast radius:** the only delicate change is threading `procIdentity` through
  `superviseProcess`; every other change is additive (new method, new endpoint,
  new CLI subcommand, new template affordance).
