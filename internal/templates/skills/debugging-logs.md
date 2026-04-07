# Debugging & Logs

## Log Locations

All logs live in the `state/` directory of your workspace.

| File | Contents |
|------|----------|
| `state/chat.log` | Interactive chat session output (appended) |
| `state/chat.pid` | PID of background chat process |
| `state/<task>.log` | Full output from the last run of each task |

### Read recent task output
```bash
tail -50 state/<taskname>.log
```

### Read recent chat output
```bash
tail -100 state/chat.log
```

### Check when a task last ran
```bash
ls -la state/*.log
```
The modification time shows when each task last wrote output.

## Common Failure Modes

### Task didn't run
1. **Cron not installed**: Run `leo cron list` — if empty, run `leo cron install`
2. **Task disabled**: Run `leo task list` — check the enabled column
3. **Crontab overwritten**: Run `crontab -l` and look for Leo marker blocks
4. **Environment missing**: Cron runs with minimal env. Check that `leo` is in the cron PATH

### Task ran but failed
Check the task log:
```bash
cat state/<taskname>.log
```

Common errors in logs:
- **`claude: command not found`** — Claude CLI not in PATH for cron environment
- **`Error: ANTHROPIC_API_KEY not set`** — API key not available in cron env
- **`Error: could not read prompt file`** — Prompt file path in leo.yaml is wrong or file missing
- **`max turns exceeded`** — Task hit the turn limit; increase `max_turns` in config
- **Exit code non-zero** — Claude encountered an error; check the full log output

### Silent mode and NO_REPLY

Tasks with `silent: true` in config prepend a silent preamble. The agent:
- Works without narration
- Outputs `NO_REPLY` if there is nothing to report
- Sends only a final Telegram message if something needs attention

If you see `NO_REPLY` in a task log, the task ran successfully with nothing to report.

## Checking Operational Health

```bash
# Is the chat daemon running?
leo chat status
leo chat status --daemon

# Are cron entries installed?
leo cron list

# Is the config valid?
leo validate

# What tasks are configured?
leo task list
```

## Telegram Delivery Issues

If task output isn't reaching Telegram:
1. Check the task log for curl errors
2. Verify `telegram.bot_token` and `telegram.chat_id` in `leo.yaml`
3. For topic-routed messages, verify the `topic_id` is a valid forum topic ID (use `leo telegram topics` to list available topics)
4. Test manually: `leo run <task>` and watch the output
