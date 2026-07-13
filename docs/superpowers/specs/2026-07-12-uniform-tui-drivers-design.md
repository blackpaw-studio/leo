# Uniform tmux-TUI drivers — design

**Date:** 2026-07-12
**Status:** Approved by Evan (approach A: shared TUI driver + per-harness profiles)
**Supersedes:** the Plan-3/Plan-4 session-driver split (codex `TurnDriver`, opencode `ServerDriver`). That split shipped in no release; v0.8 is held for this rewrite.

## Goal

Every harness drives a live session the way claude does: a persistent interactive
TUI inside `leo-<name>` tmux, message injection via readiness-probed
paste-buffer + Enter, and `leo attach` / `leo agent attach` dropping the user
into the tmux session directly. One driver model, one attach UX, one injection
machine — for claude, codex, and opencode alike.

Non-goals: non-persistent scheduled tasks (`leo run` without
`runtime: persistent`) stay headless one-shots (`claude -p`,
`codex exec --json`, `opencode run --format json`) with `ParseEvents` — that is
already the claude pattern. Remote attach delegation (#104) is untouched.

## Verified facts (live-tested 2026-07-12 — do not re-derive)

Tested in a scratch tmux server (`tmux -L scratch-tui`, 200x50 panes) on
Dionysus with codex-cli 0.144.1 and opencode 1.17.7:

- **codex TUI in tmux**: renders correctly. Input line marker is `› `
  (U+203A + space). The leo injection protocol works verbatim: probe char `.`
  echoes as `› .`, `C-u` clears it, `paste-buffer -d` + `send-keys Enter`
  submits, model responds.
- **codex trust dialog**: first launch in an untrusted directory shows
  "Do you trust the contents of this directory?" — this dialog contains
  "trust" and must never be auto-answered by the dismissal machinery
  (`dialogDenyPattern`). Pre-writing
  `[projects."<dir>"]\ntrust_level = "trusted"` into `~/.codex/config.toml`
  before launch skips the dialog entirely. An inline
  `-c 'projects."<dir>".trust_level="trusted"'` override does **not** skip it.
- **codex resume**: `codex resume <uuid>` restores the full conversation in
  the TUI. There is no flag to *choose* a session id at first launch; the id
  must be discovered post-start. Rollout files
  (`~/.codex/sessions/YYYY/MM/DD/rollout-<ts>-<uuid>.jsonl`) record the
  session cwd, so the newest rollout whose cwd matches the workspace
  identifies the session.
- **opencode TUI in tmux**: renders correctly. Input area is a bordered box
  whose lines begin with `┃`; the probe char echoes as `┃  .`. Same
  probe/paste/Enter protocol works; local lmstudio model responded.
- **opencode TUI flags**: `-m provider/model`, `-s <session-id>` (resume),
  `--prompt <text>`, `--agent <name>`, project dir as positional / cwd.
- **claude machinery is nearly generic already**: the supervise loop branches
  only on `drv.Style()`; `tmux.InjectPrompt` (internal/tmux/inject.go) has
  exactly one claude-ism — the hardcoded `claudePromptGlyph = "❯ "`. PaneCare,
  QuickExitRecovery, and the attach plumbing are optional interfaces already.

## Design

### 1. Shared TUI driver + per-harness profiles

Promote claude's `TmuxTUIDriver` (internal/harness/claude/driver.go) into a
shared implementation, constructed per harness with a small profile:

- **Input marker** — the string that identifies the TUI's input line for the
  readiness probe and echo check: claude `❯ `, codex `› `, opencode `┃`.
  `tmux.InjectPrompt` takes this as a parameter; the probe protocol itself
  (probe char → confirm echo on an input-marker line → clear → paste → Enter)
  is unchanged and shared.
- **PaneKey (optional)** — dialog policy for startup/modal prompts. Claude
  keeps its existing implementation verbatim. Codex and opencode start with
  none (codex's only known dialog is eliminated by pre-trust; anything
  unknown is left on screen for the operator, same as claude's consequential
  dialogs). `dialogDenyPattern` keeps refusing trust/permission/delete
  dialogs globally.
- **Pre-spawn hook (optional)** — runs before `tmux new-session`. Codex:
  idempotently ensure the workspace trust entry exists in
  `~/.codex/config.toml`. Claude/opencode: none.
- **Quick-exit recovery ladder** — per harness, same interface as today
  (`QuickExitRecovery`): claude keeps its `--session-id → --resume → fresh`
  chain; codex gets `resume <id> → fresh` (clear stored id); opencode gets
  `-s <id> → fresh`.

Each harness's `Driver()` returns a thin instance of the shared driver; the
per-harness driver files shrink to profile wiring. Placement (new
`internal/harness/tmuxtui` package vs. the `harness` package itself) is a
plan-time decision — whichever avoids import cycles with `internal/tmux` and
reads cleanest.

### 2. Launch and resume argv

The supervise loop already composes `tmux new-session` generically
(`harnessBinaryPath` + shell wrapper + env overlay). Each harness's
`SessionArgs` builds its TUI argv:

- **claude**: unchanged.
- **codex**: `codex [approval/sandbox flags mapped from harness_options]
  [-m <model>] [-c mcp_servers.leo.* overrides] [resume <session-id>]`.
  The existing `harness_options` keys keep their meaning, now mapped to TUI
  flags instead of `exec` flags. Whether the `-c mcp_servers` overrides work
  identically at TUI launch is a plan-time verification item (same `-c`
  parser as exec; high confidence).
- **opencode**: `opencode [-m <provider/model>] [-s <session-id>]` with the
  workspace as cwd. The #103 env overlay (lmstudio provider env, MCP bridge,
  secrets) continues to apply at tmux-session creation, unchanged.

