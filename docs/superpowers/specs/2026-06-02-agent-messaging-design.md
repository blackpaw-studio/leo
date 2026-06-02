# Agent-to-Agent Messaging — Design

Date: 2026-06-02
Status: Approved

## Goal

Give every Leo agent and supervised process an easy, built-in way to send a
text message to another Leo agent/process, and make sure every agent is aware
the capability exists without the user having to configure anything.

A "message" is delivered by injecting it into the recipient's live Claude
prompt (the same class of mechanism Leo already uses for `/clear`, `/compact`,
and interrupt), so the recipient picks it up as a new turn in its session.

## Background: the enabling gap

Leo wires its built-in MCP server into a Claude process via `--mcp-config`
(`leomcp.AppendArg`). Today that call exists in two of three arg-build sites:

- `internal/cli/service.go` — supervised **processes** ✅
- `internal/run/runner.go` — scheduled **tasks** ✅
- `internal/agent/args.go` (`BuildTemplateArgs`) — ephemeral **agents** ❌

So ephemeral agents currently get **no** `leo_*` tools at all. The supervisor
already exports `LEO_PROCESS_NAME`, `LEO_WEB_PORT`, and `LEO_API_TOKEN` for
agents (`internal/service/process.go` ~line 780), so the runtime identity and
auth are present — only the `--mcp-config` flag is missing.

**Closing this gap is step one.** It is required for agents to message at all,
and as a bonus unlocks every existing `leo_*` tool (list/spawn/stop agents,
tasks, etc.) for ephemeral agents.

## Components

### 1. Enable the leo MCP server for ephemeral agents

In `internal/agent/args.go`, route the assembled args through
`leomcp.AppendArg(args, cfg)` (same conditional behavior as the other two
sites: only appends `--mcp-config` when `cfg.Web.Enabled`). This makes the
`leo` MCP server — and therefore `leo_send_message` and all other `leo_*`
tools — available inside spawned agents.

### 2. New MCP tool: `leo_send_message`

Location: `internal/mcp/tools.go` (registry), `internal/mcp/client.go`
(daemon client method).

- **Inputs**
  - `to` (string, required): target process/agent name as shown by
    `leo_list_agents` / `leo status`.
  - `message` (string, required): the message body.
- **Sender identity**: the registry already holds `processName` (the "self"
  the slash-command tools operate on). The delivered text is prefixed so the
  recipient knows the origin, e.g. `[message from <self>] <message>`.
- **Guards**
  - Reject empty `message` (reuse `stringArg`).
  - Reject `to == self` with a clear error (no self-messaging loops).
  - Unknown target → error whose text lists all currently running session
    names (see Discovery). The error doubles as the recipient directory.
- **Result on success**: a short confirmation, e.g.
  `Sent message to <to>`.

### 3. Delivery primitive: literal paste + submit

Decision: **Option B** (dedicated literal endpoint), chosen over reusing the
existing `/send` route.

Why not reuse `/send`: `handleProcessSendKeys` types multi-char args
one-character-at-a-time using a first-character heuristic (`needsCharSplit`)
built to activate slash-command menus. For free-text messages that path is
slow and has a sharp edge — a message that happens to be a tmux key name
(`Enter`, `Escape`, `C-c`, …) would be interpreted as that key rather than
typed literally.

New daemon route: `POST /web/process/{name}/message` with body
`{"text": "<message>"}`. Handler:

1. Resolve `sessionName := "leo-" + name`.
2. `tmux send-keys -t <session> -l <text>` — the `-l` (literal) flag disables
   key-name lookup, so arbitrary text (including key-name-like tokens and
   punctuation) is typed verbatim. This registers as a paste in Claude Code's
   Ink REPL.
3. `tmux send-keys -t <session> Enter` — a separate keystroke that submits the
   prompt.

Client method on the MCP daemon client (`internal/mcp/client.go`):
`sendMessage(target, text string) error` → `POST /web/process/<target>/message`.

