# Config API

The daemon exposes everything the web UI's settings pages edit as a JSON API,
so a client (the Leo apps) can render and save configuration without
scraping HTML. It is the same code the web forms run: a save through the API
validates, persists and reloads exactly like a save from the browser.

The API is **operator-only**. It is served in two places with identical
bodies:

| Where | Path prefix | Auth |
|-------|-------------|------|
| Daemon unix socket (mode 0600) | none (`/config/schema`) | the socket |
| Web listener | `/api/v1` (`/api/v1/config/schema`) | the operator bearer token, or a paired device token (a paired app is operator-equivalent by design); the agent token and API-client tokens get 403 |

The daemon advertises it: `GET /version` (socket) and `GET /api/v1/version`
return `{"version", "capabilities": [...]}`, and `config_api` is in the list.
Gate UI on the capability, not the version string.

Every response uses the standard envelope `{"ok", "data", "error", "code",
"errors"}`. `errors` appears only on a 422.

## Revisions and concurrency

Writes **rewrite `leo.yaml` from the parsed config**: comments, key order and
keys leo does not know are not preserved (the web UI's saves do the same).
Anyone who hand-annotates the file should edit it in an editor, not through
the API.

`leo.yaml` is the single source of truth and edits can come from the
browser, the CLI or an editor. Every read returns the file's **revision**,
`sha256:<hex of the file bytes>`, both in the body and as the `ETag` header.
Every write must send it back in `If-Match`:

| Status | Meaning |
|--------|---------|
| 428 `revision_required` | no `If-Match` header; nothing was written |
| 409 `revision_mismatch` | the file changed since the read; the body's `data.revision` is the current one. Re-read, re-apply, retry |
| 200 / 201 | written; the body carries the new revision |

When `templates_file` is set the revision covers both `leo.yaml` and the shared templates file, so a change to either (a `git pull`, say) invalidates a stale `If-Match`. On a [follower](follow-lead.md) it also covers the lead cache, so a pull that changes the lead layer does the same.

`If-Match` takes the revision bare (`sha256:...`), quoted as the `ETag` header
carries it (`"sha256:..."`), or weak (`W/"sha256:..."`). There is no way to
write blindly: `*` is not accepted.

## Schema

`GET /config/schema` describes every resource and field, so a client renders
forms from it instead of hard-coding them. It works even when `leo.yaml` is
currently invalid. Fields carry `type` (`string`, `int`, `bool`, `enum`,
`reference`, `list`, `map`, `duration`), `default`, `constraints` (`required`,
`min`, `max`, `pattern`), `options`, `help`, `group`, `advanced`, `warning`,
`nullable` and `restart`. `harnesses` lists each harness's option fields.
Constraints are hints for pre-checking; the daemon stays authoritative.

`restart` says what a change needs to take effect: `agents` (running agents
must be restarted), `service` (the daemon must restart) or `none`.

## Resources

| Resource | Kind | Extra actions |
|----------|------|---------------|
| `defaults` | singleton | |
| `web` | singleton | |
| `client` | singleton | |
| `templates` | collection | add, rename, duplicate, delete |
| `tasks` | collection | add, delete |
| `environments` | collection | add, rename, delete |
| `hosts` | collection | add, delete |
| `delegation` | bespoke (below) | |

An action a resource does not offer is 404.

### Reads

`GET /config/{resource}` returns a singleton's `{revision, values, inherited?,
effective_harness?}`, or a collection's `{revision, items: [{name, values,
...}]}`. `GET /config/{resource}/{name}` returns one item. Values are typed
(`null` is "unset, inherit"). `inherited` holds what an empty field falls back
to from `defaults`. Subsections nest under their key (`values.permissions`,
`values.push`); harness options under `values.harness_options`. Secrets are
write-only: they read as `{"set": true|false}`.

### Writes

| Request | Body | Status |
|---------|------|--------|
| `PUT /config/{resource}[/{name}]` | `{"values": {...}}` | 200 |
| `POST /config/{resource}` | `{"name", "schedule"?}` | 201 |
| `POST /config/{resource}/{name}/rename` | `{"new_name"}` | 200 |
| `POST /config/{resource}/{name}/duplicate` | `{"new_name"}` | 201 |
| `DELETE /config/{resource}/{name}` | none | 200 |
| `POST /config/{templates,environments}/{name}/override` | none | 200 ([follower](#follow-lead) only) |

`PUT` is a patch applied key by key: a key you send replaces the value, `null`
clears it, a key you omit is left exactly as it is (it is not re-parsed or
re-trimmed, so a list item that contains a comma survives a write of another
field). Sending a field the schema does not define is 400;
a bad value is 422. Nested
objects (`permissions`, `push`, `harness_options`) merge the same way: a key
inside them replaces, `null` clears just that key, an omitted key is kept, and
`"harness_options": null` clears every option. Changing `harness` drops the old
harness's options (they belong to it, as in the browser form), so a switch
cannot be rejected over a leftover option; options sent in the same write are
applied to the new harness.

A write answers `{revision, restart_required: {agents, service}, warning?,
item?}`. `restart_required` reports the **accumulated** flags, not just this
write: it stays set until the service or the agents are restarted. `warning`
means the file was saved but the live reload failed.

### Secrets

Credential entries of an `env` map never leave the daemon. An entry is a
credential when its key (case-insensitive) contains `TOKEN`, `SECRET`,
`PASSWORD`, `PASSWD` or `CREDENTIAL`; contains `API_KEY`, `PRIVATE_KEY` or
`ACCESS_KEY` (the underscore or dash is optional); or has `AUTH` as a whole
segment (`ANTHROPIC_AUTH_TOKEN`, `SERVICE_AUTH`, not `AUTHOR`). This applies to
every `env` field (defaults, templates, tasks, environments). Reads and write
responses show such an entry as `{"set": true|false}` and every other entry as
its string:

```json
"env": {"ANTHROPIC_AUTH_TOKEN": {"set": true}, "ANTHROPIC_BASE_URL": "http://localhost:3325"}
```

An `env` patch is applied entry by entry: `"value"` sets or replaces the entry,
`{"set": true}` (what a read returned) keeps its current value (422 on `env` if
there is none), `null` deletes it, and an entry left out is kept. `"env": null`
clears the whole map. A client can therefore echo back what it read and change
only what the user edited. (The web UI still shows env values in full; the
redaction is the API's.)

### Validation errors (422)

```json
{"ok": false, "code": "invalid", "error": "config validation failed: ...",
 "errors": [{"field": "schedule", "message": "tasks.heartbeat.schedule: expected 5 fields, got 3"}]}
