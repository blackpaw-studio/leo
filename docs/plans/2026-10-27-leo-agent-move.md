# Plan: `leo agent move` / `leo agent copy`

Spec (source of truth): `docs/specs/2026-10-27-leo-agent-move.md`. Decision 1 is final: the destination preflight **refuses** when it lacks the agent's template or any environment the agent uses. No snapshot, no `--with-template`, no sync. Branch `feat/agent-move`, base `d744d2fc`.

## 0. Verdicts and plan-level decisions

### Per-harness resume on another host

| Harness | Move | Copy (fork) | Evidence |
|---|---|---|---|
| claude | Yes | Yes | The transcript is a file at `<CLAUDE_CONFIG_DIR>/projects/<slug>/<sid>.jsonl` (`internal/session/slug.go:20,28,65`). Leo already resumes with `--resume` (`internal/agent/resume.go:13`). claude 2.1.311 accepts `--resume X --fork-session --session-id Y`; without `--fork-session` it errors. Both were checked locally. |
| codex | Yes, from source; live check pending | Yes (`codex fork <id>`, 0.162.1) | `codex-rs/rollout/src/list.rs` `find_thread_path_by_id_str_in_subdir` (openai/codex@e3149dd) checks the state DB first. On `missing_row` it searches filenames under `$CODEX_HOME/sessions` and read-repairs the DB. Not tested live because isolated auth would need a copy of a rotating OAuth token. Step 13 decides it; if it fails, codex is unregistered and refused. |
| opencode | Yes, verified locally | Yes (`-s <id> --fork`, 1.18.18) | The DB is shared, but `opencode export <id>` → `opencode import` into an isolated `XDG_DATA_HOME` preserved the id and all 10 messages / 28 parts. Import **rewrites `directory` to the importer's cwd**, so it must run with `cmd.Dir=workspace`. Re-import upserts (5 → 10 messages). TUI resume of an imported session is checked in step 13. |

### Plan decisions (⚑ = needs Evan)

1. ⚑ **Worktree agents are refused in v1** (`Branch != ""`, or `.git` is a file). Their git dir lives in a shared canonical repo; rsync would clobber other worktrees' refs and metadata. A git-native follow-up would use fetch + `worktree add` + index sync.
2. ⚑ **Host identity.** `LEO_MOVE_FROM`/`TO` are `os.Hostname()` on each side, so both ends agree. `moved_to` stores the `client.hosts` key the user typed, for the "use `leo --host laptop …`" pointer.
3. ⚑ **Persistent-task-backed agent:** warn, don't refuse. The source cron fails until the agent comes back.
4. **Different home paths are refused** (spec non-goal; this overrides the brief). Path mapping is the identity, except that the destination resolves its own session root and staging dir.
5. **The orchestrator is a source-daemon job.** An MCP move of the calling agent kills that agent's MCP server, so the CLI process can't own the move. The job also gives one per-agent lock and one code path for CLI, web and MCP.
6. **No user data on ssh argv.**
   - Remote calls are `<leo_path> agent move-receive <verb>`, with the manifest as JSON on stdin.
   - `leo_path` stays unquoted so the remote shell expands `$HOME`, as `config.DefaultRemoteLeoPath` already relies on.
   - openrsync has no `--protect-args`, so every rsync path must match `^/[A-Za-z0-9._@%+=,/-]+$`; otherwise `unsafe_path`.
7. `leo_agent_copy` is gated by `perms.AllowsSpawn(template)` (`internal/mcp/tools.go:245`). `leo_agent_move` is ungated like `leo_stop_agent` (`:317`).
8. The wire status stays `stopped`; an additive `moved_to` field carries the marker.

## 1. Existing code (cited)

- **Record and store:**
  - `agentstore.Record` is at `internal/agentstore/store.go:37-225`.
  - `Save` overwrites everything except the fields it preserves (`:252-265`); `Update` is atomic (`:350`).
  - The store is written 0600 because env can hold tokens (`:461-472`).
