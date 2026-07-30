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

### Two probes, and why the second one is the real check

macOS attributes a connection to the *responsible* process, not necessarily
the one calling `connect(2)`. Leo's tmux server is started as a child of the
signed `leo` binary precisely so agent panes inherit leo's grant. When that
inheritance lapses — for instance once the daemon that created the server has
exited — third-party binaries under agent sessions are denied while `leo`
itself still connects fine.

A probe running in the `leo` CLI process therefore *cannot fail* for the
condition that matters, so `leo doctor` runs two:

| Probe | What it measures |
|-------|------------------|
| `Probe (leo)` | A dial from leo's own process. Corroborating detail only — leo holds its own grant. |
| `Probe (tmux)` | A third-party binary (`node` or `python3`) run in a throwaway session **inside leo's tmux server** — the same process context agent panes get. |

`curl` is deliberately not a candidate, even a Homebrew one: it reports its own
exit codes rather than errno, and `7` ("couldn't connect") covers refused,
unreachable, and timed-out alike — so it cannot distinguish "the packet left the
machine" from "the OS blocked it", which is the whole basis of the verdict.

Apple platform binaries (anything under `/usr/bin`, `/bin`, `/usr/sbin`,
`/sbin`, `/System`) are exempt from Local Network TCC and are skipped when
choosing the in-tree probe binary: probing with one would report a false pass.
If no third-party binary is available, the in-tree probe reports "no verdict"
rather than guessing — install one with `brew install node`.

The in-tree verdict is what the reported `State` reflects. leo's own dial is
consulted only when the in-tree probe reached **no verdict at all** (no server,
no probe binary); a probe that ran but came back inconclusive stays
`undetermined`, because leo's process always holds leo's grant and deferring to
it there would turn "unknown" into a false `granted`.

## States

| State | Meaning |
|-------|---------|
| `granted` | The probe connection reached the target host (connected, or was actively refused) — the packet left the machine. |
| `denied` | The probe failed with `EHOSTUNREACH`/"no route to host" — macOS blocked the packet before it left the machine. |
| `denied (tmux tree)` | The in-tree probe was blocked while leo's own dial succeeded. Agents are cut off from the LAN even though leo looks healthy. `leo service restart` does **not** fix this — the tmux server survives a daemon restart by design; recycle it with `tmux -L leo kill-server` (terminating live agent sessions). |
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
