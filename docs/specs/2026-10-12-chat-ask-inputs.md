# Typed chat ask inputs; retire leo_surface_file

Status: draft · 2026-10-12

## Goal

An agent can show Evan a file with a caption, and optionally ask a question about it using buttons, radio, checkboxes, or free text — through one mechanism: Leo Chat. `leo_surface_file` is deleted.

## Decisions

- **Show a file** = `leo_chat_post {text, files}` (exists today). The caption is `text`. No new tool.
- **Ask about a file** = two messages: `leo_chat_post {text, files}`, then `leo_chat_ask`. Asks carry no files or caption; per-agent threads keep the pair adjacent. `leo_chat_ask` only gains a typed `input`.
- **One input per ask.** Multi-field forms are out of scope; the agent asks again.
- **`leo_surface_file` is removed** with its route, state, and event. Solo-user project: no alias, no compat shim.

## `leo_chat_ask` input (new shape)

```jsonc
{
  "question": "Ship this layout?",          // required, as today
  "input": {
    "type": "buttons" | "radio" | "checkboxes" | "text",
    "options": [ "yes" | {"value","label"} ], // required for buttons/radio/checkboxes (1–12), forbidden for text
    "allow_text": false,                      // choice types only: also accept a free-text answer
    "min": 0, "max": N,                       // checkboxes only; defaults 0..len(options)
    "placeholder": "…"                        // text, or the allow_text field
  },
  "timeout_s": 600,                          // unchanged
  "message_id": "…"                          // resume, unchanged
}
```

- `input` omitted ⇒ `{type: "buttons", options}` using top-level `options`, keeping today's call shape valid for agents. (Top-level `options` is an error when `input` is present.)
- `buttons`: tapping submits immediately. `radio`: select then submit (pairs with `allow_text`). `checkboxes`: select within `[min,max]` then submit. `text`: a single free-text field.

## Answer shape (breaking)

Stored and returned answer replaces the single string:

```jsonc
{ "values": ["a","c"], "text": "optional free text" }
```

Validation in `validAnswer` (`internal/chat/outbound.go`), by input type:
- buttons/radio: `values` has exactly 1 known option, unless `allow_text` and `values` is empty with non-empty `text`.
- checkboxes: every value known, no duplicates, `min ≤ len ≤ max`. With `allow_text`, `text` may accompany or replace selections (an empty selection is allowed only when `text` is set, even if `min > 0`).
- text: `values` empty, `text` non-empty, ≤ 4000 runes.
- `text` on a choice type without `allow_text` ⇒ 400.

The tool returns `{values, labels, text, timed_out, message_id, pending?}`. The `value`/`label` scalars are removed.

## Daemon changes

- `internal/chat`: Ask stores the `input` spec on the message; answer becomes the struct above in store, `Transition`, waiter, and the `AskResult`. A schema change to chat.db → this work lands the chat.db migration mechanism called for after v0.49.0 (a versioned `user_version` migration step), not an ad-hoc ALTER.
- `internal/web/handlers_chat.go`: answer endpoint accepts `{values, text}`; the Message JSON exposes `input` and the answer object.
- `internal/mcp/chat.go`: new schema + arg parsing.
- Push: unchanged (post and ask each push as today). Coalescing a post+ask pair is a possible leoterm-ios follow-up.
- Permission prompts are untouched (allow/deny stays its own path).

## Removal of leo_surface_file

Delete: MCP tool + args (`internal/mcp/tools.go`), route + handler (`internal/web/handlers_surface_file.go`, `web.go` registration), `internal/observe/surfaced_files.go`, the `surfaced_files` snapshot field, the `file_surfaced` event, service wiring, and their tests. Keep `handlers_files.go` only if something else uses it; otherwise delete it too. Update `docs/specs/2026-07-31-observability-api.md`, `docs/configuration/chat.md`, and any skill or prompt text in-repo that mentions surface_file.

Outside this repo (not part of the PR; tracked as follow-ups): global CLAUDE.md / ai-config "Showing Evan a File" section → `leo_chat_post`; leoterm-ios drops surfaced_files and renders the new ask inputs/answer shape.

## Testing

TDD in `internal/chat` for validation per input type and the answer round-trip (ask → answer → waiter result), plus resume via `message_id`. Handler tests for the answer endpoint's 400 cases. A migration test opens a v0.49.0-shaped chat.db and upgrades it. Live check: an isolated test daemon, agent posts a file then calls `leo_chat_ask` with checkboxes, answered via curl.

## Out of scope

Files or caption on an ask, multi-field forms, file line pointers (`line` from surface_file), editable answers after submit, web UI rendering of asks beyond what exists.
