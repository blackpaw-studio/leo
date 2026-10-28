# Secret-sourced env values

Status: approved by Evan 2026-10-27, as Part A of the ai-config/workspace spec
`docs/specs/2026-10-27-op-token-consolidation.md`, written by Rocket. This copy is the source of
truth for Leo. Part B, migrating `OP_SERVICE_ACCOUNT_TOKEN` on Dionysus and the laptop, is
Rocket's work after this ships.

## Goal
Any `env` value in Leo config can be a **secret reference** instead of a literal. This covers
`defaults`, `templates`, `environments`, `tasks`, and every other place `env` is accepted today.
Leo resolves the reference each time it spawns a process: an agent, a task run, or a dispatch.
The value lives in one OS secret store per machine, so rotating it is one write per machine.

## Syntax (decided by the leo agent, 2026-10-28)
A literal stays a YAML string. A reference is a YAML **mapping** with a `secret:` key that names
the backend:
```yaml
env:
  PLAIN: some-literal                 # unchanged
  OP_SERVICE_ACCOUNT_TOKEN:
    secret: keychain                  # macOS login keychain
    service: OP_SERVICE_ACCOUNT_TOKEN
    account: evan                     # optional
  A: {secret: secret-service, attributes: {service: leo, key: A}}   # Linux libsecret
  B: {secret: systemd-creds, name: b-token}                         # $CREDENTIALS_DIRECTORY/b-token
  C: {secret: file, path: ~/.config/leo/c.token}                    # 0600 or stricter, owned by the user
  D: {secret: command, argv: [pass, show, leo/d]}                   # stdout, no shell
```
Why a mapping: a string can never be read as a reference, so every existing config parses exactly
as before. No literal needs escaping, and the reference is visible in YAML and JSON alike.
Unknown backends and unknown or missing keys are validation errors. Trailing newlines are trimmed
from `file` and `command` output; nothing else is altered.

## Backends
| Backend | Platform | Resolution |
|---|---|---|
| `keychain` | macOS | `security find-generic-password -s <service> [-a <account>] -w` |
| `secret-service` | Linux desktop | `secret-tool lookup <attr> <value>…` |
| `systemd-creds` | Linux under systemd | the file `$CREDENTIALS_DIRECTORY/<name>`, provided by `LoadCredential=` or `LoadCredentialEncrypted=` in the unit |
| `file` | any | the file's contents; refused unless its mode is 0600 or stricter and the current user owns it |
| `command` | any | stdout of the argv, run with no shell; the escape hatch for pass, gopass and similar tools |

Using a backend on a platform that doesn't support it is a resolution error, not a parse error,
so one shared or lead config can serve both macOS and Linux hosts.

## Requirements
- **Values never leak.** A resolved value never appears in logs, `leo config show`, the web UI,
  the config API, history, error messages, or the follow-lead export or cache. Only the reference
  is shown, saved, or synced. Follow-lead copies the reference, and each follower resolves it from
  its own store.
- **Resolution failure fails the spawn.** A locked keychain, a missing item, bad file permissions,
  or a non-zero command exit fails that spawn with a clear error naming the env key and the
  reference (backend plus selector), never the value. There is no fallback to an empty string.
  Other spawns are unaffected.
- **`leo validate` checks references.** It resolves every reference in the effective config and
  reports each one that fails, without printing any value.
- **Writes keep the reference.** A config save through the web UI, CLI, or config API keeps a
  reference intact and never resolves it into the file.
- **Bounded resolution.** Each resolution runs under a short timeout, so a hung `command` or
  keychain prompt fails the spawn instead of hanging it.

## Testing
- Unit tests fake each backend at the process or filesystem boundary. Assert the exact argv for
  `keychain`, `secret-service` and `command`.
- Redaction tests for every surface listed under "Values never leak", including follow export.
- The systemd-creds and secret-service backends run on Linux CI if available; otherwise they are faked.
- Live: on Dionysus, with an isolated test daemon, an agent and a task each receive a keychain
  value, and a missing item fails the spawn with the named reference.

## Out of scope
Migrating any existing secret, which is Part B. Secrets outside `env`. Claude `settings.json` env.
