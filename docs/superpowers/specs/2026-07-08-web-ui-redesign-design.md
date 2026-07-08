# Leo Web UI Redesign — "Ops Terminal"

**Date:** 2026-07-08
**Status:** Approved

## Goal

Redesign the Leo web UI for its primary job — config editing — with a polished
"Ops Terminal" visual direction, and close the settings gap permanently by
replacing hand-built config forms with schema-driven forms enforced by a
reflection test. Add Sessions and Service operational pages. Desktop-first,
usable on a phone.

## Background

The current UI is a single dark page with six htmx tabs and ~850 lines of
hand-rolled CSS. Its config forms were hand-built per field and have drifted
badly behind the config schema:

- Whole sections absent: `providers`, `sessions`, `client` (hosts), editable `web`
- Fields absent everywhere: `provider`; task `runtime`/`session`/`lazy`/`queue_max`;
  template `idle_suspend_after`; defaults `bypass_permissions`/`remote_control`/
  `stale_resume_hours`
- "Add" forms capture only a subset, forcing a second edit pass
- Known bugs: blank form fields silently erase config values on save; the
  process `bypass_permissions` handler reads a form field the template never
  renders (web can only ever set it false); Settings displays the wrong default
  bind address (`0.0.0.0` shown, actual default `127.0.0.1`); the claude
  sub-agents dropdown is cached once at server start

Operationally, persistent sessions and service reload/status are CLI-only.

## Decisions (from brainstorming)

| Question | Decision |
|---|---|
| Primary use | Config editing first; monitoring secondary |
| Devices | Desktop-first; phone usable for quick checks/toggles |
| Config architecture | Schema-driven field registry + reflection drift test |
| New surfaces | Sessions view, Service controls. Agent logs viewer deferred |
| Visual direction | A — "Ops Terminal" (over Control Panel and Instrument Panel mockups) |

## 1. Stack

Unchanged philosophy: Go `html/template` + htmx + hand CSS, embedded via
`embed.FS`. No JS framework, no build step.

- CSS rewritten around design tokens (CSS custom properties)
- Committed dark theme only (terminal idiom — a deliberate single-theme choice)
- Type: JetBrains Mono woff2 served from `/static/` (no CDN), falling back to
  `ui-monospace, SF Mono, Menlo, monospace`
- Palette: bg `#0b0e14`, panels `#10141d`/`#151a25`, hairlines `#232a38`,
  text `#d7dee8`, dim `#77839a`, links `#82aaff`
- Accent: amber `#ffb454` is the single interactive accent (primary buttons,
  active nav, focus rings). Green `#3fdc97` / red `#ff6b6b` are reserved
  strictly for status semantics and never used decoratively

## 2. Layout & navigation

- The tab row becomes a left sidebar, two groups:
  - **Operate:** Tasks, Agents, Processes, Sessions
  - **Configure:** Defaults, Templates, Providers, Settings (web + client hosts), Service
- Status line pinned at top: process health, task/agent counts, next run.
  Polls every 5s (as today). Restart-required banner and flash system retained.
- Real URLs per section (`/tasks`, `/agents`, `/processes`, `/sessions`,
  `/config/defaults`, `/config/templates`, `/config/providers`,
  `/config/settings`, `/service`)
  using htmx-boosted links — browser refresh and deep links keep your place.
  `GET /` redirects to `/tasks`.
- Mobile (~<768px): sidebar collapses to a hamburger drawer; tables stack into
  cards. CSS only, no JS beyond the drawer toggle.

## 3. Schema-driven forms (core)

New package `internal/web/schema`:

- **Field registry.** One entry per config field: yaml key, Go accessor,
  label, help text, input kind, and the sections it applies to
  (defaults / process / task / template / session / provider / client / web).
- **Input kinds:** text, number, tri-state bool (for `*bool`: inherit / on / off),
  select (with options source, e.g. models, providers, permission modes,
  templates), csv list, env map (key=value rows), duration, cron (with the
  existing human-readable preview).
- **Renderer:** builds each section's form from the registry — grouped,
  each field with label + help text. Common fields first, advanced grouped
  below (collapsed `<details>`, as today).
- **Parser:** one generic form→config apply path shared by every section.
  Explicit semantics per kind: empty text clears a string; tri-state selects
  map to `*bool` nil/true/false; "inherit from defaults" placeholders show the
  cascaded effective value. This removes the per-handler mutation code that
  caused the blank-field-erase bug.
- **Drift test:** a reflection test walks the config structs' yaml tags and
  fails if any field lacks a registry entry. An explicit exclusion list names
  genuinely CLI-only or internal fields, so every exclusion is a visible,
  reviewed decision.

Coverage this brings (everything the schema has today): `provider` on
defaults/processes/tasks/templates/sessions; task `runtime`, `session`, `lazy`,
`queue_max`, `silent`, `timezone`, `notify_on_fail`; template
`idle_suspend_after`; defaults `bypass_permissions`, `remote_control`,
`stale_resume_hours`, `idle_suspend_after`; full **providers** editor
(base_url, api_key_env, api_key_cmd, default_model); **sessions** section;
**client** hosts; editable **web** section (enabled, port, bind,
allowed_hosts) with correct default display.

Save flow is unchanged mechanically: load fresh config → apply → `Validate()` →
`Save()` → scheduler reload → restart banner when process-affecting.

## 4. New operational surfaces

- **Sessions page:** list persistent task sessions with status; reset and
  drain actions. Thin wrapper over existing `internal` session machinery
  (same code paths as `leo session list/status/reset/drain`).
- **Service page:** daemon status, uptime, config reload (without restart) vs
  full service restart, and a tail of recent service logs.

Both pages are read/act surfaces only — no new daemon capabilities.

## 5. Fixes swept in

- Wrong bind-address default display in Settings
- Dead `bypass_permissions` form field (process handler reads a field the
  template never emits) — replaced by the tri-state control
- "Add" flows create an entry with just a name (+ schedule for tasks), then
  land directly in the full schema-driven edit form — no second-pass editing
- Claude sub-agents dropdown refreshes on demand instead of caching at boot

## 6. Explicitly out of scope

- Agent logs viewer / suspend / resume / prune from web (deferred)
- Auth changes (token login, sessions, bearer API — untouched)
- JSON `/api/*` surface (channel plugins depend on it — no changes)
- Light theme

## 7. Testing

- Registry reflection (drift) test — fails CI when a config field lacks a
  registry entry or exclusion
- Form round-trip tests: `parse(render(cfg)) == cfg` for each section,
  including `*bool` tri-states, env maps, and clear-vs-unset semantics
- Existing handler/middleware/auth tests updated to the new routes
- Visual pass: Playwright screenshots at 375 / 768 / 1440 against an isolated
  test daemon (separate `LEO_HOME`; never the production daemon — a restart
  bounces all running agents)

## 8. Risks / notes

- The registry must handle nested sections (`providers`, `client.hosts`,
  `sessions`) which are maps of structs — the renderer needs list-of-entries
  UI (add/remove named entries), not just flat forms
- Editing the `web` section from the web UI can lock you out (port/bind
  change requires restart; wrong allowed_hosts blocks you). Mitigate with an
  inline warning on those fields; restart banner already communicates the
  restart requirement
- Route change from single-page tabs to per-section URLs touches middleware
  (session redirect paths) — auth tests must cover the new routes
