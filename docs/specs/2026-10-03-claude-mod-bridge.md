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
daemon ──(unix socket stream)──> `leo bridge --agent <key> --launch <launch>` ──stdout JSONL──> mod
  ^                                                                       │
  └──────── `leo bridge report` (process.run, JSON on stdin) <──────────────────────────┘
```

**Mod (`leo-bridge`).**
- Embedded in the leo binary with `embed.FS`.
- Leo writes it to `~/.leo/state/mods/leo-bridge/<leo-version>-<hash12>/`. The directory is immutable once written, so a hot reload never sees a half-written mod. At its first bridged launch a daemon removes older versions' directories, keeping its own and the newest other one; a directory named in a running process's command line (a claude loaded it) is kept, and nothing is removed if the processes cannot be listed.
- Claude agents are launched with `--plugin-dir <that dir>`.
- Leo puts these variables in the launch environment, and the mod reads them with `$.env.get`:
  - `LEO_BRIDGE_BIN`: absolute path to leo
  - `LEO_BRIDGE_HOME`: the leo home of the daemon that launched this claude. The mod does not read it; the `leo bridge` processes it spawns inherit it and dial that daemon's socket ahead of `$LEO_HOME` and the default home, so the agents of a daemon run with `-c <config>` (an isolated test daemon, the e2e suite) never reach another daemon.
  - `LEO_BRIDGE_LAUNCH`: a token fresh for every launch (`[A-Za-z0-9_-]`, at most 64 chars). The mod passes it on its stream connect and on every report, and the daemon takes either only from the key's current launch (see Generations). A hot reload of the mod keeps it, a new process gets another, so the mod can also tell what an earlier load of itself already handed this process's engine (see Deduplication). The mod stays disabled unless `LEO_BRIDGE_BIN`, `LEO_BRIDGE_AGENT` and `LEO_BRIDGE_LAUNCH` are all set.
  - `LEO_BRIDGE_AGENT`: the bridge key leo routes this claude by. For an agent or persistent task it is the agent name at launch (`<name>.<nonce>` if a renamed predecessor still holds that name); for a dispatch it is `dispatch.<dispatch id>`, unique per run. `LEO_PROCESS_NAME` is not used: it is a display name and differs from the key for dispatches.
- `session.start` starts the pump. Commands are handled in order, one at a time, with a single command in flight. When the stream ends the pump reconnects after 1 s, doubling to a 5 s cap (reset by a stream that lived over 60 s), so it is back within seconds of a daemon restart. If `leo bridge` exits 3 (the daemon refused this launch for good), the mod goes dormant until it reloads: it neither reconnects nor reports, and leaves its acked entry alone.
- The mod never decides policy. It executes commands and reports events.

**`leo bridge` subcommand.**
- `leo bridge --agent <key> --launch <launch>`: opens a streaming request to the daemon over the unix socket. It writes one JSON command per line to stdout and exits when the daemon closes the stream. A launch the daemon refuses for good (409: another launch holds the key, or no daemon is adopting the session) exits 3, which the mod treats as final. Any other failure exits 1 and the mod retries on its backoff, including 503 from a restarted daemon that has yet to adopt the session.
- `leo bridge report --agent <key> --launch <launch>`: a single POST that carries one ack, hello or event, read from stdin (a report can carry a whole prompt or final message, beyond what argv holds; the daemon caps a report at 16 MiB). A report from a launch that is over (410) or not the key's current one (409) is dropped: the command exits 0, so the mod does not retry it. One the daemon cannot place yet (503) fails, so the mod retries it.
- Both take `--agent`/`--launch`, else `$LEO_BRIDGE_AGENT`/`$LEO_BRIDGE_LAUNCH`.

**Daemon (`internal/bridge`).** Keeps the following per agent:
- **Generations.** Each launch opens a generation bound to its launch token (`Open(key, launch)`), which always starts a new one: the previous generation's stream is closed and its unacked commands fail as forgotten. A stream connect or report naming any other launch is refused with 409, and a key no launch has opened refuses every mod, so a dying predecessor can never take its successor's stream or opening, mark it busy, reject its commands, or forget its generation. Mods never create generations.
- **Connection registry.** At most one live stream per agent, from its current launch. A reconnect of that launch replaces the old stream.
- **Outbox.** Commands that have not been acked, kept in memory with stable ids.
- **Redelivery.** On reconnect, every unacked command is resent in order. This closes the reload race found in the spike.
- **Lookup.** `Connected(agent) bool`, which the injection call sites use to choose between the bridge and tmux. The router resolves an agent to the generation its own launch opened and routes only while that generation is still the key's live one, so an agent that does not hold its key (a fresh launch keyed `<name>.<nonce>` while a renamed agent's session holds `<name>`) never receives the other's commands.
- **Adoption wait.** A restarted daemon refuses unopened keys with a retryable 503 until its restore settles. Before any agent launches, the restore reserves every key it is about to adopt (read from the surviving sessions' environments) for the agent adopting it: no fresh launch allocates a reserved key, and a key stays awaited, still 503, until its agent opens it or gives the reservation up (its session is gone or is adopted legacy). A rename carries the reservation. After that an unopened key answers 409. An adoption whose key another agent already holds is adopted legacy instead.

## Protocol

**Commands (daemon → mod).** One JSON line each:
- `{"id","op":"deliver","text","as_user":bool}`
- `{"id","op":"compact","instructions"?}`
- `{"id","op":"clear"}`
- `{"id","op":"interrupt"}`

**Reports (mod → daemon).**
- `{"type":"ack","id","ok":bool,"error"?}`
  - For `deliver`, the ack is sent when `$.prompt.submit` resolves, which is when the prompt's turn starts. A deliver behind a running turn stays unacked, and is answered as queued, until that turn ends.
  - For `compact`, the mod waits for idle before calling, because `$.session.compact` rejects while a turn runs. A refused compact is retried once the running turn ends (bounded), including a turn a reloaded mod never saw start.
  - For `clear`, the ack is ok only if the session id moved on; a hook that answers `/clear` in its place gets `ok:false`.
  - For `interrupt`, the running turn is aborted. One that arrives while a submit is in flight and no turn runs yet aborts the turn that submit starts, then acks.
  - A report the daemon does not take is retried with backoff for about a minute, in order.
- `{"type":"event","name":"turn.start"|"turn.complete"|"session.end","event_id"?, "usage"?, "reason"?, "prompt"?, "message"?}`
  - `prompt` (turn.start only): the text the turn began with. `message` (turn.complete only): the assistant's final visible text, which a dispatch returns as its result. The mod caps each at 1M characters. `reason` on turn.complete is `"aborted"` for an interrupted turn (absent otherwise); the dispatcher closes such a turn as interrupted.
  - `event_id`: `<name>:<turn id>` or `session.end:<session id>`, stable across the mod's retries, so the dispatcher drops a replay.
- `{"type":"hello","session_id","claude_version","busy"?}`, sent on connect; `busy` says whether a main-loop turn is running. A freshly loaded module (a new process, or a hot reload mid-turn) leaves it out until it sees a turn start or end, and the daemon keeps what it knows.

**Deduplication.** The mod records acked ids in `$.store`, capped at 500 ids, under `acked:<key>` as `{ids, at, inflight}`. A command whose id was already acked is acked again without running. Before handing a `deliver` or `clear` to the engine, the mod also records its id as in flight for its `LEO_BRIDGE_LAUNCH`, and settles it (acked, no longer in flight) in one write. The engine keeps a handed-off prompt across a hot reload of the mod, so a reloaded mod handed such an id again acks it without running it; a new process, whose queue starts empty, runs it. Delivery is therefore at-least-once on the wire and exactly-once into Claude. A process restamps its own entry on `session.start` and after every main-loop `turn.complete`; each `session.start` prunes other keys' entries left untouched for 7 days, so a live but idle agent's entry survives. A dispatch's entry is deleted when its session ends for good (not on `/clear` or resume), queued behind any restamp still being written.

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

`leo doctor` and the agent list show the bridge state for each claude agent as `bridge: connected | absent`, and an absent bridge with commands queued as `absent, N pending`: those commands wait for the mod, and the idle sweep does not suspend the agent meanwhile (that would drop them).

The opening prompt is a special case because it is assembled before launch. When the claude version supports mods, the prompt is not put on argv. Instead it is queued as the first outbox command. If the bridge has not connected within 20 s of launch, the daemon kills the session and relaunches the agent the legacy way: no `--plugin-dir`, and the opening prompt on argv or in a brief file. A mod that rejects the opening (its ack is `ok:false`) gets the same fallback whether or not its stream is still up when the ack lands. Nothing has run yet, so a relaunch is safe. Pasting the prompt instead would bring back the refusal problem that brief files exist to avoid.

Whether a launch needs the opening is decided per conversation. Its id is `OpeningID(conversation, text)`, where the conversation is the launch's `--session-id` or `--resume` value; the agent record keeps the id of the latest conversation to get it. A launch with neither flag starts a fresh conversation and always gets the opening; a resumed one gets it only if that conversation never did. A mod's ack records the conversation its hello named; an opening carried on argv (or in a brief file) is recorded once the conversation's transcript shows it as a user prompt, polled while the launch runs and checked once more when it ends, and a pasted one once the paste succeeds, so a later bridged `--resume` of the same conversation does not deliver it again.

A daemon restart does not end the agents' tmux sessions: the restarted daemon adopts them. It reads the key and launch token from the session's environment and re-opens that launch's generation. An adopted session is live and is never killed or relaunched: there is no connect timeout, and a refused opening is only logged. Before a bridged launch starts, the record also keeps the opening it queued (`{launch, id}`); when the adopted session's launch matches, the opening is queued again under the same id and waits for the mod to reconnect. If the mod already ran it, its dedup turns the repeat into a re-ack, so the opening runs exactly once. An adopted legacy session's opening is recorded from its transcript, as for any argv launch. Acks and fallbacks write to the agent's record under its name at the time of the write, so a rename while the opening is pending loses nothing.

Shell turn hooks are dropped for claude agents whose bridge is connected. The bridge owns a dispatch's reports from its launch's hello until that launch's final `session.end` (not `/clear` or resume) or the generation is forgotten, whatever reconnect gaps fall between; the daemon ignores shell-hook reports for that span, so state is never counted twice. The hooks stay installed as the fallback.

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
