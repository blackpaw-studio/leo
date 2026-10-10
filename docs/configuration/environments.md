# Environments

Named, composable env maps that are independent of templates. Any template or task can run under any environment, so one template can run on two accounts without being duplicated. Leo has no concept of an "account": an environment is just a set of variables, and the harness reads its home directory from them.

```yaml
environments:
  base:
    FOO: bar
  acct-b:
    CLAUDE_CONFIG_DIR: /Users/evan/.claude-b
  acct-b-codex:
    CODEX_HOME: /Users/evan/.codex-b

defaults:
  environments: [base]

templates:
  rocket:
    environments: [base, acct-b]   # replaces defaults.environments

tasks:
  nightly:
    environments: [acct-b]
```

## Semantics

- **Composition.** `environments` is an ordered list, merged left to right. When two names set the same key, the later one wins.
- **Cascade.** The most specific level that sets `environments` replaces the list from less specific levels: spawn / `set-environment` > task or template > `defaults`. Lists never append. At config level an empty list (`environments: []`) counts as set and clears the inherited list; at the spawn and `set-environment` surfaces an empty list means "no override".
- **Precedence of the final env, lowest to highest:** harness env, merged named environments, the literal `env:` map on the template or task, env inherited from a source agent (a spawn derived from an existing agent), then `--env K=V` at spawn.
- **Values are literal.** There is no `~` or `$VAR` expansion, as with `env:`. `leo validate` warns about a literal `~` in `CLAUDE_CONFIG_DIR` / `CODEX_HOME`.
- **Validation.** `leo validate` and every config save reject an unknown environment name, a duplicate name in one list, an invalid env key or environment name, and `environments:` on a persistent task that names a `template:` (set it on the template instead).

## Using them

```bash
leo agent spawn coding --environment acct-b          # one agent, one account
leo agent spawn coding --environment base,acct-b     # ordered list
leo agent set-environment fetch acct-b               # switch a live agent, restart, resume
leo agent set-environment fetch                      # clear the override → template default
```

- **MCP:** `leo_spawn_agent` takes `environments`; `leo_set_agent_environments` switches an existing agent. The switch tool honors `deny_tools`, and the caller's `can_spawn` must cover the target agent's template (otherwise a narrowed spawner could move an agent onto credentials it was never given).
- **Web UI:** the *Environments* page edits the map (delete is refused while anything still names an environment); the Agents page has an environments field on the spawn form and a per-agent *Set env* action.
- **Dispatches and consults** run under their template's resolved environments. Per-dispatch environment selection is not implemented.

The agent record stores the override **names**, never resolved values, so editing an environment's variables takes effect when the agent next restarts. If a saved name no longer exists in config, `restart`, `start` and a template switch fail with `unknown environment "…"` before stopping anything, and the stale-agent check reports it. Agents spawned from a template with no override always follow the template's current list.

## `set-environment`

Rebuilds the agent's env from current config (so variables of the departing environment disappear), stops it, and respawns it with its conversation resumed. A dormant agent is rewritten in place and picks the environments up at its next start. If the respawn fails the agent is left dormant — `leo agent start` recovers it. Agents backing a `runtime: persistent` task are refused: change the task or template instead.

Resume across accounts only works when both config dirs share `projects` (see below); otherwise the new account cannot find the transcript and the agent starts a fresh conversation.

## Harness paths

Each harness derives its files from the agent's resolved env, not from `$HOME`:

- **claude:** `CLAUDE_CONFIG_DIR` (default `~/.claude`) for transcripts (`projects/`), plugins, and the workspace-trust write (`$CLAUDE_CONFIG_DIR/.claude.json`). Leo pre-trusts the workspace only when the config dir differs from the default, so default-account agents behave exactly as before.
- **codex:** `CODEX_HOME` (default `~/.codex`).

## Setting up a second Claude account

Leo does not manage logins. Prepare a second config dir yourself:

1. Create it and log in once under it: `CLAUDE_CONFIG_DIR=/Users/you/.claude-b claude` → `/login`. Each dir gets its own keychain item (`Claude Code-credentials-<hash>`, Claude Code ≥ ~2.1.121).
2. Symlink the shared pieces back to `~/.claude`: `settings.json`, `CLAUDE.md`, `rules`, `skills`, `agents`, `commands`, `hooks`, `plugins`, and **`projects`**. Sharing `projects` keeps transcripts, `--resume` and auto-memory common to both accounts, which is what lets `set-environment` resume a conversation.
3. Leave `.claude.json` (login, trust, user-scope MCP servers) and `history.jsonl` per directory. User-scope MCP servers must therefore be moved into a local plugin to appear under both.
4. Add `environments.acct-b: {CLAUDE_CONFIG_DIR: /Users/you/.claude-b}` and reference it as above.

## Limitations

- Implicit persistent-task agents (a persistent task with no `template:`) keep their stored env across restarts; they do not re-resolve environments.
- `~` and `$VAR` are not expanded.
- Resume across accounts needs a shared `projects` directory.
- Environments are not selectable per dispatch or per delegation profile.
