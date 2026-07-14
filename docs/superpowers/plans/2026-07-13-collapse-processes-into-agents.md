# Collapse "processes" into "agents" — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire Leo's "process" primitive entirely so that every supervised harness instance is an "agent"; long-lived assistants become templates spawned once and auto-restored on boot.

**Architecture:** This is a **pure deletion** (plus one setup-flow change). Processes and agents already run through the same `superviseProcess()` engine parameterized by `ProcessSpec.Kind`; we remove the `KindProcess` arm, the `processes:` config section, and every process-named surface (CLI, web, daemon routes, docs), keeping the daemon, templates, and the whole agent lifecycle. No migration code — the sole user re-spawns long-lived agents by hand.

**Tech Stack:** Go 1.x, cobra CLI, gopkg.in/yaml.v3, htmx + `html/template` web UI, tmux supervision, robfig/cron.

## Global Constraints

- **Keep the build green at every commit.** Go compiles per-package and fails on any dangling reference. Tasks are ordered so that consumers of `config.ProcessConfig`/`Config.Processes` are removed *before* the types themselves (Task 8). Verify `go build ./...` before every commit.
- **Adapted TDD for deletion.** Most tasks delete code — there is no RED step for a deletion. For those, the verification is: `go build ./...` compiles, `make test` passes (with the listed process-only tests removed/updated), and any new-behavior assertions pass. Genuine new behavior (Task 9 setup spawn) uses full RED→GREEN TDD.
- **Shared symbols survive.** `agent.ProcessState`, `Supervisor.states`, `Supervisor.States()`, `EphemeralAgents()`, `SpawnAgent`, `superviseProcess`, `KindAgent`, `KindTask`, `KindSession` are used by the **agent** path and MUST NOT be deleted. Only *process-named* surfaces and the `KindProcess` value go.
- **No migration, no autostart flag, no boot-seed.** A stray `processes:` block in a live `leo.yaml` is silently ignored (loader uses `yaml.Unmarshal`, non-strict) — do not add handling for it.
- **`stale_resume_hours` is NOT salvaged** onto templates (see spec). Do not add it to `TemplateConfig`.
- **Run `make e2e` before the final commit** — config/argv changes require it (the e2e suite is behind a build tag and skipped by `go test ./...`).
- Line numbers below are anchors from a snapshot; they drift as edits land. **Match on symbol names, not line numbers.**

Governing spec: `docs/superpowers/specs/2026-07-13-collapse-processes-into-agents-design.md`.

---

## File Map (what changes)

**Delete outright**
- `internal/cli/process.go`
- `internal/cli/process_test.go`, `internal/cli/process_args_test.go`, `internal/service/process_cmd_test.go`
- `internal/web/templates/pages/processes.html`, `internal/web/templates/pages/process_edit.html`, `internal/web/templates/partials/processes.html`
- `docs/cli/process.md`

**Modify**
- `internal/config/config.go`, `internal/config/harness.go`, `internal/config/session.go`
- `internal/run/persistent.go`
- `internal/cli/root.go`, `internal/cli/service.go`, `internal/cli/agent.go`, `internal/cli/status.go`, `internal/cli/validate.go`, `internal/cli/startup_warnings.go`, `internal/cli/attach.go`, `internal/cli/attach_picker.go`
- `internal/service/process.go`
- `internal/harness/harness.go`, `internal/harness/claude/*.go`, `internal/harness/codex/codex.go`, `internal/harness/opencode/opencode.go`
- `internal/web/web.go`, `internal/web/handlers.go`, `internal/web/handlers_pages.go`, `internal/web/handlers_config.go`, `internal/web/handlers_harness.go`, `internal/web/schema/schema.go`, `internal/web/schema/registry.go`, `internal/web/templates/layout.html`
- `internal/daemon/server.go`
- `internal/setup/setup.go`
- Many `*_test.go` (listed per task)
- `CLAUDE.md` and `docs/**`

---

### Task 1: Drop persistent-task Topology C (`session: process:<name>`)

Removes the one genuinely cross-cutting semantic dependency first, while `Config.Processes` still exists (so this is a clean consumer removal that keeps compiling).

