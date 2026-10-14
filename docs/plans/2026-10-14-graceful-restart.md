# Graceful daemon restart: implementation plan

Spec (source of truth): `docs/specs/2026-10-14-graceful-restart.md`. Branch `feat/graceful-restart`.
One commit per step (`feat:`/`test:`/`docs:`). Every step starts from a failing test unless marked otherwise. No real sleeps: the drain manager, cron hold and CLI loop all take an injected clock.

## Design summary

- **New package `internal/drain`.** It holds the drain state machine, the busy classifier, the message texts, and the pending-resume store. It does not import `web`, `daemon` or `service`. Those packages provide adapters for its small interfaces.
- **The daemon owns the drain** and exposes it on new IPC routes under `/drain`. The CLI drives it: check busy, ask, begin, poll, commit, then kickstart. The daemon writes `state/pending-resume.json` at commit, and the new daemon resumes agents from it after restore.
- **Messages go through the existing `messageAgentAs` core**, using a caller-chosen bridge command id. The drain can then ask the durable outbox (`Router.Holds`) whether the pause notice has been acked yet.

## Step 1: `drain` scaffolding, message texts, pending-resume store

**Files**
- `internal/drain/deps.go`: the interfaces, so later steps can be built in parallel:
  - `Prober { BusyAgents(ctx) ([]Probe, error); Probe(ctx, name) (Probe, error) }`
  - `Probe{Name, Exists, Busy bool, Transport string /* "bridge"|"composer" */}`
  - `Notifier { Message(ctx, name, text, id string) (Delivery, error); Holds(name, id string) (bool, error) }`
  - `Delivery{Transport string; Queued bool}`
  - `CronGate { Hold(); Release(); Held() []string }`
  - `Store { Write(PendingResume) error; Remove() error }`
  - `Clock { Now(); After(d) }`. Mirror `bridge.Clock` rather than importing it.
  - `ErrNotifierUnavailable`
- `internal/drain/messages.go`: `PauseText(timeout)`, `CancelText`, `ResumeText`. All three are the spec strings verbatim. `{N}` is `max(1, ceil(timeout / time.Minute))`.
- `internal/drain/resume_store.go`:
  - `PendingResume{Version, DrainID, Agents []string, DeferredTasks []string, WrittenAt}`
  - `FileStore(statePath)` with `Write`, `Remove`, and `Take() (PendingResume, bool, error)`. `Take` reads the file, then deletes it.
  - Path: `<state>/pending-resume.json`. Writes use a package mutex (the same in-process locking as `agentstore.storeMu`), then a temp file + rename (the same pattern as `outbox.writeFileAtomic`), then `0600` with an explicit `Chmod`, as in `agentstore.write`.
  - A corrupt file is renamed to `pending-resume.json.corrupt-<unix>` and `Take` returns an error.

**Failing tests** (`messages_test.go`, `resume_store_test.go`)
- `PauseText(10m)` contains "restarts in 10 minutes".
- 90s gives "2"; 20s gives "1".
- Round trip: `Write` then `Take` returns the same value, the file was mode 0600, and it is gone afterwards.
- A missing file returns `ok=false, nil`.
- A corrupt file returns an error, leaves a `.corrupt-*` sibling, and a second `Take` reports missing.
- `Remove` on a missing file returns nil.

**Verify:** `go test -race ./internal/drain/`

## Step 2: cron hold and release (`internal/cron/cron.go`)

**Changes**
- Wrap the `AddFunc` closure in `s.fire(taskName)`. While held, `fire` appends the task to `held`, coalesced so each task appears once, in first-fire order. Otherwise it calls `runFn` as today.
- `Hold()`, `Release()` and `Held() []string` satisfy `drain.CronGate`.
- `Release()` clears the hold and runs held tasks that are still scheduled (`s.tasks`), each in its own goroutine, as robfig does.
- `RunNow(names []string)` runs each name that is still scheduled. It is used after a restart.
- The hold flag survives `Install` (config reload).

**Failing tests** (`cron_test.go`). Fire jobs directly with `s.cron.Entry(id).Job.Run()`; no clock is needed.
- A firing while held does not call `runFn`.
- Two firings of one task while held run once on `Release`.
- `Install` during a hold keeps the hold.
- `Release` skips a task removed during the hold.
- `RunNow` ignores unknown names.

**Verify:** `go test -race ./internal/cron/`

