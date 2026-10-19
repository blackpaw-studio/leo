# Plan: AskUserQuestion as a chat questions card

Spec: `docs/specs/2026-10-18-chat-ask-user-question.md` (source of truth; wire contract frozen).
Every step starts with a failing test, then the change, then verification; commit once per step (conventional message).
Verification for each step: `go test -race ./internal/<touched pkgs>/...` and `make lint`.

## 1. Types and validation — `internal/chat/model.go`, `internal/chat/input.go`
- `Option` gains `Description string \`json:"description,omitempty"\``, valid on every input type.
- New: `InputQuestions = "questions"`, `MaxAskQuestions = 4`, `ClosedDismissed = "dismissed"`.
- `Question{ID, Header, Question string; Multi bool \`json:"multi"\`; AllowText bool \`json:"allow_text"\`; Options []Option}`. `multi` and `allow_text` have no omitempty, so they always appear on the wire.
- `Input` gains `Questions []Question \`json:"questions,omitempty"\``.
- `QuestionAnswer{Values []string; Text string}`, marshalled with `values` as `[]` when empty.
- `Answer` gains `Answers map[string]QuestionAnswer \`json:"answers,omitempty"\``.
- `validateInput`, questions branch:
  - top-level options, allow_text, min, max and placeholder are all absent;
  - 1..MaxAskQuestions questions, with ids `q1..qN` by position;
  - question text is non-blank and distinct (answers are keyed by it);
  - each question's options go through `validateOptions`, with value == label.
- `resolveInput` refuses `questions`: only the daemon builds that type, never `leo_chat_ask`.
- `validAnswerFor`:
  - questions type: top-level values/text empty; every id present and no unknown ids; ≥1 value or non-blank text per question; single-select takes ≤1 value and never value plus text; multi-select values distinct; every value a known option; text ≤ MaxAnswerTextRunes.
  - any other type: a non-nil `Answers` is a 400.
- `Answer.isTerminal` also requires `Answers` to be empty.
- RED: table cases in `input_test.go`, one per rule above, plus:
  - marshal shape `{"values":[],"text":"","answers":{"q1":{"values":["L"],"text":""}}}`;
  - `description` is omitted on buttons when empty;
  - a store round-trip of `Input.Questions` and `Answer.Answers` in `store_test.go`. This proves no migration is needed; if it fails, append v5 and never edit v4.

## 2. Tool input to card, answers to updatedInput — new `internal/chat/questions.go`
- `parseQuestions(toolInput json.RawMessage) (questionsCard, error)`:
  - decodes `questions` twice: once as `json.RawMessage`, kept verbatim, and once typed;
  - maps `multiSelect` → `multi`, sets `allow_text` true (Claude always offers "Other"), and maps option `label`/`description` with value = label;
  - keeps each original `question` string untrimmed as the answer key;
  - returns an error on anything `validateInput` rejects (>4 questions, duplicate labels, …), which triggers the Allow/Deny fallback.
- `updatedInput(card, Answer) json.RawMessage` builds `{"questions":<raw verbatim>,"answers":{<exact text>:…}}`:
  - single-select: the label, or the text;
  - multi-select: `[labels…]`, with the text appended as the last element when it is non-empty.
- RED: pure table tests:
  - the multi-select text append;
  - single-select text only;
  - a key with trailing spaces is kept exactly;
  - an unknown field inside a question (`"x":1`) survives in the output `questions`, compared with `sameJSON`;
  - 5 questions → error.

## 3. Route the request — `internal/chat/prompts.go`
- `RequestPermission` returns `Decision{Behavior string; UpdatedInput json.RawMessage}` instead of `string`. Update its callers.
- When `tool_name == "AskUserQuestion"` and `parseQuestions` succeeds, post `Kind: KindAsk` with:
  - no `Permission` and `Asker` empty;
  - `Text` = first question plus `" (+N more)"` (matches the bridge summary), clipped;
  - `Input` = the questions input.
  Then `awaitWith(…, gone: answered/terminalAnswer)`. A settled answer with `Answers` returns `allow` plus `updatedInput`. Every other outcome returns a zero Decision. Never `deny`.