**Files:**
- Modify: `internal/config/session.go` (remove the `process:` topology case + `TopologyProcess` value + doc comment ~line 29)
- Modify: `internal/run/persistent.go:29` (remove the `if topo == config.TopologyProcess { … }` branch)
- Test: `internal/config/session_test.go` (remove the Topology C case ~line 108), `internal/run/persistent_test.go` (remove ~line 209), `internal/config/migration_test.go` (drop process-topology refs if any)

**Interfaces:**
- Consumes: `config.ResolveSession`, `config.SessionTopology` (existing).
- Produces: `SessionTopology` no longer has a `TopologyProcess` member; `ResolveSession` no longer accepts a `process:` prefix.

- [ ] **Step 1: Read the current topology code.** Open `internal/config/session.go`; find `type SessionTopology`, the `TopologyProcess` const, and the `case strings.HasPrefix(task.Session, "process:")` block in `ResolveSession`. Read `internal/run/persistent.go` around line 29 for the consumer.

- [ ] **Step 2: Update the tests first (they encode the removed behavior).** In `internal/config/session_test.go`, delete the test case that asserts `session: process:<name>` resolves to `TopologyProcess`. In `internal/run/persistent_test.go`, delete the case at ~209. Add/keep an assertion that a `process:`-prefixed session is now treated as an ordinary/unknown session (matching how any unknown prefix is handled — verify the existing fallback and assert that).

- [ ] **Step 3: Run the tests to see the old ones gone / new one drive.**
Run: `go test ./internal/config/ ./internal/run/ -run 'Session|Topology|Persistent' -v`
Expected: compile error or FAIL referencing `TopologyProcess` (proves the symbol is still there).

- [ ] **Step 4: Remove the topology.** Delete the `TopologyProcess` const and the `process:` case in `ResolveSession` (`session.go`). Delete the `TopologyProcess` branch in `persistent.go`. Update the doc comment that references processes.

- [ ] **Step 5: Verify green.**
Run: `go build ./... && go test ./internal/config/ ./internal/run/`
Expected: PASS.

- [ ] **Step 6: Commit.**
```bash
git add internal/config/session.go internal/run/persistent.go internal/config/session_test.go internal/run/persistent_test.go internal/config/migration_test.go
git commit -m "refactor: drop session: process:<name> topology (persistent tasks)"
```

---

### Task 2: Delete the web process surface

Removes all `/process*` routes, handlers, pages, schema section, and the daemon's process-state routes. Keeps agent state plumbing intact.

