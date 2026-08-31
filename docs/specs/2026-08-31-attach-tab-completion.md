# Host-aware tab-completion for agent names

**Date:** 2026-08-31
**Status:** Approved

## Problem

`leo attach <TAB>` completes nothing: the top-level `attach` command has no
`ValidArgsFunction`. The `leo agent` subcommands (attach, stop, start, delete,
reset, restart, rename, logs) do complete via `completeAgentNames`, but that
function always queries the **local** daemon — so on a client machine with
`client.default_host` set, or with `--host <name>` on the command line,
completions come from the wrong machine's agent list.

## Goal

Tab-completion of agent names on every command that takes one, against the
agent list of the host the command will actually run on — local or remote —
in all shells cobra generates completions for (bash, zsh, fish, powershell).

## Design

### Local gap

Add `ValidArgsFunction` to top-level `leo attach` (`internal/cli/attach.go`),
using the same host-aware completion function as everything else.

### Host-aware completion

Rework `completeAgentNames` (`internal/cli/agent.go`) to resolve the target
host the same way `dispatch()` does — read the command's `--host` flag value,
fall back to `client.default_host` via `cfg.ResolveHost`:

- **Localhost** → current behavior: `daemon.AgentList` against the local
  daemon; return all agent names (running, suspended, and dormant — attach
  prompts to start dormant agents, so they are valid targets).
- **Remote** → delegate completion to the remote binary itself:
  `ssh <host> <remote-leo-path> __complete agent attach '<toComplete>'`,
  and relay the candidate lines from its output (strip cobra's trailing
  `:<directive>` line and any `Completion ended with directive` stderr noise;
  drop description suffixes after `\t` if present). This reuses cobra's own
  wire format instead of inventing a list-parsing contract, and mirrors the
  existing "resolution is delegated to the server" philosophy of
  `runRemoteAttach`. All local commands invoke this same remote target:
  every agent-name completion offers the identical candidate set (all agent
  names), so one canonical remote invocation serves them all.

The completion directive returned to the shell is always
`ShellCompDirectiveNoFileComp`.

### Remote invocation guards

Tab-completion runs interactively in the shell, so the SSH call must never
hang or prompt:

- `ssh -o BatchMode=yes -o ConnectTimeout=2`, plus the host's configured
  `SSHArgs`.
- Overall `context.WithTimeout` of 3 seconds on the exec.
- No `-t` (no TTY needed; avoids terminal side effects during completion).

SSH flattens post-host argv into one string re-parsed by the remote login
shell (see prior finding on remote argv flattening), so every remote token —
critically the empty or partial `toComplete` argument — is shell-quoted
before being handed to ssh.

### Failure behavior

Any failure — config load error, local daemon unreachable, host unresolvable,
SSH failure, timeout, unparseable output — returns no candidates with
`ShellCompDirectiveNoFileComp`. Completion never surfaces an error, blocks
past the timeout, or falls back to filename completion.

## Scope

- `internal/cli/attach.go`: add `ValidArgsFunction` to `leo attach`.
- `internal/cli/agent.go`: make `completeAgentNames` host-aware; all agent
  subcommands keep pointing at it. `rename` keeps completing only its first
  argument.
- No daemon, IPC, or remote-protocol changes. No new commands or flags.

Out of scope: completing `--host` values, template names, or task names;
caching completion results.

## Testing

- Unit tests in `internal/cli` with the existing exec seam stubbed: remote
  path builds the expected ssh argv (BatchMode, ConnectTimeout, quoted
  `__complete` tokens) and parses candidates from canned cobra output;
  directive/description lines are stripped; failures and timeouts yield no
  candidates. Assert argv precisely (prior lesson: mocked exec seams hide
  argv bugs) and run with `env -u TMUX` semantics in mind.
- Local path: stubbed daemon list → names returned; daemon error → empty.
- Live verification: `leo __complete attach ''` locally and with
  `--host <remote>` against a real host before declaring done.