- `awaitWith`: timeout or shutdown on a `KindAsk` closes through `Store.CloseAsk(id, ClosedTimeout)`, because `Transition` never sets `closed_reason`. Keep the answer-won-race fallback; it returns the stored `Answers`.
- RED (`service_test.go`, injected `After`):
  - the card shape: kind ask, no permission, ids q1..qN;
  - answering it → the exact `updatedInput` bytes;
  - malformed input → today's permission card;
  - timeout → `expired`/`timeout` and an empty Decision;
  - ctx cancel → `answered` with `{"values":["terminal"]}` and an empty Decision.

## 4. Withhold late answers — `internal/chat/outbound.go`
- `Answer` trims each `Answers[id].Text` before validation.
- `deliverAnswer` returns early when `Input.Type == InputQuestions`. `awaitWith` never joins the waiter, so `blocked` is always 0 for this card and only this guard stops a reply. Today it is skipped only by accident (`Asker != Agent`).
- `resumeAsk` refuses questions-card ids.
- RED:
  - answering while the hook waits → the thread holds only the card (no inbound reply);
  - answering after a timeout → `ErrConflict` and no reply;
  - `resumeAsk` on a card → `ErrInvalid`.

## 5. Dismiss — `outbound.go`, `internal/web/handlers_chat.go`
- `Service.Dismiss(agent, id) (Message, error)`:
  - wrong agent or missing → `ErrNotFound`;
  - not a questions card → `ErrInvalid` (400);
  - a questions card that is no longer open → `ErrConflict` (409);
  - otherwise `closeAsk(id, ClosedDismissed)`, which settles the waiter with nil, so the hook prints nothing.
- Route `POST {prefix}/{agent}/messages/{id}/dismiss` in `registerChatRoutes`, which covers both TCP and the socket. The handler ignores the body and replies `200 {ok,data:Message}`; errors go through `writeChatError`.
- RED (`handlers_chat_test.go`):
  - 200 with `expired`/`dismissed`;
  - 400 on a buttons ask;
  - 409 on a second dismiss;
  - after a dismiss, a waiting `RequestPermission` returns an empty Decision and publishes `chat.updated` with change=state.

## 6. Answered in the terminal — `prompts.go`
`settlePermissionInTerminal` also settles the agent's open questions card on `PostToolUse` with `tool_name == "AskUserQuestion"`, as answered/terminal. This is belt and braces next to the hook-ctx cancel, mirroring permission cards. A card the chat already answered is a no-op.
RED: open card, then a PostToolUse → answered terminal and an empty Decision.

## 7. Permission endpoint and hook CLI
- `consult.PermissionDecision` gains `UpdatedInput json.RawMessage \`json:"updatedInput,omitempty"\``. The dispatch/orchestrator path never sets it, so it is unchanged (dispatch AskUserQuestion is out of scope).
- `agentPermission` (`handlers_chat.go:620`) returns `consult.PermissionDecision` instead of `map[string]string`, mapped from `chat.Decision`.
- `decodePermissionReply` (`dispatch_permission.go:136`) accepts `updatedInput` only with `allow` and only as a JSON object. Anything else is an error, so the hook stays silent.
- The CLI sets `out["updatedInput"] = decision.UpdatedInput` when behavior is allow and the field is non-empty.
- RED:
  - web: `POST /api/agent/permission` with an AskUserQuestion payload, then a chat answer → `data` equals exactly `{"behavior":"allow","updatedInput":{…}}`;
  - CLI (`dispatch_permission_test.go`, httptest daemon): **assert the exact stdout string**, e.g. `{"hookSpecificOutput":{"decision":{"behavior":"allow","updatedInput":{"answers":{…},"questions":[…]}},"hookEventName":"PermissionRequest"}}\n`. Key order comes from the map encoder; the raw questions are compacted but otherwise identical;
  - `deny` with updatedInput → empty stdout; an array updatedInput → empty stdout;
  - the existing allow/deny stdout tests stay byte-identical.