**Files:**
- Modify: `internal/web/web.go` — remove routes: `GET /processes` (238), `GET /processes/{name}` (239), `GET /partials/processes` (249), `POST /web/config/process/{name}` (264), `POST /web/process/add` (268), `DELETE /web/process/{name}` (269), `POST /web/process/{name}/restart` (template-only, no MCP caller — delete). **CORRECTION (found in review): `POST /web/process/{name}/{send,interrupt,message}` are NOT dead — they back the MCP tools `leo` send-keys / `leo_interrupt` / `leo_send_message` via `internal/mcp/client.go`. Do NOT delete their logic; RENAME the routes to `POST /web/agent/{name}/{send,interrupt,message}` (matching the existing `/web/agent/{name}/stop|suspend|resume|rename`) and rename the handlers to `handleWebAgent{SendKeys,Interrupt,Message}`. Then update `internal/mcp/client.go` (`sendKeys`→`/web/agent/<n>/send`, `interrupt`→`/web/agent/<n>/interrupt`, `sendMessage`→`/web/agent/<n>/message`) and the MCP tests (`internal/mcp/client_test.go`, `internal/mcp/server_test.go`) that assert the old `/web/process/...` paths.** Remove `ProcessStateProvider` field/param only if it is exclusively process-facing; if it also feeds `/agents`, keep it and rename usages away from "process" as needed — verify before deleting.
- Modify: `internal/web/handlers.go` — delete `handlePartialProcesses` (60), `handleProcessInterrupt` (531), `handleProcessRestart` (553), `handleProcessSendKeys` (572), `handleProcessMessage` (616), `handleProcessAdd` (1022–1063), `handleProcessDelete` (1068–1093).
- Modify: `internal/web/handlers_pages.go` — delete `processRow` (173–184), `processesPageData` (186–191), `buildProcessesData` (196–216), `handleProcessEditPage` (443+); remove `DashboardData.ProcessCount` (48) and its setter (84).
- Modify: `internal/web/handlers_config.go` — delete `case *config.ProcessConfig:` (123–124) and `handleConfigProcessSave` (247–271).
- Modify: `internal/web/handlers_harness.go:97` — remove the `cfg.Processes[name]` lookup in `locateHarnessScope`.
- Modify: `internal/web/schema/schema.go` — remove `SectionProcess` const (37), its entry in the sections list (49), and the `case SectionProcess: return reflect.TypeOf(config.ProcessConfig{})` (60–61).
- Modify: `internal/web/schema/registry.go` — remove the `SectionProcess` deprecated-key list (34–36) and the `SectionProcess` field registry (88–100).
- Delete: `internal/web/templates/pages/processes.html`, `internal/web/templates/pages/process_edit.html`, `internal/web/templates/partials/processes.html`.
- Modify: `internal/web/templates/layout.html` — remove the Processes nav item (17) and the `{{else if eq .Page "processes"}}` dispatch (35). Grep `partials/status.html`, `pages/service.html`, `pages/config_defaults.html`, `partials/agents.html` for `process` and clean any process links/labels.
- Modify: `internal/daemon/server.go` — remove `GET /process/list` route (108) + `handleProcessList` (357) + `processAdapter` (190–205) + the `ProcessStateProvider` interface (23–27) and its `New(...)` param (60,72) **only if not reused by the agent state feed**; keep `ProcessStateInfo = agent.ProcessState` alias if the agent path uses it. Verify with grep before deleting each.
- Test: `internal/web/handlers_config_test.go`, `internal/web/handlers_harness_test.go`, `internal/web/handlers_harness_badges_test.go`, `internal/web/schema/values_test.go` — remove process cases (see line refs in the deletion map). Keep agent-related assertions.

**Interfaces:**
- Consumes: `config.ProcessConfig`, `config.Config.Processes` (still defined until Task 8).
- Produces: no web route, handler, template, or schema section references processes. `schema.Section` no longer has `SectionProcess`.

- [ ] **Step 1: Inventory reuse before deleting shared symbols.**
Run: `grep -rn "ProcessStateProvider\|ProcessStateInfo\|processAdapter\|States()" internal/daemon internal/web internal/service | grep -v _test.go`
Decide per symbol: process-only → delete; also feeds `/agents` → keep, and only drop the process-named route/handler. Note the decision inline.

- [ ] **Step 2: Delete templates and their references.** Remove the three process `.html` files; remove the nav item + dispatch in `layout.html`; clean stray `process` mentions in the other partials/pages found by:
Run: `grep -rn "process" internal/web/templates/`

- [ ] **Step 3: Delete web handlers, pages, config, schema.** Apply all `internal/web/**` modifications above.

- [ ] **Step 4: Delete daemon process routes** (per Step 1 decisions) in `internal/daemon/server.go`.

- [ ] **Step 5: Update web/daemon tests.** Remove the process cases from the four test files listed; keep everything agent-related.

- [ ] **Step 6: Verify green.**
Run: `go build ./... && go test ./internal/web/... ./internal/daemon/...`
Expected: PASS. (`config.ProcessConfig`/`Processes` still compile because they still exist.)

- [ ] **Step 7: Commit.**
```bash
git add internal/web internal/daemon
git commit -m "refactor: remove web + daemon process surface (routes, pages, schema)"
```

---

### Task 3: Delete the CLI process surface

Removes `leo process*`, the process arms of `leo service`, and every `cfg.Processes` read in CLI. `service.RunSupervised` still takes a `[]ProcessSpec` param at this point — pass an empty slice; the param is removed in Task 4.

