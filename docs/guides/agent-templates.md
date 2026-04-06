# Agent Templates

Leo ships with three personality templates that define how your agent behaves. You choose one during `leo setup`, and it's rendered into a Claude Code agent file at `~/.claude/agents/<name>.md`.

## Available Templates

### Chief of Staff

A proactive executive assistant that triages messages, manages your calendar, and sends briefings.

**Best for:** Personal productivity, communication management, daily planning.

**Personality traits:**

- Direct and concise — no fluff
- Opinionated — makes recommendations rather than listing options
- Anticipates needs based on context and patterns
- Manages daily logs and observations

**Default behaviors:**

- Triages incoming messages by priority
- Maintains a daily observation log in `daily/YYYY-MM-DD.md`
- Proactively surfaces items that need attention

### Dev Assistant

A development-focused agent for code review, monitoring, and technical tasks.

**Best for:** Software engineers who want an agent that monitors repos, runs checks, and surfaces technical issues.

**Personality traits:**

- Technical and precise
- Pragmatic — focuses on actionable items
- Monitors for issues rather than waiting to be asked

**Default behaviors:**

- Reviews code changes and PRs
- Monitors build status and alerts
- Tracks security advisories
- Surfaces technical debt and issues
- Tracks architecture decisions and patterns in persistent memory

### Skeleton

A minimal starting point with basic workspace and memory support but no predefined personality.

**Best for:** Users who want full control over their agent's personality and behavior.

**What's included:**

- Persistent memory across sessions
- Workspace configuration and daily log structure
- Basic tool access

**What's not included:**

- No predefined personality or communication style
- No default behaviors or routines
- No specific domain focus

## Shared Defaults

All templates include the following out of the box:

- **Persistent memory** — available via MCP servers configured in `config/mcp-servers.json`
- **Daily logs** — agents write observations to `daily/YYYY-MM-DD.md`
- **Standard tool access** — Read, Write, Edit, Bash, Grep, Glob, WebSearch, WebFetch

## Template Structure

All templates share a common structure:

```markdown
---
tools:
  - Read
  - Write
  - Edit
  - Bash
  - Grep
  - Glob
  - WebSearch
  - WebFetch
---

# Agent Name

[Personality and behavior instructions...]

## Workspace

[Workspace paths and file descriptions...]

## Memory Protocol

[How the agent should use persistent memory...]
```

## Customizing After Setup

The agent file at `~/.claude/agents/<name>.md` is a standard Claude Code agent file. You can edit it freely after setup:

- Add or remove tools
- Modify the personality and behavior instructions
- Add domain-specific knowledge
- Configure additional integrations

Changes take effect on the next `leo chat` or `leo run` invocation.

## See Also

- [`leo setup`](../cli/setup.md) — the setup wizard where you choose a template
- [Claude Code Agents](https://docs.anthropic.com/en/docs/claude-code) — official documentation on custom agents
