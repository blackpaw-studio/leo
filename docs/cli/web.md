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

The printed URL contains the API token. Do not share it, do not paste it into chat, do not log it. Anyone with the URL can log into the dashboard and take full control of your leo instance.

See [Web configuration](../configuration/config-reference.md#web) for the underlying auth model.