**Files:**
- Delete: `internal/cli/process.go` (whole file). Before deleting, grep for `saveConfig` and `splitAndTrim` (defined at its tail) — if used elsewhere, move them to a surviving file (e.g. a small `internal/cli/configio.go`); otherwise delete with the file.
- Modify: `internal/cli/root.go:50` — remove `newProcessCmd()` from `AddCommand`.
- Modify: `internal/cli/service.go` — delete `processEnviron` (154–166), `resolveProcess` (169–197), `buildAllProcessSpecs` (203–241), `mergeChannelsIntoEnv` (294–306), `processLeoMCPEnv` (340–350), `resolveProcessLaunch` (357–409), `buildProcessArgs` (420–437), `completeProcessNames` (687–702). In `runService` (67–149): remove the foreground `resolveProcess` arm and the supervised `buildAllProcessSpecs` arm so it just runs the daemon; call `service.RunSupervised(claudePath, nil, sessionSpecs, home, configPath, webToken)` (empty processes). `resolveSessionState` (243–288) becomes dead once its only callers (136,221) are gone — delete it too (and `mergeHarnessEnv`/`resolveSessionState` helpers only if now unused; grep first — `mergeHarnessEnv` may be used by the agent path).
- Modify: `internal/cli/agent.go:28` — delete `processSessionName` (`"leo-"+name`) once its callers (attach/picker) are gone in this task.
- Modify: `internal/cli/status.go` — remove `report.Processes.*` counts (142–146), the print line (296), and `fetchProcessStates`/`report.ProcessStates` process bits.
- Modify: `internal/cli/validate.go` — remove the process workspace check (127–131), process MCP-config check (147–158), and the process loop in `referencedHarnesses` (209–211).
- Modify: `internal/cli/startup_warnings.go` — remove the process workspace warning (33–41) and process MCP-config warning (56–72).
- Modify: `internal/cli/attach.go` — remove the `_, isProcess := cfg.Processes[name]` disambiguation (81+) and the collision message referencing `leo process attach`.
- Modify: `internal/cli/attach_picker.go` — remove `attachChoiceProcess` entries built from `cfg.Processes` (132–147).
- Delete: `internal/cli/process_test.go`, `internal/cli/process_args_test.go`.
- Test (update): `internal/cli/service_test.go` (drop `buildProcessArgs`/`buildAllProcessSpecs`/`resolveProcess`/`KindProcess` tests), `internal/cli/tmux_test.go` (`newAttachAliasTestConfig` process fixtures → agent fixtures), `internal/cli/attach_driver_test.go`, `internal/cli/attach_picker_test.go`, `internal/cli/startup_warnings_test.go`.

**Interfaces:**
- Consumes: `service.RunSupervised(claudePath string, processes []ProcessSpec, sessionSpecs []SessionSpec, home, configPath, webToken string) error` (unchanged this task; pass `nil` for processes).
- Produces: no `leo process` command; `leo service` runs the daemon only; no CLI file reads `cfg.Processes`.

- [ ] **Step 1: Guard shared helpers.**
Run: `grep -rn "saveConfig\|splitAndTrim\|mergeHarnessEnv\|resolveSessionState" internal/cli | grep -v _test.go`
Record which survive process.go/service.go deletion and where to relocate them.

- [ ] **Step 2: Delete process command + registration.** Remove `internal/cli/process.go` (relocating any shared helper per Step 1); remove `newProcessCmd()` from `root.go`.

- [ ] **Step 3: Strip process arms from `service.go`.** Delete the listed helpers; rewrite `runService` to run the daemon with `RunSupervised(claudePath, nil, sessionSpecs, …)`. Delete `resolveSessionState` if now unused.

- [ ] **Step 4: Strip `cfg.Processes` reads from the remaining CLI files** (status, validate, startup_warnings, attach, attach_picker, agent.go `processSessionName`).

- [ ] **Step 5: Update/delete CLI tests** per the list.

- [ ] **Step 6: Verify green.**
Run: `go build ./... && go test ./internal/cli/...`
Expected: PASS.

- [ ] **Step 7: Commit.**
```bash
git add internal/cli
git commit -m "refactor: remove leo process command + process arms of leo service"
```

---

### Task 4: Remove the process-spawning arm from the service supervisor

Excise the boot-time process loop from `defaultSupervisedExec` and drop the now-unused `processes` param from `RunSupervised`. Keep all agent/session supervision.