- **Exec-seam note:** faked seams have already shipped argv bugs past green suites. Every hook output change is pinned by an exact-stdout test, never decode-and-compare, and checked live in step 10.

## 8. Push routing — `internal/push/notifier.go`
- `chatMessage` already pushes a `KindAsk`, with `InputType = "questions"` and no Options (only buttons get them). Pin this with a test.
- `trackPermission` ignores the card, so no needs_input alert ever carries its id or `MessageKind=permission`.
- Dedup: a `needs_input` alert for the same AskUserQuestion (bridge reason `question`, or a classic `permission_prompt` Notification) would be a second push. Today a held classic alert fires with no id after `permissionWait`.
  - Change: hold question-reason alerts like permission ones.
  - When a questions card opens for that agent, drop the held alert; the chat push is the single source.
  - With no card within `permissionWait` (chat off, malformed fallback), send it as today.
- RED (`notifier_chat_test.go`, injected `afterFunc`):
  - card → exactly one alert: `MessageKind:"ask"`, `InputType:"questions"`, nil Options;
  - question needs_input then the card → one alert total;
  - no card → the needs_input alert after the wait.

## 9. Hello feature — `internal/observe/event.go`
Add `FeatureChatQuestions = "chat.questions"` to `Features()`.
RED: extend `TestFeaturesMatchContract` (`clamp_test.go:67`).

## 10. Docs, changelog, final verification
- `docs/configuration/chat.md`:
  - the `questions` input and `Option.description`;
  - the `answers` body and its validation (400 rules);
  - the dismiss endpoint;
  - lifecycle: timeout, dismiss, terminal answer and hook gone all fall back to the TUI and never deny; a settled card is 409 and its late answers are never delivered;
  - the answered and terminal answer shapes, the push `input_type`, and the hello feature;
  - a note that the PermissionRequest `updatedInput` route is undocumented, with PreToolUse `permissionDecision:"allow"` as the fallback.
- `docs/configuration/dispatches.md:490`: drop "question dialogs are not routed". Supervised agents now route AskUserQuestion to Leo Chat; dispatch panes are unchanged.
- `CHANGELOG.md`, `[Unreleased]` → `### Added`: "**AskUserQuestion in Leo Chat.**" plus a short paragraph covering one card for all questions, answers going back to Claude, dismiss, and the TUI fallback.
- The PR body states the undocumented-path risk and the PreToolUse fallback (spec item 5).
- Final verification:
  - `go test -race ./internal/chat/... ./internal/web/... ./internal/cli/... ./internal/push/... ./internal/observe/... ./internal/consult/...`
  - `make lint`
  - `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. The hook output changed, and plain `make e2e` from a pane can kill it.
- Live check (orchestrator), on an isolated test daemon (separate `LEO_HOME`; never `leo service restart`, which hits production):
  - a supervised claude asks a single-select question, then a multi-select one with text, answered by curl to `/answer`; the model sees the answers;
  - dismiss → the TUI dialog appears;
  - answering in the TUI first → the card shows answered/terminal.

## Decisions for the orchestrator (beyond the spec text)
1. Shutdown closes an open card with `closed_reason:"timeout"`. This keeps us inside the promised reasons; a new reason would break the contract.
2. The step 8 dedup (hold and drop question-reason needs_input) is my reading of "must not use the needs_input alert". Without it, every card pushes twice.
3. Step 6 (PostToolUse settle) is defensive. Drop it if ctx-cancel is judged sufficient.
4. Step 3 fixes `Message.Text` = first question plus " (+N more)". The spec is silent; it is the push body and thread preview.
