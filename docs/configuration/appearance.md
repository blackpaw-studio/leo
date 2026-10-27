# Agent Appearance

Any agent can have an optional emoji, an optional color, or both. The Leo apps show them on the agent's row; an agent with neither looks exactly as it did before. Each value is either set by hand on one agent or comes from ordered rules in `leo.yaml`. The daemon owns both and resolves them, so every app shows the same result.

```yaml
appearance_rules:
  - when: { field: environments, op: contains, value: prod }
    color: red
  - when: { field: name, op: glob, value: "alfred-*" }
    color: pink
    emoji: "🎩"
  - when: { field: harness, op: is, value: codex }
    emoji: "🧪"
```

## Palette

Colors are names from a fixed palette: `red`, `orange`, `yellow`, `green`, `teal`, `blue`, `indigo`, `purple`, `pink`, `gray`. Each app maps a name to its own light and dark shade. There are no hex colors.

An emoji is exactly one grapheme cluster (a ZWJ sequence, a flag, a skin-toned or keycap emoji all count as one) of at most 64 bytes.

## Manual values

An agent's hand-set emoji and color live on its record in `state/agents.json`, next to its environments override, not in `leo.yaml`. A rename carries them to the new name and deleting the agent removes them. Set them from the apps (Appearance… on the agent) or through `PUT /agents/{name}/appearance` (below).

## Rules

`appearance_rules` is an ordered list. Each rule has a `when` condition and sets a `color`, an `emoji`, or both; a rule that sets neither is invalid.

| `when.field` | Tests |
|---|---|
| `name` | the agent's name (`leo-…`) |
| `environments` | the agent's effective [environments](environments.md) list |
| `template` | the agent's template name |
| `harness` | its template's harness; an unset harness is `claude` |

| `when.op` | On a string field | On `environments` |
|---|---|---|
| `is` | equal | the list contains the value |
| `contains` | substring | the list contains the value (membership, not substring) |
| `starts_with` | prefix | any element starts with the value |
| `glob` | whole-string match | any element matches |

`when.value` is required. Matching is case-sensitive. A `glob` supports only `*` (any run of characters, `/` included) and `?` (exactly one character, a Unicode code point); `[`, `]` and `\` are rejected. Rules are per daemon, so there is no machine field.

## Resolution

Color and emoji resolve independently. For each one:

1. the agent's manual value wins;
2. otherwise the first rule, top to bottom, that sets it and matches;
3. otherwise it is unset.

So a rule can supply an agent's color while its emoji is set by hand, and the first rule that matches but only sets a color does not stop a later rule from supplying the emoji. An invalid rule is skipped and never matches; the rules after it keep their positions.

The daemon re-resolves on every config reload (every rules write and every config API write reloads), when an agent's manual values, environments or template change, and when it is renamed. A `leo.yaml` edit made in an editor shows up in the next `/state` read, but subscribers to the event stream only hear about it after a reload (`leo service reload` or `POST /config/reload`).

## Validation

A bad rule is reported as `appearance_rules[i]: reason`, through the same error path as an undefined environment: `leo validate` fails, `leo service start` and `leo run` refuse to start until it is fixed, and config API writes are refused while it is on disk (`PUT /appearance/rules` can always replace it). A running daemon is unaffected: a reload loads the bad rule, skips it when resolving, and lists it in `GET /appearance/rules`.

Even a rule that does not parse (a string where the `when` map belongs, say), or an `appearance_rules` value that is not a list, never fails loading `leo.yaml`. It loads as an invalid entry, is reported the same way, and any save writes it back unchanged.

## API

Advertised as the `agent_appearance` feature on the event stream's `hello` and in `/version` capabilities. Gate UI on it: an older daemon sends no appearance and has none of these routes.

### On agents

Every agent in `/state` (socket), `GET /api/v1/state` and `agent_spawned` carries the resolved values and where each came from:

```json
"appearance": {"emoji": "🏛️", "color": "teal", "emoji_source": "manual", "color_source": "rule:0"}
```

A source is `manual` or `rule:<i>`, where `i` is the 0-based index into `appearance_rules`. An unset value and its source are omitted, and when neither value is set the `appearance` key is absent altogether.

`agent_appearance_changed` carries `{seq, at, agent, appearance}`, with `appearance` an explicit `null` when nothing is set. It is published after a manual set, after every config reload (for every agent, live or stopped, without diffing, so apply it as-is), after an environments or template change, and after a live rename (whose `agent_spawned` carries no appearance). A stopped agent's rename carries the appearance on its `agent_spawned` instead.

### Rules

Served on the daemon socket and under `/api/v1` with the operator token or a paired device token, like the [config API](config-api.md); the agent token is refused with 403.

`GET /appearance/rules` answers the rules in order with the `leo.yaml` revision (also the `ETag`) and the problems of any rule that is invalid on disk:

```json
{"ok": true, "data": {"revision": "sha256:…", "rules": [{"when": {"field": "environments", "op": "contains", "value": "prod"}, "color": "red"}], "errors": [{"index": 2, "reason": "color \"magenta\" is not in the palette (…)"}]}}
```

`PUT /appearance/rules` with `{"rules": [...]}` replaces the whole list atomically. It follows the config API's [revision rules](config-api.md#revisions-and-concurrency): `If-Match` is required (428 `revision_required` without it, 409 `revision_mismatch` when stale). The first invalid rule is a 400:

```json
{"ok": false, "code": "invalid_appearance_rule", "error": "appearance_rules[1]: color \"magenta\" is not in the palette (…)", "data": {"index": 1, "reason": "color \"magenta\" is not in the palette (…)"}}
```

A malformed body (unknown keys, a non-object rule) is 400 `bad_request`. On success it answers 200 with the same document as the `GET` (new revision, `errors: []`). An empty list removes `appearance_rules` from `leo.yaml`. Like every config API write, it rewrites `leo.yaml` from the parsed config, so comments are not kept, and any other open editor's next write gets a 409.

### Manual values

`PUT /agents/{name}/appearance` on the socket and `PUT /api/v1/agents/{name}/appearance` (operator or paired device token) take `{"emoji"?, "color"?}`: a string sets that value, `null` clears it, and an absent key leaves it unchanged. "Reset to rules" is `{"emoji": null, "color": null}`. `{name}` resolves like the other agent routes.

| Status | Body |
|---|---|
| 200 | `data: {"agent", "appearance"}`, the resolved appearance (`null` when nothing is set) |
| 400 `invalid_appearance` | a value is off the palette or not a single emoji; `data: {"field", "reason"}` |
| 400 `bad_request` | malformed body: unknown keys or a non-string value |
| 404 `not_found` | no such agent, or one with no agent record |
| 409 `ambiguous` | the name matches more than one agent |