**Files:**
- Modify: `internal/service/process.go`:
  - `defaultSupervisedExec` (491–680): delete the workspace-validation loop (619–624) and the `for _, proc := range processes { registerIdentity; go superviseProcess(...) }` block (626–638); delete the `specsByName` build (572–575) and the `resolveProcessHandle` wiring (576–578); delete `resolveProcessHandle` (702–718) if only process-facing (grep — the web message-dispatch resolver may be replaced by an agent-facing one; if the agent path needs handle resolution, keep an agent version). Remove the `"supervising %d process(es)"` log (669) or reword to sessions/agents only.
  - `handleForSpec` (721–736): change the default `if kind == "" { kind = harness.KindProcess }` to `KindAgent` (or drop `Kind` from the handle entirely if unused after `KindProcess` removal — verify).
  - `RunSupervised` (487–489) and `defaultSupervisedExec` signatures: remove the `processes []ProcessSpec` parameter. Update the `supervisedExecFn` seam type accordingly.
  - `registerIdentity` (191–197): keep only if the session boot loop uses it; otherwise delete. Grep first.
  - `clearProcessSession` (1067–1083) `process:` session-store key (1077): reconcile — with processes and Topology C gone, this fallback is dead; delete `clearProcessSession` if unused, else repoint to the agent session store.
  - `LEO_PROCESS_NAME` exports: `processLeoMCPEnv` was deleted in Task 3; remove the `LEO_PROCESS_NAME` export in `buildClaudeShellCmd` (1165) — confirm the agent path exports its own identity var (`LEO_AGENT_NAME` or equivalent) so channels/MCP still identify the agent; if agents currently rely on `LEO_PROCESS_NAME`, rename the export to the agent var rather than deleting.
- Modify: `internal/cli/service.go` `runService` — drop the `nil` processes arg now that the param is gone.
- Test (update): `internal/service/session_test.go` (drop `Processes` fixture at ~196), `internal/service/hook_wiring_test.go` (KindProcess at 133,169,201), `internal/service/driver_helpers_test.go` (KindProcess at 55–56,78,81–82 → KindAgent), and any `supervisedExecFn` stub whose signature changes.

**Interfaces:**
- Consumes: `superviseProcess`, `SpawnAgent`, `Supervisor` (unchanged).
- Produces: `RunSupervised(claudePath string, sessionSpecs []SessionSpec, home, configPath, webToken string) error` (no `processes` param). `handleForSpec` defaults to `KindAgent`.

- [ ] **Step 1: Grep the seams that decide keep-vs-delete.**
Run: `grep -rn "resolveProcessHandle\|registerIdentity\|clearProcessSession\|LEO_PROCESS_NAME\|LEO_AGENT_NAME\|supervisedExecFn" internal | grep -v _test.go`
Record which survive.

- [ ] **Step 2: Excise the process-spawning block** (619–638, 572–578) and reword/remove the supervising-count log in `defaultSupervisedExec`.

- [ ] **Step 3: Change `RunSupervised`/`defaultSupervisedExec`/`supervisedExecFn` signatures** to drop `processes []ProcessSpec`; update `handleForSpec` default to `KindAgent`; reconcile `registerIdentity`/`clearProcessSession`/`LEO_PROCESS_NAME` per Step 1.

- [ ] **Step 4: Update the `runService` caller** in `internal/cli/service.go` to the new signature.

- [ ] **Step 5: Update service tests** (signatures + KindProcess→KindAgent).

- [ ] **Step 6: Verify green.**
Run: `go build ./... && go test ./internal/service/... ./internal/cli/...`
Expected: PASS.

- [ ] **Step 7: Commit.**
```bash
git add internal/service internal/cli/service.go
git commit -m "refactor: remove process-spawning arm from supervisor; RunSupervised drops processes param"
```

---

### Task 5: Remove `KindProcess` from the harness layer

**Files:**
- Modify: `internal/harness/harness.go` — remove the `KindProcess` const (18); keep `KindAgent`, `KindTask`, `KindSession`.
- Modify: `internal/harness/claude/claude.go` — in `Args()` (102–119) remove `case harness.KindProcess: return processArgs(spec, opts)` (108–109). If `processArgs` is now unreferenced, delete it (and its file/tests). Confirm `agentArgs` produces the intended argv for long-lived agents.
Run: `grep -rn "processArgs" internal/harness/claude`
- Modify: `internal/harness/codex/codex.go:41–43` — drop `|| k == harness.KindProcess` from `SupportsKind`.
- Modify: `internal/harness/opencode/opencode.go` — drop `KindProcess` from `SupportsKind` (43–45) and from the `spec.Kind ==` condition at 75.
- Test (update): `internal/harness/claude/{claude_test.go,args_test.go,system_context_test.go}`, `internal/harness/codex/codex_test.go` (`TestSupportsKind`), `internal/harness/opencode/{opencode_test.go,driver_test.go}` — remove `KindProcess` cases; keep `KindAgent`/`KindTask`/`KindSession` cases.