## Step 3: busy classification

**Files**
- `internal/drain/busy.go`: a pure function `Classify(in ProbeInput) (Probe, error)`.
  - Input: `ProbeInput{Name, BridgeConnected, BridgeBusy bool, Harness string, Capture func() (string, error)}`.
  - A connected bridge returns `BridgeBusy` with transport `bridge`, and `Capture` is never called.
  - Otherwise the capture goes through the harness's classifier: `tmux.ClaudeComposerClassifier`, `CodexComposerClassifier` or `OpenCodeComposerClassifier`. An empty harness means claude.
  - Only `ComposerBusy` counts as busy. `ComposerUnknown` (dialogs) and a capture error count as not busy, and the error is returned for logging.
- `internal/service/drain_probe.go`: `drainProber{sup *Supervisor, tmuxPath string, command tmux.CommandFunc}`.
  - Agent list: `sup.EphemeralAgents()`.
  - Bridge state: `sup.BridgeState(name)`.
  - Harness: `sup.identities`. Add a small `HarnessOf(name)` accessor under `mu.RLock`.
  - Pane: `tmux.ResolvePane`.
  - Capture: `capture-pane -p -t <pane>` with the same 5s timeout `InjectIntoWith` uses.
  - `Exists=false` when the name is not in `EphemeralAgents()`.

**Failing tests**
- `busy_test.go`, a table test:
  - bridge busy;
  - bridge idle while a busy capture is available (assert `Capture` is not called);
  - legacy claude busy and empty, using fixture captures copied from `internal/tmux` composer tests;
  - legacy dialog → not busy;
  - capture error → not busy, error returned;
  - codex harness uses the codex classifier.
- `drain_probe_test.go`: assert the exact capture argv through the `command` seam (memory: mocked exec seams hide argv bugs), and a missing agent gives `Exists=false`.

**Verify:** `go test -race ./internal/drain/ ./internal/service/ -run 'Classify|DrainProbe'`

## Step 4: drain manager state machine (`internal/drain/manager.go`)

**API**
- `New(Deps{Prober, Notifier, Cron, Store, Clock, Logf, PollInterval=2s, LeaseTTL=60s, SettlePolls=2})`.
- Methods: `Begin(ctx, timeout) (Status, error)`, `Status() Status` (renews the lease), `Tick(ctx)`, `Commit(ctx, force bool) error`, `Cancel(ctx, reason) error`, `Run(ctx)` (calls `Tick` every `PollInterval` while a drain is active).
- `Status{ID, Phase /* idle|draining|committed */, Done bool, TimeoutMinutes int, Warning string, Members []MemberStatus{Name, Transport, State /* waiting|notified|drained|gone */}}`.
- Errors: `ErrInProgress`, `ErrNotDrained`, `ErrNoDrain`.

**Rules** (each is a test)
1. `Begin` takes the drain set from `Prober.BusyAgents`, gives the drain a random ID, and calls `Cron.Hold()`. An empty set is `Done` immediately. `Begin` while a drain is active returns `ErrInProgress`.
2. Bridge members get `PauseText` from `Begin` once, with id `drain-<ID>-<name>`. A member counts as **notified** once the delivery is not queued, or once `Holds` returns false.
3. Composer members are messaged on the first `Tick` whose probe is not busy. A failed send is retried on the next tick ("retried until delivered").
4. A member is **drained** when it is notified and its probe is not busy on `SettlePolls` consecutive ticks after notification. Going busy resets the count, so the member is waited on again. A member with `Exists=false` is **gone** and counts as drained.
5. If the notifier returns `ErrNotifierUnavailable` (web disabled), the drain falls back to waiting only: a member is drained after `SettlePolls` idle ticks, and `Warning` is set.
6. Agents outside the drain set are never probed or messaged.
7. `Commit(force)` runs a fresh `Tick` first. Without force it requires `Done`. It then calls `Store.Write` with the drain set and `Cron.Held()`, and sets the phase to committed. The cron hold stays on until the process dies.
8. `Cancel`:
   - sends `CancelText` to members whose pause was delivered or is still queued (bridge outbox), but not to composer members that were never pasted;
   - calls `Cron.Release()` and `Store.Remove()`;
   - sets the phase to idle;
   - is also valid while committed, for the restart-failed path.
