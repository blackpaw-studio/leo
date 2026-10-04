# Claude mod bridge

Status: draft · 2026-10-03

## Goal

Drive claude agents through a Claude Code mod that runs inside the claude process, instead of through tmux keystrokes, screen scraping and the inbox socket. This removes the paste-refusal, 16 KiB, lost-turn, "composer busy" and dialog-race failure classes for claude. Codex and opencode are unchanged.

Spike evidence (2026-10-01, Claude Code v2.1.287): `$.process.spawn` streams indefinitely. `$.prompt.submit` waits for idle and delivers in order, and a 41 KB message arrived intact. The composer draft survives a delivery. `turn.start` and `turn.complete` fire reliably. `$.session.usage()` returns context, rate limits and cost.

## Scope (v1 = everything)

| Concern | Today | With the bridge |
|---|---|---|
| `leo_send_message` | Inbox socket, falling back to tmux paste | Bridge `deliver` |
| Persistent-task prompts | tmux load-buffer paste with readiness probe | Bridge `deliver` |
| Dispatch continuation (`leo_send_dispatch`) | tmux paste | Bridge `deliver` |
| Opening prompt / dispatch brief | argv, or brief file + `$(cat)` at ≥96 KiB | Bridge `deliver` on connect |
| `leo_compact` | tmux keys `/compact` | `$.session.compact()` |
| `leo_clear` | tmux keys `/clear` | `$.command.run({command:'clear'})` if the spike step confirms a mod can run a built-in; otherwise this stays on tmux keys |
| `leo_interrupt` | tmux Escape + Ctrl-C | `$.turn.abort()` |
| Turn state for dispatch | `Stop`, `UserPromptSubmit` and `SessionEnd` shell hooks posting `/api/dispatch/{id}/report` | Bridge events `turn.start`, `turn.complete` and `session.end` feed the same `consults.Report` |
| Idle-suspend | No pane changes for the configured duration | Agent idle since the last `turn.complete`, with no turn running |
| Usage | `claude/usage.go` parsing | `$.session.usage()` sent with each `turn.complete` |

## Architecture

```
daemon ──(unix socket stream)──> `leo bridge --agent <name>` ──stdout JSONL──> mod
  ^                                                                       │
  └──────── `leo bridge report` (process.run, JSON on stdin) <────────────┘
```

**Mod (`leo-bridge`).**
- Embedded in the leo binary with `embed.FS`.
- Leo writes it to `~/.leo/state/mods/leo-bridge/<leo-version>/`. The directory is immutable once written, so a hot reload never sees a half-written mod.
- Claude agents are launched with `--plugin-dir <that dir>`.
- Leo puts these variables in the launch environment, and the mod reads them with `$.env.get`:
  - `LEO_BRIDGE_BIN`: absolute path to leo
  - `LEO_BRIDGE_HOME`: the leo home of the daemon that launched this claude. The mod does not read it; the `leo bridge` processes it spawns inherit it and dial that daemon's socket ahead of `$LEO_HOME` and the default home, so the agents of a daemon run with `-c <config>` (an isolated test daemon, the e2e suite) never reach another daemon.
  - `LEO_BRIDGE_AGENT`: the bridge key leo routes this claude by. For an agent or persistent task it is the agent name at launch (`<name>.<nonce>` if a renamed predecessor still holds that name); for a dispatch it is `dispatch.<dispatch id>`, unique per run. `LEO_PROCESS_NAME` is not used: it is a display name and differs from the key for dispatches.
- `session.start` starts the pump. Commands are handled in order, one at a time, with a single command in flight.
- The mod never decides policy. It executes commands and reports events.

**`leo bridge` subcommand.**
- `leo bridge --agent <name>`: opens a streaming request to the daemon over the unix socket. It writes one JSON command per line to stdout and exits when the daemon closes the stream.
- `leo bridge report --agent <key>`: a single POST that carries one ack, hello or event, read from stdin (a report can carry a whole prompt or final message, beyond what argv holds; the daemon caps a report at 16 MiB).

**Daemon (`internal/bridge`).** Keeps the following per agent:
- **Connection registry.** At most one live stream per agent. A new connection replaces the old one.
- **Outbox.** Commands that have not been acked, kept in memory with stable ids.
- **Redelivery.** On reconnect, every unacked command is resent in order. This closes the reload race found in the spike.
- **Lookup.** `Connected(agent) bool`, which the injection call sites use to choose between the bridge and tmux.

## Protocol

**Commands (daemon → mod).** One JSON line each:
- `{"id","op":"deliver","text","as_user":bool}`
- `{"id","op":"compact","instructions"?}`
- `{"id","op":"clear"}`
- `{"id","op":"interrupt"}`

