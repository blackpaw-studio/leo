# Dispatch adopt-on-restart: implementation plan

Spec: `docs/specs/2026-10-19-dispatch-adopt-on-restart.md`. Where this plan and the spec conflict, the spec wins. Branch: `feat/dispatch-adopt-on-restart`. Commit after each step and report the hash.

## The spec's open facts, answered from the code
1. **Hook reports are mostly lost.**
   - The shell hook `leo dispatch report` makes 3 tries with 200/400 ms backoff, 3 s per try (`internal/cli/dispatch.go:343-444`). Tool events get 1 try. So a daemon that is down for more than about 1 s loses them.
   - Reports for a run that isn't in memory get a 200 "ignoring report for unknown run" (`interactive.go:854`), so they are lost for good. After a restart, every adopted run is "unknown" until a `runState` exists for it.
   - Bridge reports retry 8 times over about 61 s (`protocol.js:38`, `register.js:233`). A 409 or 410 drops them at once (`cli/bridge.go:132`).
   - The bridge stream reconnects forever (backoff of 5 s or less) until it gets a 409. Then the mod goes dormant until it reloads (`register.js:750,765`).
2. **The `released` records with the restart error.**
   - `d-4fac363cebca` and `d-d70004f66e52` had only one turn, so `MarkInterrupted` set them to `failed` (`consult.go:1241-1245`).
   - Their callers then ran `leo_release`. `releaseLocked` accepts `failed` (`release.go:22`) and overwrites the status (`:71`) but keeps `Error`.
   - So no restart bug is involved, and nothing changes here.
3. **The reconnect does re-send the launch token, but the new daemon has no copy.**
   - Every stream and report passes `--launch $LEO_BRIDGE_LAUNCH` (`register.js:223`), and the hub binds the key to that token (`generation.go:315`).
   - A dispatch's token goes only to the pane, through `-e` (`interactive_runtime.go:290-313`), and to the in-memory `r.bridged`. Nothing persists it.
   - Order at boot:
     - `srv.Start` serves the bridge, and mods get a 503 and retry.
     - Then `StartWeb` runs `MarkInterrupted` (`process.go:1016`).
     - Then `RestoreAgents` calls `AwaitAdoption(agentKeys)` (`bridge_adopt.go:34`). From that point every dispatch key returns `ErrStaleLaunch`, a 409, and the mod goes dormant.
   - So adoption must `Open` the key with the original token inside `MarkInterrupted`, which already runs early enough.

## Approach
- **Startup.** Probe each live interactive run's pane once, then adopt or settle.
  - An adopted run gets a real `runState`, so `Report`, `Sweep`, notifications and slots all work for it.
  - Its bridge key is re-opened with the token recovered from a pane option, `@leo_bridge_launch`. This mirrors how PR #86 reads tmux session env.
- **`Sweep`.** Watch runs that are still `adopting`:
  - Activity ends the adoption window.
  - An idle composer means the turn finished while the daemon was down, so reconcile it from the transcript.
  - Passing the deadline settles the run as today.

## Steps

### 1. Config: `defaults.dispatch.adopt_timeout`
- **Files:** `internal/config/config.go`:
  - Add `DispatchConfig.AdoptTimeout string`.
  - Add `(*Config).DispatchAdoptTimeout() time.Duration`.
  - Validate the field, following `approval_timeout`.
- **RED:** table test in `config/dispatch_adopt_test.go`:
  - `""` gives 2m, and `"45s"` gives 45s.
  - `"0"`, `"-1s"`, `"2h"` and `"x"` each fail `Validate` with a message naming `defaults.dispatch.adopt_timeout`.
- **Verify:** `go test -race ./internal/config/` and `make lint`.

### 2. Extract the restart settle (refactor, no behavior change)
- **Files:**
  - Move the interactive branch of `MarkInterrupted` into a new `internal/consult/restart_adopt.go`: `consult.go:1225-1245`, `addRestartCandidates`, and the pane kill at `:1269-1283`.
  - Call it `settleRestarted(rec, reason)`. Both startup and the timeout path will use it.
