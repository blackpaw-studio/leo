# Delegation profiles: declarative role → template routing

Status: approved by Evan 2026-09-22 (design decisions below are his calls).

## Problem

The model-delegation ladder (which template/model handles planning,
implementation, review, exploration, etc.) lives as prose in
`~/.leo/agents/codex/instructions/claude-coding.md`, rendered into
`~/.leo/agents/CLAUDE.md` by ai-config. Agents read the prose and pick a
template name themselves. Consequences:

- Changing a route = edit prose → `ai-config sync` → restart running agents
  (they keep the old CLAUDE.md until restart).
- Prose and `leo.yaml` templates drift (e.g. a removed template still named in
  instructions).
- Evan wants to shift routing week to week based on AI usage (Codex vs Claude
  quota), which today means rewriting the ladder by hand.

## Goal

Agents dispatch by **role**; Leo resolves role → template (+ optional model
override) at dispatch time from a declarative, profile-based config. Evan
switches the active profile from the Leo web UI (or CLI) and the next dispatch
uses it — no instruction edits, no agent restarts.

## Decisions (Evan, 2026-09-22)

1. **Profile switching is manual only.** No automatic switching on usage
   thresholds in this spec.
2. **UI lives in the existing Leo web UI** (`internal/web`), not a TUI or a
   separate tool.
3. **Unmapped role fails loudly.** If the active profile has no mapping for a
   requested role, dispatch returns an error naming the role and the active
   profile. No silent fallback to another profile or to a default template.

## Non-goals

- Automatic/usage-triggered profile switching.
- Pulling live usage/quota from Anthropic/OpenAI (nice-to-have follow-up; see
  Future).
- Replacing templates. Templates stay the unit of harness/model/permissions;
  profiles only point at them.
- Per-repo/per-agent profile overrides (possible follow-up; v1 is global).

## Config

New top-level `delegation` block in `leo.yaml`:

```yaml
delegation:
  active_profile: codex-heavy
  profiles:
    codex-heavy:
      description: "Codex does the grinding; Opus plans"
      roles:
        plan: opus-planner
        implement: codex-implementer
        explore: codex-explorer
        review: codex-reviewer
        review.security: { template: codex-reviewer, model: gpt-6-astra }
        review.concurrency: opus-planner
    claude-heavy:
      description: "Codex quota low — route to Claude"
      roles:
        plan: opus-planner
        implement: { template: claude-implementer, model: sonnet }
        explore: { template: claude-explorer, model: haiku }
        review: opus-planner
        review.security: { template: codex-reviewer, model: gpt-6-astra }
```

Rules:

- A role value is either a template name (string) or
  `{ template, model?, effort? }` where `model`/`effort` are the same overrides
  `leo_dispatch` already accepts.
- Role names are free-form dotted strings. Resolution is **exact match only**
  — `review.security` does not fall back to `review` (consistent with
  fail-loudly; a fallback here would silently send security review to the
  wrong model). If a hierarchical fallback is wanted later, it's opt-in.
- `leo validate` errors on: unknown `active_profile`; a role pointing at a
  template that doesn't exist; a model override the template's harness can't
  accept (to the extent validation already knows this).
- Absent `delegation` block = feature off; dispatch-by-template behaves exactly
  as today.

## Dispatch

- `leo_dispatch` (MCP) and the daemon dispatch endpoint gain a `role` param,
  mutually exclusive with `template`. Passing both, or neither, is an error.
- Resolution happens at dispatch time against the **current** config, so a
  profile switch affects the next dispatch with no restart. Config is re-read
  via the existing reload path (`Server.ReloadConfig`) — switching a profile
  writes `leo.yaml` and triggers reload.
- Explicit `model`/`effort` on the dispatch call override the profile's.
- The dispatch record/roster stores `role`, `profile`, and the resolved
  `template`/`model`, so history shows why a given model ran.
- Error text on unmapped role, e.g.:
  `role "review.perf" is not mapped in active delegation profile "claude-heavy" (mapped: plan, implement, explore, review, review.security)`
- Dispatch by explicit `template` keeps working unchanged (escape hatch).

## Instructions injection

When `delegation` is configured, **Leo injects a delegation block into the
agent's system prompt at launch** (same mechanism Leo already uses for its
managed prompt/instruction blocks). No external tooling (ai-config) is needed.

- Content: the roles in the active profile with each role's `use_for` guidance
  (new optional per-role field, see below), plus the rule "dispatch with
  `leo_dispatch(role: ...)`; do not pick templates or models yourself".
- Role names and guidance only — **no template or model names**. Profiles
  normally share a role set, so a profile switch doesn't make a running agent's
  prompt stale; only adding/removing roles does.
- For the live view, a read-only MCP tool `leo_delegation` returns the current
  roles (and resolved template/model, for debugging). The injected block tells
  agents to call it if a role they expect is missing.
- `leo delegation render` prints the exact block that would be injected.
- Absent `delegation` block = nothing injected.

Per-role guidance lives in config, profile-independent:

```yaml
delegation:
  roles:
    plan: { use_for: "Designs and plans before any nontrivial change; read-only" }
    implement: { use_for: "All code changes, from a complete brief" }
    review.security: { use_for: "Required review for auth, secrets, crypto, input handling" }
  active_profile: codex-heavy
  profiles: { ... }
```

`leo validate` warns when a profile maps a role not declared under
`delegation.roles`, and errors when a declared role is unmapped in the active
profile (fail loudly at config time, not just dispatch time).

## Web UI

In the existing Leo web UI, a **Delegation** page:

- Profile list with the active one marked; one-click "Make active" (confirm
  dialog showing the role diff vs current profile).
- Role × profile grid: rows = roles, columns = profiles, cells = template
  (+ model badge). Cells editable via template picker (from configured
  templates) and optional model/effort override.
- Add/remove/rename roles and profiles; duplicate a profile.
- Validation inline, using the same checks as `leo validate`; saving an invalid
  config is blocked with the error shown.
- Writes go through the existing config handler path (`handlers_config.go`),
  preserving the rest of `leo.yaml` (comments/ordering as well as the current
  config writer does), then reload.
- Recent dispatches panel: last N dispatches with role → resolved template/model
  and profile, so Evan can see the effect of a switch.
- Same auth as the rest of the web UI.

## CLI

- `leo delegation list` — profiles, active marked.
- `leo delegation use <profile>` — switch active profile (write + reload).
- `leo delegation show [profile]` / `leo delegation render` — as above.
- `leo delegation resolve <role>` — print what a dispatch would resolve to.

## Testing / acceptance

- Unit: resolution (string vs object role, override precedence, unmapped role
  error text, both/neither of role/template), validation errors.
- Integration: switch profile via CLI and via web handler → next `role`
  dispatch resolves to the new template without daemon restart; in-flight
  dispatches unaffected.
- Web: grid edit round-trips to `leo.yaml` without clobbering unrelated keys.
- Absent `delegation` block: all existing dispatch tests pass unchanged.
- Docs: `docs/configuration` reference entry + changelog.

## Future (not in this spec)

- Usage panel on the Delegation page (Codex/Claude consumption for the week)
  to inform manual switching.
- Per-agent/per-repo profile override.
- Opt-in hierarchical role fallback.
