# Sync appearance rules through follow-lead

Status: draft · 2026-11-09 · Requested by Evan via leoterm

## Goal

A follower (for example the laptop) uses its lead's (Dionysus's) `appearance_rules` unless it has a local list of its own. leoterm can show where the rules come from, and editing rules on a follower creates a local override.

## Non-goals

- Manual per-agent emoji and color (`state/agents.json`). These stay per host.
- Merging lead and local lists. The list is ordered, so one whole list always wins.
- Bumping the follow export schema.

## Design

### Export

- `leo config export --follow` gains `appearance_rules`, holding the lead's list. A lead can't be a follower itself (`lead_is_follower`), so this is always the lead's local list.
- The key is always present when the lead's leo supports it, even as an empty list. Followers on an older leo ignore unknown keys, so `FollowExportSchema` stays at 2.
- If the export has no `appearance_rules` key (an older lead), the follower treats that as "the lead has no rules". It does not keep a stale cached list.

### Resolution on the follower

- If the follower's `leo.yaml` contains the key `appearance_rules`, that list wins whole, including an empty list. This matches follow-lead's existing rule that an empty local value overrides the lead's.
- Otherwise the follower uses the lead's list from the follow cache.
- A host that isn't following behaves exactly as it does today.
- Lead rules are validated just like local ones. Invalid rules surface as warnings and are skipped, as today. A bad lead rule never blocks the follower.
- `ResolveAppearance`, the observability snapshot and every other consumer read the effective list. The agent's `rule:<i>` source index refers to that effective list.

### API: `/appearance/rules`

- **GET** returns `{revision, rules, errors, source}`, where `source` is one of:
  - `"lead"`: following, with no local key.
  - `"local"`: a local key is present, or the host isn't following.
- **PUT** works as it does today, and on a follower it writes the local key. That creates the override, and `source` becomes `"local"`. It needs no new 409.
- **DELETE** (new) removes the `appearance_rules` key from `leo.yaml` and needs `If-Match`.
  - On a follower, the rules revert to the lead's list (`source: "lead"`).
  - On a host that isn't following, the rules become an empty list.
  - It returns the same body as GET. If no local key exists, it is a no-op that still returns 200.
- `revision` must change whenever the effective list changes, including when a follow pull updates the lead's list. That keeps `If-Match` honest.

### CLI and UI

There's no appearance-rules view in the web UI or in `leo config show` today, so this adds none. leoterm surfaces `source`. `leo validate` already warns on rules from the effective list.

## Compatibility

- **Old follower, new lead:** the key is ignored and the rules stay per host, as today.
- **New follower, old lead:** the follower treats the lead as having no rules, so its local list, if any, applies. A follower with no local list ends up with no rules, the same as today.
- **Hosts that aren't following:** unchanged, apart from the new `source: "local"` field and DELETE.
- Additive only. External users need no migration.

## Testing

- **Export:** the key is present (including when the list is empty).
- **Layering:**
  - A lead list with no local key gives `lead`.
  - A local list gives `local`.
  - An empty local list gives `local` with no rules.
  - An export with no key gives no lead rules.
  - An invalid lead rule is skipped and reported as a warning.
- **API:**
  - GET reports `source` in every one of those states.
  - PUT on a follower creates the override.
  - DELETE reverts to the lead, needs `If-Match`, and is a 200 no-op when nothing is local.
  - `revision` changes after a follow pull changes the lead's list.
- **Resolution:** an agent picks up a lead rule on the follower.
- Update the wire golden for the new GET field.

## Docs

- `docs/configuration/follow-lead.md`: what gets synced, and the override semantics.
- `docs/configuration/appearance.md` and `config-api.md`: `source`, follower PUT, and DELETE.
- The observability spec's appearance section.

## Hand-off to leoterm

Once this is released, tell leoterm the contract: the `source` field, PUT creating the override on a follower, DELETE to revert, and the leo version that ships it.