9. **Lease:** `Tick` cancels the drain (reason "lease expired") when `now - lastStatus > LeaseTTL`. This covers a CLI that is SIGKILLed or loses its SSH session.

**Failing tests** (`manager_test.go`). Use a fake clock, prober, notifier, cron and store, and drive the manager with `Tick` only. Cover rules 1–9, including:
- busy → idle without notification is not done;
- a bridge queued (202) member is notified only after `Holds` turns false;
- re-busy after drained;
- the lease fires at TTL+1s and not before.

**Verify:** `go test -race ./internal/drain/`

## Step 5: messenger adapter in the web server

**Files**
- `internal/web/drain_notifier.go`: `func (s *Server) DrainNotifier() drain.Notifier` returns `drainNotifier{s}`.
  - `Message` calls `messageAgentAs(ctx, name, "", text, id)`. A non-empty `out.err` becomes an error, `out.bridgeQueued` becomes `Queued`, and the transport comes from `out.transport`.
  - `Holds` calls `s.bridgeRouter.Holds`.
  - This follows the `chatDeliverer` precedent.

**Failing tests** (`drain_notifier_test.go`, using the existing fakes from the bridge and legacy message tests):
- a bridged 202 gives `Queued=true` and the id is passed through unchanged;
- a legacy paste gives transport legacy;
- an unknown agent returns an error.

**Verify:** `go test -race ./internal/web/ -run DrainNotifier`

## Step 6: daemon IPC routes and client

**Files**
- `internal/daemon/handlers_drain.go`. Routes:
  - `GET /drain/busy` → `[]Probe` with `Busy` true;
  - `POST /drain {timeout_seconds}`;
  - `GET /drain`;
  - `POST /drain/commit {force}`;
  - `POST /drain/cancel`.
  - Error mapping: `ErrInProgress`, `ErrNotDrained` and `ErrNoDrain` → 409; no manager → 503.
  - Validate `timeout_seconds` is in 1..86400.
- `Server.SetDrain(*drain.Manager)` starts `m.Run(s.parentContext)`.
- The notifier given to the manager is a daemon adapter reading an `atomic.Pointer` that `StartWeb` sets from `s.webServer.DrainNotifier()`, the same pattern as `setControl`. While unset it returns `ErrNotifierUnavailable`.
- `internal/service/process.go`: build the manager with `drainProber`, `srv` as the scheduler's `CronGate` (expose `Server.Scheduler()` or pass `s.scheduler` inside daemon), `drain.FileStore(cfg.StatePath())` and the system clock. Call `srv.SetDrain` before `srv.Start`.
- `internal/daemon/client_drain.go`: `DrainBusy`, `DrainBegin`, `DrainStatus`, `DrainCommit`, `DrainCancel`. A 404 or 405 from an old daemon becomes `ErrDrainUnsupported`.

**Failing tests**
- `handlers_drain_test.go`: drive `Handler()` with `httptest`. Check the status mapping, the JSON shape, the timeout bounds, and the 503 without a manager.
- `client_drain_test.go`: a socket server answering 404 gives `ErrDrainUnsupported`.

**Verify:** `go test -race ./internal/daemon/`

## Step 7: resume on startup

**Files**
- `internal/drain/resume.go`: `Resume(ctx, p PendingResume, d ResumeDeps{Exists func(string) bool, AwaitBridge func(ctx, name), Notifier, RunTasks func([]string), Logf})`.
  - For each agent in `p.Agents`: if it does not exist, log and skip. Otherwise call `AwaitBridge` (bounded), then `Message(ResumeText, id "")`. Failures are logged.
  - Then call `RunTasks(p.DeferredTasks)`.
- `internal/service/process.go`: after `srv.MarkReady()`, start a goroutine running `runPendingResume(ctx, deps)`, which calls `Take` and then `Resume`.
  - `Exists` checks for an agentstore record, so a dormant wakeable agent is woken by `messageAgent`.
  - `AwaitBridge` applies when `sup.BridgeKey(name)` is known: `hub.WaitFor(Connected)` under a 60s `context.WithTimeout`, then it proceeds anyway. Without this wait a mod that hasn't reconnected yet would get the message by tmux paste.
  - `RunTasks` is `scheduler.RunNow`.
  - A `Take` error is logged and the daemon boots normally.

**Failing tests**
- `resume_test.go`:
  - a missing agent is skipped and logged while others get `ResumeText`;
  - deferred tasks run after the messages;
  - an `AwaitBridge` timeout still messages;
  - a notifier error on one agent does not stop the rest.
