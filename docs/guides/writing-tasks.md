# Writing Tasks

Tasks are the core of Leo's scheduling system. Each task is a prompt file that tells the agent what to do when cron triggers it.

## Anatomy of a Task

A task has two parts:

1. **Configuration** in `leo.yaml` — schedule, model, routing
2. **Prompt file** in your workspace — instructions for the agent

### Configuration

```yaml
tasks:
  daily-briefing:
    schedule: "0 7 * * *"
    timezone: America/New_York
    prompt_file: reports/daily-briefing.md
    model: opus
    max_turns: 20
    topic_id: 3
    enabled: true
    silent: true
```

### Prompt File

The prompt file is a plain markdown file with instructions:

```markdown
# Daily Briefing

Check the following and compile a morning briefing:

1. Scan my inbox for anything urgent
2. Check today's calendar for meetings
3. Review any open PRs that need my attention
4. Check for breaking news in my industry

Format as a concise Telegram message with sections.
```

## What Leo Adds

When `leo run <task>` executes, it assembles the final prompt from multiple parts. You only write the prompt file — Leo handles the rest.

### Silent Preamble (if `silent: true`)

Leo prepends instructions telling the agent to:

- Work without narration or progress updates
- Only send output if there's something meaningful
- Output `NO_REPLY` if there's nothing to report

### Telegram Notification Protocol

Leo appends a protocol section with:

- A `curl` command template for sending Telegram messages
- The bot token and chat ID (from config)
- The `message_thread_id` (from `topic_id`) if topic routing is configured

The agent uses this protocol to send its final output as a Telegram message.

## Examples

### Heartbeat Check

The default heartbeat task runs every 30 minutes during waking hours and only messages when something needs attention:

```yaml
# leo.yaml
tasks:
  heartbeat:
    schedule: "0,30 7-22 * * *"
    prompt_file: HEARTBEAT.md
    model: sonnet
    max_turns: 10
    topic_id: 1                          # discover IDs via `leo telegram topics`
    enabled: true
    silent: true      # silent: only report if something needs attention
```

```markdown
<!-- HEARTBEAT.md -->
# Heartbeat Check

Run through this checklist. Only report items that need my attention.

- [ ] Unread messages that are urgent or time-sensitive
- [ ] Calendar events in the next 2 hours
- [ ] Pending tasks or reminders that are due
- [ ] Any alerts or notifications that need action

If everything is clear, output NO_REPLY.
If something needs attention, send a concise summary.
```

### Weekly Report

A longer-running task that compiles a weekly summary:

```yaml
tasks:
  weekly-report:
    schedule: "0 9 * * 1"           # Monday at 9 AM
    prompt_file: reports/weekly.md
    model: opus                      # use opus for deeper analysis
    max_turns: 30
    topic_id: 7
    enabled: true
```

```markdown
<!-- reports/weekly.md -->
# Weekly Report

Compile a summary of the past week:

1. Key accomplishments and milestones
2. Open items and blockers
3. Upcoming deadlines this week
4. Recommendations or suggestions

Format for easy reading on mobile.
```

### Custom Monitoring

A task that monitors something specific:

```yaml
tasks:
  repo-monitor:
    schedule: "0 */4 * * *"         # every 4 hours
    prompt_file: reports/repo-monitor.md
    model: sonnet
    max_turns: 15
    enabled: true
    silent: true
```

```markdown
<!-- reports/repo-monitor.md -->
# Repository Monitor

Check the following repositories for activity:

- github.com/myorg/main-app
- github.com/myorg/api-service

Look for:
- New PRs that need review
- Failed CI builds
- Security advisories
- Dependency updates

Only report if there's something actionable.
```

## Best Practices

- **Keep prompts focused** — one clear objective per task
- **Use silent mode** for frequent checks to avoid notification spam
- **Set appropriate max_turns** — simple checks need fewer turns than complex analysis
- **Choose the right model** — use `sonnet` for routine tasks, `opus` for tasks requiring deeper reasoning
- **Include output format guidance** — tell the agent how to format its Telegram message
- **Test manually first** — run `leo run <task>` before installing to cron

## Creating a New Task

```bash
# 1. Write your prompt file
vim ~/leo/reports/my-task.md

# 2. Add the task interactively
leo task add

# 3. Install to cron
leo cron install

# 4. Verify
leo cron list
```

Or edit `leo.yaml` directly and run `leo cron install`.

## See Also

- [`leo task`](../cli/task.md) — managing tasks
- [`leo run`](../cli/run.md) — executing tasks
- [Scheduling](scheduling.md) — cron expressions and timezone handling
- [Config Reference](../configuration/config-reference.md#tasks) — full task field specification
