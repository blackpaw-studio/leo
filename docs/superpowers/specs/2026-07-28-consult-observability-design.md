# Consult observability — watch a consultant work

**Date:** 2026-07-28
**Status:** Draft

## One sentence

Persist every consult's event stream as it arrives and add `leo consult
list` / `leo consult watch` so a running consultant's tool calls and output
can be watched live from the CLI.

## Motivation

A consult is invisible. `Dispatcher.Consult` runs the consultant with
`cmd.CombinedOutput()`, parses the buffer once at the end, returns
`Result.Text`, and discards everything else. Nothing registers the run: no
daemon state, no record, no log file. Up to four consults can be in flight
for up to ten minutes each (`RunTimeout`) with no way to tell whether one is
making progress, stuck, or reading the wrong files.

The data already exists. All three harnesses emit incremental NDJSON in task
mode — claude `--output-format stream-json --verbose`
(`internal/harness/claude/args.go:70`), codex `exec --json`, opencode
`run --format json`. Every assistant message, tool call, and tool result is
already streaming past on stdout, line by line, and being thrown away. This
is not "add instrumentation to an opaque process"; it is "stop discarding a
stream we already have."

Consults are triggered by agents, never by the user, so discovery matters as
much as the stream: you have to find out a consult is in flight before you
can watch it.

## Scope

In:

- Persisting each consult's record and event stream to disk as it runs.
- `leo consult list` — in-flight and recent consults.
- `leo consult watch [<id>]` — replay-then-follow a consult's rendered feed.
- A per-harness streaming-event mapping for readable output.

Out:

- Web UI. CLI only.
- `leo consult run` — starting a consult by hand. Consults come from agents.
- Cancelling or interrupting a running consult from the CLI. This is the
  obvious follow-up once a consult can be seen going off the rails, but it
  changes the calling agent's semantics (it would receive an error) and is
  a separate decision.
- Interactive follow-up with a consultant. Consults remain one-shot.

## Data model

`<state>/consults/` (mode 0700), two files per consult, both 0600:

`<id>.json` — the record:

| field        | notes                                                   |
|--------------|---------------------------------------------------------|
| `id`         | `c-` + 8 hex chars                                       |
| `caller`     | the `from` field of the API request; may be empty        |
| `template`   | requested template                                       |
| `harness`    | resolved harness name                                    |
| `model`      | resolved model                                           |
| `workspace`  | resolved workspace                                       |
| `prompt`     | the caller's prompt, stored in full, without the preamble|
| `status`     | `queued` \| `running` \| `done` \| `failed` \| `timeout` \| `canceled` |
| `started_at` | record creation (i.e. when queued)                       |
| `ended_at`   | set on any terminal status                               |
| `error`      | the error string on `failed`/`timeout`                   |

The final answer is **not** duplicated into the record; it is the last event
in the stream and the renderer prints it in full.

`<id>.ndjson` — the event stream, one JSON object per line:

- `{"t": 12.482, "d": {…raw harness event…}}` for a parseable line, where
  `t` is seconds since `started_at`.
- `{"t": 12.482, "raw": "…"}` for a line that is not valid JSON — harness
  crash output and stderr chatter, which today vanishes into an error
  string.

Leo owns this framing rather than teeing verbatim because timestamps must
survive replay and no harness emits them. `jq .d` recovers the raw stream.

**Retention:** the 20 most recent consults are kept; each new consult prunes
older records and their `.ndjson` files, mirroring
`history.maxHistoryPerTask` (`internal/history/history.go:11`).

## Dispatcher changes

`internal/consult/consult.go`:

1. **Recorder seam.** `NewDispatcher` takes a `Recorder`, injected rather
   than reaching for a global path:

   ```go
   type Recorder interface {
       // Open registers a consult and returns a handle whose Writer
       // receives the harness's raw output as it arrives.
       Open(Record) (Handle, error)
   }

   type Handle interface {
       io.Writer
       SetStatus(Status) error
       Close(Status, error) error
   }
   ```

   A filesystem implementation lives in the same package and is constructed
   by the daemon from `cfg.StatePath()`, passed to `web.New` through
   `web.Options`. A nil recorder degrades to a no-op so tests and any other
   caller are unaffected.

2. **Record before queueing.** `Open` is called with status `queued` *before*
   the `d.sem` acquire, and flipped to `running` after. Consults waiting
   behind the concurrency limit are therefore listable.

3. **Tee instead of buffer.** `cmd.CombinedOutput()` is replaced by one tee
   writer assigned to *both* `cmd.Stdout` and `cmd.Stderr` as the **same
   interface value**. `os/exec` only serializes concurrent writes to a
   shared output when Stdout and Stderr are the same value — the rule
   documented at `internal/run/runner.go:583`, where a `MultiWriter` being a
   distinct value from `cmd.Stderr` forced a `syncBuffer`. Assigning one
   value means one pipe and one copying goroutine. The tee additionally
   carries an internal mutex: it is redundant under that rule, but the rule
   is subtle enough that a future `MultiWriter` at this call site would
   silently reintroduce the race.

   The tee appends raw bytes to an in-memory buffer and line-frames to the
   handle. Partial trailing lines are flushed on close.

4. **Result is unchanged.** `ParseEvents` still runs over the in-memory
   buffer, so the returned `Result` is byte-identical to today's. Every
   existing error branch additionally maps to a terminal status passed to
   `Handle.Close`: `context.DeadlineExceeded` → `timeout`,
   `context.Canceled` (the caller went away) → `canceled`, and `runErr` /
   `parseErr` / `parsed.IsError` / empty text → `failed` with the same
   string the caller receives.

## Harness event streaming

A new optional capability in `internal/harness`, asserted by the CLI
renderer only:

```go
type Event struct {
    Kind    string // "text" | "tool" | "result" | "error"
    Tool    string // tool name, for Kind == "tool"
    Summary string // one-line rendering, or full text for "text"/"result"
}

