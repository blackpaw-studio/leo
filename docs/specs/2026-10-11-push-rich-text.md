# Rich push notification text

## Goal

Pushes say *what* needs attention, not just the state. A question push shows
the question, a permission push shows the command or path, and a finished
push shows what the agent said it did.

## Wire format

- `push.Alert` gains `Body string`. The v1 plaintext gains an optional
  `"body"` key that is omitted when empty. `payloadVersion` stays 1: the key
  is additive, and the app's Codable decoder ignores unknown keys. Older app
  builds show the title only.
- `title` keeps its role as the short state line ("Has a question",
  "Needs permission: Bash", "Finished", "Hit an error"). `agent` is unchanged.
- Body sanitising: strip control characters except `\n`, collapse runs of
  blank lines, and clip to 400 runes with `…`.
- Fitting into `maxPlaintextBytes`: trim `body` first, then `title`
  (existing logic). An alert is dropped only if it won't fit with an empty
  body and an empty title (current behavior).
- `internal/push/doc.go` and the push section of
  `docs/configuration/config-reference.md` document the `body` key.

## Body sources

| State / reason | Body |
|---|---|
| needs_input · question | Text of the first `AskUserQuestion` question. If there are several, append ` (+N more)` |
| needs_input · permission | Existing `AttentionReason.Detail` (command, file path, URL or pattern from `permissionSummary`) |
| needs_input · elicitation | `AttentionReason.Detail` if set |
| finished | The turn's last-assistant-message preview (`ClampPreview`, ≤280 runes) |
| errored | `AttentionReason.Detail` if set |
| anything else / missing | Empty, so the push is title only |

### Question text

The leo-bridge mod (`leo-bridge/hooks/register.js` ~:1308) raises the
`question` reason with only `{kind, tool}`. Change it to also send
`detail` = the formatted question text from `tool_input.questions`, through
the same clamp as permission details. That puts it on
`AttentionReason.Detail` and in `/api/v1`, the same exposure class as the
permission commands that already flow there. The implementer checks the
`private?` test fixture in `tests/observe.test.ts`. If it documents an
explicit privacy decision, they stop and report it rather than overriding
it.

### Finished preview

The bridge already keeps `ClampPreview(ev.Message)` on turn completion
(`internal/observe/bridgefeed.go` ~:278, finished at ~:294). Rule:

- If the event carrying the preview is published **before** the finished
  attention transition for the same agent, the Notifier caches the latest
  preview per agent (cleared on `AgentStoppedPayload`, alongside `last`) and
  uses it as the body of the next `finished` alert. This keeps `observe`
  types unchanged.
- Otherwise, add an omitempty `Detail` to the finished transition's
  `AgentAttention.Reason` path, or its nearest equivalent, so the preview
  travels with the transition. A test pins whichever ordering is chosen.

## Privacy

Content stays end-to-end encrypted to the device: the relay and APNs see
ciphertext only. What does change is lock-screen visibility, because
questions, commands and agent summaries now appear in the notification. iOS
"Show Previews: When Unlocked" controls this. The docs say so. There's no
leo toggle (YAGNI; can be added to the `push` block later).

## Out of scope

- Dispatch or task labels in the title (the agent name is already shown).
- Codex and opencode enrichment beyond what their attention reasons already
  carry. Their body falls back to empty.
- iOS app rendering. leoterm-ios is told about the `body` key after merge.

## Testing

- `push`: body is included in the plaintext when set and omitted when
  empty. Sanitising and the 400-rune clip work. Fitting trims body before
  title, and an oversized body still delivers with a trimmed body.
- Notifier: correct body for each row of the table. Finished uses the
  cached preview and doesn't reuse a stale one after the agent stops.
- Mod: the `AskUserQuestion` reason carries question detail, including
  multiple questions → `(+N more)`. Run with the repo's existing JS test
  command.
- `make test`, `make lint`, and `env -u TMUX go test -race` on the touched
  packages.
