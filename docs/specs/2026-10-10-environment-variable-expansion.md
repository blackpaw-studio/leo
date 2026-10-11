# Environment variable expansion

Status: approved, 2026-10-10

Amends [named environments](2026-10-10-named-environments.md), whose values
were literal.

## Problem

`CLAUDE_CONFIG_DIR: /Users/evan/.claude-b` hardcodes a home directory, and
`~/.claude-b` silently names a directory called `~`. Configs that move between
machines, or share a file between users, need `$HOME`.

## Behavior

- **Syntax:** `$VAR`, `${VAR}`, and a leading `~` or `~/`. `$$` is a literal
  `$`. `~user` and a mid-value `~` are untouched; a `$` not followed by a name
  or `{` stays literal.
- **Where:** values of `environments:` and the literal `env:` maps on
  templates and tasks (defaults has no `env:`). Per-spawn `--env K=V` pairs are
  not expanded; the caller's shell already did.
- **When:** at resolve time only, by one pure function with an injected
  variable lookup and home-dir source (`config.Expander`; the zero value is
  `os.LookupEnv` / `os.UserHomeDir`). It lives in the existing merge path
  (`ResolveEnv`, `ResolveNamed`, `TaskEnv`, `TemplateEnv`), so every consumer
  (spawn, restart, set-environment, dispatch, consult, scheduled task,
  `leo run --dry-run`) gets it. Callers hold an `Expander` field or parameter
  rather than a global.
- **Source:** the launching process's environment. The daemon for agents,
  dispatches and scheduled tasks; the shell for `leo run`.
- **Unset variable:** the launch fails with the environment (or `env`), key and
  variable named. Each environment is expanded before merging, so a typo in a
  key that a later layer overrides still fails.
- **`leo validate`:** the "`~` is not expanded" warning is replaced by a
  warning for each value referencing a variable unset in the validating
  process. It is a warning, not an error: the daemon's environment may differ.

## Rationale

- **Raw config.** `Load` does not expand and structs stay raw. The web UI
  loads, edits and saves config; expanding on load would write the expanded
  value back to `leo.yaml` on the next save and destroy the portable form.
  `TestLoadSaveKeepsVariableReferencesLiteral` pins this.
- **Fail on unset.** Shell-style empty substitution turns `$HOME/x` into `/x`,
  which points a harness at the wrong directory (a wrong account's credentials)
  without any error. A launch error is cheap; a wrong `CLAUDE_CONFIG_DIR` is not.
- **Escape hatch.** Values that legitimately contain `$` (tokens, passwords)
  write `$$`. This is a behavior change for existing values containing
  `$name`; they now need escaping.