- **Lifecycle (`internal/agent/manager.go`):**
  - `Stop` keeps the record, setting `Stopped` and `WakeOnMessage` (`:1148-1215`).
  - `Start` (`:1241-1338`) is the wake chokepoint: `ResumeIDFor` (`resume.go:45`; newest jsonl unless pinned by `SessionPinnedAt`) → `resolveRestartArgs` (`:1577`) / `resolveTemplateWiring` (`:1661`, which composes env fresh when `rec.Env==nil`) → `SpawnAgent`.
  - Must also refuse moved agents: `Restart` (`:1444`), `Reset` (`:1340`), `SwitchTemplate` (`switch.go:70`), `SetEnvironments` (`environments.go:66`) and `Rename` (`:2006`).
- **Wake and delivery paths:**
  - `Wakeable` / `Stopped` (`:576`/`:553`) feed the persistent-task ensurer (`internal/daemon/ensure.go:76-83`).
  - Web message wake: `internal/web/agent_control_core.go:174,199`.
  - Chat delivery calls `Start` directly (`internal/web/handlers_chat.go:147`).
  - Boot restore skips stopped agents at `internal/service/agents.go:127`.
  - Attach auto-start goes through `daemon.AgentStart` (`internal/daemon/client_agents.go:127`).
  - List and stopped status: `manager.go:1079,194`; CLI header at `internal/cli/agent.go:177`.
- **SSH:**
  - `buildSSHArgs` is at `internal/cli/agent.go:111`, `sshControlOpts` at `internal/cli/ssh.go:22`.
  - `runRemoteGroup` doesn't quote subcommand args (`agent.go:131`).
  - Completion quotes its token with `shellQuoteArg` (`internal/cli/tmux.go:408`); `remoteShellTarget` (`:421`) exists because of zsh `=` expansion.
  - `ResolveHost` step 4 (`internal/config/client.go:39`) makes the first `client.hosts` entry the default.
  - `HostConfig` is at `internal/config/config.go:139`.
- **Harnesses:**
  - `SessionMode` is at `internal/harness/harness.go:27`, `Harness` at `:92`, `SessionArgsRefresher` at `internal/harness/driver.go:98`.
  - codex: `resume <id>` comes from `codex.go:73`. The refresher at `driver.go:55` strips only a leading `resume`. Discovery walks `$CODEX_HOME/sessions` matching `session_meta.cwd` (`:89`). `CodexHome` is at `hooks.go:40`.
  - opencode: `-s <id>` comes from `opencode.go:70`; refresher at `driver.go:26`; discovery uses `session list --format json` filtered by directory (`:73`).
  - claude: the slug replaces `/` and `.` with `-` (`slug.go:15-21`). Sidecars are `projects/<slug>/<sid>/`, `file-history/<sid>/` and `session-env/<sid>`. Memory is `projects/<slug>/memory/`.
- **Destination queries:**
  - The existing socket routes `GET /templates`, `GET /environments` (`internal/daemon/server.go:306-307`) and `GET /drain/busy` (`:321`, backed by `drain.Prober.Probe`, `internal/drain/deps.go:40`) don't cover the resolved session root, per-agent env resolution or record collisions. A new read-only preflight route is needed.
  - Config: `TemplateConfig` is at `internal/config/config.go:407`; `Config` at `:94-122`.

## 2. Architecture

```
leo agent move X --to laptop → local daemon POST /agents/X/move {to,force} (202, job; CLI polls GET /moves/{id})
  agentmove.Job (move-lock held):
    ssh … <leo_path> agent move-receive preflight   (stdin manifest → JSON)
    idle check → stop → pre_send → harness PrepareSend → rsync ×3
    ssh … <leo_path> agent move-receive commit      → dest daemon POST /agents/move/receive:
         FinishReceive → post_receive → record → Start → confirm
    mark source moved_to
```

- The new package `internal/agentmove` holds:
  - `manifest.go`
  - `transport.go` (the `Endpoint` interface, with Local and SSH implementations)
  - `rsync.go` (pure argv builders and path validation)
  - `hooks.go`
  - `preflight.go` (the pure destination check)
  - `job.go`
