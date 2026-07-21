# leo doctor

Diagnose local network and daemon health, with a focus on macOS Local
Network privacy.

## Usage

```bash
leo doctor [--probe-host host:port] [--trigger]
```

## Description

On macOS, a process needs the user's one-time consent to talk to other
devices on the local network ("Local Network" privacy, introduced in
macOS 15 Sequoia — see [Apple TN3179: Understanding local network
privacy](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy)).
That consent is granted (or denied) per signed binary the first time it
performs a local-network operation — and, crucially, only when the binary
runs in a context where macOS can surface the Allow/Deny dialog to the
user.

Third-party tools spawned inside a background tmux session under Leo's
daemon can inherit a silent denial: connections to other LAN devices fail
with `EHOSTUNREACH` ("no route to host") even though the identical command
works fine from an interactive Terminal window. `leo doctor`:

1. Deliberately performs local-network operations (an mDNS multicast send
   and an mDNS multicast group join) so that, when run interactively as the
   signed `leo` binary, macOS surfaces the one-time Allow/Deny dialog
   attributed to `leo`.
2. Reports a best-effort verdict on whether Local Network access is
   currently granted, by classifying the outcome of a TCP connect attempt
   to a known on-subnet LAN host.

## States

| State | Meaning |
|-------|---------|
| `granted` | The probe connection reached the target host (connected, or was actively refused) — the packet left the machine. |
| `denied` | The probe failed with `EHOSTUNREACH`/"no route to host" — macOS blocked the packet before it left the machine. |
| `undetermined` | Inconclusive (timeout, DNS failure, gateway unreachable for other reasons, etc.). Re-run, or pass `--probe-host` with a host you know is reachable. |
| `n/a` | Non-macOS platform; Local Network privacy doesn't apply. |

## Flags

| Flag | Description |
|------|-------------|
| `--probe-host <host:port>` | Explicit LAN endpoint to test connectivity against (e.g. `10.0.2.9:443`). Defaults to the machine's default gateway on port 80 when unset. |
| `--trigger` | Perform the mDNS consent-raising operations before probing (default `true`). Pass `--trigger=false` to only classify, without attempting to trigger a fresh consent prompt. |

## Examples

```bash
# Best-effort check against the default gateway
leo doctor

# Check against a specific known-reachable LAN host
leo doctor --probe-host 10.0.2.9:443

# Classify only, without re-triggering the consent dialog
leo doctor --trigger=false
```

## See Also

- [`leo status`](status.md) — overall daemon/agent/task health
- [`leo validate`](validate.md) — deeper prerequisite and config checks
- [Apple TN3179: Understanding local network privacy](https://developer.apple.com/documentation/technotes/tn3179-understanding-local-network-privacy)