**Interfaces:**
- Consumes: `harness.Kind` (existing).
- Produces: `harness.KindProcess` no longer exists; all adapters accept `KindAgent`/`KindTask`/`KindSession`.

- [ ] **Step 1: Update the harness tests** to drop `KindProcess` assertions.

- [ ] **Step 2: Run to confirm they now reference a missing symbol.**
Run: `go test ./internal/harness/... 2>&1 | head`
Expected: compile error on `KindProcess` (proves the const is still defined).

- [ ] **Step 3: Remove `KindProcess`** from `harness.go` and each adapter; delete `processArgs` if dead.

- [ ] **Step 4: Verify green.**
Run: `go build ./... && go test ./internal/harness/...`
Expected: PASS.

- [ ] **Step 5: Commit.**
```bash
git add internal/harness
git commit -m "refactor: remove harness KindProcess kind"
```

---

### Task 6: Remove `ProcessConfig`, `Config.Processes`, and accessors from config

By now every consumer is gone, so the root types can be deleted.

**Files:**
- Modify: `internal/config/config.go`:
  - Remove `Processes map[string]ProcessConfig` field (63) from `Config`.
  - Remove the `ProcessConfig` struct (198–221).
  - Remove the `len(c.Processes) == 0` clause in `IsClientOnly()` (306).
  - Remove accessors: `ProcessWorkspace` (326–331), `ProcessModel` (335–343), `ProcessMaxTurns` (346–354), `ProcessStaleResume` (363–375), `ProcessMCPConfigPath` (402–411).
  - Remove the process branch in `Validate()` (586–641). Keep the template branch (643+) and the `DefaultStaleResumeHours` const (358) and `Defaults.StaleResumeHours` (186)
  - **Also remove the `process:` residue in the task-channel-subset check in `Validate()` (~836–844)** — carried over from the now-deleted Topology C. The branch `if strings.HasPrefix(task.Session, "process:") { src = fmt.Sprintf("processes.%s.channels", sessName) } else { src = fmt.Sprintf("sessions.%s.channels", sessName) }` must collapse to just the `sessions.%s.channels` form (drop the `process:` special-case and fix the stale "For B and C" comment). Confirm `strings` is still used elsewhere in the file before removing the import. — those remain valid defaults even though no scope reads them now; leave them (YAGNI to also rip out defaults). *(If lint flags `DefaultStaleResumeHours`/`ProcessStaleResume` removal as leaving `Defaults.StaleResumeHours` unused, keep the field — it is still validated at 546 and documented.)*
  - Remove the process loop in `expandPaths()` (886–893).
- Modify: `internal/config/harness.go` — remove `ProcessHarness` (26–28), `ProcessHarnessOptions` (95–97), and the process loop in `UsesHarness()` (49–53). Keep template/session/task loops.
- Test (update): `internal/config/config_test.go` (remove all `ProcessConfig`/`cfg.Processes` cases incl. the `ProcessStaleResume` tests at 529–559), `internal/config/harness_test.go` (`ProcessHarness`/`SupportsKind` stub cases), `internal/config/migration_test.go` (Processes at 17,60–92,210), and `internal/web/schema/values_test.go` (the `ProcessConfig` round-trip incl. `StaleResumeHours` at 105–117 — delete; do NOT port to templates).

**Interfaces:**
- Consumes: nothing new.
- Produces: `config` package has no `ProcessConfig` type, no `Config.Processes` field, no `Process*` accessors.

- [ ] **Step 1: Remove config types + accessors** per the list.

- [ ] **Step 2: Remove config/harness process accessors + loops.**

- [ ] **Step 3: Update config + schema tests.**

