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
| Web listener | `/api/v1` (`/api/v1/config/schema`) | operator bearer token; the agent token and device tokens get 403 |

The daemon advertises it: `GET /version` (socket) and `GET /api/v1/version`
return `{"version", "capabilities": [...]}`, and `config_api` is in the list.
Gate UI on the capability, not the version string.

Every response uses the standard envelope `{"ok", "data", "error", "code",
"errors"}`. `errors` appears only on a 422.

## Revisions and concurrency

`leo.yaml` is the single source of truth and edits can come from the
browser, the CLI or an editor. Every read returns the file's **revision**,
`sha256:<hex of the file bytes>`, both in the body and as the `ETag` header.
Every write must send it back in `If-Match`:

| Status | Meaning |
|--------|---------|
| 428 `revision_required` | no `If-Match` header; nothing was written |
| 409 `revision_mismatch` | the file changed since the read; the body's `data.revision` is the current one. Re-read, re-apply, retry |
| 200 / 201 | written; the body carries the new revision |

There is no way to write blindly: `*` is not accepted.

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

`PUT` is a patch: a key you send replaces the value, `null` clears it, a key
you omit is left alone. Sending a field the schema does not define is 400;
a bad value is 422. A harness change and its `harness_options` can go in one
write.

A write answers `{revision, restart_required: {agents, service}, warning?,
item?}`. `restart_required` reports the **accumulated** flags, not just this
write: it stays set until the service or the agents are restarted. `warning`
means the file was saved but the live reload failed.

### Validation errors (422)

```json
{"ok": false, "code": "invalid", "error": "config validation failed: ...",
 "errors": [{"field": "schedule", "message": "tasks.heartbeat.schedule: expected 5 fields, got 3"}]}
```

`field` is the request key (`harness_options.permission_mode`,
`permissions.deny_tools`, `new_name`, ...). A problem outside the edited
resource (another resource that is already invalid on disk) has `field: ""`.
A name that is taken is 422 on `name` / `new_name`.

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

A cell's validation errors land on `template`, `model` or `effort`.

## Operations

| Request | Result |
|---------|--------|
| `POST /tasks/{name}/run` | 202 `{name, status: "started"}`; runs `leo run` detached |
| `GET /tasks/{name}/history?limit=20` | run history, newest first (`limit` at most 200) |
| `GET /tasks/{name}/log?file=` | `{file, text}`; `file` must be one of the task's logs |
| `GET /config/tasks/{name}/prompt[?file=]` | `{revision, file, files, content}` |
| `PUT /config/tasks/{name}/prompt` | `{content}`; writes the task's current `prompt_file` |
| `GET /cron/preview?expr=` | `{valid, description, error?}` |
| `POST /service/restart` | 202; graceful `leo service restart`, clears the service banner |
| `POST /service/restart-agents` | `{restarted, skipped, failed: {name: message}}`; clears the agents banner only on zero failures |
| `GET /service/log?lines=200` | `{text}`, at most 1000 lines |
| `GET /service/status` | `{version, pid, started_at, config_path, log_path, restart_required, processes}` |
| `POST /config/reload` | `{revision}`; re-reads `leo.yaml` into the running daemon |

The prompt file has its own revision: the sha256 of its bytes (the hash of
nothing when the file does not exist yet). `PUT` needs it in `If-Match`
(428 / 409 as above) and creates the file when it is missing. The prompt path
is confined to the task's workspace.

## Contract tests

The wire bodies are pinned as golden files in
`internal/web/testdata/configapi/`; regenerate them with
`go test ./internal/web -run TestConfigAPIGolden -update` and review the diff.
Clients (the Leo apps) build their decoder tests from these files, so a diff is
an API change. Two other tests keep the API complete: every registered field,
subsection and harness option must round-trip through `PUT`/`GET`, and every
config-affecting web route must have a JSON twin (`routeTwins` in
`config_api_twins_test.go`).
