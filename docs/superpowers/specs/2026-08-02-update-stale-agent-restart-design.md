# `leo update`: detect and offer to restart stale agents

Status: proposed
Date: 2026-08-02

## Problem

`leo update` swaps the binary and offers to restart the daemon. It says nothing
about running agents, which keep whatever wiring they were spawned with. A user
who answers "yes" to the daemon prompt reasonably believes the update is live —
it isn't.

Observed after v0.13.2 shipped the `MCP_TOOL_TIMEOUT` harness env var: codex
agents picked it up (their ceiling rides in argv, which restart re-resolves)
while claude and opencode agents did not (theirs ride in env). Nothing in the
update flow surfaced the gap.

Two mechanics keep an agent stale:

1. `Resume` (suspended → running) replays `rec.ClaudeArgs`/`rec.Env` verbatim,
   the same way daemon restore does. Only `Restart` re-resolves from config.
   So waking a suspended agent does not refresh it.
2. Nothing tells the user which agents are stale, or that restart is the remedy.

## Goals

- After a binary swap, `leo update` reports which running agents would change if
  restarted, and offers to restart exactly those.
- A suspended agent self-heals on wake instead of resuming stale.

Non-goals: template-`env:` drift on legacy records (a known, separate
limitation); a background auto-restart; any new scheduled or periodic check.

## Design

### Staleness = "restarting would change something"

No version stamps. `resolveRestartArgs(cfg, rec, webToken)` is already a pure
function returning the args+env an agent *would* get. Dry-run it per agent and
diff against the stored record; a difference is exactly the definition of
"needs a restart to pick up changes". This detects binary upgrades and
`leo.yaml`/template edits with one mechanism, and has no false positives.

Agents `resolveRestartArgs` declines to re-resolve (no template, template
deleted, harness changed) fall back to stored args by design — they report no
drift and are never offered.

`--session-id`/`--resume` tokens differ by construction and are stripped from
both sides before the args diff.

### `Manager.StaleAgents() []StaleAgent`

New method on the existing manager, in `internal/agent`. For each **running**
agent with a record: dry-run, diff, and on drift emit

```go
type StaleAgent struct {
    Name        string   `json:"name"`
    ArgsChanged []string `json:"args_changed,omitempty"` // per-flag, redacted
    EnvAdded    []string `json:"env_added,omitempty"`    // KEY NAMES ONLY
    EnvChanged  []string `json:"env_changed,omitempty"`
    EnvRemoved  []string `json:"env_removed,omitempty"`
}
```

**No free-form value is ever carried.** `rec.Env` holds live credentials, and
`agent.Record` already omits env for exactly this reason — so env drift is key
names only.

The same applies to argv, which this spec originally left implicit and code
review caught: an agent's entire `--append-system-prompt` lives in
`ClaudeArgs`, so shipping a raw argv delta would echo it over the API and onto
the terminal. Argv drift is therefore summarized per flag, with values echoed
only when short and single-line (`--model sonnet -> opus`) and otherwise
elided (`--append-system-prompt changed`, `+--model (set)`). Positional tokens
— which is where an opening prompt lands — are counted, never printed.
Redaction happens in `internal/agent`, so nothing raw crosses the package
boundary in the first place.

### `GET /agents/stale` (daemon IPC)

Detection lives in the daemon, not the CLI: the daemon owns the manager, the
web token, and the live running-set. Returns `[]StaleAgent`. Registered
alongside the existing `/agents/...` routes on the Unix socket.

### Update flow

`maybeRestartDaemon()` gains a follow-on step, run **only after a successful
daemon restart** — the recompute happens inside the daemon, so an un-restarted
daemon would re-resolve with the old binary's logic.

```
Daemon restarted

3 of 9 running agents would pick up changes:
  chronicle   env: +MCP_TOOL_TIMEOUT
  plex        env: +MCP_TOOL_TIMEOUT
  assistant   args: --model sonnet -> opus

Restart them now? [Y/n]
```

- Restarts **only the drifted agents**, via the existing per-agent
  `POST /agents/{name}/restart`. Not `restart-all`.
- Default **yes**, matching the daemon prompt. Restart resumes the conversation.
- No drift → print nothing; stay quiet on the happy path.
- Non-interactive → list the agents and print the remedy, restart nothing.
  Mirrors how the daemon prompt already degrades.
- Per-agent failures are reported and do not abort the batch.

### Resume recomputes

`Manager.Resume` switches to `resolveRestartArgs`, matching `Restart`. A
suspended agent then wakes with current wiring, so it never needs to appear in
the update report. Folded into this change at the user's request, despite being
a bug fix rather than a feature.

## Testing

- `StaleAgents`: no drift → empty; env-only drift; args-only drift; both;
  record without template / harness changed → skipped; session tokens ignored;
  **env values never appear in the result**.
- `Resume`: recomputes like `Restart`, including the legacy-record layering.
- Daemon handler: shape and empty case.
- CLI: interactive yes restarts only the drifted set; no restarts nothing;
  non-interactive prints the hint and restarts nothing; empty drift prints
  nothing.
- Live check after install: the same `tmux show-environment` verification that
  caught the original gap.

## Risks

- **Restart bounces an in-flight turn.** Restart preserves the conversation via
  `--resume`, but an agent mid-turn loses that turn. Accepted: the prompt is
  explicit, names the agents, and is declinable.
- **A dry run that disagrees with the real restart** would report drift that
  never resolves, re-prompting on every update. Both paths call
  `resolveRestartArgs`, so they cannot diverge without a test failing.