type EventStreamer interface {
    StreamEvents(r io.Reader, fn func(Event)) error
}
```

Implemented for claude, codex, and opencode against their native JSON
shapes; `internal/harness/claude/parse_test.go` already carries usable
stream fixtures. A harness that does not implement it makes the CLI fall
back to printing raw lines.

This is rendering only. `ParseEvents` remains the sole authority on a
consult's final result and is not touched.

## CLI surface

New `internal/cli/consult.go`. Both commands read `<state>/consults/`
directly rather than round-tripping the daemon, so they still work when the
daemon is wedged — which is one of the times you most want to look.

```
leo consult list [--json] [--host <h>]
leo consult watch [<id-prefix>] [--host <h>]
```

- `list` prints ID, CALLER, TEMPLATE, MODEL, ELAPSED, STATUS — running and
  queued first, then recent completed, newest first.
- `watch` with no argument selects the newest running consult, falling back
  to the newest overall. IDs match on any unique prefix, git-style;
  an ambiguous prefix is an error listing the candidates.
- `watch` replays the existing stream, then follows by polling the file for
  growth every 250ms and re-reading the record, exiting when the status
  becomes terminal. No fsnotify dependency.
- Ctrl-C detaches only. It never signals the consultant.

Rendered output:

```
$ leo consult watch
[consult c-7f3a2b1e · codex/gpt-5.6-sol · from leo · running]
  0:01  read   internal/consult/consult.go
  0:04  grep   "CombinedOutput" (3 matches)
  0:09  text   The dispatcher discards everything but the final text.
  0:11  bash   go test ./internal/consult/  → ok
```

**Remote.** `runRemote` (`internal/cli/agent.go:121`) hardcodes `"agent"` in
its argv tail. It gains a command-group parameter, with the existing
signature delegating to it — a targeted change, not a refactor of the file.
`leo consult watch --host X` then ssh's to the remote leo, which tails its
own files.

## Testing

- **Dispatcher, characterization:** a fake harness stream produces a
  `Result` identical to the pre-change implementation. This is the guard on
  the `CombinedOutput` replacement.
- **Dispatcher, recording:** the `.ndjson` contains an entry for every line
  the fake harness emitted, in order, including a non-JSON line captured as
  `raw`; status transitions `queued → running → done`; each error branch
  maps to its terminal status.
- **Queue visibility:** with the semaphore saturated, a fifth consult's
  record exists with status `queued` before it starts.
- **Race:** `go test -race` over concurrent consults, asserting distinct
  files and no interleaving.
- **Retention:** the 21st consult prunes the oldest record and its stream.
- **StreamEvents:** table-driven per harness over captured fixtures.
- **CLI:** prefix resolution (unique, ambiguous, absent); no-arg selection
  prefers running over newest; follow terminates on terminal status;
  list rendering and `--json` shape.

## Risks

- **Disk growth.** A verbose consultant reading large files writes a large
  `.ndjson`. Bounded by the 20-record retention, not by per-file size. If
  this bites, a per-file cap is the fix; not adding one now.
- **Contents.** The stream contains whatever the consultant read. Same trust
  boundary as existing task logs under `<state>/logs`; files are 0600 in a
  0700 directory.