```

`field` is the request key (`harness_options.permission_mode`,
`permissions.deny_tools`, `new_name`, ...). A problem outside the edited
resource (another resource that is already invalid on disk) has `field: ""`.
A name that is taken is 422 on `name` / `new_name`.

Add, rename and duplicate answer with the same write body, with `item` set to
the new item's document (status 201 for add and duplicate).

## Web UI pages

The matching web pages (for an "Edit in the web UI" link; there is no `/config`
index): Defaults `/config/defaults`, Templates `/config/templates[/{name}]`,
Tasks `/tasks[/{name}]`, Environments `/config/environments`, Delegation
`/config/delegation`, Web UI, Remote client and Remote hosts
`/config/settings`, Service `/service`.

## Delegation

Delegation is a grid (profile × role), not a form, so it has its own routes
under `/config/delegation`. All writes need `If-Match` and answer with the
usual write body whose `item` is the refreshed document. With no `delegation:`
block, reads answer `configured: false` and writes are 404
`delegation_not_configured`.

| Request | Body |
|---------|------|
| `GET /config/delegation` | document: `revision, configured, enabled, active_profile, hide_native_agents(_is_default), templates, roles, profiles, warnings, recent_consults` |
| `GET /config/delegation/preview?profile=` | `{profile, changes: [{role, kind, before, after}], missing}` (what activating it would change) |
| `PUT /config/delegation/profiles/{p}/roles/{r}` | `{template, model, effort}`; an empty `template` deletes the cell |
| `PUT /config/delegation/roles/{r}` | `{use_for}` |
| `PUT /config/delegation/active` | `{profile}` |
| `PUT /config/delegation/enabled` | `{enabled}` |
| `PUT /config/delegation/hide-agents` | `{names}` |
| `POST /config/delegation/roles` | `{name, template}` (201) |
| `POST /config/delegation/roles/{r}/rename` | `{new_name}` |
| `DELETE /config/delegation/roles/{r}` | |
| `POST /config/delegation/profiles` | `{name}` (201) |
| `POST /config/delegation/profiles/{p}/rename` | `{new_name}` |
| `POST /config/delegation/profiles/{p}/duplicate` | `{new_name}` (201) |
| `DELETE /config/delegation/profiles/{p}` | |
| `POST /config/delegation/profiles/{p}/override` | none ([follower](#follow-lead) only) |

A cell's validation errors land on `template`, `model` or `effort`.

## Follow lead

On a host with a `follow:` block ([Following a lead host](follow-lead.md)),
templates, environments and delegation profiles can come from the lead.

- **Sources.** Item documents (`GET /config/templates`, `/config/environments`
  and their items) and each profile in the delegation document gain `source`:
  `lead`, `local`, `local-override-lead` or `shared-override-lead` (plus
  `shared` / `local-override` with a `templates_file`). A lead entry that is
  unusable on this host is listed with empty `values` and an `invalid` reason.
  Both fields are omitted on a host without `follow`, so its documents are
  unchanged.
- **Ownership.** A write that would edit, rename or delete a lead entry is
  409 `owned_by_lead` with `data: {kind, name, lead}`, and nothing is written.
  The check runs before validation, so it is the error you see even when the
  write would also break a reference.
- **Override.** `POST /config/templates/{name}/override`,
  `POST /config/environments/{name}/override` and
  `POST /config/delegation/profiles/{p}/override` copy the lead's entry into
  `leo.yaml` as a local override (revision-gated like any write; the item is
  the entry's new document). A non-lead entry or one already overridden is
  422, an invalid lead entry is 422 (fix it on the lead), and an unknown name
  is 404.
- **Status and sync.**

| Request | Answer |
|---------|--------|
| `GET /config/follow` | the follow report: `following, host, ssh, interval, pulled_at, age_seconds, stale, lead_revision, lead_version, last_attempt, last_error, last_error_code, warnings, invalid` (`following: false` and nothing else on a non-follower) |
| `POST /config/follow/sync` | pulls now through the daemon's follow loop: `{changed, status}`. 409 `not_following` without a `follow:` block, 503 `follow_unavailable` when no follow loop runs, 502 with the follow error code (`unreachable`, `lead_too_old`, ...) and the report in `data` when the pull fails |

`GET /api/v1/follow` serves the same report on the observability API
(operator token). `/api/v1/templates` and `/api/v1/environments` gain the same
omitempty `source` and `invalid` fields and list invalid lead entries.

## Appearance rules

`appearance_rules` has its own routes, outside `/config/{resource}`, with the
same auth, revision and envelope rules as the rest of this API. See
[Agent Appearance](appearance.md#rules) for the bodies.

| Request | Result |
|---------|--------|
| `GET /appearance/rules` | `{revision, rules, errors: [{index, reason}]}`; `errors` lists rules invalid on disk (warnings only: they never block other config writes) |
| `PUT /appearance/rules` | `{rules}` replaces the list; `If-Match` required; the first invalid rule is 400 `invalid_appearance_rule` with `data: {index, reason}` (not a 422); 200 answers the new document |

An agent's own emoji and color are not config: they live on its agent record
and are set with `PUT /agents/{name}/appearance` (see
[Agent Appearance](appearance.md#manual-values)).

## Operations

| Request | Result |
|---------|--------|
| `POST /tasks/{name}/run` | 202 `{name, status: "started"}`; runs `leo run` detached |
| `GET /tasks/{name}/history?limit=20` | run history, newest first (`limit` at most 200) |
| `GET /tasks/{name}/log?file=` | `{file, text, truncated?}`; `file` must be one of the task's logs. Only the last 4 MiB is read; a longer log comes back cut to whole lines with `truncated: true` |
| `GET /config/tasks/{name}/prompt` | `{revision, file, files, content}`; always the task's configured `prompt_file` (a `?file=` query is ignored) |
| `PUT /config/tasks/{name}/prompt` | `{content}`; writes the task's current `prompt_file` |
| `GET /cron/preview?expr=` | `{valid, description, error?}` |
| `POST /service/restart` | 202; graceful `leo service restart`, clears the service banner. Single-flight: while a restart is running another request is 409 `restart_in_progress` (the restart is not queued or repeated) |
| `POST /service/restart-agents` | `{restarted, skipped, failed: {name: message}}`; clears the agents banner only on zero failures |
| `GET /service/log?lines=200` | `{text}`, at most 1000 lines |
| `GET /service/status` | `{version, pid, started_at, config_path, log_path, restart_required, processes}` |
| `POST /config/reload` | `{revision}`; re-reads `leo.yaml` into the running daemon |

The prompt file has its own revision: the sha256 of its bytes (the hash of
nothing when the file does not exist yet). `PUT` needs it in `If-Match`
(428 / 409 as above) and creates the file when it is missing. The prompt path
is confined to the task's workspace, and a `prompt_file` that is `leo.yaml`, resolves (through symlinks or hard links) to it, or lies under leo's state directory is refused with 422 on `prompt_file`, for both `GET` and `PUT`. `files` omits those paths. To edit a different prompt, change the task's `prompt_file` field.

## Contract tests

The wire bodies are pinned as golden files in
`internal/web/testdata/configapi/`; regenerate them with
`go test ./internal/web -run TestConfigAPIGolden -update` and review the diff.
Clients (the Leo apps) build their decoder tests from these files, so a diff is
an API change. Two other tests keep the API complete: every registered field,
subsection and harness option must round-trip through `PUT`/`GET`, and every
config-affecting web route must have a JSON twin (`routeTwins` in
`config_api_twins_test.go`).