### 3. Session-id bookkeeping

- **claude**: unchanged (`--session-id` chosen up front; PR-#84 ladder).
- **codex**: no start-time id flag exists. After first launch reaches
  readiness, discover the id by scanning `~/.codex/sessions/**/rollout-*.jsonl`
  for the newest file whose recorded cwd equals the workspace; store the id
  (and the rollout path, to disambiguate) in the session store. Two codex
  agents sharing a workspace make the newest-by-cwd heuristic ambiguous —
  recording the rollout filename at discovery time and preferring
  files created after the spawn timestamp bounds the risk; the plan must
  handle the race explicitly.
- **opencode**: the TUI creates its session on the first turn. Discover the
  id post-first-turn and store it; resume with `-s`. The exact discovery
  mechanism (opencode state dir vs. `opencode session list` vs. export) is a
  plan-time verification item.

### 4. Deletions

- codex: `TurnDriver`, `superviseTurnBased`, per-turn transcript files and
  `appendTranscript`, the attach "history tail" path.
- opencode: `ServerDriver`, `ServerState` (port/password allocation and the
  `<home>/state/opencode/*.json` files), `opencode serve` supervision
  special-casing, `opencode run --attach` injection, the #106 keyed-window
  attach machinery.
- `harness.AttachSpec` collapses: the tmux-window flavor fields
  (`TmuxSession`/`WindowName`/`WindowCmd`/`WindowKey`) and `HistoryPath` go
  away; attach is a plain tmux attach argv for every harness.
- `leo agent logs` uses the tmux capture-pane path for every harness (the
  DriveTurns transcript branch dies).
- The hardcoded `isClaude` special-cases in internal/agent/manager.go,
  internal/cli/service.go, internal/cli/process.go, internal/service/sweep.go,
  and internal/service/session.go are swept in favor of uniform behavior.
- `isSweepEligibleHarness` is removed: **idle-suspend works for all
  harnesses**, since codex and opencode sessions now resume by stored id.
- `DriveStyle` itself likely dies (everything is DriveTmux); keep the
  `SessionDriver` interface shape only where it still earns its place —
  plan-time simplification pass.

### 5. Behavior changes (accepted)

- Injection is fire-and-forget for codex/opencode (claude parity). No
  structured turn Result from injections; persistent tasks adopt claude's
  in-session delivery semantics. The "leo_send_message MCP timeout on slow
  local models" follow-up dissolves.
- codex "restart is bookkeeping" becomes a real process restart with resume.
- codex agent history becomes tmux scrollback instead of a transcript file.
- `leo attach` for codex/opencode agents drops into the live TUI in tmux —
  status bar, `Ctrl-b d`, identical muscle memory to claude agents.

### 6. Migration

Solo-user project; no compat shims. After the update: stop and respawn any
running codex/opencode agents. Remove stale `<home>/state/opencode/` and
`<home>/state/transcripts/` artifacts (a small cleanup on daemon start or a
release note — plan decides). No `leo.yaml` changes required.

### 7. Testing

- **Unit**: generalized `InjectPrompt` readiness/echo classification against
  real pane fixtures captured from all three TUIs (fixtures exist from the
  2026-07-12 lab); per-harness `SessionArgs` fresh + resume argv; codex
  rollout discovery against a fixture sessions dir (cwd matching, newest-file
  selection, spawn-time bound); trust-entry writer idempotency (no duplicate
  entries, preserves existing file content); quick-exit ladders per harness.
- **Regression gate**: existing claude inject/attach/supervise tests pass
  unmodified.
- **CI reality**: runners have no tmux and no harness binaries — stub the
  existing seams (`injectPromptFn`, `execCommand`, `supervisedExecFn`,
  `lookPath`); `go test -race ./...`, `make lint`, `make e2e` (build-tagged),
  golangci-lint 2.12.2, gosec (pinned exclude list) all green before push.
- **Live verification before merge**: on Dionysus, spawn one agent per
  harness; verify inject (leo_send_message), attach from a remote client,
  restart-with-resume, and idle-suspend/wake for codex and opencode.

## Risks

- **TUI-scraping fragility now spans three programs.** Each harness update
  can move the prompt. Mitigations: the probe-echo check verifies the message
  actually landed before Enter fires (never fire-and-lose); markers live in
  one profile per harness; pane fixtures make breakage visible in tests.
- **codex id discovery is heuristic.** Bounded by rollout-path recording and
  spawn-time filtering; the multi-agent-per-workspace race must be handled
  in the plan, not hand-waved.
- **opencode session discovery mechanism unverified** — plan-time item; if no
  clean mechanism exists, fallback is launching with a pre-created session via
  `opencode run --prompt` once, then `-s` thereafter.
