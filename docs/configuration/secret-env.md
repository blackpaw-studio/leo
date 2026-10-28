# Secret env values

Any `env` value — in `environments:`, a template's `env:` or a task's `env:` — can be a **secret reference** instead of a literal. Leo resolves the reference from an OS secret store each time it launches a process (an agent, a task run, a dispatch or a consult), so the value lives in one store per machine and rotating it is one write per machine. The value itself never reaches `leo.yaml`, logs, `leo config show`, the web UI, the config API, `state/agents.json`, task history, error messages, or the [follow-lead](follow-lead.md) export and cache.

```yaml
environments:
  ops:
    PLAIN: some-literal                     # a string is always a literal
    OP_SERVICE_ACCOUNT_TOKEN:
      secret: keychain                      # macOS login keychain
      service: OP_SERVICE_ACCOUNT_TOKEN
      account: evan                         # optional
    A: {secret: secret-service, attributes: {service: leo, key: A}}   # Linux libsecret
    B: {secret: systemd-creds, name: b-token}                         # $CREDENTIALS_DIRECTORY/b-token
    C: {secret: file, path: ~/.config/leo/c.token}
    D: {secret: command, argv: [/opt/homebrew/bin/pass, show, leo/d]}
```

A reference is a YAML mapping whose `secret:` key names the backend. A string is never read as a reference, so existing configs parse exactly as before and no literal needs escaping. An unknown backend, an unknown key, or a missing required key fails `leo validate` and every config save, naming the scope and key (`environments.ops.B: secret: systemd-creds requires name`).

## Backends

| Backend | Platform | Keys | Resolves to |
|---|---|---|---|
| `keychain` | macOS | `service` (required), `account` | `/usr/bin/security find-generic-password -s <service> [-a <account>] -w` |
| `secret-service` | Linux | `attributes` (required, non-empty) | `secret-tool lookup <attr> <value>…`, attributes in sorted order |
| `systemd-creds` | Linux | `name` (one path element) | the file `$CREDENTIALS_DIRECTORY/<name>` |
| `file` | any | `path` | the file's contents; `~` and `$VAR` in the path expand at launch, as in a literal, and the result must be absolute |
| `command` | any | `argv` (non-empty) | the command's stdout; run with no shell, empty stdin, argv not expanded |

- **Platform rules.** Using `keychain` on Linux, or `secret-service` / `systemd-creds` on macOS, is a resolution error, not a parse error, so one shared `templates_file` or lead config can serve both platforms. Swap whole environments per host (a local environment of the same name replaces a shared or lead one) rather than mixing platforms in one env map.
- **Trimming.** Trailing newlines are trimmed from `file`, `command` and `systemd-creds` output, and `keychain` loses the one newline `security -w` appends. `secret-service` output is used as is.
- **Limits.** A value over 64 KiB or containing a NUL byte is refused. A `file` must be a regular file, mode `0600` or stricter, owned by the user leo runs as.
- **Timeout.** Each resolution runs under a 10 s timeout, so a hung command or a keychain ACL prompt fails the launch instead of hanging it.

## When references resolve