- Harness storage knowledge lives in `internal/harness/<h>/move.go` behind optional interfaces.
- **Manifest (protocol 1):** `{protocol, mode, from:{hostname,home,user}, agent:<portable record>, source_session_id, last_move, staging_dir, copy?:{name,workspace,branch,session_id}}`.
  - **Sent** portable-record fields: name, template, repo, workspace, harness, session_id, sessions_by_template, environments, spawn_env, inherited_env, env_layered, idle_suspend_after, appearance, move_log.
  - **Not sent:** claude_args, env, web_port, attention_token, claimed_session, opening_*, no_resume. The destination rebuilds env from its own config.
- **Preflight response:** `{protocol, hostname, home, user, leo_version, ok, problems:[{code,message}], session_root, staging_dir, existing:{present,moved_to,live}}`.
  - Problem codes: protocol_mismatch, home_mismatch, user_mismatch, template_missing, environment_missing, harness_mismatch, harness_binary_missing, session_root_missing, name_taken, workspace_in_use, workspace_exists, unsafe_path.

## 3. Transfer

Paths are identical on both hosts, except:
- the **session root**, which the destination resolves from its own template plus environments and reports in preflight;
- **staging:** `<state>/moves/<agent>/staging/{hooks/,harness/}` on each side.

The rsync transport is `-e "ssh -o BatchMode=yes [ssh_args] -o ControlMaster=auto -o ControlPath=<state>/remotes/<host>.ctl"`. rsync splits `-e` on whitespace, so the move refuses `ssh_args` or a ControlPath containing whitespace or quotes.

| Class | Command | Semantics |
|---|---|---|
| Workspace | `rsync -a --delete --itemize-changes --stats [--exclude=P…] <ws>/ <host>:<ws>/` | The source is authoritative. Only changed files cross. Excluded paths are never deleted on the destination. |
| Session files | `rsync -a -R -u --itemize-changes <root>/./<rel>… <host>:<dest_root>/` | `--update`, no delete. `-R` with `/./` maps between different roots. |
| Staging | `rsync -a --delete <stage>/ <host>:<dest_stage>/` | Cleared before pre_send. |

Workspace excludes:
1. Global `move.exclude`, then the template's (source config).
2. Auto `/.worktrees/`.
3. Auto: other local records' workspaces nested under this one.

`.git` is included (plain repos only).

Session file sets:
- **claude:**
  - Sent: `projects/<slug>/`, covering all transcripts, sidecars and `memory/`.
  - Sent: `file-history/<sid>/` for SessionID plus the values in SessionsByTemplate.
  - Not sent: session-env, todos, shell-snapshots.
- **codex:**
  - Sent: `sessions/**/rollout-*-<id>.jsonl*` for each known id.
  - Not sent: the global sqlite DBs; read-repair fixes the index.
- **opencode:**
  - No files. `PrepareSend` runs `opencode export <id>` (agent env, `cmd.Dir=ws`) into `staging/harness/opencode/<id>.json`.
  - `FinishReceive` runs `opencode import <file>` with the destination env and `cmd.Dir=workspace`.

Copy differs:
- The workspace goes to `<newws>.leo-partial/` and is renamed at commit.
- claude: the transcript snapshot is **truncated after the last `assistant` line with `stop_reason=="end_turn"`** and sent through staging.
  - On the destination it goes to `projects/<newslug>/<sid>.jsonl`, with its sidecar dir.
  - `memory/` seeds the new slug's memory only if that dir is absent.
- codex: as in a move.
- opencode: import with `cmd.Dir=<newws>`. A **local copy skips the import**, which would otherwise rewrite the source session's directory.

## 4. `moved_to` marker

New `agentstore.Record` fields (omitempty):
- `MovedTo string`: the `client.hosts` key on the source, or the source's hostname after a failed receive.
- `MovedAt *time.Time`.
- `MoveLog map[string]time.Time`: keyed by peer hostname; feeds `LEO_MOVE_LAST`.
- `ForkFromSession string`: copy only, consumed on first launch.

A moved record is always `Stopped=true, WakeOnMessage=false`.

