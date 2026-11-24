# Cross-workspace read grants for dispatches

Status: approved 2026-11-23 (Evan). Follow-up to #348 (dispatch cwd confinement); must land before the next release.

## Problem

Under `defaults.dispatch_confinement: enforce` (#348) an agent can no longer dispatch into another
agent's workspace. Some of those dispatches are legitimate information gathering (e.g. marketing
asking about leo's code). Agents need a sanctioned way to ask, without regaining raw access to
other workspaces.

## Design

### Call

`leo_dispatch{role, target_agent: "<name>", prompt, …}` (MCP tool, `/api/dispatch`, `leo dispatch --target-agent`).

- `target_agent` is an exact agent name; Leo resolves its workspace (a stopped agent is fine).
  Unknown name → refused.
- `target_agent` is mutually exclusive with `cwd` and with `isolation`.
- Only read-only roles (`explore`, `plan`, `review*` — whatever the read-only guard from #334
  classifies as read-only). A template dispatch (`template:` instead of `role:`) with
  `target_agent` is refused.

### Grant

The target opts in on its **template**:

```yaml
templates:
  leo:
    share:
      readable_by: [template:marketing, agent:leoterm, task:weekly-digest]
```

- Entries are prefixed: `agent:<exact agent name>`, `template:<template name>`,
  `task:<task name>`. Unprefixed or unknown prefixes fail config validation. No wildcard.
- Matched against the **root caller** (as in #348): an agent matches `agent:` by name and
  `template:` by the template it was spawned from; a oneshot task caller (`task:<name>`) matches
  `task:`. Persistent tasks are agents.
- No grant → refused (in every confinement mode, including `off`: `target_agent` is an explicit
  request, never a silent fallback). Empty callers (human CLI) and `api_clients` callers may
  target any agent without a grant, still under the read-only sandbox.
- Grants are evaluated at dispatch start; continuations of a granted run are not re-checked.

### Read-only sandbox

A granted run is read-only at the OS level, not just by role permissions:

- **claude:** Leo enables Claude Code's sandbox for the run via its settings merge, with
  filesystem writes denied in the target workspace (writes allowed only to the run's own temp/
  scratch area the harness needs) and the secret denylist below denied for reads. Bash runs
  sandboxed with no unsandboxed escape hatch. The run ignores the target repo's project/local
  Claude settings (`--setting-sources user`) so its hooks and sandbox exclusions don't apply.
- **codex:** `--sandbox read-only` (overriding the template's permission mode), no `writable_roots`
  grant for the target workspace. Codex cannot deny reads, so the secret denylist is **not**
  enforced on codex runs — an accepted, documented gap (Evan, 2026-11-23).
- **opencode** (or any harness without an enforceable sandbox): the dispatch is refused with an
  error naming the harness.

Built-in secret read denylist (inside the target workspace; claude only): `.env`, `.env.*`, `*.local`,
`*.pem`, `*.key`, `id_*` keys, `.netrc`, `.npmrc`, `Secrets.xcconfig`. Not configurable.

### Nesting

Nested dispatches under a granted run (read-only guard: explore only) may use the target
workspace and inherit the same sandbox and denylist. Confinement treats the granted target
workspace as an allowed root for that run's tree only.

### Audit

The dispatch record stores `target_agent` and the matching grant entry; the daemon logs one line
per granted start (caller, target, role, grant entry). Shown in `leo dispatch` listings/output.
No notification to the target agent.

## Out of scope

- Remote targets (`agent@host`).
- Per-target path denylists; configurable secret denylist.
- `leo_chat_post{files}` path scoping.

## Acceptance

- Config validation tests for `share.readable_by` (prefixes, unknown prefix, wildcard refused).
- Start tests: granted via each prefix; no grant refused (all modes); write-role / template
  dispatch refused; `cwd`/`isolation` with `target_agent` refused; unknown target refused;
  opencode refused; nested inherits; empty/api_clients callers allowed.
- Harness arg/settings tests asserting the real sandbox argv/settings for claude and codex.
- Live check on an isolated daemon: a granted claude explore in the target workspace can read
  files, cannot write (`touch` via Bash fails), cannot read `.env`; an ungranted request is refused.
- `make test`, `make lint`, isolated `make e2e`.