- `process` wiring test, in the style of `observe_wiring_test.go`: `runPendingResume` with a corrupt file logs and does not panic, and with a missing file it does nothing.

**Verify:** `go test -race ./internal/drain/ ./internal/service/`

## Step 8: CLI graceful flow

**File** `internal/cli/graceful_restart.go`

**Flags:** `addGracefulFlags(cmd, *gracefulOptions)` registers:
- `--graceful` and `--no-graceful` (mutually exclusive, via `MarkFlagsMutuallyExclusive`);
- `--drain-timeout` (default `10m`, must be > 0);
- `--force`.

**Entry point:** `gracefulRestart(ctx, home, opts, deps)`, where `deps` holds:
- `drain` (the client interface from step 6);
- `restart func(ctx) error`, which wraps `restartDaemonAndWait`;
- `isInteractive`;
- `ask`, a ctx-aware prompter that reads stdin in a goroutine so Ctrl-C isn't stuck in a blocking read;
- `clock`, `out`, and `pollInterval` (2s).

**Flow**
1. Call `DrainBusy`. If it returns `ErrDrainUnsupported`, warn ("running daemon predates graceful restart") and restart. If no agents are busy, restart.
2. Choose the mode:
   - `--no-graceful` → restart;
   - `--graceful` → drain;
   - interactive → `N agents are working (a, b, c). Graceful restart? [Y/n]`;
   - non-interactive → restart.
3. Call `DrainBegin`. A heartbeat goroutine calls `DrainStatus` every `pollInterval` until the drain ends, so the lease stays renewed while a prompt is open. The main loop prints transitions such as "a: notified", "b: drained".
4. When `Done`: call `DrainCommit(false)`, then `restart`. If `restart` fails, call `DrainCancel` with `context.WithoutCancel` + 10s and return the error. `errDaemonNotReady` stays a warning.
5. When the deadline passes:
   - interactive: list the stragglers and offer `[r]estart anyway / [w]ait another <timeout> / [a]bort`;
   - non-interactive: with `--force`, call `DrainCommit(true)` and restart; without it, cancel and return a non-zero error;
   - `--force` also skips the interactive question.
6. On Ctrl-C (`signal.NotifyContext`: SIGINT, SIGTERM, SIGHUP, set up in the command `RunE`), call `DrainCancel` on a fresh context and return an error.

**Wiring**
- `newServiceRestartCmd` uses `addGracefulFlags` and `gracefulRestart`.
- `newUpdateCmd` gets the same flags, and `maybeRestartDaemon(opts)` calls `gracefulRestart` after the existing "Restart it now?" yes.
- A non-interactive update with an explicit `--graceful` or `--no-graceful` counts as consent to restart (see open question 1). Without a flag it behaves as today and does not restart.

**Failing tests** (`graceful_restart_test.go`, with a fake drain client, fake clock and scripted prompter):
- prompt and flag matrix:
  - no busy agents;
  - `--no-graceful`;
  - `--graceful`;
  - interactive answers Y, n, and default;
  - non-interactive with no flag;
  - old daemon;
- timeout branches: restart anyway, wait again then done, abort, non-interactive with and without force;
- Ctrl-C cancels on a live context;
- restart failure after commit calls cancel;
- the heartbeat keeps polling while the prompt blocks;
- flag validation: both flags given, and timeout ≤ 0.
- Extend `daemon_restart_test.go` only if `restartDaemonAndWait`'s signature changes.

**Verify:** `go test -race ./internal/cli/ -run 'Graceful|RestartDaemon'`, also run with `env -u TMUX` (memory: `$TMUX` masks unstubbed seams).

## Step 9: web restart button

- `internal/web/handlers.go` `handleServiceRestart`: argv becomes `service restart --graceful --force --config <path>`. The flash text becomes "Service restarting. Busy agents get up to 10 minutes to pause first."
- **Failing test** (`handlers_restart_banner_test.go`): assert the exact argv through the `execCommand` seam.
- **Verify:** `go test -race ./internal/web/ -run ServiceRestart`

## Step 10: docs (no TDD)