| Surface | Moved agent |
|---|---|
| Start (CLI/web/socket, attach auto-start, chat delivery) | `*agent.MovedError` / `ErrAgentMoved`; code `agent_moved`. Message: "agent X moved to laptop on T; use `leo --host laptop agent attach X`, or move it back with `leo --host laptop agent move X --to <this>`". |
| Restart / Reset / SwitchTemplate / SetEnvironments / Rename | Same error. |
| Wakeable | false. The web message path returns 409 `agent_moved` instead of 404. |
| Persistent ensure (`ensure.go:82`) | Checks `Moved()` first: "moved to laptop; task delivery refused on this host". |
| Boot restore | Explicit `MovedTo` guard, so a failed-restore retry never fires. |
| `leo agent list` | `moved→laptop`. JSON and `/api/v1` keep `status:"stopped"` and add `moved_to`. |
| Web UI | A badge on the row; start/restart controls disabled. |
| Delete | Allowed (removes the local shadow). |
| Receive with an existing name | Accepted only if the local record has `MovedTo` set (move-back clears it). Otherwise `name_taken`. |

There is also a transient in-memory lock, `Manager.BeginMove(name, mode)`:
- In **move** mode, `Start` returns `ErrAgentMoving`, so nothing wakes the source between stop and mark.
- In **copy** mode it blocks only another move or copy.
- A daemon crash drops the lock, which leaves the spec's "stopped, unmarked" state.

## 5. Hooks contract

