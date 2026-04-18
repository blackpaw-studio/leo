# `leo web`

Web UI utilities. Subcommands:

## `leo web login-url`

Print a one-click login URL for the web dashboard.

On first start the daemon mints a 64-hex-char API token at `~/.leo/state/api.token`. The web login page accepts the token in a query parameter and auto-submits, saving you from pasting. `leo web login-url` reads the token file (creating one if missing) and prints a URL like:

```
http://10.0.4.16:8370/login?token=<64-hex>
```

### Flags

| Flag | Description |
|------|-------------|
| `--state-dir` | Override state directory (defaults to config's state dir). |
| `--bind` | Host to embed in the URL. Defaults to `web.bind` from config, with a fallback to `allowed_hosts[0]` when `web.bind` is non-loopback. |
| `--port` | Port to embed in the URL. Defaults to `web.port` (8370). |

### Examples

Loopback:

```bash
leo web login-url
# http://127.0.0.1:8370/login?token=...
```

LAN host:

```bash
leo web login-url --bind 10.0.4.16
# http://10.0.4.16:8370/login?token=...
```

Non-loopback bind with `allowed_hosts`:

```yaml
# ~/.leo/leo.yaml
web:
  enabled: true
  bind: 0.0.0.0
  allowed_hosts: [10.0.4.16]
```

```bash
leo web login-url
# note: using allowed_hosts[0] (10.0.4.16) as URL host; bind is 0.0.0.0
# http://10.0.4.16:8370/login?token=...
```

Non-loopback bind with **no** `allowed_hosts` is an error — pass `--bind` or add an entry to `web.allowed_hosts`.

### Security

The printed URL embeds the **long-lived API bearer token** in its query string. This is the same token stored in `~/.leo/state/api.token` that scripts and channel plugins use for bearer auth — it does not expire and is not rotated after use.

Consequences:
- **Browser history**: the full URL, including the token, is written to history on every device you open it on.
- **Referer headers**: if the logged-in dashboard ever links off-site, the token may leak in `Referer`. Leo avoids outbound links, but third-party widgets or user-added content could reintroduce this risk.
- **Shell history / logs**: if you pipe the command or copy-paste the URL, it lands in shell history, tmux scrollback, screenshots, and any terminal recording.

Treat the URL like the raw token:
- Generate it on the machine you will open it on.
- Open it once, immediately, in a trusted browser.
- Do not share, paste into chat, screenshot, or commit it.
- To invalidate a leaked token, `rm ~/.leo/state/api.token` and restart the daemon — this forces a fresh token to be minted and logs out all existing bearer callers.

See [Web configuration](../configuration/config-reference.md#web) for the underlying auth model.
