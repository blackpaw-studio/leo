# Following a lead host

A Leo host can **follow** another Leo host, the *lead*. The follower mirrors the
lead's `templates`, `environments`, `delegation` and `defaults`, and can replace
any of them locally by name. It keeps working from its last synced copy when the
lead is unreachable.

Typical setup: a server is the lead and holds the shared config; a laptop
follows it, so a template edited on the server is available on the laptop
within one interval.

Nothing else is synced: `tasks` (cron would fire twice), `web`, `client`,
`api_clients`, `chat`, state and agents stay per host. An environment's
*definition* syncs, but what it points at does not: Claude config dirs,
keychain logins and plugins are per host.

## Setup

On the follower, name the lead in `client.hosts` and point `follow` at it:

```yaml
# follower leo.yaml
client:
  hosts:
    server:
      ssh: evan@10.0.2.10
follow:
  host: server     # a client.hosts name (required)
  interval: 5m     # re-pull period (default 5m, minimum 30s)
```

The lead needs no config. Anyone who can ssh to a host and run its `leo` can
follow it, the same trust model as `leo agent --host`.

Then pull once and check the result:

```bash
leo sync            # through the running daemon, or directly when none runs
leo sync --status   # host, last pull and its age, lead revision, last error
leo template list   # SOURCE shows lead / local / local · overrides lead
```

Rules checked when the config loads:

- `follow.host` must name a `client.hosts` entry. Naming `localhost`, or a host
  whose `ssh` is a loopback address, is rejected: a host cannot follow itself.
  A remote that turns out to be this same Leo home fails the pull with
  `self_follow`.
- The lead must not follow anyone itself. Chains are refused with
  `lead_is_follower`.
- With `follow` set and no `client.default_host`, the CLI's default host is
  `localhost`. The follower's `client.hosts` entry for its lead does not turn
  it into a remote client. `--host`, `LEO_HOST` and `default_host` still work
  as before.

### ssh from the daemon

The daemon runs
`ssh -o BatchMode=yes <ssh> <ssh_args> -o ControlMaster=auto -o ControlPath=… -o ConnectTimeout=10 <leo_path> config export --follow`
on the lead. `BatchMode=yes` means ssh never prompts: a pull that needs a
password, a passphrase or a new host key fails with `unreachable` instead of
hanging the daemon. Before the first pull:

- connect once from a terminal (`ssh evan@10.0.2.10 true`) to accept the lead's
  host key;
- use a key the daemon can read without a prompt, such as one held by the
  ssh-agent the daemon inherits, or one without a passphrase;
- set `leo_path` on the host entry when the lead's `leo` is not at the default
  (`~/.local/bin/leo`).

A pull is bounded at 30 seconds and its output at 8 MiB.

## When pulls happen

- At daemon start, then every `interval` after the last pull.
- On `leo sync`, and on **Sync now** on the web Settings page.
- When a config reload changes `follow.host` or adds `follow`.

A pull whose content or lead revision changed triggers the same in-process
reload as `leo service reload`. Running agents keep running; changes apply on
their next start, as with any config edit.

## Cache and status

A successful pull replaces `~/.leo/state/follow-cache.json` atomically (mode
0600, since environments can hold tokens). Every attempt, failed or not, is
recorded in `follow-status.json` next to it. A failed pull leaves the cache
untouched.

- **No cache yet:** there is no lead layer. Leo runs on the local config alone
  and warns.
- **Stale:** the cache is stale when its last successful pull is older than
  3 × `interval`. Stale is shown (`leo sync --status`, `leo validate`, the
  Settings page) but nothing is blocked.

The config revision used by the web UI and the [config API](config-api.md)
covers the cache, so a pull that changes the lead layer invalidates a stale
`If-Match`.

## Layering

Per section, lowest to highest: the lead, then the follower's `templates_file`
(templates only), then the follower's `leo.yaml`.