- **Test:** pin today's result in a characterization test, and add it first if none exists:
  - `TurnLost`, `daemon restarted`, and `failed` vs `closed`.
  - The close notice, and the pane killed.
  - It must pass both before and after the move.
- **Verify:** `go test -race ./internal/consult/` and `make lint`.

### 3. Runtime: tag, probe and re-register panes (tmux seam, exact-argv tests)
- **Files:** `consult/interactive_runtime.go`, `interactive_bridge.go`, `slots.go`.
- **Tag:**
  - After a bridged launch has its pane: `set-option -p -t <pane> @leo_bridge_launch <token>`.
  - On the `AwaitOpening` fallback, before `respawn-pane`: `set-option -p -u -t <pane> @leo_bridge_launch`.
- **Probe:** `ProbeAdoption(pane, id) (AdoptProbe{Presence, Owned, Launch}, error)` runs `list-panes -t <pane> -F "#{pane_id}\t#{pane_dead}\t#{@leo_bridge_launch}\t#{pane_start_command}"`.
  - `Owned` comes from `startCommandHasDispatchID`.
  - "can't find pane" means absent and certain. Any other error means uncertain.
- **Re-register:**
  - `AdoptPane(pane, harness, placement)` restores the classifier (codex or claude) and the placement.
  - `AdoptBridge(id, pane, caller, launch) (bridge.Target, error)` calls `Hub.Open(DispatchBridgeKey(id), launch)` and adds an `r.bridged[id]` entry with `ownsReports: true`.
- **Slots:** `slotLimiter.Adopt()` takes a slot unconditionally, even over the cap, because the turn is already running.
- **RED tests:**
  - Capture argv through `ExecCommandContext` and check it with `reflect.DeepEqual` for the tag, the untag and the probe.
  - Probe parse table: alive and owned with a token, dead, foreign start command, empty token, absent, tmux error.
  - After `AdoptBridge`, `hub.Connect(key, token)` succeeds, any other token gets `ErrStaleLaunch`, and `BridgeOwnsReports(id)` is true.
  - `Adopt` over the cap, then `Release`, grants the slot to the waiter.
- **Verify:** `go test -race ./internal/consult/ ./internal/bridge/` and `make lint`.

### 4. Adoption at startup in the Dispatcher
- **Files:**
  - `consult.go` (`MarkInterrupted`) and `restart_adopt.go`.
  - `record.go`: add `AdoptedAt time.Time` as `json:"adopted_at,omitempty"`.
  - `runState`: add `adopting`, `adoptDeadline` and `adoptSnapshot`.
  - Add `(*Dispatcher).SetAdoptTimeout(time.Duration)`.
- **Adopt when all of these hold:**
  - The run is interactive and not terminal.
  - It has a `PaneID`, after `reconcileRestartViewer`.
  - The probe says alive, owned and certain.
  - Every open turn is `Delivered`.
  - It is not `awaitingSlot`.
- **Otherwise settle it with `settleRestarted`**, using one of these reasons: `pane absent`, `pane dead`, `pane uncertain`, `pane not ours`, `turn undelivered`.
- **On adoption:**
  - Build the `runState` from the `FileRecorder.Resume` handle (it must satisfy `interactiveRecordHandle`). Keep `done` open and take `cancel` from `daemonCtx`. Keep the status and the open turn.
  - Clear `OpenTools`, `BackgroundTasks` and `PendingWork`, so #277 tracking starts empty.
  - Call `Adopt()` once per `SlotHeld` turn, then `AdoptPane`.
  - Call `AdoptBridge` only if the probe found a token. Otherwise hooks drive the run, and the mod gets a 409 and goes dormant.
  - Seed `paneWant` and `wantPlacement`, then nudge `reconcilePane` (`interactive_pane_move.go:133`).
  - Start `go watchLiveUsage` for claude runs.
  - Set `AdoptedAt` and the deadline, then persist.
