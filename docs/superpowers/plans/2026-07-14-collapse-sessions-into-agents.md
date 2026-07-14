# Collapse "sessions" into "agents" — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Retire the persistent-session primitive: scheduled tasks target agents (via `template:` or an implicit per-task template), the delivery pipeline is repointed at agent tmux sessions, and all session config/supervision/CLI/web surfaces are deleted.

**Architecture:** Build-then-delete, keeping the tree green at every commit. Tasks 1–3 build the new task→agent path alongside the session path (config resolution, ensure-exists delivery, session-ID-on-agent-record + `leo agent reset`). Task 4 deletes the entire session surface once nothing consumes it. Tasks 5–6 retarget e2e and rewrite docs. Task 7 verifies.

**Tech Stack:** Go 1.x, cobra, gopkg.in/yaml.v3, htmx web UI, tmux supervision, robfig/cron.

Governing spec: `docs/superpowers/specs/2026-07-14-collapse-sessions-into-agents-design.md`.

## Global Constraints

- **Build green at every commit**: `go build ./...` before each commit; consumers are repointed (Tasks 1–3) before the session types die (Task 4).
- **The delivery pipeline's semantics do not change**: FIFO per target, one in-flight slot, `queue_max` (default 5) rejection, readiness-probed injection, invocation-marker Stop-hook correlation for claude, timeout completion for non-claude, `notify_on_fail` re-enqueue. Only the addressing changes (queue key + tmux target = agent).
- **Agent naming rule (from PR #113)**: template `assistant` with no repo → agent named `assistant`; tmux session = `agent.SessionName(name)` (prefixes `leo-` iff absent). The implicit case names the agent after the **task**.
- **Non-claude completion reporting stays timeout-based** — explicitly the next PR; do not build turn-detection.
- **No migration tooling, no `lazy`/`idle_timeout` implementation** (both stubs are deleted).
- **Line numbers are anchors from a snapshot — match on symbol names.** Grep-guard before deleting anything plausibly shared.
- **Before push**: `make e2e` (build-tagged, invisible to `go test ./...`) and `golangci-lint run ./...` (CI's linter; `make lint` does NOT catch `unused`/quickfix findings — /opt/homebrew/bin/golangci-lint, v2.12.2).
- **Never run live daemon/service/spawn commands** against the user's production leo home.

## File Map

**Create:** `internal/run/target.go` (+test) — task→target resolution; `internal/daemon/ensure.go` (+test) — ensure-exists (or fold into the router file if <100 lines; implementer's call, keep it separate if the router file is already large).
**Modify:** `internal/config/config.go` (TaskConfig), `internal/config/session.go` → shrinks, `internal/run/persistent.go`, `internal/daemon/session_router.go`, `internal/service/process.go` (boot), `internal/service/session.go` → mostly deleted, `internal/agent/manager.go` (reset + session-id setter), `internal/cli/agent.go` (reset cmd), `internal/cli/root.go`, `internal/web/web.go` + `handlers_sessions.go` (delete), `internal/daemon/server.go` (+client), `internal/mcp` (if session refs), e2e `persistent_*.go`, docs.

---

### Task 1: Task→target resolution in config (build alongside sessions)

`TaskConfig` gains `Template string`; a new resolver replaces `ResolveSession` for the agent world, returning the target agent name + an effective `TemplateConfig` (explicit or synthesized). Old session path untouched this task.

**Files:**
- Modify: `internal/config/config.go` — `TaskConfig` (~215–242): add `Template string \`yaml:"template,omitempty"\`` beside `Session`.
- Create: resolver in `internal/config/target.go` (new file; sibling of session.go)
- Modify: `internal/config/config.go` `Validate()` — add persistent-task template checks; leave the session-subset check in place (deleted in Task 4).
- Test: `internal/config/target_test.go`

**Interfaces:**
- Produces: `func (c *Config) ResolveTaskTarget(taskName string) (agentName string, tmpl TemplateConfig, implicit bool, err error)` — errors for unknown task, non-persistent task, or `template:` naming a missing template. Explicit: `agentName = task.Template`, `tmpl = c.Templates[task.Template]`, `implicit=false`. Implicit (no `template:`): `agentName = taskName`, `tmpl = TemplateConfig{Workspace: task.Workspace, Model: task.Model, Channels: task.Channels, DevChannels: task.DevChannels}`, `implicit=true`.
- Consumed by: Task 2 (`runPersistent`), Task 4 (deleting `ResolveSession`).

- [ ] **Step 1: Write failing tests** in `internal/config/target_test.go`:

```go
func TestResolveTaskTargetExplicitTemplate(t *testing.T) {
	cfg := &Config{
		Templates: map[string]TemplateConfig{"assistant": {Workspace: "/w", Channels: []string{"plugin:a@b"}}},
		Tasks: map[string]TaskConfig{"brief": {Runtime: "persistent", Template: "assistant", Channels: []string{"plugin:a@b"}}},
	}
	name, tmpl, implicit, err := cfg.ResolveTaskTarget("brief")
	if err != nil { t.Fatalf("err: %v", err) }
	if name != "assistant" || implicit || tmpl.Workspace != "/w" {
		t.Fatalf("got name=%q implicit=%v ws=%q", name, implicit, tmpl.Workspace)
	}
}

func TestResolveTaskTargetImplicit(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"digest": {Runtime: "persistent", Workspace: "/tw", Model: "opus", Channels: []string{"plugin:a@b"}}}}
	name, tmpl, implicit, err := cfg.ResolveTaskTarget("digest")
	if err != nil { t.Fatalf("err: %v", err) }
	if name != "digest" || !implicit || tmpl.Workspace != "/tw" || tmpl.Model != "opus" {
		t.Fatalf("got name=%q implicit=%v tmpl=%+v", name, implicit, tmpl)
	}
}

func TestResolveTaskTargetMissingTemplate(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"x": {Runtime: "persistent", Template: "nope"}}}
	if _, _, _, err := cfg.ResolveTaskTarget("x"); err == nil {
		t.Fatal("expected error for missing template")
	}
}

func TestResolveTaskTargetOneshotErrors(t *testing.T) {
	cfg := &Config{Tasks: map[string]TaskConfig{"o": {Runtime: "oneshot"}}}
	if _, _, _, err := cfg.ResolveTaskTarget("o"); err == nil {
		t.Fatal("expected error for non-persistent task")
	}
}
```

- [ ] **Step 2: Run** `go test ./internal/config/ -run TestResolveTaskTarget -v` — expect FAIL (undefined `ResolveTaskTarget`, `Template` field).

- [ ] **Step 3: Implement.** Add the `Template` field. Create `internal/config/target.go` with `ResolveTaskTarget` per the interface above (mirror `ResolveSession`'s task-lookup/runtime-check shape at `internal/config/session.go:26-57`). In `Validate()`, for each `runtime: persistent` task with `Template != ""`: error if the template doesn't exist; error if task channels+dev_channels ⊄ template channels+dev_channels (reuse `channelSubset` in session.go — do NOT duplicate it; it survives Task 4). Also validate `Template` and `Session` are not both set (error: "task %q: template and session are mutually exclusive" — session still exists until Task 4).

- [ ] **Step 4: Run** `go test ./internal/config/` — PASS. `go build ./...` — green.

- [ ] **Step 5: Commit** `feat(config): task template binding + ResolveTaskTarget (agents-as-task-targets)`.

---

### Task 2: Ensure-exists delivery — repoint the pipeline at agents

`runPersistent` resolves via `ResolveTaskTarget`; the daemon guarantees the target agent is injectable (running→inject, suspended→resume, missing→spawn) before the router injects. Queue key + tmux target become the agent. Session-named tasks (legacy `session:` field) keep working through the old path until Task 4.

**Files:**
- Modify: `internal/run/persistent.go` — `runPersistent` (~67–136): when the task resolves via `ResolveTaskTarget` (i.e. `task.Session == ""`), set `Session: agentName` (queue key) and `TmuxSession: agent.SessionName(agentName)`, and pass the ensure-exists intent (new `EnsureAgent` fields on the enqueue request: template + name + implicit flag). When `task.Session != ""`, keep today's session path verbatim (dies in Task 4).
- Modify: `internal/daemon/` enqueue request struct (where `EnqueueRequest` lives — grep `EnqueueRequest`; likely `client.go`/`session_router.go`): add `EnsureTemplate *config.TemplateConfig` + `EnsureName string` (nil ⇒ legacy behavior). If `config` import creates a cycle, define a minimal `EnsureSpec{Name, Template, Repoless bool}` DTO in daemon and map in run/.
- Create: `internal/daemon/ensure.go` — the ensure-exists step invoked by the router pump before injection.
- Modify: `internal/daemon/session_router.go` — pump (~392–497): before `r.inject(...)` for an invocation carrying an ensure spec, call the ensurer; on ensure failure complete the invocation as failed (reuses the existing failure/report path so `notify_on_fail` fires).
- Modify: `internal/service/process.go` / daemon wiring — the router needs access to the agent Manager (it's already constructed in `defaultSupervisedExec`; inject it into the router/ensurer at construction).
- Test: `internal/daemon/ensure_test.go` + a `persistent_test.go` case in `internal/run/`.

**Interfaces:**
- Consumes: `ResolveTaskTarget` (Task 1); `agent.Manager.Spawn(ctx, agent.SpawnSpec)` (SpawnSpec{Template, Name, Repo:"", Env, ...}), `Manager.Resume(name)`, `Manager.List()`/`EphemeralAgents()` for liveness, `agent.SessionName(name)`.
- Produces: `type AgentEnsurer interface { Ensure(ctx context.Context, spec EnsureSpec) error }` implemented over the agent Manager; `EnsureSpec{Name string; TemplateName string; Template config.TemplateConfig; Implicit bool}`. Router pump calls `Ensure` when `invocation.Ensure != nil`.
- Ensure logic: agent live → nil. Suspended record → `Resume(name)`. No record → spawn: explicit ⇒ `Spawn(SpawnSpec{Template: spec.TemplateName, Name: spec.Name})` (template from config, repo-less); implicit ⇒ spawn from the synthesized `Template` value (the Manager's Spawn reads templates from config — for the implicit case add a Manager entrypoint `SpawnFromTemplate(ctx, name string, tmpl config.TemplateConfig) (Record, error)` that skips the config lookup; grep Manager.Spawn's template resolution (`cfg.Templates[spec.Template]`) and factor the post-lookup body so both paths share it).

- [ ] **Step 1: Write failing ensurer tests** (`internal/daemon/ensure_test.go`) with a fake Manager capturing calls:

```go
type fakeEnsureMgr struct{ live map[string]bool; suspended map[string]bool; resumed, spawned []string }
// implement the narrow interface the ensurer needs (Live(name) bool / Suspended(name) bool / Resume / SpawnFromTemplate)

func TestEnsureRunningIsNoop(t *testing.T)      { /* live => no Resume/Spawn calls */ }
func TestEnsureSuspendedResumes(t *testing.T)   { /* suspended record => Resume called once, no spawn */ }
func TestEnsureMissingSpawns(t *testing.T)      { /* no record => SpawnFromTemplate called with name+template */ }
func TestEnsureSpawnFailurePropagates(t *testing.T) { /* spawn err => Ensure returns err */ }
```

Define the ensurer against a small local interface (not the concrete Manager) so the fake stays tiny.

- [ ] **Step 2: Run** `go test ./internal/daemon/ -run TestEnsure -v` — FAIL (undefined).

- [ ] **Step 3: Implement** `ensure.go`, the `SpawnFromTemplate` factoring in `internal/agent/manager.go` (shared body with `Spawn`'s post-template-lookup flow; add a Manager unit test that `SpawnFromTemplate` reaches the supervisor like a repo-less `Spawn`), the enqueue-request fields, the pump hook, and the `runPersistent` branch. Wire the Manager into the router at daemon construction (grep `NewRouter\|newSessionRouter` for the construction site).

- [ ] **Step 4: Run** `go test ./internal/daemon/... ./internal/run/... ./internal/agent/... -race` — PASS; `go build ./...` green. Legacy session e2e still passes: `make e2e`.

- [ ] **Step 5: Commit** `feat(daemon): ensure-exists task delivery into agents (spawn/resume on fire)`.

---

### Task 3: Session IDs on the agent record + `leo agent reset`

**Files:**
- Modify: the task-report path that persists discovered session ids (grep `session.Store` writes keyed `"session:"` — `internal/service/session.go` Stop-hook flow / `internal/daemon` report handling): when the queue key names an **agent** (ensure-spec invocations), write the discovered claude session id to the agentstore record's `SessionID` (there is an existing setter path — grep `SessionID` in `internal/agent/manager.go` / `internal/agentstore`); leave the `session:<name>` store write for legacy session invocations (dies in Task 4).
- Modify: `internal/agent/manager.go` — add `Reset(name string) error`: stop the agent's process/tmux, clear `SessionID` + set no-resume (reuse `markAgentNoResume` — grep it in `internal/service/process.go`; if it lives service-side, expose the equivalent through the Manager's supervisor interface), then start it again fresh (same path Resume uses minus `--resume`).
- Modify: `internal/cli/agent.go` — `newAgentResetCmd()` registered under `leo agent`: resolves the name (same resolution as suspend/stop), calls the daemon reset endpoint.
- Modify: `internal/daemon/server.go` + client — `POST /agents/{name}/reset` (mirror the existing agent suspend/resume IPC endpoints; grep `handleAgentSuspend` for the pattern).
- Test: Manager `Reset` unit test (fake supervisor: assert stop+clear+start order); CLI test mirroring the suspend cmd test; report-path test asserting agent-keyed invocations persist to the agentstore not the session store.

**Interfaces:**
- Produces: `Manager.Reset(name string) error`; daemon route `POST /agents/{name}/reset`; `leo agent reset <name>`.
- Consumes: Task 2's ensure-spec marking (an invocation "targets an agent").

- [ ] **Step 1: Failing tests** for `Manager.Reset` (fake supervisor records call order: Stop→clear→Spawn/start) + the report-path storage switch.
- [ ] **Step 2: Run** — FAIL (undefined Reset / wrong store).
- [ ] **Step 3: Implement** per interfaces above.
- [ ] **Step 4:** `go test ./internal/agent/... ./internal/cli/... ./internal/daemon/... ./internal/service/... -race` PASS; build green.
- [ ] **Step 5: Commit** `feat(agent): session ids on the agent record + leo agent reset`.

---

### Task 4: Delete the session primitive

Everything now flows through Tasks 1–3; delete the session surface. Adapted TDD: deletions verify by build+tests+greps.

**Files (delete/modify — grep-guard each shared symbol before deleting):**
- `internal/config/config.go`: `SessionConfig` (~198–213), `Sessions` map field, `TaskConfig.Session` + `Lazy` + `IdleTimeout` stubs, the session channel-subset branch in `Validate()` (~688; the template form from Task 1 replaces it), the mutual-exclusion check from Task 1 (now `Session` is gone — becomes just the template checks), session loops in `expandPaths`/`UsesHarness`/`IsClientOnly` (grep `cfg.Sessions|c.Sessions`).
- `internal/config/session.go`: `ResolveSession`, `SessionTopology`, `TopologyDedicated`/`TopologyShared` — delete the file if only `channelSubset` remains (move `channelSubset` to `target.go`).
- `internal/service/session.go`: `SessionSpec`, `SessionSpecsFromConfig`, `superviseClaudeSession`, `superviseTUISession`, `sessionTmuxTarget`/`SessionTmuxName`, `BuildSessionDispatch` — grep each; the boot arm in `defaultSupervisedExec` (`internal/service/process.go` — session boot ~648–667 region) goes with it. KEEP anything the agent path consumes (grep before deleting: the Stop-hook/task-report plumbing and injection helpers are shared with Task 2/3 paths).
- `internal/run/persistent.go`: delete the legacy `task.Session != ""` branch; `runPersistent` is agent-only now.
- `internal/daemon/`: `/session/{name}/reset` + `/session/{name}/depth` endpoints and client funcs (`ResetSession`, `SessionDepth`) — the reset was replaced by the agent reset (Task 3); depth: delete (drain is dropped per spec).
- `internal/cli/session.go` (whole file: list/status/attach/logs/reset/drain) + registration in `internal/cli/root.go`; grep `session` in `internal/cli/attach*.go`/`status.go` for session rows/choices and remove.
- `internal/web/`: `handlers_sessions.go`, `/sessions` routes in `web.go`, `pages/sessions.html` + partials, layout nav entry + page dispatch; grep templates for `session` (primitive sense). **In `internal/web/schema/registry.go`: delete the task `session` field entry (~line 99) and REGISTER `template` in its place** (a `KindSelect` over `templates`, mirroring how `session` selected over sessions; remove the Task-1 exclusion comment at ~277-279) — otherwise persistent tasks become unconfigurable from the web UI (Task 1 review finding).
- `internal/session` store: grep remaining consumers; if only legacy `session:<name>` writes remain, remove those writes. The package may still be used for other key-value persistence — grep before deleting the package.
- e2e is Task 5 — but if `go build`/`go vet` on e2e files breaks compile due to deleted symbols, apply the minimal retarget here and note it.
- `grep -rn "leo-session-\|SessionConfig\|cfg.Sessions\|ResolveSession\|SessionTopology\|SessionSpecsFromConfig\|leo session " internal` → ZERO after this task (source and tests; e2e may still have hits until Task 5 — scope the grep to `internal`).

- [ ] **Step 1: Grep-guard sweep** (record keep/delete per shared symbol — especially the report/injection plumbing shared with the new path).
- [ ] **Step 2: Delete config surface + update config tests.**
- [ ] **Step 3: Delete service/daemon/run session arms + tests.**
- [ ] **Step 4: Delete CLI + web surfaces + tests.**
- [ ] **Step 5:** `go build ./... && go test ./... && go vet ./...` green; the grep gate above returns zero.
- [ ] **Step 6: Commit** `refactor: delete the session primitive (config, supervision, CLI, web, daemon)`.

---

### Task 5: Retarget e2e

**Files:** `e2e/persistent_basic_test.go`, `e2e/persistent_queue_test.go`, `e2e/persistent_shared_test.go`, `e2e/persistent_helpers_test.go` (+ any fixture yaml embedded in them).

- [ ] **Step 1:** Rewrite fixtures: tasks use `template: <name>` (+ a `templates:` entry) instead of `session:`/`sessions:`; assert against agent tmux names (`leo-<agent>`) instead of `leo-session-<name>`. Coverage to preserve: basic fire + completion report + channel footer; FIFO ordering + `queue_max` rejection; two tasks sharing one template/agent. ADD: the implicit case (persistent task with no `template:` → agent named after the task spawns on first fire) and the ensure-exists respawn (stop the agent between fires → next fire respawns).
- [ ] **Step 2:** `make e2e` — PASS.
- [ ] **Step 3: Commit** `test(e2e): retarget persistent suite at agent targets + implicit/ensure coverage`.

---

### Task 6: Docs + CLAUDE.md

- [ ] **Step 1:** Rewrite `docs/configuration/persistent-tasks.md` around task→template (explicit + implicit, ensure-exists, idle-suspend interaction, `queue_max`, `leo agent reset`). Update `docs/configuration/config-reference.md` (drop `sessions:`, add `tasks.*.template`), `docs/cli/index.md` + delete `docs/cli/session.md` (+ mkdocs nav), `docs/index.md`, `docs/guides/*` (grep `session` primitive-sense), `CLAUDE.md` (primitives → Agents + Tasks; config sections list; `sessions:` mentions).
- [ ] **Step 2:** `grep -rn "sessions:\|leo session\|session: " docs CLAUDE.md` → only OS/tmux/claude-session senses remain (verify each hit); no dead links (`grep -rn "cli/session" docs mkdocs.yml`).
- [ ] **Step 3: Commit** `docs: agents + tasks — retire the session primitive`.

---

### Task 7: Full verification

- [ ] `make test` PASS; `golangci-lint run ./...` 0 issues; `make e2e` PASS.
- [ ] Manual smoke against an ISOLATED test daemon only (separate LEO_HOME; never production; do not use `leo service restart` — kill the test PID): configure a persistent task with `template:`, fire it with `leo run <task>`, watch the agent spawn + receive the prompt; suspend the agent, fire again, watch it wake. Skip if the isolated-daemon setup is unavailable and say so.
- [ ] Commit any fixups.

---

## Self-Review (by plan author)

- **Spec coverage:** §1 config → Task 1; §2 pipeline repoint + §3 ensure-exists → Task 2; §5 ids/reset → Task 3; §6 deletions → Task 4 (e2e retarget → Task 5); §7 docs → Task 6; Testing section → Tasks 1–3 (TDD), 5, 7. Non-goals enforced in Global Constraints.
- **Placeholders:** none; Task 2/3 use interface-level specs with named fakes where exact code depends on grep results — each has the concrete grep and the decision rule.
- **Type consistency:** `ResolveTaskTarget` signature consistent across Tasks 1/2/4; `EnsureSpec`/`SpawnFromTemplate`/`Manager.Reset` named identically in Tasks 2/3; grep gates scoped to `internal` until Task 5.
