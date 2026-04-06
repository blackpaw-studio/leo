# Daemon IPC Design

## Context

Claude Code's bash sandbox blocks system-modifying commands like `crontab -` when running inside the chat daemon's tmux session. Cron tasks work because they run as separate processes outside the sandbox, but the interactive chat session cannot manage cron entries or task configuration.

The solution is to make the Leo daemon a proper service with a Unix socket API. The daemon runs with full user permissions (and macOS Full Disk Access when granted). Claude runs `leo cron install` or `leo task add` via bash as usual — the CLI detects the running daemon and forwards the request over the socket, bypassing the sandbox entirely.

## Design Decisions

| Decision | Choice | Rationale |
|----------|--------|-----------|
| Scope | Crontab + task config | Covers the immediate pain points without over-engineering |
| Transport | HTTP over Unix socket | stdlib `net/http`, zero dependencies, trivially testable |
| Client model | CLI passthrough | Claude runs normal `leo` commands; no new tools to learn |
| Auth | Unix socket permissions (0600) | Same-user only; standard Unix security model |

## Architecture

```
┌─────────────────────────────────────────────────┐
│ Leo Daemon (leo chat --supervised)               │
│                                                   │
│  ┌──────────────┐    ┌────────────────────────┐  │
│  │ HTTP Server   │    │ Claude in tmux          │  │
│  │ (leo.sock)    │    │ (telegram channels)     │  │
│  │               │    │                          │  │
│  │ POST /cron/*  │    │  bash: leo cron install  │  │
│  │ POST /task/*  │    │    ↓ detects socket      │  │
│  │               │◄───│    ↓ forwards request    │  │
│  │ executes with │    │                          │  │
│  │ full perms    │    │                          │  │
│  └──────────────┘    └────────────────────────┘  │
└─────────────────────────────────────────────────┘
```

## New Package: `internal/daemon/`

### Server (`server.go`)

`Server` struct wraps `net/http.Server` with a Unix socket listener.

```go
type Server struct {
    sockPath   string
    httpServer *http.Server
    listener   net.Listener
    configPath string
}

func New(sockPath, configPath string) *Server
func (s *Server) Start() error      // bind socket, serve in goroutine
func (s *Server) Shutdown() error   // graceful shutdown, remove socket file
```

- Socket file at `{workspace}/state/leo.sock`, permissions `0600`
- On startup: remove stale socket file if present, then bind
- On shutdown: graceful `http.Server.Shutdown()`, then remove socket file
- Config is reloaded from disk on every request (no stale cache)

### API Endpoints

| Method | Path | Request Body | Description |
|--------|------|-------------|-------------|
| GET | `/health` | — | Returns `{"ok":true}`. Used by client to detect running daemon |
| POST | `/cron/install` | `{"config_path":"..."}` | Install cron entries from config |
| POST | `/cron/remove` | `{"config_path":"..."}` | Remove cron entries for agent |
| GET | `/cron/list` | — | List installed cron entries |
| POST | `/task/add` | `TaskAddRequest` | Add task to leo.yaml + sync cron |
| POST | `/task/remove` | `{"name":"..."}` | Remove task from leo.yaml + sync cron |
| POST | `/task/enable` | `{"name":"..."}` | Enable task + sync cron |
| POST | `/task/disable` | `{"name":"..."}` | Disable task + sync cron |
| GET | `/task/list` | — | List configured tasks |

**`TaskAddRequest`:**
```go
type TaskAddRequest struct {
    Name       string `json:"name"`
    Schedule   string `json:"schedule"`
    PromptFile string `json:"prompt_file"`
    Model      string `json:"model,omitempty"`
    MaxTurns   int    `json:"max_turns,omitempty"`
    Topic      string `json:"topic,omitempty"`
    Silent     bool   `json:"silent,omitempty"`
    Enabled    bool   `json:"enabled"`
}
```

**Response envelope:**
```go
type Response struct {
    OK    bool        `json:"ok"`
    Data  interface{} `json:"data,omitempty"`
    Error string      `json:"error,omitempty"`
}
```

### Handlers (`handlers.go`)

