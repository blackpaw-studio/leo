# Leo Web Control Interface — Design Spec

## Context

Leo is a Go CLI that supervises persistent Claude Code processes and schedules tasks. It already has a daemon that serves a JSON API over a Unix socket for CLI IPC. Users currently manage everything via the terminal.

The goal is to add a web-based dashboard and control panel so Leo can be monitored and operated from a browser on the local network — without replacing the CLI. This gives at-a-glance visibility into process health, task scheduling, and execution history, plus the ability to toggle tasks and reload config without SSH-ing into the machine.

## Decisions

- **Go embedded**: Web UI served directly from the daemon via `net/http` + `embed.FS`. Single binary, no sidecar.
- **htmx + server-rendered HTML**: Go `html/template` renders pages, htmx handles interactivity (polling, partial swaps). No JS build step.
- **Dual listener**: Unix socket stays for CLI. New TCP listener on configurable port (default `8370`) for web UI.
- **LAN access, no auth**: Binds to `0.0.0.0` by default. No authentication layer.
- **No log streaming**: Logs stay in the CLI. Web UI focuses on status, control, and history.
- **Visual direction**: Dark terminal theme — deep blacks, monospace accents, green/amber/red status colors.

## Architecture

```
leo daemon (existing process)
├── Unix socket listener  ← CLI commands (unchanged)
├── TCP listener :8370    ← Web UI (LAN-accessible)
│   ├── GET /             → full dashboard page
│   ├── GET /partials/*   → htmx fragment endpoints
│   ├── POST /web/*       → mutation endpoints
│   └── GET /static/*     → embedded CSS, htmx.min.js
│
├── Supervisor            ← process state (unchanged)
├── Scheduler             ← cron state (unchanged)
└── Config                ← yaml config (unchanged)
```

Web handlers call the same `Supervisor`, `Scheduler`, and `Config` methods that the existing JSON API handlers use. The difference is rendering HTML templates instead of JSON.

## UI Design

### Layout: Single Page Dashboard

Everything on one scrollable page. No sidebar, no multi-page navigation.

### Status Banner (top, always visible)

- Service running/stopped indicator (green dot or red dot)
- Summary stats: process count, task count, next upcoming run
- Daemon uptime, Leo version
- Auto-refreshes every 5s via `hx-trigger="every 5s"` on the banner div

### Process Cards (below banner, always visible)

- One card per configured process
- Each card shows: name, status, uptime, restart count
- Color-coded left border:
  - Green = running
  - Amber = restarting
  - Red = stopped
  - Gray = disabled
- Cards auto-refresh via htmx polling (every 5s, same trigger as banner)

### Tab: Tasks (default)

- Table columns: name, schedule (cron expression), next run (relative time), last exit code, enabled state
- Inline actions per row:
  - Enable/disable toggle (POST, htmx swaps updated row)
  - "Run now" button (POST, shows brief flash confirmation)
- Expandable row: click task name to reveal last 10 history entries (exit code + timestamp)
- "Add task" form at bottom: name, schedule, prompt file, model dropdown
- Disabled tasks shown at reduced opacity

### Tab: Config

- Read-only rendered view of effective config (defaults, telegram status, processes, tasks)
- "Reload config" button → POST `/web/config/reload` → success/error flash message
- Config displayed in structured sections, not raw YAML

## Endpoints

### Existing (unchanged, Unix socket)

All 11 current daemon endpoints remain as-is for CLI IPC.

### New Web Endpoints (TCP listener)

| Method | Path | Purpose | Response |
|--------|------|---------|----------|
| GET | `/` | Full dashboard page | Complete HTML document |
| GET | `/partials/status` | Status banner | HTML fragment |
| GET | `/partials/processes` | Process cards | HTML fragment |
| GET | `/partials/tasks` | Task table | HTML fragment |
| GET | `/partials/task/{name}/history` | Task history expansion | HTML fragment |
| GET | `/partials/config` | Config view | HTML fragment |
| POST | `/web/task/{name}/toggle` | Toggle task enable/disable | Updated task row fragment |
| POST | `/web/task/{name}/run` | Trigger immediate task run (async, via subprocess `leo run <name>`) | Status flash fragment |
| POST | `/web/config/reload` | Reload config + reschedule | Status flash fragment |
| GET | `/static/*` | Embedded static assets | CSS, JS files |

All `/partials/*` endpoints return HTML fragments for htmx to swap into the page.

All `/web/*` POST endpoints return small HTML fragments (updated row, flash message) for htmx to swap.

## File Structure

