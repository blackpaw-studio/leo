# Leo Manual Integration Testing Runbook

A structured checklist for verifying all user-facing Leo flows against a real Claude subscription and Telegram bot. Run through this before releases or after significant changes.

**Estimated time:** 45-60 minutes (full run including cron wait)
**Estimated time:** 20-25 minutes (skip fresh install and cron wait sections)

---

## Prerequisites

Before starting, confirm:

- [ ] Claude Code CLI installed and authenticated (`claude --version`)
- [ ] Active Claude subscription (Pro or Team)
- [ ] `tmux` installed (`tmux -V`)
- [ ] `bun` installed (`bun --version`)
- [ ] Telegram bot token available (from @BotFather)
- [ ] Telegram chat ID known (DM chat with your bot)
- [ ] (Optional) Telegram forum group ID and topic IDs for topic routing tests
- [ ] Leo binary built and on PATH (`make build && export PATH=$PWD/bin:$PATH`)

**Choose your starting state:**

- **Clean:** Remove `~/.leo` before starting. Begin at Section 1.
- **Existing:** You already have a working Leo installation. Skip Section 2, begin at Section 1, then jump to Section 3.

---

## 1. Version & Validation (Baseline)

### 1.1 Version prints correctly

```
leo version
```

- [ ] **PASS** Output is `leo <version>` (e.g. `leo v0.5.0` or `leo dev`)

### 1.2 Validate on unconfigured system (clean state only)

```
cd /tmp && leo validate
```

- [ ] **PASS** Exits with error about config not found
- [ ] **PASS** Exit code is non-zero

---

## 2. Fresh Install (Setup Wizard)

> Skip this section if testing against an existing installation.

### 2.1 Run setup wizard

```
leo setup
```

Walk through the interactive wizard:

1. Accept default workspace `~/.leo`
2. Fill in user profile
3. Paste Telegram bot token when prompted
4. Send a message to the bot when prompted for chat ID detection
5. Optionally enter forum group ID and topic IDs
6. Accept heartbeat task
7. Accept cron installation
8. Accept daemon installation

- [ ] **PASS** Wizard completes without errors

### 2.2 Verify directory scaffolding

```
ls -la ~/.leo/
ls -la ~/.leo/workspace/
ls -la ~/.leo/state/
```

- [ ] **PASS** The following exist:
  - `~/.leo/leo.yaml`
  - `~/.leo/workspace/USER.md`
  - `~/.leo/workspace/reports/`
  - `~/.leo/workspace/config/mcp-servers.json`
  - `~/.leo/state/`

### 2.3 Verify Telegram plugin configured

```
cat ~/.claude/channels/telegram/.env
```

- [ ] **PASS** Contains `TELEGRAM_BOT_TOKEN=<your-token>`

```
cat ~/.claude/channels/telegram/access.json
```

- [ ] **PASS** Contains `allowFrom` array with your chat ID

### 2.4 Verify settings.json updated

```
cat ~/.claude/settings.json | python3 -m json.tool
```

- [ ] **PASS** `trustedDirectories` includes workspace path
- [ ] **PASS** `enabledPlugins` includes `telegram@claude-plugins-official`

### 2.5 Verify test Telegram message received

- [ ] **PASS** Test message appeared in your Telegram chat

---

## 3. Configuration Validation

### 3.1 Validate passes on configured system

```
leo validate
```

- [ ] **PASS** Prints `Config: valid`
- [ ] **PASS** Prints `claude CLI: <version>`
- [ ] **PASS** Prints `tmux: installed`
- [ ] **PASS** Prints `bun: installed`
- [ ] **PASS** Prints `Workspace: <path>`
- [ ] **PASS** Prints `All checks passed.`
- [ ] **PASS** Exit code is 0

### 3.2 Validate catches invalid model

Temporarily edit `~/.leo/leo.yaml` and set `model: gpt-4` under `defaults`:

```
leo validate
```

- [ ] **PASS** Reports validation error about invalid model
- [ ] **PASS** Exit code is non-zero

**Revert** the model to `sonnet` before continuing.

### 3.3 Validate catches missing prompt file

```
mv ~/.leo/HEARTBEAT.md ~/.leo/HEARTBEAT.md.bak
leo validate
```

- [ ] **PASS** Reports warning about prompt file not found for heartbeat task

**Restore:** `mv ~/.leo/HEARTBEAT.md.bak ~/.leo/HEARTBEAT.md`

---

## 4. Task Management

### 4.1 List tasks

```
leo task list
```

- [ ] **PASS** Shows tasks with columns: name, schedule, model, enabled/disabled

### 4.2 Add a test task

```
leo task add
```

Enter:

- Name: `test-task`
- Schedule: `0 12 * * *`
- Prompt file: `HEARTBEAT.md`
- Model: (press enter for default)
- Topic: (press enter to skip)
- Silent: `y`

- [ ] **PASS** Prints `Task "test-task" added.`

```
leo task list
```

- [ ] **PASS** `test-task` appears in the list as `enabled`

### 4.3 Disable a task

```
leo task disable test-task
```