- **`templates`, `environments`, `delegation.profiles`:** a local entry
  *replaces* the lead's entry of the same name entirely. Fields are never
  merged, the same rule as [`templates_file`](config-reference.md#sharing-templates-between-machines-templates_file).
- **`defaults` and the other `delegation` keys** (`active_profile`, `roles`,
  `enabled`, ...): a key set locally replaces the lead's value for that key.
  Keys are top-level, except `defaults.dispatch.*` and `defaults.push.*`,
  which are one level deeper. `harness_options` is replaced whole. A local key
  overrides the lead even when its value is empty (`""`, `0`, `false`, `[]`,
  `{}`), so clearing a lead value on the follower sticks. A null value counts
  as unset. The exceptions are `defaults.model: ""`, `defaults.max_turns: 0`
  and `delegation.active_profile: ""`: Leo writes these even when they are
  unset, so they count as unset too, and a follower cannot override a lead's
  value for them with an empty one.
- **Values are copied verbatim.** `~` and `$VAR` expand on the follower, as
  they do for local values. Usernames and paths are not rewritten.

## Validation on the follower

The effective config is validated as usual, but a lead entry that fails on
this host is **invalid** instead of fatal: it is listed with its reason and
cannot be used, and the rest of the config still loads. Besides the normal
rules, two host checks apply to lead entries:

- a template's harness binary must be on the daemon's `PATH`;
- an environment's `CLAUDE_CONFIG_DIR` or `CODEX_HOME`, when it is an absolute
  path, must exist.

A lead `defaults` key that fails falls back to the built-in default and is
reported as ignored. A task or agent that references an invalid entry fails
with that entry's reason (the spawn API answers `400 invalid_lead_entry`).
Local entries keep failing loudly, as without `follow`.

## Editing on a follower

Lead entries are read-only on the follower. Editing, renaming or deleting one
through the web UI, the CLI or the config API is refused with `owned_by_lead`,
naming the lead host. To change one:

- change it on the lead (it reaches followers on their next pull), or
- **override it locally**, which copies the lead's entry into the follower's
  `leo.yaml`, where it can be edited:

```bash
leo template override coding
leo delegation override day
leo config override environment work
```

The web UI has an **Override locally** button on each lead entry. Deleting
the local override shows the lead's entry again. An entry that is invalid on
this host cannot be overridden; fix it on the lead instead.

Changing a lead `defaults` key or a delegation setting such as the active
profile needs no override: the edit is saved as a local key in `leo.yaml`.

## Visibility

| Badge / source | Meaning |
|---|---|
| `lead` | mirrored from the lead |
| `local` | defined only in this host's config |
| `local · overrides lead` | a local entry replacing the lead's |
| `shared · overrides lead` | a `templates_file` template replacing the lead's |
| `lead · invalid` | a lead entry unusable on this host (with the reason) |

The badges appear on the web Templates, Environments and Delegation pages,
`leo template list` (SOURCE column, `source`/`invalid` in `--json`), the config
API item documents, and `/api/v1/templates` and `/api/v1/environments`. A host
without `follow` shows none of them.

## Errors

The last error is shown by `leo sync --status`, `leo validate`, service start
and the Settings page. The cache is kept in every case.

| Code | Cause |
|---|---|
| `unreachable` | ssh failed, timed out, or needs a prompt (auth, host key) |
| `lead_too_old` | the lead's `leo` predates `config export --follow`; the detail is its version |
| `lead_is_follower` | the lead follows another host |
| `lead_config_invalid` | the lead's own config does not load |
| `self_follow` | the lead is this same Leo home |
| `malformed` | the export is not valid JSON of the expected shape, or is over 8 MiB |
| `follower_too_old` | the lead's export schema is newer than this `leo` understands; update the follower |

A lead running a newer `leo` than the follower is a warning, not an error:
settings only the newer version knows are ignored on the follower.

## Interfaces

- CLI: [`leo sync`](../cli/sync.md), `leo template override`,
  `leo delegation override`, `leo config override`, `leo config export --follow`
  (what the follower runs on the lead).
- Config API: `GET /config/follow`, `POST /config/follow/sync` and the override
  routes. See [Config API](config-api.md#follow-lead).
- Observability API: `GET /api/v1/follow` (operator token) returns the same
  status report as `GET /config/follow`.