```
internal/web/
├── web.go              # NewWebServer(), ListenAndServe(), route setup
├── handlers.go         # HTTP handler functions
├── templates/
│   ├── layout.html     # Base layout (<html>, <head>, CSS/JS includes, page shell)
│   ├── dashboard.html  # Full page (extends layout, includes all sections)
│   ├── partials/
│   │   ├── status.html       # Status banner fragment
│   │   ├── processes.html    # Process cards grid fragment
│   │   ├── tasks.html        # Task table fragment
│   │   ├── task_history.html # Expandable history rows
│   │   └── config.html       # Config view fragment
│   └── components/
│       ├── process_card.html # Single process card
│       ├── task_row.html     # Single task table row
│       └── flash.html        # Success/error flash message
├── static/
│   ├── htmx.min.js    # Vendored htmx (~14kb gzipped)
│   └── style.css      # Dark terminal theme
└── embed.go            # //go:embed templates static
```

## Config Changes

New `web` section in `leo.yaml`:

```yaml
web:
  enabled: true       # default: false
  port: 8370          # default: 8370
  bind: "0.0.0.0"     # default: "0.0.0.0"
```

New fields in `config.Config`:

```go
type WebConfig struct {
    Enabled bool   `yaml:"enabled"`
    Port    int    `yaml:"port"`
    Bind    string `yaml:"bind"`
}
```

Validation:
- Port must be 1-65535
- Bind must be a valid IP address or "0.0.0.0"

## Integration with Daemon

In `internal/daemon/server.go`, after the existing Unix socket setup:

1. If `config.Web.Enabled`, create a `web.Server` with references to `Supervisor`, `Scheduler`, and `Config`
2. Start TCP listener on `config.Web.Bind:config.Web.Port` in a separate goroutine
3. Shut down TCP listener on daemon stop (same context)

The web server needs access to:
- `daemon.ProcessStateProvider` (already an interface) for process states
- `*cron.Scheduler` for cron entry listing
- `*config.Config` for config display and task management
- The existing task add/remove/enable/disable/reload logic in the daemon handlers
- For "run now": the web handler spawns `leo run <name>` as a detached subprocess (same approach as cron). This avoids duplicating the run logic and keeps the web handler non-blocking.

## htmx Patterns

### Auto-refresh (polling)

```html
<div id="status-banner"
     hx-get="/partials/status"
     hx-trigger="every 5s"
     hx-swap="outerHTML">
  <!-- server-rendered status content -->
</div>
```

### Task toggle

```html
<button hx-post="/web/task/daily-report/toggle"
        hx-target="closest tr"
        hx-swap="outerHTML">
  disable
</button>
```

### Tab switching

```html
<button hx-get="/partials/tasks"
        hx-target="#tab-content"
        hx-swap="innerHTML"
        class="active">Tasks</button>
<button hx-get="/partials/config"
        hx-target="#tab-content"
        hx-swap="innerHTML">Config</button>
```

### Flash messages

```html
<!-- Returned from POST endpoints -->
<div class="flash flash-success"
     hx-swap-oob="afterbegin:#flash-container">
  Config reloaded successfully
</div>
```

Auto-dismiss via CSS animation or htmx `hx-trigger="load delay:3s" hx-swap="delete"`.

## Visual Theme

Dark terminal aesthetic:
- Background: `#0a0a1a` (page), `#111127` (cards/surfaces)
- Text: `#e0e0e0` (primary), `#808090` (secondary)
- Status green: `#4ade80`
- Status amber: `#f59e0b`
- Status red: `#ef4444`
- Accent: `#6366f1` (indigo, for links and active tabs)
- Font: system monospace stack for data, system sans for labels
- Borders: subtle `#1a1a2e`

## Verification Plan

1. **Build**: `make build` succeeds — binary includes embedded web assets
2. **Startup**: `leo service start` with `web.enabled: true` — daemon logs "web UI listening on :8370"
3. **Dashboard loads**: Open `http://<host>:8370` from browser — full page renders
4. **Auto-refresh**: Process status updates within 5s of a change (no manual reload)
5. **Task toggle**: Click disable on a task → row updates inline → verify via `leo task list`
6. **Run now**: Click "run now" → flash confirmation → task executes (check history)
7. **Config reload**: Click "reload config" → flash confirmation → scheduler updates
8. **Tab switching**: Tasks ↔ Config tabs swap content without full page reload
9. **LAN access**: Open from another device on the network — page loads and functions
10. **CLI regression**: All existing `leo` CLI commands still work via Unix socket
11. **Disabled state**: With `web.enabled: false`, no TCP listener starts — only Unix socket