- [ ] **PASS** Prints `Task "test-task" disabled.`

```
leo task list
```

- [ ] **PASS** `test-task` shows as `disabled`

### 4.4 Enable a task

```
leo task enable test-task
```

- [ ] **PASS** Prints `Task "test-task" enabled.`

### 4.5 Remove a task

```
leo task remove test-task
```

- [ ] **PASS** Prints `Task "test-task" removed.`

```
leo task list
```

- [ ] **PASS** `test-task` no longer appears

---

## 5. Task Execution

### 5.1 Dry run

```
leo run heartbeat --dry-run
```

- [ ] **PASS** Prints `Command:` followed by claude CLI args including `-p`, `--model`, `--max-turns`
- [ ] **PASS** Prints `Assembled prompt:` followed by the full prompt
- [ ] **PASS** Prompt contains HEARTBEAT.md content
- [ ] **PASS** Prompt contains Telegram notification protocol with curl example
- [ ] **PASS** Completes instantly (does NOT invoke Claude)

### 5.2 Live run (consumes Claude subscription)

```
leo run heartbeat
```

- [ ] **PASS** Completes without error (may take 30-120 seconds)
- [ ] **PASS** Log file written to `~/.leo/state/heartbeat.log`

```
cat ~/.leo/state/heartbeat.log
```

- [ ] **PASS** Contains Claude's output (a Telegram message confirmation or `NO_REPLY`)

### 5.3 Telegram notification from task

If the heartbeat task produced a Telegram message:

- [ ] **PASS** Message appeared in the correct Telegram chat/topic
- [ ] **N/A** (Claude output `NO_REPLY` -- nothing to report)

---

## 6. Service Lifecycle (Background Process)

### 6.1 Start background service

```
leo service start
```

- [ ] **PASS** Prints service started confirmation
- [ ] **PASS** Prints log path

### 6.2 Check status

```
leo service status
```

- [ ] **PASS** Prints `Service: running (pid <N>)`

### 6.3 Stop background service

```
leo service stop
```

- [ ] **PASS** Prints service stopped confirmation

```
leo service status
```

- [ ] **PASS** Prints `Service: stopped`

### 6.4 Install as daemon (launchd)

```
leo service start --daemon
```

- [ ] **PASS** Prints daemon installation confirmation

```
ls ~/Library/LaunchAgents/com.blackpaw.leo.plist
```

- [ ] **PASS** Plist file exists

```
leo service status --daemon
```

- [ ] **PASS** Prints `Daemon: running (pid <N>)` or similar running status

### 6.5 Restart daemon

```
leo service restart
```

- [ ] **PASS** Prints restart confirmation

```
leo service status --daemon
```

- [ ] **PASS** Still shows running status

### 6.6 Stop daemon

```
leo service stop --daemon
```

- [ ] **PASS** Prints daemon removed confirmation

```
ls ~/Library/LaunchAgents/com.blackpaw.leo.plist 2>&1
```

- [ ] **PASS** Plist file no longer exists

```
leo service status --daemon
```

- [ ] **PASS** Shows not installed/not running status

---

## 7. Task Management via Daemon IPC

> These tests verify that task commands route through the daemon socket when running.

### 7.1 Start daemon for IPC tests

```
leo service start --daemon
```

Wait ~10 seconds for Claude to initialize.

- [ ] **PASS** Daemon is running (`leo service status --daemon`)

### 7.2 List tasks via daemon

```
leo task list
```

- [ ] **PASS** Output includes task list (same data as direct config read)

### 7.3 Disable task via daemon

```
leo task disable heartbeat
```

- [ ] **PASS** Prints `Task "heartbeat" disabled (via daemon).`

### 7.4 Enable task via daemon

```
leo task enable heartbeat
```

- [ ] **PASS** Prints `Task "heartbeat" enabled (via daemon).`

### 7.5 Stop daemon after IPC tests

```
leo service stop --daemon
```

- [ ] **PASS** Daemon removed

---

## 8. Scheduler Management

Leo runs its own in-process scheduler inside the daemon. There is no system crontab to install. The `leo cron` command is a deprecated compatibility shim and is hidden from `leo --help`.

### 8.1 List scheduled tasks

```
leo task list
```

- [ ] **PASS** Shows every task with its schedule and `NEXT RUN` column
- [ ] **PASS** Only enabled tasks display a non-empty `NEXT RUN`

### 8.2 Reload the scheduler after editing `leo.yaml`

```
leo service reload
```

- [ ] **PASS** Prints confirmation and the daemon picks up added/removed/modified tasks without a full restart

### 8.3 Tasks fire automatically (optional, requires waiting)

1. Create a task scheduled for 1 minute from now:
   ```
   leo task add
   ```
   Use a schedule matching the next minute (e.g. `42 15 6 4 *` for 15:42 on April 6)

2. Reload the scheduler:
   ```
   leo service reload
   ```

3. Wait for the scheduled minute to pass

4. Inspect the run:
   ```
   leo task history <task-name>
   leo task logs <task-name>
   ```

- [ ] **PASS** History shows a new entry with exit status
- [ ] **PASS** Log contains Claude output

