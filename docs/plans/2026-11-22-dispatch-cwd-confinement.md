# Dispatch cwd confinement — implementation plan

Spec: `docs/specs/2026-11-22-dispatch-cwd-confinement.md` (source of truth). Branch: `feat/dispatch-cwd-confinement`.
Review bar: fix P0/P1, any confinement escape a dispatching agent can trigger, any false refusal of a same-repo/scratchpad cwd. Narrow races → accepted risks.

## Grounding (verified by planner)

- `consult.Dispatcher.Start(ctx, cfg, req)` (`internal/consult/consult.go:355`) is the single start path for web `/api/dispatch`, MCP `leo_dispatch` (socket → same handler) and `leo dispatch` CLI. cwd is only existence-checked (`:435-441`). `cfg` is per call (hot reload free).
- Root caller is already resolved: `resolveParent` (`:1042`) returns the parent's `Caller`; nested records file under the root's name (`:455-462`). `treeOf` (`readonly_guard.go:40`) refuses an unresolvable `parent_dispatch_id`.
- Callers: agents send `LEO_PROCESS_NAME` (`mcp/tools.go:572-576`; persistent tasks are agents); oneshot tasks `task:<name>` (`run/runner.go:1046`); CLI sends empty `from`; API clients send their `api_clients` name (e.g. `leoterm-ios`); inside a dispatch the MCP tool blanks `from`, sends `ParentDispatchID`, default cwd `os.Getwd()`.
- Warnings already flow: `warnings` → `Record.Warnings` → `logDispatchWarnings` (`claude_profile.go:105`, called `consult.go:509`) → `Started.Warnings`, shown by MCP (`tools.go:593`) and CLI (`cli/dispatch.go:179`).
- `invalidf` → `*ValidationError` → 400, never retried headless.
- Worktrees: `<stateDir>/worktrees/<dispatch-id>` (`worktree.go:96`); git seam `d.git` (`worktree.go:33-47`).
- Agent workspace: exact name match over `agentSvc.List()` (as `callerEnvironments`, `handlers_consult.go:438`). **Not** `agentSvc.Resolve` (fuzzy). Task workspace: `cfg.TaskWorkspace(task)` (`config.go:534`).

## Decisions

1. Check runs only for `kind == "dispatch"` (consult cwd is daemon-chosen). Continuations never call `Start`; not re-checked.
2. Root caller = `caller` at `consult.go:459-462`.
   - **Exempt:** empty root caller (human CLI; tree rooted there), and root caller that is a configured `api_clients` name.
   - **Any other named caller with no resolvable workspace** (stale agent, unknown `task:`, spoofed string): `warn` → warning; `enforce` → refuse. (Orchestrator decision, resolves planner Q1: fail closed, since API clients are identifiable by config.)
3. Resolver interface in `consult`, implemented in `web`, set via `Dispatcher.SetCallerWorkspaces`:
   `type CallerWorkspaces interface { CallerWorkspace(cfg *config.Config, caller string) (CallerHome, bool) }`, `CallerHome{Workspace, Harness string}`. `task:<name>` → `cfg.Tasks[name]` + `cfg.TaskWorkspace` + task harness; else exact agent name → `rec.Workspace` + `cfg.TemplateHarness(cfg.Templates[rec.Template])`. Nil resolver = unresolved.
4. Allowed roots (each `EvalSymlinks`'d; unresolvable root dropped; requested cwd `EvalSymlinks`'d after the stat):
   - **R1** `realpath(workspace)`.
   - **R2** `git -C <dir> rev-parse --path-format=absolute --git-common-dir` for workspace and cwd, realpath'd, equal. Via `d.git`, 5 s timeout; error/timeout → no match. Only when workspace's `--show-toplevel` realpath is inside the workspace (a workspace nested in a larger repo must not grant that repo). No scan for repos inside the workspace (planner Q2: no).
   - **R3** cwd under `realpath(<stateDir>/worktrees)/<id>`, `ValidDispatchID(id)`, `d.lookup(id)` has `Isolation == "worktree"` and `Caller == root caller`.
   - **R4** claude only: `realpath(<tmp>)/claude-<uid>/<key>`, key ∈ {`sanitize(workspace)`, `sanitize(realpath(workspace))`}; `<tmp>` = `$CLAUDE_CODE_TMPDIR` else `/tmp`; `sanitize` maps every non-`[A-Za-z0-9]` byte to `-`. Codex/opencode: no R4.
5. `within(p, root) = p == root || HasPrefix(p, root+sep)`, root `/` special-cased; never bare `HasPrefix` or `Rel` heuristics.
6. Mode: `cfg.DispatchConfinement()` (`""` → `warn`). `off` skips (no git calls). `warn` appends `dispatch cwd <cwd> is outside caller <c>'s workspace <ws> (allowed roots: …); defaults.dispatch_confinement=enforce would refuse it` before the `Record` literal. `enforce` → `invalidf("cwd %s is outside caller %q's workspace %s; allowed roots: %s", …)`. Placed after `checkReadOnlyParent` (`:470`), before `nestMu` (`:476`); refusals are not recorded.
7. Launch keeps `req.Cwd` as given (planner Q3: symlink-swap race is an accepted risk).