- `docs/cli/service.md`, restart section: busy check, prompt, the flags, timeout choices, non-interactive and `--force` behaviour, the web button, `state/pending-resume.json`, cron deferral, and the old-daemon fallback.
- `docs/cli/update.md`: the same flags, and the explicit-flag consent rule.
- `internal/templates/skills/daemon-management.md`: what the pause, cancel and resume messages mean to an agent.
- `CHANGELOG.md` entry.

## Step 11: verification

- Run `make test && make lint`, then `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. The argv and flags change, so e2e is required.
- **Live, isolated.** Ask Evan before any `service restart`, test daemon included (standing rule).
  1. Build: `make build`.
  2. Create `/tmp/lgr/leo.yaml`. Use a short path (the socket path limit), a distinct `web.port`, and one enabled persistent-task or agent template.
  3. Install the test daemon as **its own** launchd job: `./bin/leo service start --daemon --config /tmp/lgr/leo.yaml`.
  4. Confirm `launchctl print gui/$(id -u)/com.blackpaw.leo.<hash>` exists and that the production `com.blackpaw.leo` PID is unchanged. Only then restart. `daemonLabel` hashes non-default homes, but the memory says `service restart` once hit production, so verify the label before the first kickstart.
  5. Spawn a claude agent. Give it a long turn, e.g. Bash `sleep 120`, then reply.
  6. Run `./bin/leo service restart --graceful --config /tmp/lgr/leo.yaml`.
  7. Confirm each stage: the prompt is skipped; capture the pane to show the pause text after the turn ends; the CLI waits; it restarts; the resume message is in the pane; `pending-resume.json` is gone.
  8. Repeat with Ctrl-C mid-drain and confirm the cancel text, cron released, and the file removed.
  9. Repeat through the web button (`POST /web/service/restart` with the test token).
- **Teardown:** `./bin/leo service stop --daemon --config /tmp/lgr/leo.yaml`, kill the test tmux sessions by exact name, `trash /tmp/lgr`, and confirm `leo agent list` in production is unchanged. Tell Evan about the roster-marker side effect (memory: the test daemon strips production roster lines).
- **Review:** dispatch `review.concurrency` over `git diff main...HEAD`. The bar is P0/P1 plus anything a real restart can hit. Accept and record sub-second races.

## Decisions the spec left open (made here; veto if wrong)

1. **"Idle" means notified, then idle on 2 consecutive polls.** Bridge `deliver` waits for idle (bridge spec: `prompt.submit` waits for idle), so the pause notice always lands after the current turn. Counting the first idle moment would restart with the notice still queued, and the agent would wake to a pause and then a resume.
2. **Drain lease (60s without a status poll → auto-cancel).** The spec covers Ctrl-C but not a SIGKILLed CLI or a lost SSH session. Without the lease, cron stays deferred forever.
3. **Deferred cron firings survive the restart.** They are stored in `pending-resume.json` and run by `RunNow` after the resume messages. The spec's "not dropped" would otherwise fail on every drain that ends in a restart. Firings are coalesced to one run per task.
4. **Old daemon fallback.** On the first `leo update` to this version, the CLI is talking to a daemon that has no `/drain` routes. It warns and restarts immediately, which is today's behaviour.
5. **Composer members that never got the pause get no cancel.** A bridge member whose notice is still queued does get the cancel, ordered behind the pause in its outbox.

## Risks

- A single turn longer than the timeout always reaches the timeout. The pause message can't cut a running turn short; it only stops the agent from starting new work afterwards.
- **Forced restart with a pause still queued:** the durable outbox carries it to the next launch, so the agent sees the pause and then the resume. Accepted.
- **`ComposerUnknown` counts as not busy.** That includes a mid-turn permission dialog, so such an agent is restarted without draining. The spec's literal wording says this. Treating it as busy would just use up the timeout waiting for a human.
- **`leo update`'s stale-agent restart offer** (`maybeRestartStaleAgents`) runs right after the new daemon sends resume messages. It can bounce an agent mid-resume-turn. This is out of scope per the spec, but should be noted in the docs.
- A daemon crash mid-drain, before commit, loses the in-memory hold and its deferred firings. Accepted: it is rare and the outcome matches today.

## Open questions

1. **`leo update` non-interactive with an explicit `--graceful`/`--no-graceful`:** should the flag count as consent to restart? Today an unattended update never restarts. Recommendation: yes, otherwise the flag does nothing there.

**Resolved (orchestrator, 2026-10-14):** Yes. An explicit graceful flag, either form, passed to an unattended `leo update` counts as consent to restart.