- **Effective keys only.** Layers merge first and only the final value of each key resolves (precedence as in [Environments](environments.md#semantics)). A reference that a higher layer overrides — say a lead's `keychain` entry that a Linux host shadows with a local literal — is never resolved and cannot fail that launch.
- **Every launch.** Spawn, `leo agent start`, `restart`, `reset`, `set-environment`, boot restore, each task run, each dispatch and consult launch, and each headless dispatch follow-up resolve afresh. An interactive dispatch's follow-ups go to the pane already running. The agent record stores the reference, never the value.
- **Rotation** takes effect at the next launch: spawn, start, restart, reset, boot restore, or the supervisor's relaunch after a crash. A running agent keeps the env it launched with, so to apply a rotation now run `leo agent restart <name>`.
- **Display paths never resolve.** `leo config show`, `leo template show`, `leo run --dry-run`, the web UI and the config API show the reference (`<secret keychain(service=OP_SERVICE_ACCOUNT_TOKEN, account=evan)>`) and never touch the store.

## Failures

A failed resolution fails that launch only, with an error naming the scope, key and reference, never a value:

```
templates.coder.env.OP_TOKEN: secret keychain(service=OP): item not found
```

There is no fallback to an empty string. `restart`, `reset` and `set-environment` resolve before stopping the agent, so a failure leaves it running. At boot restore an agent whose secret fails is marked failed with `secret unavailable: …`; an agent whose tmux session survived a daemon restart is adopted as is. The supervisor never relaunches an agent without its secrets: when a crash restart (or the end of an adopted session) needs a reference that does not resolve, it logs the named reference and retries with backoff until it does. A failed command reports only its exit status, never its stderr, since that may echo what it read.

## Output redaction

A child that prints its own secret (a token echoed in an error, say) does not carry the value into what leo keeps or returns. Every resolved value of a task run or dispatch is replaced with `<secret KEY>` in the task log and history, the returned and notified error, the failure card, a dispatch's recording (`leo dispatch output`), its wait result, and an interactive dispatch's turn text. A headless follow-up that resolved a rotated value masks both the old and the new value.

- **Threshold.** Values shorter than 4 bytes are never replaced: a PIN or `on` would match ordinary output everywhere and shred it. Keep secrets you want masked at 4 bytes or longer.
- **Not covered.** Only literal occurrences are masked, not an encoded or split form (base64, URL-escaped). An interactive dispatch adopted after a daemon restart has no values to mask with, so its later turn text is not redacted. Output leo never stores is out of scope: a tmux pane's scrollback and the harness's own transcript files.

## `leo validate`

`leo validate` resolves every reference a configured launch would resolve: each template's and task's effective env (shadowed keys skipped, as at launch), plus the references of any environment nothing in config names, since only a spawn-time `--environment` list applies those. Each distinct reference resolves once. A failure is an ERROR naming scope, key and reference. A backend that cannot run in the validating process is a WARN: another platform's store, or `systemd-creds` with `CREDENTIALS_DIRECTORY` unset, which is normal outside the unit. Neither text nor `--json` output carries a value.

## Setting up each backend

### macOS keychain

```bash
security add-generic-password -s OP_SERVICE_ACCOUNT_TOKEN -a evan -w -T /usr/bin/security
```

`-w` with no value prompts for it, so it stays out of shell history. `-T /usr/bin/security` adds leo's lookup tool to the item's access list. Without it the first lookup raises an ACL prompt that nobody answers under launchd, and the launch times out. The item must be in a keychain that is unlocked while the daemon runs (the login keychain, for a logged-in user). To rotate, run the same command with `-U`.

### `command` under launchd

The daemon's launchd `PATH` is minimal, so make `argv[0]` an absolute path (`/opt/homebrew/bin/op`, not `op`). The command runs with leo's environment and no shell: there are no pipes, globs or `$VAR` in argv. Wrap anything more complex in a script and point `argv` at the script.

### systemd credentials

`systemd-creds` reads credentials that systemd hands the leo unit, so the unit must load them. `leo service install` sets leo up as a systemd **user** unit (`leo.service` for the default home), and a credential for the per-user manager must be encrypted with `--user` (systemd 256 or later):

```bash
mkdir -p ~/.config/credstore.encrypted
systemd-creds --user encrypt --name=op-token - ~/.config/credstore.encrypted/op-token   # reads the value from stdin
```

```ini
# systemctl --user edit leo
[Service]
LoadCredentialEncrypted=op-token:%h/.config/credstore.encrypted/op-token
```

```yaml
environments:
  linux:
    OP_SERVICE_ACCOUNT_TOKEN: {secret: systemd-creds, name: op-token}
```

`CREDENTIALS_DIRECTORY` exists only inside the unit, so `leo run` and `leo validate` from a shell report it unavailable (a WARN in validate). Plain `LoadCredential=op-token:/path/to/file` works the same way for an unencrypted file.

## Follow-lead and shared templates

A [lead](follow-lead.md) exports references as they are written, and each follower resolves them from its own store. An export whose templates or environments hold a reference is schema 2. A follower older than this release refuses it with `follower_too_old`, so update followers before adding references to a lead. Exports with no references stay schema 1. On a follower, a malformed reference marks only its own lead entry `lead · invalid`; a backend for another platform does not. Saving a shared `templates_file` keeps every reference, and its comments, as written.

## Editing references

Write references in `leo.yaml` (or the shared templates file), or set them through the [config API](config-api.md#secrets) with `{"ref": {…}}`. The web UI's env editors list references read-only and keep them when you save other keys. Typing `KEY=value` for a reference's key replaces it with that literal.
