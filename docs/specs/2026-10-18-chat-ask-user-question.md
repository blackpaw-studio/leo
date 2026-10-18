# AskUserQuestion as a chat questions card

Status: approved by Evan through leoterm on 2026-10-18. The client contract was sent to leoterm the same day and ships in v0.55.0.

## Problem
Claude's `AskUserQuestion` tool reaches Leo Chat through the PermissionRequest hook. `RequestPermission` (`internal/chat/prompts.go`) renders that as a generic Allow/Deny card around clipped JSON, so the user can neither see the options nor pick one.

## Verified hook behaviour (claude 2.1.303, leoterm spike)
- PermissionRequest fires for `AskUserQuestion` with this tool input: `{questions:[{question, header, options:[{label,description}], multiSelect}]}`.
- The hook can answer with output of this shape: `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":{"behavior":"allow","updatedInput":{"questions":<original array>,"answers":{"<exact question text>": "Label" | ["A","B"] | "free text"}}}}}`. The TUI dialog is skipped and the model receives the answers. This is undocumented. The documented route is PreToolUse with `permissionDecision:"allow"` plus the same `updatedInput`, and it works too.
- A hook that prints nothing falls through to the TUI dialog.

## Behaviour
1. When the PermissionRequest tool is `AskUserQuestion` and the tool input parses, post ONE chat message for all the questions:
   - `kind:"ask"`, with no `permission` field.
   - The input is of type `questions` (see the contract).
   - If the input does not parse, fall back to today's Allow/Deny card.
2. When the card is answered, the hook emits `allow` with `updatedInput = {questions: <original array, verbatim>, answers}`. `answers` is keyed by exact question text, built as follows:
   - Single-select: the chosen label, or the free text.
   - Multi-select: an array of the chosen labels, with the free text appended as a final element when present.
3. In these cases the hook prints nothing, so the TUI takes over: dismissal (new `dismiss` endpoint), timeout (the existing permission wait), hook context cancelled, and an answer given in the terminal first. Never deny.
4. Push: this card goes out as the ordinary chat push with `input_type:"questions"` and no options. It must not use the `needs_input` permission alert, i.e. `MessageKind=permission`.
5. Stay on PermissionRequest because the plumbing already exists there. The PR states that this path is undocumented and that PreToolUse is the documented fallback if it ever breaks.
6. Docs:
   - `docs/configuration/chat.md`: the input type, answer, dismiss, and lifecycle.
   - `docs/configuration/dispatches.md` ~452-490: remove the claim that question dialogs are not routed.
   - Changelog.

## Wire contract (as sent to leoterm)
- Input:

  ```json
  {"type":"questions","questions":[{"id":"q1","header":"…","question":"…","multi":false,"allow_text":true,"options":[{"value":"L","label":"L","description":"…"}]}]}
  ```

  - The daemon assigns ids `q1..qN` by position.
  - `value` is equal to `label`.
  - `Option` gains `description` with `omitempty`, and it is valid on every input type.
  - Top-level `options`, `min`, `max`, and `placeholder` are absent.
- Answer body for `POST /messages/{id}/answer`:

  ```json
  {"answers":{"q1":{"values":["L"],"text":"…"}}}
  ```

  - Top-level `values` and `text` must be empty or absent for this type.
  - For any other type, `answers` must be absent. If it is present, the server returns 400.
- Validation (400 on failure):
  - Every question id is present, and no unknown ids appear.
  - Each question has at least one value or non-blank text.
  - Single-select allows at most one value, and does not allow a value and text together.
  - Multi-select values must be distinct.
  - Every value must be a known option.
  - Text is trimmed and limited to `MaxAnswerTextRunes`.
  - Each question allows up to `MaxAskOptions` options. Allow up to 4 questions, which is Claude's limit, and reject more as malformed so the hook falls back to Allow/Deny.
- Settled card: 409. Late answers on a questions card are never delivered as replies, because the tool call is gone.
- Answered state:
  - `state:"answered"`, with `answer = {"values":[],"text":"","answers":{…}}`.
  - Answered in the terminal: `answer = {"values":["terminal"],"text":""}`, with no `answers`.
- Dismiss: `POST /messages/{id}/dismiss` with an empty body. It returns `200 {ok,data:Message}` and sets `state:"expired"`, `closed_reason:"dismissed"`. It returns 400 if the message is not an open questions card and 409 if the card is already settled.
- Timeout: `state:"expired"`, `closed_reason:"timeout"`.
- Events are unchanged: `chat.updated` with `change=answer`, or with `change=state` on dismiss or timeout.
- The observe hello `Features` list gains `"chat.questions"`.
- No schema migration is expected, because `answer` and `input` are stored as JSON. If the stored shape needs a column, append v5 and do not edit v4.

## Out of scope
- Routing AskUserQuestion for dispatch panes through `needs_input` / `leo_send_dispatch`.
- Sequential per-question asks.
