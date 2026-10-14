# Graceful daemon restart

Status: draft · 2026-10-14

## Problem

A daemon restart (`leo service restart`, `leo update` and variants, the web restart button) sometimes restarts agents. Examples: failed tmux adoption, config-driven bounces, and Linux systemd without `KillMode`. An agent that is mid-turn loses its work. Leo has no drain step today: the CLI runs `kickstart -k` or `systemctl restart` without checking for busy agents.

## Goal

Before a restart, let busy agents reach a safe stopping point. After the restart, tell the same agents to resume.

## Behavior

1. **Busy check.** The CLI asks the daemon which agents are busy, through a new IPC endpoint. An agent is busy if:
   - it is bridge-connected and the hub's `busy` flag is set, or
   - it is not bridge-connected and the tmux composer shows a running turn (the same read `InjectInto` uses for `ErrComposerBusy`).

   If no agents are busy, the restart proceeds as today with no prompt.
2. **Opt-in.** If any agent is busy, the CLI asks `N agents are working (a, b, c). Graceful restart? [Y/n]`.
   - `--graceful` skips the prompt and drains.
   - `--no-graceful` skips the prompt and restarts immediately.
   - In non-interactive runs without a flag, the CLI restarts immediately. This is today's behavior.
   - The web restart button always drains. It passes `--graceful --force`, so the restart is never blocked on a prompt.
3. **Drain.** The daemon enters a draining state and records the busy set as the drain set. While draining:
   - The daemon sends each agent in the drain set this message through the normal `messageAgent` path: `[leo] Daemon restart pending. Reach a safe stopping point, note where you are, and end your turn. You'll get a resume message after the restart.`
   - Bridge agents receive it through the outbox at the turn boundary. Legacy agents receive it when the composer frees up, retried until delivered.
   - Cron task firings are deferred until the drain ends. They are not dropped.
   - Agents outside the drain set are unaffected. An agent in the set that goes busy again is waited on again.
4. **Wait.** The CLI polls the drain status until every agent in the drain set is idle. The default timeout is 10 minutes, set with `--drain-timeout`.
   - When the timeout expires in an interactive run, the CLI lists the stragglers and asks: restart anyway, keep waiting another timeout, or abort.
   - When the timeout expires in a non-interactive run, the CLI aborts with a non-zero exit, unless `--force` is set, which restarts anyway.
   - On abort or Ctrl-C, the daemon exits draining and sends each agent in the set `[leo] Restart cancelled. Resume your work.` It also resumes the deferred cron firings.
5. **Persist.** Before the kickstart, the daemon writes the drain set to `state/pending-resume.json`, using the same locking and 0600 permissions as `agentstore`.
6. **Resume.** On startup, after agent restore finishes, the daemon reads `pending-resume.json` and sends each listed agent that exists `[leo] Daemon restarted. Resume where you left off.` It then deletes the file. Agents that no longer exist are logged and skipped. Because the daemon owns this step, resume still happens if the CLI dies mid-restart.

## Out of scope

- **Dispatch runs.** They are not messaged or waited on. Their panes live in the caller's tmux and their records are on disk. The caller agent's drain covers the parent turn.
- **Avoiding the restart-kills-agents causes.** Linux `KillMode` and config bounces are separate fixes.
- **Holding inbound chat or `leo_send_message` traffic during drain.**

## Testing

- **Unit tests:** busy classification (bridge vs. composer), drain-set state machine (busy, idle, re-busy, cancel), cron deferral, and the pending-resume round-trip, including a missing agent and a corrupt file.
- **CLI tests:** prompt and flag matrix, and timeout branches. Use an injected clock with no real sleeps.
- **Live:** an isolated test daemon (separate `LEO_HOME`, never production `leo service restart`) with one agent mid-turn. Run `leo service restart --graceful` and confirm the pause message, the wait, the restart, and the resume message.