- **Log** exactly one stderr line per run: `dispatch <id>: adopted (bridge|hooks)` or `dispatch <id>: settled: <reason>`.
- **RED tests** (fake runtime, fixed `d.now`):
  - **(a)** Adopted: same status and open turn, no notifications or close notice, pane not killed, `AdoptedAt` set.
  - **(b)** Each settle reason produces today's record plus a close notice.
  - **(c)** Headless runs are unchanged.
  - **(d)** A `Report(id, Stop)` after adoption closes the turn as finished and queues the caller's notification. Today it is "unknown run".
  - **(e)** The adopted slot counts against `max_concurrent`.
- **Verify:** `go test -race ./internal/consult/` and `make lint`.

### 5. Wiring, and re-keying the bridge before `AwaitAdoption`
- **Files:** `internal/web/consult_runtime.go`: call `SetAdoptTimeout(cfg.DispatchAdoptTimeout())` before `:124`. Add `consult_runtime_test.go`.
- **RED:** use a real `bridge.Hub` and a fake runtime that returns an owned pane with token T.
  - Run the setup, then `hub.AwaitAdoption(nil)`, as `RestoreAgents` does.
  - Assert `hub.Connect("dispatch.<id>", T)` succeeds. Today it gets a 503, then a 409.
  - Assert that a hello plus `turn.complete` sent through `hub.Apply` finishes the turn with its text.
- **Verify:** `go test -race ./internal/web/ ./internal/consult/` and `make lint`.

### 6. Watching adopted runs in `Sweep`
- **Files:**
  - The `Sweep` probe phase in `interactive.go`.
  - `restart_adopt.go`.
  - `lastAssistantTextSince(path, since)` next to `usage_transcript.go`, plus a JSONL fixture in `testdata`.
  - The runtime method `PaneSnapshot(pane)`, which runs `capture-pane -p -t <pane>` (exact-argv test).
- **Activity** means an accepted report, a bridge hello for the run, or a capture that differs from `adoptSnapshot`. Activity clears `adopting`.
- **Idle composer:** if the composer stays `Empty` for `adoptIdleGrace` (15s) with no report, finish the turn from the transcript.
  - The path comes from the hook or from `session.JSONLPath(cwd, SessionID)`.
  - Use the text after the turn's `StartedAt`.
  - If there is no text, mark the turn `TurnLost` and send the notice "finished while the daemon was down; no result recovered".
- **Deadline passed while still `adopting`:** call `settleRestarted("adopt timeout")`, which kills the pane.
- **RED tests** (fake clock, no sleeps):
  - The run settles at the deadline and not 1 ns before it.
  - A capture change before the deadline keeps the run alive.
  - Idle plus the fixture gives a finished turn with its text and a notification.
  - Idle plus an empty transcript gives a lost turn plus the notice.
  - A pane that disappears takes the existing `closeReasonPaneGone` path.
- **Verify:** `go test -race ./internal/consult/` and `make lint`.

### 7. Graceful restart lists dispatches
- **Files:**
  - `internal/drain/deps.go`: add `Probe.Kind` as `json:"kind,omitempty"`, with the value `"dispatch"`.
  - `drain/manager.go`: `Begin` never messages dispatch probes, never waits on them and never persists them.
  - `service/drain_probe.go`: inject `dispatches func() []drain.Probe`.
  - `service/process.go` plus a daemon/web accessor: return the live interactive runs that have a working turn.
  - `cli/graceful_restart.go`: update `workingSummary`.
- **RED:**
  - Manager: a dispatch probe is not messaged, and a set made only of dispatches counts as done.
  - CLI table: `2 agents are working (a, b) and 1 dispatch is running (fix-x)`, covering the plural and singular forms.
  - Prober: test with the injected function.
  - Handler: the JSON includes `kind`.
- **Verify:** `go test -race ./internal/drain/ ./internal/service/ ./internal/daemon/`, `env -u TMUX go test -race ./internal/cli/` and `make lint`.

### 8. Docs and CHANGELOG
- **Docs:**
  - `docs/configuration/dispatches.md`: a new "Daemon restarts" section covering the adopt and settle rules, `adopt_timeout`, headless runs unchanged, and older runs being adopted hook-driven.
  - `docs/configuration/config-reference.md`: add `defaults.dispatch.adopt_timeout`.
  - `docs/cli/service.md` and `docs/cli/update.md`: the busy prompt lists dispatches, and dispatches are not paused.
