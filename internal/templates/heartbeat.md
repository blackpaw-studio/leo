# Heartbeat Checklist

This file defines what you check on every heartbeat run. Customize it.

## Checks

- [ ] Review unread messages and flag anything urgent
- [ ] Check calendar for upcoming events in the next 2 hours
- [ ] Review any pending tasks or follow-ups
- [ ] Check for any new alerts or notifications that need attention

## Output

After running checks:
- If anything needs attention → send a message via your configured channel plugin (see `$LEO_CHANNELS`) with a concise summary
- If everything is clear, or no channel plugin is configured → output NO_REPLY