Each handler:
1. Parses request body (JSON)
2. Loads config from disk (`config.Load(configPath)`)
3. Executes the operation (delegates to existing `cron` and `config` packages)
4. For task mutations: writes updated config back to disk, then syncs cron
5. Returns JSON response

### Client (`client.go`)

```go
func IsRunning(workDir string) bool
func Send(workDir, method, path string, body any) (*Response, error)
```

- `IsRunning`: connects to socket, hits `GET /health`, returns true/false. Timeout of 1 second.
- `Send`: creates `http.Client` with Unix socket transport (`net.Dial("unix", ...)`), sends request, decodes response.

## Integration Points

### `internal/service/process.go` — `RunSupervised()`

The signature changes to accept `configPath`:

```go
func RunSupervised(claudePath string, claudeArgs []string, workDir, configPath string) error
```

Start the daemon server before the tmux loop, keep it running across Claude restarts:

```
RunSupervised() {
    sockPath := filepath.Join(workDir, "state", "leo.sock")
    server := daemon.New(sockPath, configPath)
    server.Start()
    defer server.Shutdown()

    for {
        // existing tmux loop with backoff
    }
}
```

The server stays up during Claude's backoff restarts, so cron/task ops work even when Claude is restarting.

The call site in `chat.go:55` passes `cfgPath` (already available from the `--config` flag).

### CLI Commands — Daemon-Aware Wrapper

Each `cron` and `task` subcommand gains a daemon check at the top of its `RunE`:

```go
if daemon.IsRunning(cfg.Agent.Workspace) {
    resp, err := daemon.Send(workspace, "POST", "/cron/install", req)
    // handle response, return
}
// fallback: execute locally (existing code)
```

**Affected commands:**
- `internal/cli/cron.go` — `install`, `remove`, `list`
- `internal/cli/task.go` — `remove`, `enable`, `disable`, `list`

Note: `leo task add` is currently interactive (prompts the user). The interactive flow always runs locally. The daemon's `/task/add` endpoint accepts the full task spec as JSON, which is what Claude would use if it calls a non-interactive variant (e.g., `leo task add --name heartbeat --schedule "0 9 * * *" ...` with flags instead of prompts). If no flag-based `task add` exists yet, one should be added.

The local execution path is preserved as fallback when no daemon is running.

### Config Mutations

Task add/remove/enable/disable modify `leo.yaml`. The daemon:
1. Loads config from disk
2. Modifies the in-memory config
3. Writes back to disk (using existing `config.Save()` or a new save function)
4. Calls `cron.Install()` to sync

This requires a `config.Save()` function if one doesn't exist yet. The write is atomic (write to temp file, rename).

## Files to Create

| File | Purpose |
|------|---------|
| `internal/daemon/server.go` | HTTP server, socket lifecycle |
| `internal/daemon/handlers.go` | Request handlers for cron and task endpoints |
| `internal/daemon/client.go` | Client for CLI passthrough |
| `internal/daemon/types.go` | Request/response types |
| `internal/daemon/server_test.go` | Server + handler tests |
| `internal/daemon/client_test.go` | Client tests |

## Files to Modify

| File | Change |
|------|--------|
| `internal/service/process.go` | Start/stop daemon server in `RunSupervised()` |
| `internal/cli/cron.go` | Add daemon passthrough check |
| `internal/cli/task.go` | Add daemon passthrough check |
| `internal/config/config.go` | No change needed — `Save()` already exists at line 169 |

## Verification

1. **Unit tests**: Server starts/stops cleanly, handlers return correct responses, client detects running/stopped daemon
2. **Integration test**: Start server on temp socket, send requests via client, verify cron/config mutations
3. **Manual test flow**:
   - Start daemon: `leo chat start --daemon`
   - From another terminal: `leo cron list` (should forward to daemon)
   - Kill daemon, run `leo cron list` again (should execute locally)
4. **E2E**: Add e2e test that starts a daemon server, runs CLI commands, and verifies they were forwarded
5. **Run existing tests**: `go test -race ./...` and `go test -race -tags e2e ./e2e/` to verify no regressions