**Cleanup:** `leo task remove <test-task-name>`

---

## 9. Telegram Integration (Interactive Chat)

### 9.1 Foreground session (brief test)

```
leo service start
```

- [ ] **PASS** Service starts in background

Send a DM to the bot from Telegram:

- [ ] **PASS** Claude responds, and the response appears in Telegram

Stop the session (`leo service stop`).

### 9.2 Daemon mode Telegram (DM)

```
leo service start --daemon
```

Wait 15-20 seconds for Claude to fully initialize.

Send a DM to the bot from Telegram:

- [ ] **PASS** Bot responds in Telegram within 60 seconds

### 9.3 Forum group message (if configured)

Send a message in the Telegram forum group where the bot is a member:

- [ ] **PASS** Bot responds in the correct topic thread
- [ ] **N/A** (no forum group configured)

### 9.4 Topic routing for tasks

Run a task that has a topic configured:

```
leo run heartbeat
```

- [ ] **PASS** Notification appears in the configured topic, not the general chat
- [ ] **N/A** (no topics configured or task produced `NO_REPLY`)

### 9.5 Clean up

```
leo service stop --daemon
```

---

## 10. Update

### 10.1 Check for updates

```
leo update --check
```

- [ ] **PASS** Prints either `Update available: <current> -> <latest>` or `Already up to date (<version>)`
- [ ] **PASS** Does NOT download or modify anything

### 10.2 Workspace-only refresh

```
leo update --workspace-only
```

- [ ] **PASS** Prints `Refreshing workspace files...`
- [ ] **PASS** Lists updated files with `Updated <path>` lines
- [ ] **PASS** Prints `Refreshed <N> file(s)`
- [ ] **PASS** Does NOT update the binary

### 10.3 Full update (only if newer version available)

```
leo update
```

- [ ] **PASS** Downloads and replaces binary if newer version exists
- [ ] **PASS** Refreshes workspace files
- [ ] **PASS** If daemon is running, prompts `Daemon is running. Restart it now?`
- [ ] **N/A** (already up to date)

---

## 11. Edge Cases & Recovery

### 11.1 Daemon auto-restart (KeepAlive)

```
leo service start --daemon
leo service status --daemon
```

Note the PID, then kill it:

```
kill <pid>
```

Wait 10 seconds:

```
leo service status --daemon
```

- [ ] **PASS** Daemon is running again with a new PID (launchd restarted it)

**Cleanup:** `leo service stop --daemon`

### 11.2 Stale PID file

```
echo "99999" > ~/.leo/state/service.pid
leo service status
```

- [ ] **PASS** Shows `Service: stopped` (detects stale PID)

### 11.3 Missing prompt file at run time

```
mv ~/.leo/HEARTBEAT.md ~/.leo/HEARTBEAT.md.bak
leo run heartbeat
```

- [ ] **PASS** Exits with error mentioning the missing prompt file

**Restore:** `mv ~/.leo/HEARTBEAT.md.bak ~/.leo/HEARTBEAT.md`

### 11.4 Invalid task name

```
leo run nonexistent-task
```

- [ ] **PASS** Exits with error: `task "nonexistent-task" not found in config`

### 11.5 Nonexistent task remove

```
leo task remove nonexistent-task
```

- [ ] **PASS** Exits with error: `task "nonexistent-task" not found`

### 11.6 Double start prevention

```
leo service start
leo service start
```

- [ ] **PASS** Second invocation prints error: `already running (pid <N>)`

**Cleanup:** `leo service stop`

---

## 12. Setup Wizard

### 12.1 Setup detects existing installation

```
leo setup
```

- [ ] **PASS** Detects existing workspace and offers reconfigure option
- [ ] **PASS** Prerequisite checks run (claude, tmux, bun)

Cancel the wizard (Ctrl+C) -- this is just to verify detection works.

---

## Cleanup

After a full test run, return the system to a known-good state:

1. Stop the daemon if running: `leo service stop --daemon`
2. Stop background service if running: `leo service stop`
3. Remove any leftover test tasks: `leo task remove <name>`
4. Reload the scheduler so the daemon forgets the removed tasks: `leo service reload`
5. (Optional) Re-install daemon: `leo service start --daemon`
6. Verify final state: `leo validate`

---

## Results Summary

| Section | Tests | Passed | Failed | Skipped |
|---------|-------|--------|--------|---------|
| 1. Version & Validation | 2 | | | |
| 2. Fresh Install | 5 | | | |
| 3. Configuration | 3 | | | |
| 4. Task Management | 5 | | | |
| 5. Task Execution | 3 | | | |
| 6. Chat Lifecycle | 6 | | | |
| 7. Daemon IPC | 5 | | | |
| 8. Scheduler Management | 3 | | | |
| 9. Telegram Integration | 5 | | | |
| 10. Update | 3 | | | |
| 11. Edge Cases | 6 | | | |
| 12. Setup Wizard | 1 | | | |
| **Total** | **47** | | | |

**Date:** _______________
**Leo version:** _______________
**Claude CLI version:** _______________
**macOS version:** _______________
**Tester:** _______________
