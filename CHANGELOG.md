# Changelog

## Unreleased

### Breaking

- **Web UI default bind changed from `0.0.0.0` to `127.0.0.1`.** The UI has no built-in auth, so the previous default exposed full process control to anyone who could reach the port. Anyone who was relying on LAN access must set `web.bind: 0.0.0.0` explicitly. The daemon now prints a prominent warning at startup when `web.bind` is non-loopback.

### Security

- **`leo update` now verifies release archive integrity.** Before replacing the running binary, `DownloadAndReplace` fetches the release's `checksums.txt`, parses out the entry for the platform archive, and rejects the update on any mismatch or missing entry. Prevents a compromised CDN or MITM from shipping a tampered binary. Archive size is capped at 100 MB and `checksums.txt` at 1 MB to bound the damage from a hostile server.

## v0.3.0 — Channel-agnostic (BREAKING)

Leo no longer ships with Telegram built in. Channels are now opaque [Claude Code plugin](https://docs.anthropic.com/en/docs/claude-code/plugins) IDs that you install separately and reference by ID on processes and tasks.

### Migration

1. **Install a channel plugin** (if you want one):
   ```bash
   claude plugin install telegram@claude-plugins-official
   ```
2. **Update `leo.yaml`**:
   - Remove the top-level `telegram:` block entirely.
   - Add a `channels:` list to each process that needs messaging:
     ```yaml
     processes:
       assistant:
         channels: [plugin:telegram@claude-plugins-official]
     ```
   - Tasks using `notify_on_fail` must now also declare `channels:`:
     ```yaml
     tasks:
       my-task:
         notify_on_fail: true
         channels: [plugin:telegram@claude-plugins-official]
     ```
   - Remove any `topic_id:` fields from tasks. Topic routing is now owned by the channel plugin (read the plugin's own docs for how it handles threading).

### Breaking changes

- `telegram:` config block removed. Any top-level `telegram:` key in `leo.yaml` is silently ignored on load. Bot tokens and chat IDs now live in the channel plugin's own config (e.g. `~/.claude/channels/telegram/.env`).
- `task.topic_id` field removed.
- `leo telegram topics` CLI command removed.
- `leo setup` no longer prompts for a bot token, installs the Telegram plugin, or sends a test message. The wizard is now channel-agnostic — install a channel plugin yourself via `claude plugin install <id>`.
- `bun` is no longer a prerequisite (was only used by the forked Telegram plugin).
- Supervisor no longer monitors the Telegram plugin's lock file or restarts the claude session when the plugin dies. Channel plugin lifecycle is Claude's plugin host's responsibility now.
- `notify_on_fail` is now implemented by spawning a short child `claude` invocation (max-turns 3, 60s timeout) that asks the agent to deliver the failure notification via its configured channel plugin. Requires `channels:` on the task.

### Internals

- Deleted packages: `internal/telegram/`, `internal/pluginsync/`, the embedded `plugins/telegram/` source tree.
- Supervisor exports `LEO_CHANNELS` into each spawned claude process so agents can enumerate their configured channels.
- Agent templates reference "configured channel plugins" generically instead of hardcoded Telegram messaging rules.

## v0.1.2 and earlier

See `git log` or the GitHub releases page.
