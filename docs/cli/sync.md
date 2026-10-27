# leo sync

Pull a follower's lead config now, or show the follow state. Only meaningful on a host with a [`follow:` block](../configuration/follow-lead.md).

## Usage

```bash
leo sync                    # pull the lead now
leo sync --json             # same, as {changed, status}
leo sync --status           # show the follow state without pulling
leo sync --status --json    # the same report as GET /config/follow
```

## Behaviour

- With the daemon running, `leo sync` asks its follow loop to pull; a pull that changed the lead layer reloads the daemon's config. Running agents keep running, and changes apply on their next start.
- With no daemon (or one whose leo predates follow), `leo sync` pulls directly over ssh, writes the cache, and asks a running daemon to reload.
- Any other daemon failure, such as a timeout, is reported rather than retried in process, since the daemon may still be pulling. `leo sync` waits up to 60 seconds for the daemon.
- On a host without `follow`, `leo sync` fails, and `leo sync --status` says the host does not follow a lead.

`--status` prints the lead host and ssh target, the interval, the last successful pull and its age (marked `[stale]` past 3 × interval), the lead's revision and Leo version, the last attempt, and any warnings: a missing cache, the last pull's error, a lead on a newer Leo, and lead entries invalid on this host.

```
Following lead "server" (evan@10.0.2.10), every 5m0s
  Last pull:     2026-10-27T12:00:00Z (2m0s ago)
  Lead revision: sha256:4f1c…
  Lead version:  v0.64.0
  Last attempt:  2026-10-27T12:00:00Z
```

A failed pull exits non-zero with the follow error code (`unreachable`, `lead_too_old`, `self_follow`, ...); the cache is kept. See [Errors](../configuration/follow-lead.md#errors).

## Flags

| Flag | Description |
|------|-------------|
| `--status` | Show the follow state without pulling. |
| `--json` | Emit JSON. |

## See Also

- [Following a lead host](../configuration/follow-lead.md)
- [`leo config override`](config.md#leo-config-override)
