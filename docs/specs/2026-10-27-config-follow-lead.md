# Config follow-lead: one host is the source of truth for shared config

Status: draft, for Evan's review (brainstormed 2026-10-27).
Related:
- `2026-10-27-leo-agent-move.md`: move/copy refuses when the destination lacks a template or environment. This spec is how the destination normally has them.
- `2026-10-26-shared-templates-file.md` (`templates_file`).
- ai-config `2026-10-27-multi-machine-config.md`: paused. This spec replaces its Leo-templates part.

## Goal
A Leo host can **follow** another Leo host, the *lead*. It mirrors the lead's
`templates`, `environments`, `delegation` and `defaults`. Each one can be
replaced locally by name. A follower keeps working, from its last synced copy,
when the lead is unreachable.

Evan's setup: Dionysus is the lead; the laptop (Leo.app) follows it.

## Non-goals
- Peer-to-peer or two-way sync, merging, conflict resolution. There is one lead and edits happen there.
- Syncing anything else: `tasks` (cron would fire twice), `web`, `client`, `api_clients`, `chat`, state, agents.
- Syncing what an environment *points at*. An environment's definition syncs, but the account behind it does not. Claude config dirs, keychain logins and plugins stay per-host. A follower whose `CLAUDE_CONFIG_DIR` isn't logged in fails the way it would today.
- Path rewriting. Values are copied verbatim; `~` and `$HOME` expand locally as they already do. Different usernames are not handled.
- A follower that is itself a lead (chains).

## Config
```yaml
# follower leo.yaml
follow:
  host: dionysus          # a client.hosts name (required)
  interval: 5m            # re-pull period (default 5m, min 30s)
```
- The lead needs no config. Any Leo host can be followed by anyone who can SSH to it and run its `leo` (the existing `client.hosts` trust model).
- `follow` absent means today's behaviour exactly. External users see no change.
- `follow.host` must name a `client.hosts` entry. Pointing it at the host itself is rejected.

## Behaviour
**Pull.**
- The follower's daemon runs `leo config export --follow` on the lead over SSH, through the same transport as `leo agent --host` (same quoting rules). It runs locally on the lead and ignores the lead's own `client.default_host`.
- Export returns JSON: the lead's *effective* `templates` (after the lead's own `templates_file` merge), `environments`, `delegation`, `defaults`, plus the lead's config revision and Leo version.
- Pulls happen on daemon start, every `interval`, on `leo sync`, and from the web UI "Sync now" button.

**Cache.**
- A successful pull is written atomically to `~/.leo/state/follow-cache.json` (0600, since it can hold env tokens), with `pulled_at` and the lead revision.
- When a pull fails, the cache is used unchanged and the failure is recorded: `last_error`, `last_attempt`.
- With no cache yet, there is no lead layer. Local config only, plus a warning.

**Layering, per section, lowest to highest:** lead, then the follower's `templates_file` (templates only), then the follower's `leo.yaml`.
- `templates`, `environments`, `delegation.profiles`: a local entry **replaces** a lead entry of the same name entirely. Fields are never merged, the same rule as `templates_file`.
- `defaults` and the scalar fields of `delegation` (e.g. `active` profile, `roles`): a key set locally replaces the lead's value for that key.
- An entry can't be deleted locally; overriding it is the only way to change it.

**Validation.**
- The effective config is validated as today, but a lead-sourced entry that fails on the follower is marked **invalid** (for example a harness that isn't installed, or a missing env dir). The entry shows the reason and is unusable, and the rest of the config still loads.
- Local entries keep failing loudly as today.
- A task or agent that references an invalid entry fails with that entry's reason.

**Writes on a follower.**
- Editing or deleting a lead-sourced entry, via the web UI, the `leo template` CLI or the config API, is refused with `owned_by_lead`, naming the lead host.
- The web UI and CLI offer an explicit **Override locally** action: `leo template override <name>`, and the equivalent for environments and profiles. It copies the lead entry into local `leo.yaml` for editing.
- Deleting a local override shows the lead's entry again.

**Visibility.**
- Each entry is badged `lead`, `local`, `local · overrides lead` or `lead · invalid`, extending the existing `templates_file` badges and the `leo template list` SOURCE column.
- `leo sync --status` and the web Settings page show the lead host, `pulled_at` age, the lead revision, and the last error.
- The cache is called **stale** when `pulled_at` is older than 3 × `interval`. Stale is shown but nothing is blocked.

**Reload.**
- A pull that changes the lead revision triggers the same in-process config reload as `leo service reload`.
- Agents keep running; changes apply on their next start, as with any config edit today.

## Interaction with agent move
The move preflight reads the destination's *effective* config, so lead-sourced
entries count. A destination where an entry is `lead · invalid` refuses as
"missing", with the entry's reason.

## Errors
| Case | Behaviour |
|---|---|
| Lead unreachable or ssh auth fails | Keep the cache; record `last_error`; retry next interval. |
| The lead's `leo` predates `config export --follow` | Same as unreachable, with `lead_too_old: <version>`. |
| Export JSON malformed or schema-invalid | Reject the whole pull and keep the old cache. |
| Lead entry invalid locally | That entry is marked invalid; the rest applies. |
| `follow.host` is not in `client.hosts` | Config validation error, raised at load. |

## Testing
- Unit tests for each layering rule: replace by name, key-level override for scalars, `templates_file` in the middle.
- Unit tests for the invalid-entry isolation and the `owned_by_lead` refusal on each write path (web, CLI, config API).
- Cache tests: atomic write, used on failure, stale threshold (with an injected clock).
- Argv assertion for the ssh export call. The e2e test uses two isolated daemons and the fake-ssh shim from the move work.
- Live: the laptop follows Dionysus. A template edit on Dionysus appears on the laptop within one interval. A laptop override survives a pull. With Dionysus unreachable, the laptop still spawns from the cache.

## Follow-ups (not this spec)
- Retire the paused ai-config `templates_file` plan, and point Rocket at this spec.
- Pushing changes from the lead to followers instead of polling, if 5 minutes proves too slow.