Newlines in `message` are sent literally; submission is the single trailing
`Enter`. Multi-line messages are an accepted edge — the recipient may need to
submit a trailing line manually. We do not attempt to flatten or rewrite the
body.

### 4. Target discovery / validation

The single source of truth for "can I reach `name`" is "does tmux session
`leo-<name>` exist." The supervisor already enumerates all of its sessions —
both supervised processes and ephemeral agents, each tagged with an
`Ephemeral` flag — to serve `leo status` and `leo agent list`.

`leo_send_message` validates `to` against that running-session set before
attempting delivery. On miss, the error message lists the available names,
covering both processes and agents through one uniform mechanism. No separate
"list recipients" tool is added; `leo_list_agents` remains available for
explicit enumeration of agents.

### 5. Awareness (automatic system-prompt line)

Surfaced two ways:

1. **Tool description** — `leo_send_message`'s description is always visible in
   the recipient's `/mcp` output. Baseline, no wiring required.
2. **Built-in append-system-prompt line** — injected by Leo automatically,
   **only when the MCP server is actually wired in** (i.e. `cfg.Web.Enabled`,
   the same condition as `leomcp.AppendArg`). This keeps the awareness text and
   the working tool coupled: an agent is only told it can message others when
   it actually can.

Implementation:

- Add a helper, e.g. `leomcp.SystemPromptAddition(cfg *config.Config) string`,
  returning the awareness sentence when web is enabled, otherwise `""`.
- At each of the three arg-build sites (`service.go`, `runner.go`,
  `args.go`), merge this built-in text with any user-configured
  `append_system_prompt` into a **single** `--append-system-prompt` value
  (built-in text first, then a blank line, then the user's text). Merging into
  one flag avoids depending on Claude Code accepting `--append-system-prompt`
  more than once.

Proposed wording (final wording may be tuned during implementation):

> You can send a message to another Leo agent or process with the
> `leo_send_message` tool — set `to` to its name and `message` to the text.
> Use `leo_list_agents` to see which agents are running. The message arrives in
> the recipient's prompt as a new turn.

## Error Handling

- Empty `message` → `argument "message" must be a non-empty string`.
- Empty `to` → `argument "to" must be a non-empty string`.
- `to == self` → explicit "cannot message yourself" error.
- Unknown `to` → error listing running session names.
- tmux/daemon failure → wrapped error surfaced as an MCP `isError` result
  (existing convention in `dispatch`).

## Testing

Table-driven Go tests with `-race`:

- `internal/mcp`: `leo_send_message` registry call — success, empty/missing
  args, self-send rejection, unknown-target error shape; new `sendMessage`
  client method against a stub daemon.
- `internal/web`: new `/web/process/{name}/message` route — asserts a literal
  (`-l`) send followed by an `Enter`, via the `execCommand` test seam.
- `internal/agent`: `BuildTemplateArgs` now includes `--mcp-config` when web is
  enabled, and the merged `--append-system-prompt` contains the built-in line.
- `internal/leomcp`: `SystemPromptAddition` returns the line when enabled and
  `""` when disabled.

Target ≥80% coverage on changed packages.

## Out of Scope (YAGNI)

- No persistent inbox / mailbox / message history.
- No read receipts or delivery acknowledgements.
- No broadcast / multicast / group messaging.
- No new channel-plugin involvement — delivery is internal tmux injection.

## Affected Files

- `internal/mcp/tools.go` — new tool definition + handler.
- `internal/mcp/client.go` — `sendMessage` client method.
- `internal/web/handlers.go` — new `/web/process/{name}/message` handler.
- `internal/web/web.go` — route registration (alongside the existing
  `POST /web/process/{name}/send` at line ~175).
- `internal/agent/args.go` — `leomcp.AppendArg` + merged system prompt.
- `internal/cli/service.go`, `internal/run/runner.go` — merged system prompt.
- `internal/leomcp/leomcp.go` — `SystemPromptAddition` helper.
- Corresponding `_test.go` files.
