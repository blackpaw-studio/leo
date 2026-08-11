# Agent template switching

Switch a running agent from one template to another in place — keeping its name,
workspace, and worktree — and remember each template's conversation so switching
back resumes where that template left off.

## Motivation

Templates are how a project's wiring is expressed: harness, model, permissions,
env, `harness_options`. Working on one repo, you often want to try that repo
under a different template — a different harness (`claude` → `codex`), a
different model, or a tighter permission set — without abandoning the workspace
you are in.

Today there is no way to do it. `leo agent restart` re-resolves args from the
agent's template but deliberately falls back to stored args when the effective
harness changed (`resolveRestartArgs`, `internal/agent/manager.go:1170`), and it
never changes which template the record points at. The workaround is
`leo agent stop` + `leo agent spawn <other-template> <repo>`, which loses the
agent name and, for worktree agents, means a fresh branch and checkout.

Switching also throws away the conversation today, which makes flipping back and
forth expensive: each return to a template starts from nothing.

## Non-goals

- **Carrying a conversation across a switch.** Sessions are per-template by
  design here. Switching is a session swap, not a migration; transcripts are
  harness-native and not portable.
- **Changing where an agent works.** The target template's `workspace` is
  ignored — see [Preserved](#preserved).
- **An MCP tool for self-switching.** Agents cannot switch their own or others'
  templates. The surface is CLI + picker only.
- **Renaming.** The agent keeps its name even when that name embeds the old
  template (`leo-claude-owner-repo` running `codex`). Stable names keep tmux
  sessions, channel routing, persistent-task bindings, and scripts working.
  `leo agent rename` remains available for anyone who wants the name to match.

## Surface

### CLI

```
leo agent switch <name> <template> [--json] [--host <host>]
```

```
$ leo agent switch leo-coding-owner-fetch codex
switched leo-coding-owner-fetch: coding → codex (claude → codex), new session
$ leo agent switch leo-coding-owner-fetch coding
switched leo-coding-owner-fetch: codex → coding (codex → claude), resumed session
```

Name resolution matches `stop`/`reset`/`restart` (`daemon.AgentResolve`, so
shorthand works). Template names complete via the existing template completion
helper. `--json` emits:

```json
{
  "name": "leo-coding-owner-fetch",
  "from_template": "coding",
  "to_template": "codex",
  "from_harness": "claude",
  "to_harness": "codex",
  "resumed": false,
  "status": "running"
}
```

### Picker (`leo attach`)

A new `t` binding on the agent list opens a template menu over the current row:
arrow keys to move, `enter` to confirm, `esc` to cancel. The agent's current
template is marked and selecting it is a no-op. This follows the existing modal
pattern in `internal/picker/model.go` (`renaming` + `textinput`, `confirming`
for stop), and dispatches through the same async action/status-line machinery,
so a slow switch shows the spinner and failures land in the status bar.

The menu lists the templates of the row's own host (local config for `local`,
`leo template list --json` for SSH hosts). Attach-only rows — the tmux-fallback
rows an old or partially-unreachable remote produces — do not support switching;
`t` on one shows "not supported for this row" in the status bar.

## Semantics

A switch is: archive the current session, adopt the target template's wiring,
restore that template's session if one was archived, otherwise start fresh.

| Agent state | Behavior |
| --- | --- |
| running | Stop the live process/tmux session, respawn on the new template. |
| suspended | Rewrite the record only. The next resume comes up on the new template. |
| stopped / unknown | Error. Nothing to switch. |

### Per-template sessions