- [ ] **Step 4: Verify the whole tree builds.**
Run: `go build ./... && go test ./...`
Expected: PASS (all packages — this is the first commit where `ProcessConfig` no longer exists anywhere).

- [ ] **Step 5: Commit.**
```bash
git add internal/config internal/web/schema
git commit -m "refactor: delete ProcessConfig, Config.Processes, and process accessors"
```

---

### Task 7: Setup seeds a `templates.assistant` + one-time spawn (TDD)

Replaces the removed `processes.assistant` default so a fresh install still comes up with a running assistant. This is the one genuine behavior change — full TDD.

**Files:**
- Modify: `internal/setup/setup.go`:
  - Default config seeding (214–220): replace the `Processes: map[string]config.ProcessConfig{"assistant": {…, Enabled: true}}` block with `Templates: map[string]config.TemplateConfig{"assistant": { Workspace: <same>, HarnessOptions: map[string]any{"remote_control": true} }}` (mirror the old fields onto a template — no `Enabled`).
  - Merge-existing (231–233): merge `existing.Templates` instead of `existing.Processes` (or merge templates in addition, without clobbering).
  - `buildClientConfig` (586): `cfg.Templates = maps.Clone(existing.Templates)` instead of processes.
  - Summary line (89): print `Templates: %d` (or drop the process line).
  - Add a **one-time assistant spawn** after the daemon is started in the setup/onboard flow: locate where setup starts the service (grep `service` / `Start` in `internal/setup` and `internal/onboard`) and, after it is up, invoke the existing agent-spawn path for `template=assistant, name=assistant` (use the same code `leo agent spawn` uses — the daemon API or `agent.Manager.Spawn`). Make it best-effort: log a warning if it fails, do not abort setup.
- Test: `internal/setup/setup_test.go` — update fixtures (Processes → Templates at 33,69,89,214,439–440,450,460,526,539) and add a new test asserting the default config contains a `templates["assistant"]` and no `processes` map, plus (if the spawn is unit-testable via a seam) that setup triggers exactly one spawn of `template=assistant`.

**Interfaces:**
- Consumes: `config.TemplateConfig`, the agent spawn entrypoint (`agent.Manager.Spawn` / daemon `AgentSpawnSpec`).
- Produces: fresh `leo.yaml` has `templates.assistant`, no `processes:`; setup performs one imperative assistant spawn.

- [ ] **Step 1: Write the failing test.** In `internal/setup/setup_test.go`:
```go
func TestDefaultConfigSeedsAssistantTemplate(t *testing.T) {
    cfg := defaultConfig() // or the actual seeding entrypoint used in setup.go
    if _, ok := cfg.Templates["assistant"]; !ok {
        t.Fatalf("expected templates[assistant] in seeded config")
    }
    if len(cfg.Processes) != 0 { // Processes field is gone; this line should not compile pre-change
        t.Fatalf("did not expect a processes map")
    }
}
```
*(Note: once Task 6 lands, `cfg.Processes` does not exist — drop that assertion and instead assert the YAML has no `processes:` key by marshalling.)*

- [ ] **Step 2: Run it — expect failure.**
Run: `go test ./internal/setup/ -run TestDefaultConfigSeedsAssistantTemplate -v`
Expected: FAIL (`templates["assistant"]` missing).

- [ ] **Step 3: Implement the seeding change** (Processes→Templates) and the one-time spawn wiring.

- [ ] **Step 4: Run tests.**
Run: `go test ./internal/setup/... -v`
Expected: PASS.

