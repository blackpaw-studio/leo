# The Den — pixel-office fish tank for Leo agents

**Status: concept spec — implementation not scheduled.**

**Split: The Den is NOT part of Leo.** Leo grows a small, generic observability API
(activity tracker + snapshot + SSE endpoints); The Den is a standalone project in its own
repo that consumes it. Go plugins are a non-option (platform- and toolchain-version-locked);
a scraping sidecar would couple to Leo's internal state files. A stable read-only API is
the boundary — also reusable by the macOS app and leoterm.

A watch-only, game-like standalone web app: a retro pixel-art office where every
supervised agent is an animated character whose behavior mirrors reality. No controls, no
panels, no chrome — a living dashboard you put on a TV. "The Sims meets AI ops," in the
genre of the OpenClaw pixel-office projects (pixel-claw, OpenClawfice), but built on Leo's
primitives.

## Goals

- Glanceable truth: everything on screen is driven by real daemon state and real activity
  signals. If an agent looks busy, it is busy.
- Flashy and fun: charm is the point. Events in the cluster become visible drama.
- Zero interaction: watch-only. All agent control stays in the existing UI/CLI.

## Non-goals

- No in-world chat, inspect panels, or actions of any kind.
- No historical analytics; the Den shows now, not trends.
- No game code, assets, or rendering in the Leo repo — Leo's contribution ends at the
  observability API.

## World design

**Layout — Leo lore.** The office is generated from the API snapshot: one room per distinct
workspace/project (leo, olympus, …), auto-laid-out on a tile grid with a shared lobby,
break room, and a den (couch corner). Persistent/supervised agents are residents with a
desk in their workspace's room. Oneshot task firings are visitors: a courier character
walks in the front door, sits at a guest desk for the duration of the run, then leaves.

**Character identity.** Sprite composition encodes real attributes:
- Model tier → headwear: Fable crown, Opus wizard hat, Sonnet cap, Haiku bare (small,
  fast walk cycle).
- Harness → shirt color: claude / codex / opencode each get a fixed color.
- Name floats under the sprite.

**Behavior states** (mapped from daemon state + activity signals):
- Working: seated at desk, typing animation, speech bubble with current activity
  ("$ make test", "editing manager.go"). Bubble text is truncated and sanitized.
- Idle (running, no recent activity): wanders, gets coffee, chats at the water cooler.
- Suspended: asleep on the den couch, zzz bubble.
- Stopped: desk sits empty and dark.
- Crash-looping (restarts climbing): desk on fire 🔥 until the restart loop settles.

**World events.** Cluster events become moments: confetti burst when a task run succeeds;
red rotating alarm light + the visitor storming out when one fails; a spawn walks in the
door; a stop packs a box and leaves. A simple "camera director" slowly pans the office and
cuts to wherever an event just fired (with a manual free-look override).

**Ambient environment.** Day/night lighting synced to the real clock; real local weather
(fetched server-side, e.g. open-meteo, pushed over the event stream) rendered out the
windows; optional muted-by-default lofi + keyboard-clack audio toggle.

## Architecture

### Leo side — generic observability API (the only Leo changes)

**Activity tracker (new).** The supervisor gains a lightweight per-agent activity poller:
hash tmux `capture-pane` output every ~2s → busy/idle + last-activity timestamp (works
for all three harnesses). For claude-harness agents, additionally tail the session JSONL
(session ID is already tracked) to extract tool_use events for activity descriptions.
Codex/opencode start with pane-delta busy/idle only.

**Observability endpoints (new).** Served on the daemon's existing TCP listener,
deliberately Den-agnostic:
- `GET /api/v1/state` — full snapshot: agents (identity, status, workspace, template,
  model, harness, restarts, busy/idle, last activity), tasks, recent task runs.
- `GET /api/v1/events` — SSE stream of typed events: agent_state_changed,
  agent_activity, task_run_started/succeeded/failed, agent_spawned/stopped/suspended/
  resumed. SSE (not websockets) — one-way is all a consumer needs and it fits the
  existing net/http stack.

Read-only, same trust domain as the web UI (LAN). No game concepts leak into the API —
rooms, sprites, weather, and camera are entirely the consumer's business.

### Den side — standalone project (own repo)

A separate web app, free of Leo's embed.FS/no-build-step constraints: its own repo,
proper JS tooling (e.g. Vite + kaplay or Phaser). A thin server (or pure static app)
points at the Leo daemon's base URL, consumes snapshot + SSE, and runs the whole
simulation client-side (world generation from the snapshot, movement, animation, camera
director). Weather comes from the Den's own fetch (e.g. open-meteo), not from Leo.
Reconnect/re-snapshot on SSE drop.

Prior art to mine before building (Research & Reuse): pixel-claw and
pixel-office-openclaw for behavior loops and asset pipelines, license permitting; their
OpenClaw coupling means adaptation, not adoption.

## Implementation ordering (when picked up)

1. Leo: activity tracker + `/api/v1` snapshot/SSE endpoints (testable without any UI).
2. Den repo: static world — rooms generated from the snapshot, residents at desks,
   state-driven poses.
3. Den: movement, visitors, world events, camera director.
4. Den: ambient layer (day/night, weather, audio) + character identity details.

## Open questions (defer to build time)

- Sprite asset sourcing: commission/pixel-edit vs. generate vs. adapt a CC0 asset pack.
- Whether task history (internal/history) can backfill "visitor" arrivals on page load.
