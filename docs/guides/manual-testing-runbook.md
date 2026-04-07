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

- **Clean:** Remove `~/.leo` and `~/.claude/agents/assistant.md` before starting. Begin at Section 1.
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

1. Enter agent name (use default `assistant` or a test name)
2. Accept default workspace `~/.leo`
3. Choose agent personality template
4. Fill in user profile
5. Paste Telegram bot token when prompted
6. Send a message to the bot when prompted for chat ID detection
7. Optionally enter forum group ID and topic IDs
8. Accept heartbeat task
9. Accept cron installation
10. Accept daemon installation

- [ ] **PASS** Wizard completes without errors

### 2.2 Verify workspace scaffolding

```
ls -la ~/.leo/
```

- [ ] **PASS** The following exist:
  - `~/.leo/leo.yaml`
  - `~/.leo/CLAUDE.md`
  - `~/.leo/HEARTBEAT.md`
  - `~/.leo/USER.md`
  - `~/.leo/daily/`
  - `~/.leo/reports/`
  - `~/.leo/state/`
  - `~/.leo/config/mcp-servers.json`
  - `~/.leo/skills/` (with `.md` files)

### 2.3 Verify agent file

```
cat ~/.claude/agents/assistant.md
```

- [ ] **PASS** File exists and contains personality/system prompt content

### 2.4 Verify Telegram plugin configured

```
cat ~/.claude/channels/telegram/.env
```

- [ ] **PASS** Contains `TELEGRAM_BOT_TOKEN=<your-token>`

```
cat ~/.claude/channels/telegram/access.json
```

- [ ] **PASS** Contains `allowFrom` array with your chat ID

### 2.5 Verify settings.json updated

```
cat ~/.claude/settings.json | python3 -m json.tool
```

- [ ] **PASS** `trustedDirectories` includes workspace path
- [ ] **PASS** `enabledPlugins` includes `telegram@claude-plugins-official`

### 2.6 Verify test Telegram message received

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
- [ ] **PASS** Prints `Agent file: <path>`
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

- [ ] **PASS** Prints `Command:` followed by claude CLI args including `--agent`, `-p`, `--model`, `--max-turns`
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

## 6. Chat Lifecycle (Background Process)

### 6.1 Start background chat

```
leo chat start
```

- [ ] **PASS** Prints `Chat session started for agent "<name>".`
- [ ] **PASS** Prints log path

### 6.2 Check status

```
leo chat status
```

- [ ] **PASS** Prints `Chat: running (pid <N>)`

### 6.3 Stop background chat

```
leo chat stop
```

- [ ] **PASS** Prints `Chat session stopped for agent "<name>".`

```
leo chat status
```

- [ ] **PASS** Prints `Chat: stopped`

### 6.4 Install as daemon (launchd)

```
leo chat start --daemon
```

- [ ] **PASS** Prints `Installing daemon for agent "<name>"...`
- [ ] **PASS** Prints `Daemon installed for agent "<name>" (<status>).`

```
ls ~/Library/LaunchAgents/com.blackpaw.leo.*.plist
```

- [ ] **PASS** Plist file exists

```
leo chat status --daemon
```

- [ ] **PASS** Prints `Daemon: running (pid <N>)` or similar running status

### 6.5 Restart daemon

```
leo chat restart
```

- [ ] **PASS** Prints restart confirmation

```
leo chat status --daemon
```

- [ ] **PASS** Still shows running status

### 6.6 Stop daemon

```
leo chat stop --daemon
```

- [ ] **PASS** Prints `Daemon removed for agent "<name>".`

```
ls ~/Library/LaunchAgents/com.blackpaw.leo.*.plist 2>&1
```

- [ ] **PASS** Plist file no longer exists

```
leo chat status --daemon
```

- [ ] **PASS** Shows not installed/not running status

---

## 7. Task Management via Daemon IPC

> These tests verify that task commands route through the daemon socket when running.

### 7.1 Start daemon for IPC tests

```
leo chat start --daemon
```

Wait ~10 seconds for Claude to initialize.

