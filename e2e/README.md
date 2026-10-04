# e2e suite

Build-tagged (`e2e`), so `go test ./...` skips it. It builds leo, runs real
daemons against fake harness binaries, and drives a private tmux server.

## Running it

Always run it isolated from the shell you are in. A leaked `TMUX`,
`LEO_DISPATCH_ID` or similar once let a test act on a live leo pane:

```sh
env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e
```

- `E2E_RUN=<pattern>` runs only the tests matching a `go test -run` pattern:
  `... make e2e E2E_RUN=TestClaudeBridge`.
- `LEO_E2E_CLAUDE=1` (in the `env -i` list) also runs the real-claude bridge
  tests (`claude_bridge_test.go`). They need an installed, logged-in claude
  new enough for the mods API, and they spend a little model usage (haiku).
  Without the variable they skip.

```sh
env -i HOME=$HOME PATH=$PATH USER=$USER TMUX_TMPDIR=$(mktemp -d) \
  LEO_E2E_CLAUDE=1 make e2e E2E_RUN=TestClaudeBridge
```

`USER` lets claude read its macOS keychain login; the suite adds it for the
processes it starts when it is missing.