- **CHANGELOG**, under `[Unreleased]` → `### Added`:
  > **Interactive dispatches survive a daemon restart.** `leo update`, `leo service restart` or a crash no longer cut off a working interactive dispatch: if its pane is still alive, the new daemon adopts it under the same run id and turn, and its result reaches the caller as usual. A run that doesn't reattach within `defaults.dispatch.adopt_timeout` (default 2m), or whose pane is gone, closes as `daemon restarted`, as before. Graceful restart lists running dispatches next to busy agents and doesn't pause them.

### 9. Full suite, e2e and live check (isolated daemon only)
- **Suites:**
  - `make test`
  - `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`
- **Never** run `leo service restart`, `make install`, or anything else that touches production launchd.
- **Before starting:** tell Evan that a test daemon strips production's roster line.
- **Setup:**
  - `make build`.
  - Create `/tmp/leoadopt/leo.yaml` with `web.port: 8371`, `bind: 127.0.0.1`, `defaults.dispatch.adopt_timeout: 30s`, and a claude haiku template.
  - Start the daemon with Bash `run_in_background`: `env LEO_HOME=/tmp/leoadopt ./bin/leo service --supervised --config /tmp/leoadopt/leo.yaml`.
  - Spawn the agent `adopt-orch`. Dismiss the trust dialog with Down, then Enter.
  - Have the agent `leo_dispatch` an interactive run: "Run `sleep 150` with Bash, then reply exactly ADOPT-OK", with notify on.
- **Adopt path:**
  - Mid-sleep, run `pkill -f "service --supervised --config /tmp/leoadopt/leo.yaml"`.
  - Confirm with `tmux -L leo list-panes -a` that the pane is alive, then start the daemon again the same way.
  - Expect in the daemon log: `dispatch <id>: adopted (bridge)`, `bridge: dispatch.<id> connected`, and no 409.
  - Expect `leo dispatch show <id> --config …` to show the same run and turn with `adopted_at` set.
  - When the sleep ends, the turn finishes and `adopt-orch`'s pane shows a completion notice containing ADOPT-OK, with no "daemon restarted" close notice.
- **Timeout path:**
  - Start a second dispatch and `kill -STOP` its claude PID (the child of `#{pane_pid}`).
  - Kill the daemon and start it again.
  - Expect `adopted`, then about 30s later `settled: adopt timeout`. The record shows `lost` and `daemon restarted`, and the caller gets the close notice.
  - Then `kill -CONT` the process.
- **Pane-gone path:** kill a third dispatch's pane while the daemon is down. Expect `settled: pane absent` at boot.
- **Teardown:**
  - `pkill` the test daemon.
  - Kill the test sessions by exact `=name`.
  - `trash` `/tmp/leoadopt` and its `~/.claude/projects/-private-tmp-leoadopt*` directories.
  - Check that `leo agent list` shows production untouched.

## Decisions the spec doesn't settle (recommendation first)
1. **Where the token lives.** Store it in the pane option `@leo_bridge_launch`.
   - This mirrors #86, needs no cleanup, and has the same exposure as session env.
   - The alternative is a 0600 sidecar file.
   - Runs launched before this release have no tag, so they are adopted hook-driven. Follow-ups to them go through a tmux paste.
2. **A further adopt condition.** Every open turn must be `Delivered`.
   - Bridged openings and follow-ups that were still queued lived in the dead hub's memory, so those runs settle.
3. **What counts as activity.** An accepted report, a bridge hello, or a change in the pane capture.
   - A bare stream connect without a hello does not count.
4. **Slots.** An adopted working turn takes a slot even above `max_concurrent`, and adoption never blocks.
5. **The spec's "existing pane reconciler" only restores placement.** Turn reconciliation is new `Sweep` code, with a 15s idle grace so retried bridge reports land first.
6. **Graceful restart with only dispatches running.** Don't prompt. Print one line instead: "M dispatches are running (…); they will be adopted across the restart."
7. **Bounds on `adopt_timeout`.** Greater than 0 and at most 1h. Empty means 2m.
8. **`adopted_at` is an additive `/api/v1` record field**, which the spec requires. Mention it to leoterm.
