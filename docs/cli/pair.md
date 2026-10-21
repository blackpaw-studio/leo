# leo pair

Pair a phone (the Leo iOS app) or Leo.app's menu bar with this machine's daemon.

## Usage

```bash
leo pair
leo pair --json
```

## Description

`leo pair` prints a QR code and a one-line JSON payload — `{daemon_id, name, urls, pairing_code}`. The `urls` are `web.public_urls` when set, otherwise the web listener's LAN addresses. The pairing code works once and expires after 5 minutes. Redeeming it issues the device its own bearer token; manage paired devices with `leo devices list` and `leo devices revoke <id>`.

Warnings (a loopback-only listener, a LAN URL missing from `web.allowed_hosts`) go to stderr. With the web listener disabled the command fails; [`leo web enable`](web.md#leo-web-enable) turns it on.

## `--json`

Prints one JSON object instead of a QR code, for the Leo.app menu bar (which draws the QR itself):

```json
{
  "daemon_id": "9f2c…",
  "name": "studio-mac",
  "urls": ["https://leo.example.com"],
  "pairing_code": "…",
  "warnings": [],
  "expires_at": "2026-10-20T23:10:00Z",
  "qr_payload": "{\"daemon_id\":\"9f2c…\",\"name\":\"studio-mac\",\"urls\":[…],\"pairing_code\":\"…\"}"
}
```

| Field | Meaning |
|-------|---------|
| `daemon_id`, `name`, `urls`, `pairing_code` | The payload fields, as printed in text mode. |
| `warnings` | Always an array; empty when there is nothing to flag. |
| `expires_at` | RFC 3339 UTC time the code stops working. |
| `qr_payload` | The exact string to encode as the QR code. Encode this, not a re-serialisation of the other fields. |

stdout carries only the object. With the web listener off it is exactly `{"error":"web_disabled"}` and the exit status is non-zero (the app then offers `leo web enable`). Other failures are `{"error":"config_error"|"pairing_failed","message":"…"}`. See [the JSON rule](../configuration/managed-install.md#the-json-rule).