- [ ] **PASS** Daemon is running (`leo chat status --daemon`)

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
leo chat stop --daemon
```

- [ ] **PASS** Daemon removed

---

## 8. Cron Management

### 8.1 Install cron entries

```
leo cron install
```

- [ ] **PASS** Prints `Cron entries installed.`

### 8.2 List cron entries

```
leo cron list
```

- [ ] **PASS** Shows cron block with `LEO:` markers
- [ ] **PASS** Each enabled task has a cron line with correct schedule

```
crontab -l | grep -A5 LEO
```

- [ ] **PASS** Entries present in actual system crontab

### 8.3 Remove cron entries

```
leo cron remove
```

- [ ] **PASS** Prints `Cron entries removed.`

```
leo cron list
```

- [ ] **PASS** Prints `No leo cron entries found.`

```
crontab -l | grep LEO
```

- [ ] **PASS** No leo entries in crontab (grep returns no output)

### 8.4 Cron fires automatically (optional, requires waiting)

1. Create a task scheduled for 1 minute from now:
   ```
   leo task add
   ```
   Use a schedule matching the next minute (e.g. `42 15 6 4 *` for 15:42 on April 6)

2. Install cron entries:
   ```
   leo cron install
   ```

3. Wait for the scheduled minute to pass

4. Check the log:
   ```
   cat ~/.leo/state/<task-name>.log
   ```

- [ ] **PASS** Log file created with Claude output

**Cleanup:** `leo task remove <test-task-name> && leo cron install`

---

## 9. Telegram Integration (Interactive Chat)

### 9.1 Foreground chat (brief test)

```
leo chat
```

- [ ] **PASS** Claude interactive session starts (you see the Claude REPL)

Send a DM to the bot from Telegram:

- [ ] **PASS** Message appears in the Claude session
- [ ] **PASS** Claude responds, and the response appears in Telegram

Exit the session (Ctrl+C or `/exit`).

### 9.2 Daemon mode Telegram (DM)

```
leo chat start --daemon
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
leo chat stop --daemon
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
leo chat start --daemon
leo chat status --daemon
```

Note the PID, then kill it:

```
kill <pid>
```

Wait 10 seconds:

```
leo chat status --daemon
```

- [ ] **PASS** Daemon is running again with a new PID (launchd restarted it)

**Cleanup:** `leo chat stop --daemon`

### 11.2 Stale PID file

```
echo "99999" > ~/.leo/state/chat.pid
leo chat status
```

- [ ] **PASS** Shows `Chat: stopped` (detects stale PID)

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
leo chat start
leo chat start
```

- [ ] **PASS** Second invocation prints error: `already running (pid <N>)`

**Cleanup:** `leo chat stop`

---

## 12. Onboard Command

### 12.1 Onboard detects existing installation

```
leo onboard
```

- [ ] **PASS** Detects existing workspace and offers reconfigure option
- [ ] **PASS** Prerequisite checks run (claude, tmux, bun)

Cancel the wizard (Ctrl+C) -- this is just to verify detection works.

---

## Cleanup

After a full test run, return the system to a known-good state:

1. Stop the daemon if running: `leo chat stop --daemon`
2. Stop background chat if running: `leo chat stop`
3. Remove test cron entries: `leo cron remove`
4. Remove any leftover test tasks: `leo task remove <name>`
5. Re-install cron for production tasks: `leo cron install`
6. (Optional) Re-install daemon: `leo chat start --daemon`
7. Verify final state: `leo validate`

---

## Results Summary

| Section | Tests | Passed | Failed | Skipped |
|---------|-------|--------|--------|---------|
| 1. Version & Validation | 2 | | | |
| 2. Fresh Install | 6 | | | |
| 3. Configuration | 3 | | | |
| 4. Task Management | 5 | | | |
| 5. Task Execution | 3 | | | |
| 6. Chat Lifecycle | 6 | | | |
| 7. Daemon IPC | 5 | | | |
| 8. Cron Management | 4 | | | |
| 9. Telegram Integration | 5 | | | |
| 10. Update | 3 | | | |
| 11. Edge Cases | 6 | | | |
| 12. Onboard | 1 | | | |
| **Total** | **49** | | | |

**Date:** _______________
**Leo version:** _______________
**Claude CLI version:** _______________
**macOS version:** _______________
**Tester:** _______________