`agentstore.Record` gains `sessions_by_template` (`map[string]string`). It is an
**archive of inactive templates only** — the active template's session stays in
`rec.SessionID`, so every existing path (`Restart`, `Resume`, `RestoreAgents`,
the drivers' post-hoc `SessionIDStore`) keeps working unchanged.

Switching from `A` to `B`:

1. `sessions_by_template[A] = rec.SessionID` when non-empty.
2. `rec.Template = B`; `rec.Harness = cfg.TemplateHarness(B)`.
3. `rec.SessionID = sessions_by_template[B]`, and that key is deleted.
4. Non-empty → resume it. Empty → fresh session: claude mints a new
   `--session-id` exactly as `Reset` does; other harnesses leave it empty so the
   tmux-TUI driver's post-hoc discovery re-arms.

Keying is by **template name**, not harness: two `claude` templates (`coding`,
`review`) each keep their own conversation, which is the common case this
feature exists for.

An archived session that no longer exists on disk (transcript deleted, harness
state cleared) fails the same way any stale resume does, and is caught by the
existing quick-exit recovery ladder (`QuickExitClearAndNoResume`).

### The newest-jsonl preference must be bypassed

`Restart` and `Resume` prefer the newest on-disk jsonl in the workspace over the
stored `SessionID` (`manager.go:1139`, `manager.go:956`) and again on daemon restart (`service/agents.go:117`) to catch `/clear`
sessions the store never saw. That heuristic is workspace-wide and
template-blind: after switching between two claude templates it would resume the
*other* template's conversation, defeating the archive.

The record therefore gains `session_pinned` (bool), set by a switch and consumed
— honored, then cleared — by the next `Resume`/`Restart`/`RestoreAgents`. While
set, those paths use `rec.SessionID` verbatim and skip the jsonl scan. This
mirrors the existing one-shot `NoResume` flag.

Known narrow cost: if the user runs `/clear` inside the agent immediately after a
switch and then restarts before the switch's pin is consumed, that restart
resumes the pre-`/clear` session. Blast radius is one restart, since the pin
clears on first use.

### Re-resolution

After mutating `rec.Template`/`rec.Harness`, the switch calls the existing
`resolveRestartArgs(cfg, rec, webToken)`. Its preconditions now hold by
construction (template exists, harness matches the record), so args and env are
rebuilt from today's cascade with the standard layering: harness env, template
env, re-pruned `InheritedEnv`, then `SpawnEnv` winning on top, with
`applyPermissions` normalizing `LEO_PERMISSIONS` from the **new** template.

### Preserved

`Name`, `Workspace`, `Branch`, `CanonicalPath`, `Repo`, `WebPort`, `SpawnEnv`,
`InheritedEnv`, `IdleSuspendAfter`. The target template's `workspace` is ignored:
the agent stays in the project it is working in, which also keeps archived
sessions valid (sessions are per-workspace).

### Guards

| Condition | Result |
| --- | --- |
| Target template missing from config | Error, no change. |
| Target template == current template | No-op, reported as such (exit 0). |
| Agent has no agentstore record | Error (matches `reset`/`restart`). |
| Agent is the target of a `runtime: persistent` task | Error. Point at editing `tasks.<name>.template` instead. |
| Caller lacks spawn permission for the target template | Error. |

The persistent-task guard exists because those tasks bind by agent *name*
(`config.ResolveTaskTarget`), so a switch would silently redirect a scheduled
task's prompts into a template it was never configured for — including into a
harness that cannot deliver its `channels`.

Permission gating reuses `gateSpawnTemplate` (`internal/cli/permissions.go:69`):
a switch launches the target template, so it is governed by the same
`can_spawn` allowlist as `leo agent spawn`.

### Interaction with existing verbs

- `leo agent reset` clears only the **active** template's session. The archive is
  untouched, so a reset does not wipe other templates' conversations.
- `leo agent stop` / `prune` delete the record for shared-workspace agents,
  taking the archive with them. Terminal means terminal.
- `leo agent restart` is unaffected: after a switch, `rec.Harness` matches the
  new template, so its normal re-resolution path applies.

## Implementation

| Layer | Change |
| --- | --- |
| `internal/agentstore/store.go` | `Record.SessionsByTemplate map[string]string`, `Record.SessionPinned bool`. Both omitempty; absent on legacy records means "no archive, no pin". |
| `internal/agent/manager.go` | `Manager.SwitchTemplate(name, template) (SwitchResult, error)`. Honor + clear `SessionPinned` in `Restart`/`Resume`. |
| `internal/service` (restore path) | Honor + clear `SessionPinned` in `RestoreAgents`' jsonl scan. |
| `internal/daemon` | `/agents/switch` handler + `daemon.AgentSwitchTemplate` client func, following the `AgentRestart` pattern. |
| `internal/cli/agent.go` | `newAgentSwitchCmd`, wired in `newAgentCmd`; remote passthrough via `runRemote`; `gateSpawnTemplate`. |
| `internal/picker/keys.go` | `Switch` binding on `t`, added to short/full help. |
| `internal/picker/picker.go` | `Backend.Templates(ctx) ([]string, error)`, `Backend.SwitchTemplate(ctx, name, template) error`. |
| `internal/picker/backend_local.go` | New methods; templates come from an injected `templates func() ([]string, error)` seam supplied by the CLI layer (which holds the config), mirroring how `sshArgs` is injected for SSH. |
| `internal/picker/backend_ssh.go` | `leo template list --json` and `leo agent switch <name> <template>`, both shell-quoted per the existing SSH argv rules. |
| `internal/picker/model.go` | `switching` modal state + template list, `actionSwitchTemplate` action kind. |
| `docs/cli/agent.md`, `docs/cli/attach.md`, `docs/guides/agents.md` | Document the verb, the key, and the per-template session model. |

## Testing

TDD, failing test first, in the existing table-driven style of each package.

**`internal/agent`** — switch on running / suspended / stopped agents; archive
round-trip (A→B→A resumes A's original id); fresh session on first visit to a
template (claude mints `--session-id`, codex does not); same-template no-op;
missing template; missing record; persistent-task guard; args/env re-resolved
from the new template including `LEO_PERMISSIONS`; `SessionPinned` set by switch
and cleared by the next `Restart`/`Resume`; jsonl preference not applied while
pinned.

**`internal/picker`** — `t` opens the menu and marks the current template; `esc`
cancels with no dispatch; `enter` dispatches `SwitchTemplate` with the right host
and name; attach-only rows refuse; backend error renders in the status bar.
SSH backend argv assertion for both new commands (per the argv-assertion lesson —
mocked exec seams hide argv bugs).

**`internal/cli`** — verb output, `--json` shape, remote passthrough argv,
permission gate rejection.

Manual verification before merge: a real claude → codex → claude round trip on a
live agent, confirming the claude conversation comes back, plus one switch
between two claude templates confirming the sessions stay distinct.