## Steps

1. **Config** — `internal/config/config.go`: `DispatchConfinement` on `DefaultsConfig` (near `Session`, `:296`), constants (near `:1557`), getter, `validateDefaultsDispatchConfinement` from `validateDefaults` (`:677`), message prefix `defaults.dispatch_confinement` (`:634`). Tests first: `TestValidateDefaultsDispatchConfinement`, `TestDispatchConfinementDefault`. Verify: `go test -race -run 'TestValidateDefaultsDispatchConfinement|TestDispatchConfinementDefault' ./internal/config/`
2. **Pure checker** — new `internal/consult/confinement.go` (<~200 lines): `confinementDeps{evalSymlinks, gitCommonDir, gitTopLevel, worktreeOwner, tmpDir, uid}`, `checkConfinement(ctx, deps, confinementInput{Cwd, RootCaller, Home, WorktreesDir}) confinementVerdict{Allowed, Rule, Roots}`, `within`, `claudeScratchKey`. Table tests first (`t.TempDir`, `os.Symlink`, fake git/owner): R1 exact/nested; sibling `ws-evil` deny; `ws/../other` deny; `ws/link -> ../other` deny; outside link into ws allow; R2 same/different common dir; git error deny; nested-in-larger-repo skips R2; R3 own/other caller/`worktrees/../x`; R4 raw+realpath keys; codex/opencode R4 deny; cwd `/` deny; `claudeScratchKey("/Users/evan/.leo/agents/leo") == "-Users-evan--leo-agents-leo"`; one real-git row (`git init` + `git worktree add`, skip without git) proving argv and `/var`→`/private/var`. Verify: `go test -race -run 'TestCheckConfinement|TestClaudeScratchKey|TestConfinementRealGit' ./internal/consult/`
3. **Wire into Start** — `consult.go` field + `SetCallerWorkspaces`, deps from `d.git`/`os`/`d.lookup`; `request.go` declares `CallerHome`/`CallerWorkspaces`; api_clients exemption from `cfg.APIClients`. Tests first (`confinement_start_test.go`, through `Start`, headless, existing fake-exec helpers): warn warns + persisted; enforce refuses, no record written; off no warning and no git calls; nested judged by root; nested under CLI root exempt; empty caller exempt; api_clients name exempt; unknown named caller warns (warn) / refuses (enforce); consult kind unchecked; `isolation: worktree` judges source cwd, check precedes `prepareWorktree`. Verify: `go test -race -run 'TestStartConfinement' ./internal/consult/`
4. **Web resolver + wiring** — new `internal/web/caller_workspace.go`; call `SetCallerWorkspaces(server)` where the daemon builds dispatcher+server (`grep -rn "SetCloseFinishedViewer\|NewDispatcherWithOnStart" internal/`). Tests first: `TestCallerWorkspace` (exact hit; fuzzy suffix → miss; `task:x` incl. default workspace; unknown task → miss); `TestAPIDispatchConfinementEnforce` (enforce → 400 with message; warn → 200 with warnings). Verify: `go test -race -run 'TestCallerWorkspace|TestAPIDispatchConfinement' ./internal/web/`
5. **Schema/form** — `internal/web/schema/registry.go` row after `session` (`:119`), `KindSelect`, options `dispatch_confinements` in `options.go`; goldens under `internal/web/testdata/configapi/`. `registry_drift_test.go` fails after step 1 — run it first. Verify: `go test -race ./internal/web/schema/ && go test -race -run 'Schema|ConfigAPI|Drift' ./internal/web/`
6. **Docs** (no TDD) — `docs/configuration/config-reference.md` row; `docs/configuration/dispatches.md` "cwd confinement" section: rules, claude scratch path, exemptions, modes + messages, continuations/consults unchecked, accepted risks.
7. **Gate** — check load; `make test` (under `heavy` in background if slow), `make lint`, `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. If e2e asserts empty warnings, move the fixture cwd inside its workspace; never weaken assertions.
8. **Live check (orchestrator)** — isolated test daemon (own `LEO_HOME`/port, never `leo service restart`): warn → warning in result + log; enforce → cross-agent refused; enforce → sibling worktree and `isolation: worktree` allowed.

## Accepted risks (PR body)

- Caller spoofing over the socket (same class as #334 parent-id follow-up); an agent invoking the `leo dispatch` CLI sends empty `from` and is exempt.
- Check→launch symlink swap race on an agent-controlled path component.
- Case-variant paths on case-insensitive APFS are refused, not escaped.
- Claude scratch-key collisions (`/a/b-c` vs `/a/b/c`) and Claude's truncation of very long keys (R4 misses past ~200 chars).