**Reports (mod → daemon).**
- `{"type":"ack","id","ok":bool,"error"?}`
  - For `deliver`, the ack is sent when `$.prompt.submit` resolves, meaning the prompt was accepted (started, or queued behind the running turn).
  - For `compact`, the mod waits for idle before calling, because `$.session.compact` rejects while a turn runs.
- `{"type":"event","name":"turn.start"|"turn.complete"|"session.end","event_id"?, "usage"?, "reason"?, "prompt"?, "message"?}`
  - `prompt` (turn.start only): the text the turn began with. `message` (turn.complete only): the assistant's final visible text, which a dispatch returns as its result. The mod caps each at 1M characters. `reason` on turn.complete is `"aborted"` for an interrupted turn (absent otherwise); the dispatcher closes such a turn as interrupted.
  - `event_id`: `<name>:<turn id>` or `session.end:<session id>`, stable across the mod's retries, so the dispatcher drops a replay.
- `{"type":"hello","session_id","claude_version","busy"?}`, sent on connect; `busy` says whether a main-loop turn is running.

**Deduplication.** The mod records acked ids in `$.store`, capped at 500 ids. A command whose id was already acked is acked again without running. Delivery is therefore at-least-once on the wire and exactly-once into Claude.

## Framing

Delivered text is framed by where it comes from:

| Source | `as_user` | Text |
|---|---|---|
| Persistent-task prompt | true | Verbatim |
| Opening prompt or dispatch brief | true | Verbatim. The orchestrator's brief is the agent's task, and the argv path is user-framed today. |
| Agent-to-agent message (`leo_send_message`) | false | Starts with `From agent <sender> via leo:`. Claude adds its own "plugin sent a message" frame around it. |
| Dispatch continuation (`leo_send_dispatch`) | false | Starts with `From <orchestrator> via leo:` |

## Fallback

Each injection call site checks `bridge.Connected(agent)`:
- **Connected:** the command goes through the bridge. The call returns once the ack arrives, or fails after a 30 s ack timeout. On timeout, the command stays in the outbox for redelivery and the caller gets an error. There is no tmux retry, so a message is never delivered twice.
- **Not connected:** today's path runs unchanged (inbox socket or tmux paste), and a warning is logged.

`leo doctor` and the agent list show the bridge state for each claude agent as `bridge: connected | absent`.

The opening prompt is a special case because it is assembled before launch. When the claude version supports mods, the prompt is not put on argv. Instead it is queued as the first outbox command. If the bridge has not connected within 20 s of launch, the daemon kills the session and relaunches the agent the legacy way: no `--plugin-dir`, and the opening prompt on argv or in a brief file. Nothing has run yet, so a relaunch is safe. Pasting the prompt instead would bring back the refusal problem that brief files exist to avoid.

Shell turn hooks are dropped for claude agents whose bridge is connected. The daemon ignores shell-hook reports for a dispatch once that dispatch's bridge has said hello, so state is never counted twice. The hooks stay installed as the fallback.

## Removed / kept

- **Kept for claude** as the fallback: tmux inject, inbox socket, `DialogKey`, the shell `TurnHooks`, and brief files (`openingbrief.go`). Brief files are now used only by legacy launches.
- **Removed:** nothing in v1. Deleting fallback code is a follow-up once the bridge has run in production. Usage comes from the bridge when it is connected, and from `usage.go` otherwise.
- **Minimum Claude Code version:** v2.1.287, checked once at launch with `claude --version`. Below that version, the agent launches the legacy way and gets no `--plugin-dir`.

## Testing

- **Mod unit tests** (`claude plugin test`):
  - commands map to the right API calls
  - dedup
  - the `compact` wait-for-idle path
  - reports are well-formed
- **Go unit tests:**
  - outbox ordering, redelivery on reconnect, ack timeout
  - the connection-replace race
  - fallback selection at each call site
  - framing per source
- **e2e** (run with the `make e2e` recipe from memory): a real claude agent on an isolated test daemon.
  - deliver while busy
  - a 50 KB message
  - an opening prompt over 96 KiB
  - compact, interrupt
  - a forced mod reload mid-stream with no loss and no duplicate
  - bridge absent (`--safe-mode`) falls back to tmux
- **Live verification** on the isolated test daemon before merge. Production restarts only with Evan's go-ahead.

## Open risks

- **API churn.** The mods API may change between Claude Code releases. The mod is pinned to the types of the tested version, and `leo doctor` warns when the claude version is newer than the tested version.
- **Startup dialogs.** It is not yet known whether `$.prompt.submit` waits behind a blocking startup dialog. The opening-prompt step of the e2e suite covers this. If it does not wait, `DialogKey` stays active for claude.
- **Untested built-in.** `$.command.run({command:'clear'})` running a built-in command is unverified. If it doesn't work, `clear` stays on tmux keys.
