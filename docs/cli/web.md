# `leo web`

Web UI utilities. Subcommands:

## `leo web enable`

Turn the web listener on and restart the daemon so it comes up.

```bash
leo web enable [--bind <addr>] [--allow-host <host>]... [--json]
```

It sets `web.enabled: true` (and `web.bind` with `--bind`, and appends each `--allow-host` to `web.allowed_hosts`), validates the result, writes `leo.yaml`, and gracefully restarts a running daemon through the same routing as [`leo service restart`](service.md#leo-service-restart) — the managed label when [Leo.app manages the install](../configuration/managed-install.md). Writing the file drops its comments, as other leo commands that edit it do. A config that already says so is left alone: nothing is written and nothing restarts. If the daemon is running outside launchd it cannot be restarted, so the change is saved and a warning says to restart it.

A non-loopback `--bind` needs `web.allowed_hosts`, or validation fails and the file is left unchanged; pass `--allow-host` for each host name or IP clients will use, or the daemon refuses requests to those URLs.

| Flag | Description |
|------|-------------|
| `--bind` | `web.bind` address to set. |
| `--allow-host` | Host or IP to add to `web.allowed_hosts`. Repeatable. |
| `--json` | One JSON object on stdout; progress on stderr. Restarts gracefully unless `--no-graceful`. |
| `--graceful`, `--no-graceful`, `--drain-timeout`, `--force` | As for `leo service restart`. |

With `--json` the result is `{"ok":true,"changed":true,"restarted":true,"web":{"enabled":true,"bind":"127.0.0.1","port":8370}}` (plus `warnings` when set). Failures are `{"error":"invalid_config"|"write_failed"|"restart_failed"|"drain_timeout"|"config_error","message":"…"}` with a non-zero exit; a failed restart leaves the config saved.

## `leo web login-url`

Print a one-click login URL for the web dashboard.

On first start the daemon mints a 64-hex-char API token at `~/.leo/state/api.token`. The web login page accepts the token in a query parameter and auto-submits, saving you from pasting. `leo web login-url` reads the token file (creating one if missing) and prints a URL like:

```
http://192.0.2.10:8370/login?token=<64-hex>
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
leo web login-url --bind 192.0.2.10
# http://192.0.2.10:8370/login?token=...
```

Non-loopback bind with `allowed_hosts`:

```yaml
# ~/.leo/leo.yaml
web:
  enabled: true
  bind: 0.0.0.0
  allowed_hosts: [192.0.2.10]
```

```bash
leo web login-url
# note: using allowed_hosts[0] (192.0.2.10) as URL host; bind is 0.0.0.0
# http://192.0.2.10:8370/login?token=...
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