Global `move:` block, with the same shape under `templates.<t>.move:` (also in `templates_file`):
```yaml
move:
  exclude: [node_modules/, .build/]
  hook_timeout: 10m
  hooks:
    pre_send:     [~/bin/claude-mem-export.sh]
    post_receive: [~/bin/claude-mem-import.sh]
```
- Lists concatenate, global first, then the template's.
- `pre_send` and `exclude` come from the source config; `post_receive` from the destination config.
- Each entry is an executable path. `~/` is expanded and the result must be absolute. Entries are exec'd directly (no shell, no args).
- The working dir is the workspace (a copy's new workspace on `post_receive`).
- Env: the agent's resolved env on that host, minus `LEO_API_TOKEN` and `OPENCODE_CONFIG_CONTENT`, plus:
  - `LEO_AGENT`, `LEO_WORKSPACE`, `LEO_SESSION_ID`: on a copy's `post_receive`, the new name and workspace. The session id is the new one for claude and the source's for codex/opencode.
  - `LEO_MOVE_FROM`, `LEO_MOVE_TO`: hostnames.
  - `LEO_MOVE_DIR`: staging `hooks/`.
  - `LEO_MOVE_LAST`: RFC 3339; empty on the first move and always empty for a copy.
  - `LEO_MOVE_MODE`.
  - `LEO_SOURCE_AGENT`, `LEO_SOURCE_WORKSPACE`, `LEO_SOURCE_SESSION_ID`.
- Failure: a nonzero exit, spawn error or timeout aborts the move. The error carries the path, status and last 4 KiB of output. Hooks run sequentially, stop at the first failure and are never retried.

## 6. Failure and rollback (move)

| # | Stage | On failure |
|---|---|---|
| P0 | Local validation and lock: exists, not moved, not a worktree, adapter present, paths safe, `--to` ≠ localhost | Nothing touched. |
| P1 | Remote preflight (`BatchMode`) | Nothing touched; problems listed, Decision 1 included. |
| P2 | Idle check (`Prober.Probe().Busy`; `--force` skips) | Nothing touched. |
| S1 | Stop the source (WakeOnMessage=false) | Error returned, lock released. |
| S2–S4 | Final session id (ResumeIDFor after stop) → staging + pre_send → PrepareSend | The source is **stopped and unmarked**; the error says "`leo agent start X` recovers it". |
| S5 | rsync workspace, session files, staging | Same as S2. A partial rsync is harmless (no live destination record; a shadow refuses start). Re-running is incremental and idempotent. |
| S6 | Commit: FinishReceive → post_receive → record (`SessionPinnedAt=now`) → Start → confirm running and not quick-exited within `startConfirm` (15s, injected clock) | The destination rewrites its record as `Stopped, MovedTo=<source>`; the source stays unmarked; the destination's error is shown. If the outcome is unknown, `move-receive status`: live → continue to S7; unreachable → leave unmarked and warn "check `leo --host laptop agent list` first". |
| S7 | Mark the source (`Update`, 3 retries) | The destination is live and the source unmarked. The error says "X is live on laptop; do not start it here". Accepted risk. |

Copy:
- The source is never stopped.
- A failure before commit leaves `<newws>.leo-partial`, which the next run reuses.
- A start failure leaves the record `Stopped`, `StoppedReason="copy start failed"`, so `leo agent start` recovers it.

## 7. Steps

Global rules:
- Test-first. Fakes only for exec, the supervisor and the clock.
- **Every exec-seam test asserts the exact argv, `cmd.Dir` and key env.**
- New test packages get the testenv `TestMain`.
- Full `make test`, `make lint` and `make e2e` run only at step 12.

1. **Record fields.** File: `internal/agentstore/store.go`.
   - RED: `TestRecordMoveFieldsRoundTrip`, `TestLegacyRecordDecodesWithoutMoveFields`.
   - Verify: `go test -race -run Move ./internal/agentstore/`
2. **Guards and lock.** Files: `internal/agent/{errors.go,manager.go,switch.go,environments.go,move.go}`.
   - Adds `ErrAgentMoved`, `MovedError`, `ErrAgentMoving`, `Moved`, `BeginMove`, `MarkMoved`, and `MovedTo` in List.
   - RED: `TestMovedAgentRefusesLifecycle`. It is table-driven over Start, Restart, Reset, SwitchTemplate, SetEnvironments and Rename, and asserts no `SpawnAgent` call.
   - Also: `TestStartRefusedWhileMoveLockHeld`, `TestCopyLockDoesNotBlockStart`, `TestWakeableFalseForMoved`.
   - Verify: `go test -race -run 'Moved|MoveLock' ./internal/agent/`
3. **Surfaces.** Files: `daemon/ensure.go`, `daemon/handlers_agents.go` (code `agent_moved`), `web/agent_control_core.go`, `web/handlers_chat.go`, `observe/snapshot.go`, `service/agents.go`, `cli/agent.go` list, web row template.
   - RED: `TestEnsureRefusesMovedAgent`, `TestMessageToMovedAgent409`, `TestChatDeliveryToMovedAgentFails`, `TestRestoreSkipsMovedFailedRestore`, `TestAgentListShowsMovedTo`.
   - Verify: `go test -race -run Moved ./internal/daemon/ ./internal/web/ ./internal/service/ ./internal/cli/`
4. **Config.** Files: `MoveConfig` on `Config` and `TemplateConfig`; validation; `templates_file` unknown-key rules (#305); web schema (yaml-only if the form can't render it).
   - RED: `TestMoveConfigValidate`, `TestTemplateMoveMergesGlobal`, `TestTemplatesFileAcceptsMove`.
   - Verify: `go test -race -run Move ./internal/config/ ./internal/web/...`
5. **Harness adapters.** Files: `harness.go` interfaces; `{claude,codex,opencode}/move.go`; `codex/driver.go:55,69`; `opencode/driver.go:26,51`.
   - Interfaces:
     - `SessionMover{SessionRoot(env); SessionFiles(root, ws, ids); PrepareSend(ctx, sc, stage); FinishReceive(ctx, sc, stage)}`.
     - `Forker{ForkArgs(args, src, new)}`.
   - Fork argv:
     - claude: `--resume src --fork-session --session-id new`.
     - codex: `fork src` prefix.
     - opencode: `-s src --fork`.
   - Refreshers strip fork tokens once `storedID != ""`.
   - RED, claude: `TestClaudeSessionFilesIncludesProjectDirAndFileHistory`, `TestClaudeTruncateAtLastEndTurn`, `TestClaudeForkArgs`.
   - RED, codex: `TestCodexSessionFilesFindsRolloutByID`, `TestCodexForkArgs`, `TestCodexRefreshStripsForkAfterDiscovery` (`[fork S a b]`+N → `[resume N a b]`).
   - RED, opencode: `TestOpencodeExportArgv` (argv, `Dir==ws`, `XDG_DATA_HOME`), `TestOpencodeImportArgv`, `TestOpencodeLocalCopySkipsImport`, `TestOpencodeRefreshStripsFork`.
   - Verify: `go test -race -run 'Move|Fork|Truncate|Refresh' ./internal/harness/...`
6. **Transport.**
   - Extract `buildSSHArgs`/`sshControlOpts` into `internal/remote` (`SSHArgv(res, batch, tail...)`); the CLI delegates (refactor).
   - `agentmove/transport.go` and `rsync.go` implement `Endpoint.Run(ctx, verb, stdin)`, the three argv builders and `ValidatePath`.
   - RED:
     - `TestSSHArgvRemoteVerb`: exact argv ending `"user@host","$HOME/.local/bin/leo","agent","move-receive","preflight"`, with no manifest bytes.
     - `TestRsyncWorkspaceArgv`, `TestRsyncSessionArgvRelative`.
     - `TestValidatePathRejectsShellMeta` (space, quotes, `$`, backtick, `;`, `*`, newline).
     - `TestSSHArgsWithWhitespaceRefused`.
   - Verify: `go test -race ./internal/remote/ ./internal/agentmove/ -run 'Argv|Validate' && go test -race ./internal/cli/`
7. **Hooks.** File: `agentmove/hooks.go`.
   - RED: `TestHooksEnvAndCwd` (a real temp script records its env; `LEO_API_TOKEN` is absent), `TestHookNonzeroAborts`, `TestHookTimeout` (injected timeout and a script blocked on `read`, no sleeps), `TestHooksStopAtFirstFailure`.
   - Verify: `go test -race -run Hook ./internal/agentmove/`
8. **Destination side.** Files:
   - `agentmove/preflight.go`: pure `CheckDestination`.
   - `agent/receive.go`: `Manager.Receive`.
   - Socket routes `POST /agents/move/preflight`, `POST /agents/move/receive`, `GET /agents/move/status`.
   - Hidden `leo agent move-receive {preflight|commit|status}`: stdin → socket → stdout JSON, with errors as `{error,message}` and a nonzero exit.
   - RED: a `CheckDestination` table test covering every problem code, plus shadow acceptance.
   - RED: `TestReceiveDropsHostSpecificFields`, `TestReceiveRebuildsEnvFromDestConfig`, `TestReceiveResumesSentSessionID`, `TestReceiveStartFailureLeavesMovedShadow`, `TestReceiveCopyUsesForkArgsOnce`, `TestReceiveCopyCreatesBranch` (real `git init` in a temp dir).
   - Verify: `go test -race -run 'Receive|CheckDestination|MoveRoute' ./internal/agent/ ./internal/agentmove/ ./internal/daemon/ ./internal/cli/`
9. **Source orchestrator.**
   - `agentmove/job.go`: `Mover` with deps (Manager iface, Endpoint factory, Runner, Hooks, Prober, Clock), plus the job registry, `POST /agents/{name}/move` and `GET /moves/{id}`.
   - RED:
     - `TestMoveHappyPathOrder`: ordered verbs and argv.
     - Injection at P0–S7, asserting source state, lock release and recovery text.
     - `TestMoveBusyRefusedWithoutForce`, `TestMoveUnknownCommitQueriesStatus`, `TestMoveRefusesWorktreeAgent`, `TestOneJobPerAgent`.
   - Verify: `go test -race -run Move ./internal/agentmove/ ./internal/daemon/`
10. **Copy.**
    - Naming `-copy`, `-copy-2`, unique across both hosts.
    - `.leo-partial` workspace plus rename.
    - Branch `<cur>-<new>` with a `--branch` override. A detached HEAD needs `--branch`; non-git workspaces skip the branch.
    - Local copy uses the Local endpoint.
    - codex/opencode copy is refused while the source is busy.
    - RED: `TestCopyNameSuffixes`, `TestCopyWorkspacePartialRename`, `TestCopyMidTurnClaudeTruncates`, `TestCopyMidTurnCodexRefused`, `TestLocalCopyNoSSH`, `TestCopyNotWiredToPersistentTask`.
    - Verify: `go test -race -run Copy ./internal/agentmove/ ./internal/agent/`
11. **Surfaces.**
    - `cli/agent_move.go`: `move --to [--force] [--json]` and `copy [--to] [--name] [--branch] [--json]`; polls the job with stage lines and host completion.
    - Daemon client; web `POST /api/agent/{name}/move`.
    - MCP `leo_agent_move` / `leo_agent_copy` (AllowsSpawn gate), which return the job id immediately.
    - Docs: `docs/configuration/agent-move.md`.
    - RED: `TestAgentMoveFlagsToRequest`, `TestAgentCopyJSONOutput`, `TestMCPAgentCopyRequiresSpawnPerm`. TDD exception, noted: the cobra wiring.
    - Verify: `go test -race -run 'AgentMove|AgentCopy|MCPAgent' ./internal/cli/ ./internal/mcp/ ./internal/web/`
12. **E2E and gate.** `e2e/agent_move_test.go`:
    - Two isolated daemons (separate `LEO_HOME` and `TMUX_TMPDIR`) with fakeclaude.
    - A PATH-prepended fake `ssh` (`e2e/testdata/fakessh`) parses `-o`, `-p`, `-T` and the host, then runs `sh -c` with the peer env. rsync's `-e ssh` hits the same shim.
    - The workspace rsync is a self-sync, so this covers the control plane: preflight, stop, hooks, commit, `moved_to`, refusal, move-back. Copy to localhost runs fully.
    - The shim logs argv; the test asserts no manifest bytes appear.
    - Gate:
      1. Check `uptime`.
      2. `make test && make lint`.
      3. `heavy env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e E2E_RUN=AgentMove` (background), then the full `make e2e` once.
13. **Live check** (§8).

## 8. Live acceptance, Dionysus ↔ laptop 10.0.4.163

Manual setup (Evan):
1. **Laptop:**
   - Remote Login on.
   - Dionysus's `id_ed25519.pub` in `authorized_keys`.
   - Confirm the Leo.app CLI path (expected `~/.leo/app/bin/leo`) and a version that includes this feature.
   - `$HOME` is `/Users/evan`.
2. **Dionysus `leo.yaml`:** `client: {default_host: localhost, hosts: {laptop: {ssh: evan@10.0.4.163, leo_path: /Users/evan/.leo/app/bin/leo}}}`. `default_host: localhost` is **required**.
3. **Laptop `leo.yaml`:** `client: {default_host: localhost, hosts: {dionysus: {ssh: evan@10.0.2.10}}}`, plus a key.
4. **Templates:** a `move-test` template on both hosts (claude, workspace `~/.leo/agents/move-test`, same `environments:`). On the laptop, the environment's `CLAUDE_CONFIG_DIR` exists and is logged in. Add `move-test-codex` / `move-test-oc` for those adapters.
5. **Smoke:** `ssh -o BatchMode=yes evan@10.0.4.163 /Users/evan/.leo/app/bin/leo version`.

Checks:
1. On Dionysus: spawn, `git init`, leave an uncommitted and an untracked file. Turn: "remember heliotrope".
2. `leo agent move move-test --to laptop`. On the laptop it is running, recalls heliotrope, and `git status` matches.
3. On Dionysus, `start` and `attach` both give `agent_moved` naming laptop, and `list` shows `moved→laptop`.
4. On the laptop: edit, take a turn, `move --to dionysus`. `--itemize-changes` shows only the edit, the laptop turn is present, and the laptop record is moved_to.
5. `pre_send: [/usr/bin/false]` fails the move with the hook named. The agent is stopped and unmarked, and `start` recovers it.
6. `copy --to laptop`: both run, on separate branches with separate session ids, and turns don't cross.
7. codex/opencode: repeat checks 1–3 and 6. If codex resume fails, unregister its SessionMover and record why.

## 9. Deferred and accepted risks

Deferred:
- Worktree agents.
- codex/opencode copy while mid-turn.
- Different homes.
- `.leo-partial` cleanup.
- A web UI move button.
- The `runRemoteGroup` quoting bug (`internal/cli/agent.go:131`).
- An `agent_moved` observe event.

Accepted risks:
- **codex:** the verdict comes from source, and a fork in a new cwd may hit a directory prompt.
- **opencode:** revert deletions don't propagate.
- **Transport:**
  - openrsync ↔ GNU rsync interop is untested.
  - The daemon's ssh auth under launchd may differ from the shell.
- **Crash or ambiguity:** a crash or unknown outcome between commit and mark; the S7 write failure.
- **Shared workspaces:** a shared workspace is copied whole, and preflight refuses `workspace_in_use`.
- **Persistent tasks:** the source cron fails while the agent is away.