- [ ] **Step 5: Verify tree green.**
Run: `go build ./... && go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit.**
```bash
git add internal/setup
git commit -m "feat: setup seeds templates.assistant and spawns it once (replaces processes.assistant)"
```

---

### Task 8: Documentation + CLAUDE.md

Pure docs. Rewrite the "process" primitive as "agents"; re-describe `leo service` as the daemon lifecycle command.

**Files:**
- Delete: `docs/cli/process.md`; remove the `processes:` block in `docs/demo/leo.yaml`.
- Modify: `docs/cli/index.md` (drop `leo process` entries), `docs/index.md` ("Process Supervisor" → agents), `docs/getting-started/index.md`, `docs/getting-started/prerequisites.md`, `docs/configuration/index.md`, `docs/configuration/config-reference.md` (remove `processes:` docs), `docs/configuration/harnesses.md` (supervised-process language + the `SupportsKind` error strings), `docs/configuration/workspace-structure.md`, `docs/configuration/persistent-tasks.md` (remove `session: process:<name>` Topology C), `docs/guides/{agents.md,background-mode.md,example-usage.md,remote-cli.md,tmux-config.md}`, `docs/cli/{session.md,attach.md,mcp-server.md,logs.md}`, `docs/development/index.md`.
- Modify: `CLAUDE.md` — lines 7,10,41,44,59,61,63,73,74,80,82: rewrite the intro/primitives to describe agents only; `leo service` = the daemon; drop the "Multi-process supervisor" pattern bullet; remove `processes` from the config section list; fix the web sidebar sections list (drop "Processes").
- Leave `docs/superpowers/**` historical specs as-is.

- [ ] **Step 1: Grep for every doc reference.**
Run: `grep -rln "processes:\|leo process\|Process Supervisor\|process:<name>\|KindProcess" docs CLAUDE.md`

- [ ] **Step 2: Edit/delete each doc** per the list; ensure no dead links to `docs/cli/process.md` remain (grep for `cli/process`).

- [ ] **Step 3: Sanity-check the docs build/links** (if the repo has a docs linter/build, run it; otherwise grep for `process` and confirm only intentional OS-process mentions remain).
Run: `grep -rn "leo process\|Process Supervisor" docs CLAUDE.md`
Expected: no matches.

- [ ] **Step 4: Commit.**
```bash
git add docs CLAUDE.md
git commit -m "docs: describe agents as the sole supervised primitive; leo service = daemon"
```

---

### Task 9: Full verification + e2e

**Files:** none (verification only).

- [ ] **Step 1: Full build + unit/integration suite.**
Run: `make test`
Expected: PASS, no references to removed symbols.

- [ ] **Step 2: Lint.**
Run: `make lint`
Expected: clean (watch for now-unused helpers — remove any the compiler/staticcheck flags).

- [ ] **Step 3: e2e (required — config/argv changed).**
Run: `make e2e`
Expected: PASS. If it references `leo process` or `processes:` fixtures, update those e2e fixtures to the agent workflow and re-run.

- [ ] **Step 4: Manual smoke (real behavior).** Against an isolated test daemon (separate `LEO_HOME`, own port — never touch production per project rules):
  - `leo agent spawn --template assistant --name smoke` (no-repo fixed workspace) → confirm it starts and channels (if configured) appear in the launched argv.
  - Restart the test daemon → confirm `RestoreAgents()` brings `smoke` back under the same name.
  - `leo agent suspend smoke` / `resume smoke` / `stop smoke` → confirm lifecycle.
  - Confirm `leo process ...` now errors as unknown command and the web UI has no `/processes` page.

- [ ] **Step 5: Final commit (if smoke fixes anything).**
```bash
git add -A
git commit -m "test: e2e + smoke for processes-into-agents removal"
```

---

## Self-Review (completed by plan author)

- **Spec coverage:** Delete config/CLI/web/daemon/harness process surfaces (Tasks 2–6,8) ✓; keep daemon + templates + agent lifecycle (verified in Global Constraints + Task 4) ✓; salvage cut (Global Constraints + Task 6) ✓; Topology C dropped (Task 1) ✓; setup template+spawn (Task 7) ✓; testing incl. e2e (Task 9) ✓; non-goals (no migration/autostart) enforced in Global Constraints ✓.
- **Placeholder scan:** No TBD/TODO. "Verify with grep before deleting" steps are deliberate reuse-guards for shared symbols (`ProcessStateProvider`, `resolveProcessHandle`, `mergeHarnessEnv`, `registerIdentity`, `LEO_PROCESS_NAME`), not vagueness — each has an exact grep and a keep/delete rule.
- **Type consistency:** `RunSupervised` signature change (drop `processes []ProcessSpec`) is introduced in Task 4 and its only caller `runService` is updated in the same task; Task 3 passes `nil` in the interim to keep compiling. `handleForSpec` default `KindProcess`→`KindAgent` in Task 4. `SectionProcess` removed from schema in Task 2 before `ProcessConfig` is deleted in Task 6.
