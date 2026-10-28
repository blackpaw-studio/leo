# Environments

Named, composable env maps that are independent of templates. Any template or task can run under any environment, so one template can run with different variables without being duplicated. An environment is just a set of variables; harnesses that read their home directory from env (`CLAUDE_CONFIG_DIR`, `CODEX_HOME`) follow it.

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
- **Variable expansion.** Values in `environments:` and in the literal `env:` of a template or task expand `$VAR`, `${VAR}` and a leading `~` / `~/` when an agent, dispatch or task launches; write `$$` for a literal `$`. Variables come from the launching process: the daemon for agents, dispatches and scheduled tasks, your shell for `leo run`. A variable that is not set fails the launch with the environment, key and variable named, rather than expanding to an empty string. `--env K=V` pairs at spawn are not expanded (your shell already did). See [Variable expansion](#variable-expansion).
- **Secret references.** Any value can be a reference to an OS secret store (`{secret: keychain, service: …}`, `file`, `command`, …) that resolves at launch instead of being written in config. See [Secret env values](secret-env.md).
- **Validation.** `leo validate` and every config save reject an unknown environment name, a duplicate name in one list, an invalid env key or environment name, and `environments:` on a persistent task that names a `template:` (set it on the template instead).

## Variable expansion

```yaml
environments:
  acct-b:
    CLAUDE_CONFIG_DIR: ${HOME}/.claude-b   # or $HOME/.claude-b, or ~/.claude-b
    CODEX_HOME: ~/.codex-b
    API_TOKEN: $ACCT_B_TOKEN                # must be set where the launch happens
    LITERAL_DOLLAR: price$$5                # → price$5
```

- Expansion happens when an environment is resolved for a launch, never when config loads. `leo.yaml` (and a save from the web UI) keeps `${HOME}` exactly as written.
- A `~` expands only at the start of a value and only as `~` or `~/…`; `~user` is left alone. A `$` that is not followed by a name or `{` stays literal.
- An unset variable fails the launch, naming the scope, key and variable (for example `environments.acct-b.CLAUDE_CONFIG_DIR: variable $ACCT_ROOT is not set`), so `$ROOT/x` can never silently become `/x`. `leo agent restart` of an agent whose environment no longer resolves fails before stopping it. `leo run --dry-run` fails the same way.
- `leo validate` warns (it does not error) about variables unset in its own process, since the daemon that launches agents may have a different environment. The daemon's environment is what a managed agent sees; set variables for it where the daemon starts (for example the launchd plist or systemd unit).

## Using them

```bash
leo agent spawn coding --environment acct-b          # one agent, one account
leo agent spawn coding --environment base,acct-b     # ordered list
leo agent set-environment fetch acct-b               # switch a live agent, restart, resume
leo agent set-environment fetch                      # clear the override → template default
```

- **MCP:** `leo_spawn_agent` takes `environments`; `leo_set_agent_environments` switches an existing agent. The switch tool honors `deny_tools`, and the caller's `can_spawn` must cover the target agent's template (otherwise a narrowed spawner could move an agent onto credentials it was never given).
- **Observability API:** `GET /api/v1/environments`, `GET /api/v1/templates`, the agent `environments` fields and `POST /api/v1/agents/spawn` / `POST /api/v1/agents/{name}/environments` (names only; see the [observability spec](../specs/2026-07-31-observability-api.md#named-environments)).
- **Web UI:** the *Environments* page edits the map (delete is refused while anything still names an environment); the Agents page has an environments field on the spawn form and a per-agent *Set env* action.
- **Dispatches and consults** inherit their caller's environments. The child resolves its template's environments (and literal `env:`) first, then layers the inherited list on top, so the caller wins any key both set: an agent running under `work` dispatches under `work` too. The inherited list is, in order of precedence:
  1. an explicit `environments` on the dispatch (`leo_dispatch`'s `environments`, `leo dispatch --environment`, or `environments` in `POST /api/dispatch`), which replaces the inherited list; `[]` (or `--environment=`) inherits nothing, so only the template's environments apply;
  2. for a dispatch started by another dispatch, the parent run's effective list;
  3. the calling agent's effective list (its own override, else its template's, else `defaults.environments`).

  A caller with no environments, such as a plain `leo dispatch` from your shell, gives only the template's. The run records its effective names and their source (`inherited`, `override` or `template`), shown in `leo dispatch show` and `/api/v1` dispatches. Follow-ups through `leo_send_dispatch` reuse the recorded list. An interactive dispatch's pane resets every key that leo's environments or template `env:` could set and that the child does not set, so the caller's tmux session env cannot leak into it.

The agent record stores the override **names**, never resolved values, so editing an environment's variables takes effect when the agent next restarts. If a saved name no longer exists in config, `restart`, `start` and a template switch fail with `unknown environment "…"` before stopping anything, and the stale-agent check reports it. Agents spawned from a template with no override always follow the template's current list.

## `set-environment`

Rebuilds the agent's env from current config (so variables of the departing environment disappear), stops it, and respawns it with its conversation resumed. A dormant agent is rewritten in place and picks the environments up at its next start. If the respawn fails the agent is left dormant — `leo agent start` recovers it. Agents backing a `runtime: persistent` task are refused: change the task or template instead.

If the switch changes `CLAUDE_CONFIG_DIR`, resume only works when both config dirs share `projects` (see the example below); otherwise the transcript is not found and the agent starts a fresh conversation.

## Harness paths

Each harness derives its files from the agent's resolved env, not from `$HOME`:

- **claude:** `CLAUDE_CONFIG_DIR` (default `~/.claude`) for transcripts (`projects/`), plugins, and the workspace-trust write (`$CLAUDE_CONFIG_DIR/.claude.json`). Leo pre-trusts the workspace only when the config dir differs from the default, so default-account agents behave exactly as before.
- **codex:** `CODEX_HOME` (default `~/.codex`).

## Example: a second Claude config dir

Leo does not manage logins. Prepare a second config dir yourself:

1. Create it and log in once under it: `CLAUDE_CONFIG_DIR=/Users/you/.claude-b claude` → `/login`. Credentials stay per dir: on macOS with Claude Code 2.1.296, a non-default dir stores its login in `<dir>/.credentials.json` (mode 0600) rather than the keychain, so the default login is untouched.
2. Symlink the shared pieces back to `~/.claude`: `settings.json`, `CLAUDE.md`, `rules`, `skills`, `agents`, `commands`, `hooks`, `plugins`, and **`projects`**. Sharing `projects` keeps transcripts, `--resume` and auto-memory common to both accounts, which is what lets `set-environment` resume a conversation.
3. Leave `.claude.json` (login, trust, user-scope MCP servers) and `history.jsonl` per directory. User-scope MCP servers must therefore be moved into a local plugin to appear under both.
4. Add `environments.acct-b: {CLAUDE_CONFIG_DIR: /Users/you/.claude-b}` and reference it as above.

## Limitations

- Implicit persistent-task agents (a persistent task with no `template:`) keep their stored env across restarts; they do not re-resolve environments.
- `--env K=V` pairs given at spawn are not expanded.
- Resume across Claude config dirs needs a shared `projects` directory.
- Environments are not selectable per delegation profile.
