# Named environments

Status: approved, 2026-10-10

## Problem

Running agents on a second Claude account (or a second codex account, or a
third-party Anthropic-compatible endpoint) means changing the agent's
environment, most importantly `CLAUDE_CONFIG_DIR`. Today `env:` lives only as a
literal map on templates and tasks. That forces one template per account,
which is unacceptable. Leo also assumes `~/.claude` in a few places, so an
agent with a different `CLAUDE_CONFIG_DIR` breaks.

## Design

Named, composable environments that are independent of templates. Any
template can run under any environment. Leo has no concept of an "account".

```yaml
environments:
  base:
    FOO: bar
  acct-b:
    CLAUDE_CONFIG_DIR: /Users/evan/.claude-b
  acct-b-codex:
    CODEX_HOME: /Users/evan/.codex-b

templates:
  rocket:
    environments: [base]      # default when spawn doesn't choose

tasks:
  nightly:
    environments: [base, acct-b]
```

### Semantics

- **Composition:** `environments` is an ordered list of names, merged left to
  right. When names set the same key, the later one wins.
- **Cascade:** the most specific level that sets `environments` replaces the
  list from less specific levels (spawn > task/template > defaults). Lists do
  not append to each other.
- **Precedence of the final environment, lowest to highest:** merged named
  environments, then the literal `env:` map on the template or task, then
  `--env K=V` given at spawn.
- **Values are literal.** As with `env:` today, there is no `~` or `$VAR`
  expansion.
- **Validation:** `Config.Validate()` rejects an unknown environment name, an
  invalid env key, and a duplicate name in a single list.

### Surfaces (v1)

- `leo agent spawn <template> --environment a,b`
- `leo_spawn_agent` gets an `environments` parameter.
- The web UI spawn form gets a multi-select. The Environments page uses the
  schema form.
- `leo agent set-environment <agent> a,b` (plus the matching MCP tool and a
  web action) updates the agent record and restarts the agent, resuming its
  session. An empty list clears the override, so the template default applies.
- Tasks: an `environments` field on the task. Persistent tasks use it for
  their implicit agent.
- The agent record (`agentstore`) saves the environment **names**, not the
  resolved values, so config edits take effect on the next restart. A saved
  name that no longer exists in config: the agent fails to start with a
  clear error and is not silently dropped.

Out of scope for v1: `leo_dispatch` / delegation-profile environments
(follow-up), secret references (such as `op://`), and an `extends:` field on
environments.

### Harness path awareness

Each harness adapter derives its home paths from the agent's **resolved**
env, not from `$HOME`:

- **claude:** `CLAUDE_CONFIG_DIR` (default `~/.claude`). The trust write goes
  to `$CLAUDE_CONFIG_DIR/.claude.json` instead of `~/.claude.json` when the
  dir is set (`harness/claude/hooks.go`, caller
  `consult/interactive_runtime.go`). The same applies to
  `harness/claude/plugins.go` and to `session/slug.go`
  (`JSONLPath`/`LatestSession`).
- **codex:** `CODEX_HOME`, where the adapter reads codex paths. Audit this
  during implementation.

## Account setup (outside leo, documented in a guide)

The second Claude config dir symlinks back to `~/.claude` for `settings.json`,
`CLAUDE.md`, `rules`, `skills`, `agents`, `commands`, `hooks`, `plugins` and
`projects`. Sharing `projects` keeps transcripts, `--resume` and auto-memory
shared across accounts. `.claude.json` (login, trust, user-scope MCP) and
`history.jsonl` stay per dir. User-scope MCP servers move into a local plugin
so both dirs get them. Each dir gets its own keychain item
(`Claude Code-credentials-<hash>`, Claude Code ≥ ~2.1.121).

## Acceptance

- One template, spawned twice with different environments, runs on two
  accounts at once with no trust-dialog stall.
- `set-environment` switches a running agent's account, restarts it, and
  the agent resumes the same session.
- Config validation rejects unknown names. Unit tests cover merge and
  precedence order.
- Tests assert the spawned argv/env (exec seams hid argv bugs before), and
  the feature is verified live against an isolated test daemon.
