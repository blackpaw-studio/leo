# Plan: config follow-lead

## Orchestrator decisions (2026-10-27)
The planner's recommendations for D1–D6 are accepted as written. They are implementation-level and consistent with the spec:
- D1: editing a `defaults` or delegation scalar key on a follower writes a local override of that key.
- D2: key depth is top-level, except `dispatch.*` and `push.*`, which go one level deeper; `harness_options` is replaced whole.
- D3: a bad lead `defaults` key falls back to the built-in default and is shown as `lead · invalid`.
- D4: "invalid" means today's Validate rules per entry, plus two probes on lead entries: the harness binary is on PATH, and an absolute config dir exists.
- D5: with `follow` set and `default_host` empty, the CLI's default host is localhost.
- D6: "Override locally" is refused on a `lead · invalid` entry in v1.
Spec: `docs/specs/2026-10-27-config-follow-lead.md`. Where this plan and the spec disagree, the spec wins. Plan against `main`.

## Code map (main @ 90b1e50b)
- **Load.** `LoadWithRevision` is at `internal/config/config.go:844`. It runs `parse` (`config.go:862`, which expands `~` at 882), then `loadSharedTemplates` (`shared_templates.go:162`). `Load` (`config.go:819`) wraps it.
- **Revision.** `RevisionOf` and `revisionOfFiles` are at `config.go:827` and `config.go:835`.
- **Validate** is at `config.go:562`.
  - The template loop is 670-719 and tasks are 721-796.
  - Delegation validation is at `delegation.go:235`, environments at `environments.go:112` and `environments.go:137`.
  - Failures are `ValidationErrors`, one message per problem, each starting with a dotted path (`config.go:807-816`).
  - The CLI fails the whole command on any of them (`cli/helpers.go:21-41`).
  - The web turns them into a 422 with code `invalid` through `fieldProblems` (`web/config_cores.go:76-82`).
- **Reload.** `daemon.Server.ReloadConfig` (`daemon/server.go:663`) re-runs `config.Load` and `scheduler.Install`. `leo service reload` reaches it through the socket at `POST /config/reload` (`cli/service.go:378`). The web reaches it through `reloadConfigOrWarn` (`web/config_service.go:191`).
- **Config is never cached.** There are about 39 `config.Load` call sites across 21 files. That is why the lead layer has to be applied *inside* Load: every consumer then sees the effective config.
- **The `templates_file` mechanism from #305.**
  - The unexported `Config.shared *sharedTemplates` is at `config.go:128`.
  - `TemplateSource` (local, shared, local-override) and `Label()` are at `shared_templates.go:19-33`. `TemplateSource(name)` is at :89.
  - Merge happens at :183-197.
  - `Save` splits through `saveSplit` (:250). `marshalLeo` (:506) filters out shared entries. `planEdits` (:293) diffs against `baseline`, and `spliceShared` is in `shared_templates_splice.go`.
  - `InheritTemplatesFile` (:82) is used by setup at `setup/setup.go:252`.
- **Write paths, which all end at `config.Save` (`config.go:895`).**
  - The web goes through `ConfigService.Mutate` (`web/config_cores.go:56`, Save at :84). That covers `ApplyValues`, `AddItem`, `DeleteItem`, `RenameItem` and `DuplicateItem` (:140-266), the environments editor (`handlers_environments.go`) and the delegation API (`config_api_delegation.go:125`).
  - `validateAndSave` (`config_service.go:176`) is used by `handlers.go:456`, `handlers_dispatch_viewer.go:70` and `handlers_agents.go:555`.
  - The CLI goes through `saveConfig` (`cli/configio.go:20`). Callers are `template add`/`remove` (`cli/template.go:304` and :352) and `saveDelegationAndReload` (`cli/delegation.go:171`).
  - The daemon socket only writes tasks (`daemon/handlers.go:65,94,133`), and tasks are not synced.
  - Setup writes at `setup.go:401` and :509.
- **ssh transport.**
  - `buildSSHArgs` (`cli/agent.go:111`) builds `host, ssh_args, ControlMaster opts, tail`, using `sshControlOpts` (`cli/ssh.go:22`).
  - `runRemoteGroup` (`agent.go:131`) runs commands remotely.
  - Quoting: `remoteLeoWord` (`cli/tmux.go:394`) gives `"$HOME"/'rest'`, and `shellQuoteArg` is at :408.
  - Host resolution: `ResolveHost` (`config/client.go:39`) and `HostControlPath` (:87). `HostConfig.RemoteLeoPath` is at `config.go:168`.
  - The setup probe already uses `BatchMode` and `ConnectTimeout` (`setup.go:607`).
  - `feat/agent-move` lifts all of this into `internal/remote/ssh.go` (`ControlOpts`, `SSHArgv(res, batch, tail...)`) and adds `e2e/testdata/fakessh`. **Both are merge points.**
- **Background loops and state.**
  - Model for the pull loop: `runIdleSweep` (`service/sweep.go:22`), started at `service/process.go:1133`.
  - The socket mux is at `daemon/server.go:301-377`.
  - The state dir is `Config.StatePath()`, which is `HomePath/state` (`config.go:460`).
  - There is no host identity anywhere yet.
- **Where sources and names reach users.**
  - CLI: `templateListEntry.Source` (`cli/template.go:79-91`) and the SOURCE column (:137-158).
  - Web: `templateSourceLabels` (`web/handlers_pages.go:163`), the pill in `config_templates.html:33`, and the Settings page (`handlers_pages.go:233`).
  - Config API: `itemDocument` (`web/config_documents.go:95`). It has no source field today.
  - `/api/v1`: `observe.Template` and `observe.Environment` (`observe/snapshot.go:157-168`), served from `web/handlers_v1_environments.go:42`.
  - Error codes live in `web/config_errors.go:13-21`.
- **Where references are resolved.**
  - Templates: `agent/manager.go:490` (`UnknownTemplateError`), :813, :843, and `config/target.go:35`.
  - Environments: `MergeEnvironments` (`environments.go:38`).
  - Roles: `ResolveRole` (`delegation.go:184`).

## Design
**Export** (`leo config export --follow`, written to stdout; errors go to stderr with a non-zero exit):
```json
{ "schema": 1, "kind": "leo.follow.export", "leo_version": "v0.64.0",
  "home_id": "<hex>", "revision": "sha256:…",
  "templates": {"<name>": {<yaml keys>}}, "environments": {"<name>": {"K":"v"}},
  "delegation": {"enabled":…, "active_profile":…, "roles":{…}, "hide_native_agents":[…], "profiles":{"<n>":{…}}} | null,
  "defaults": {<yaml keys>} }
```
- **Content.** The four sections are the lead's effective config, with `templates_file` already merged. They are taken from an **unexpanded** load, so a `~` is copied exactly as written.
- **Encoding.** Sections go from YAML nodes to JSON and keep their types: ints stay ints and bools stay bools. The follower reads them back with `json.Decoder.UseNumber`, converts to YAML, and decodes with the existing types. This keeps `RoleTarget`'s shorthand form (`delegation.go:113`) and `harness_options`.
- **What the export refuses.**
  - `lead_is_follower`: the lead has its own `follow:` (chains are a non-goal).
  - `lead_config_invalid`: the lead's own config fails Validate.
  - The export never reads `client.*` or `LEO_HOST`. It doesn't resolve a host at all.
- **Versioning.**
  - `schema` is an int, and the follower supports 1. Adding fields doesn't bump it.
  - If the export's `schema` is newer than the follower supports, the error is `follower_too_old: schema N`.
  - A lead that predates the command fails with cobra's `unknown command` or `unknown flag`. The follower then runs `<leo> version` over the same ControlMaster and records `lead_too_old: <version>`.
- **home id.** `<state>/home-id` is 16 random bytes in hex, mode 0600, created on first read. The follower rejects a pull whose `home_id` equals its own, with `self_follow`. Validate also rejects a loopback `ssh` target.

**Cache files** (both written through `writeFileAtomicWith`, mode 0600, under `config.StatePath()`)
- `follow-cache.json`:
  ```json
  {"schema":1,"host":"dionysus","ssh":"evan@10.0.2.10","pulled_at":…,"lead_revision":…,"lead_version":…,"lead_home_id":…,"digest":"sha256:<canonical JSON of the 4 sections>","export":{verbatim}}
  ```
  - It is written only when `digest` or `lead_revision` changes.
  - Load ignores a cache whose `host` isn't the current `follow.host`, and warns.
- `follow-status.json`: `{"last_attempt","last_error","last_error_code"}`. It is written on every attempt, so a failed pull leaves the cache literally unchanged.
- **Error codes:** `unreachable` (ssh exits 255 or times out), `lead_too_old`, `lead_is_follower`, `lead_config_invalid`, `self_follow`, `malformed`, `follower_too_old`.
- **Stale** means `now - pulled_at > 3 × interval`. The clock is passed in.

**Layering, applied in `applyFollowLayer()` after `loadSharedTemplates`** (lowest layer first: lead, then shared, then local)
- **Templates.** Start from the lead map. Shared entries replace lead entries by name (already merged by #305), then local entries replace both.
  - Sources: `lead`, `shared`, `local`, `local-override` (local over shared, unchanged from #305), `local-override-lead`, `shared-override-lead`.
  - Labels: `lead`, `local · overrides lead`, `shared · overrides lead`, `lead · invalid`. A local entry over both layers is labelled after the nearest one.
  - Lead templates go through `expandTemplatePaths` on the follower.
- **Environments and `delegation.profiles`.** Lead entries are replaced by local entries by name. Fields are never merged.
- **`defaults` and delegation scalars** (`enabled`, `active_profile`, `roles`, `hide_native_agents`) merge key by key, at the node level.
  - A local key wins only when it is present *and* not empty. Empty means `""`, `0`, null, `{}` or `[]`.
  - The emptiness rule is needed because `Save` always writes `model: ""` and `max_turns: 0` (`config.go:247-248` have no omitempty).
  - Depth follows D2. The keys set locally at load time are recorded.
- **Bookkeeping.** The unexported `Config.follow *followLayer` holds:
  - the host and cache metadata;
  - the lead sections, both unexpanded and as an expanded baseline;
  - the per-kind sources;
  - the local key sets;
  - `invalid[kind][name] = reason` and `invalidKeys`.

  Like `shared`, it is a pointer, so shallow copies keep it.

**Invalid isolation**
- For each lead entry, Load runs the per-entry validators (extracted from Validate) and the D4 probes.
- An entry that fails is moved out of the effective map and into `invalid`, so nothing can use it by accident.
- Existence checks treat an invalid lead name as known. This prevents a cascade where one bad entry makes everything that references it fail too.
- Every lookup returns `*InvalidEntryError{Kind, Name, Lead, Reason}`.
- Local entries go through Validate unchanged and still fail loudly.

**Ownership**
- `(*Config).CheckLeadOwnership()` compares every lead entry that is not overridden against its baseline.
  - An entry that is missing (and not invalid) gives `OwnedByLeadError{Op: "delete"}`.
  - An entry that differs gives `OwnedByLeadError{Op: "edit"}`.
  - Renames show up as a delete of the old name.
- `Save` calls it first, as the defence that covers every caller. `Mutate` and `saveConfig` call it before Validate, so the user sees `owned_by_lead` and not some validation error.
- `OverrideLocally(kind, name)` flips the entry's source to local so that `Save` writes it to leo.yaml.

**Revision**
- With no follow layer: unchanged.
- With one: `sha256(sha(leo) ‖ sha(shared or empty) ‖ cache.digest)`.
- Writing the status file never changes it.

## Steps
Each step is test-first: write the named failing test, watch it fail, implement, re-run. Mock only the ssh/exec runner and the D4 probe (LookPath/Stat). One commit per step.

1. **`internal/remote` (merge point).**
   - Create `internal/remote/ssh.go` with `ControlOpts` and `SSHArgv(res, batch, tail...)`, byte-identical to `feat/agent-move`.
   - Move `remoteLeoWord` and `shellQuoteArg` to a new file, `internal/remote/quote.go`, as `LeoWord` and `Quote`, so they don't collide with agent-move's file.
   - `cli.buildSSHArgs` and `sshControlOpts` delegate to the new package.
   - Test first: `remote/ssh_test.go` covers the argv order and that batch mode prepends `-o BatchMode=yes`. `quote_test.go` covers `$HOME/` and `~/` prefixes, spaces, `'` and `$()`.
   - Verify: `go test -race ./internal/remote ./internal/cli -run 'SSH|Quote|Remote'`.
2. **The `follow:` block** (`internal/config/follow.go`; `Config.Follow *FollowConfig`).
   - `FollowConfig{Host, Interval}` and `(*Config).FollowInterval()`, which defaults to 5m.
   - Validate requires a host, requires it to be in `client.hosts`, requires an interval of at least 30s, and rejects a loopback ssh target.
   - Test first: a `follow_test.go` table, plus a check that existing fixtures without `follow` are unaffected.
   - Verify: `go test -race ./internal/config -run Follow`.
3. **Home id** (`config/homeid.go`: `HomeID(statePath) (string, error)`).
   - Test first: the id is stable across calls and the file is 0600.
4. **Export.**
   - `config/follow_export.go`: `BuildFollowExport(path) (FollowExport, error)`. It uses an unexpanded load: give `parse` and `loadSharedTemplates` an `expand bool` option.
   - Add the YAML-to-JSON codec (`yamlnode_json.go`).
   - CLI: `leo config export --follow` in `cli/config.go`. It finds the config through `cfgFile` or `FindConfig` and never calls `ResolveHost`.
   - Test first:
     - The export includes `templates_file` entries, keeps `~` verbatim, and omits `tasks`, `web`, `client`, `api_clients` and `chat`.
     - ints, bools, `harness_options` and the RoleTarget shorthand round-trip.
     - It refuses `lead_is_follower`.
     - CLI: with `client.default_host` set to a remote and `LEO_HOST` set, the export still runs locally. The `agentExecCommand` stub records no call.
   - Verify: `go test -race ./internal/config ./internal/cli -run 'Export'`.
5. **Cache and status files** (`config/follow_cache.go`).
   - `Read`/`WriteFollowCache`, `Read`/`WriteFollowStatus`, and `Stale(now, interval)`.
   - Test first:
     - A failed write (through the `writeTemp` seam) leaves the old file in place.
     - The result is 0600 even when the old file was 0644.
     - Exactly 3 × interval is not stale; one second more is.
     - A cache for another host is ignored.
6. **Layering** (`config/follow_layer.go`, hooked into `LoadWithRevision` after the shared merge).
   - Test first, one test per rule:
     - Replace by name, for templates, environments and profiles.
     - No field merge.
     - Lead under shared under local, with all of its sources.
     - A defaults key overrides; a `dispatch.max_concurrent` sub-key overrides (D2); an empty local value doesn't override.
     - A local `active_profile` combines with lead profiles.
     - With no cache: local config only, plus a warning.
     - Without `follow`: a deep-equal match with today on the existing fixtures.
   - Verify: `go test -race ./internal/config -run 'Layer|Follow'`.
7. **Invalid isolation.**
   - Pure refactor first: extract `validateTemplate` (`config.go:670-719`), `validateEnvironment` (`environments.go:115-122`) and `validateProfile` (`delegation.go:264-302`). Validate calls them, and the existing suite must stay green.
   - Then add isolation with the probe seam `followProbe{LookPath, Stat}`.
   - Add `LookupTemplate`, `LookupEnvironment` and `LookupProfile`, which return `UnknownTemplateError` or `InvalidEntryError`. Replace the direct map reads at `manager.go:490,813,843`, `target.go:35` and `environments.go:45`. Make the existence checks at `environments.go:137`, `delegation.go:~285` and `config.go:1310` aware of invalid entries.
   - Test first:
     - A lead template with an unregistered harness is marked invalid while Validate still passes.
     - A local template with the same problem still fails Validate.
     - A missing binary or env dir (D4, probes faked) marks the entry invalid.
     - A persistent task and a spawn that reference an invalid entry error with its reason.
     - A lead profile that references an invalid lead template doesn't cascade.
     - A bad lead `defaults.harness` drops the key (D3).
8. **Save, ownership and override** (`config/follow_save.go`).
   - `localView(cfg)` is used by `Save` (`config.go:895`) and by `marshalLeo` (`shared_templates.go:506`).
   - Add `CheckLeadOwnership`, `OverrideLocally(kind, name)` and `InheritFollow` (setup, `setup.go:252`).
   - Test first:
     - Load and save with no edits gives a byte-identical leo.yaml with no lead entries in it. Check each section.
     - Editing, deleting or renaming a lead template, environment or profile fails with `OwnedByLeadError`, and the file is unchanged.
     - Renaming a local template that a lead profile references fails, and the error names the profile.
     - Override then edit writes the entry to leo.yaml with source `local-override-lead`.
     - Deleting the override brings the lead entry back.
     - Editing a lead defaults key writes it as a local key (D1).
     - Invalid entries are never written.
     - A setup re-run keeps the layer.
9. **Revision.**
   - Test first: the revision changes with the cache digest and stays the same across status-file writes. The existing exact-digest tests stay green.
10. **Puller** (new package `internal/follow`, `pull.go`).
    - `Runner` interface: `Run(ctx, name, args) (stdout, stderr, err)`.
    - `Pull(ctx)` runs `ssh` with `remote.SSHArgv(res, true, "-o", "ConnectTimeout=10", …tail)`, where the tail is `remote.LeoWord(leo_path) config export --follow`. It has a 30s context and an 8 MiB stdout cap, then classifies the result, compares digests, and writes the cache and status files.
    - Tests use a fake Runner and assert the argv:
      - the exact argv, including BatchMode, ControlPath, `ssh_args`, and a `leo_path` with a space or a `$HOME/` prefix;
      - the target is `follow.host` even when `default_host` names another host;
      - each error code leaves the cache bytes unchanged;
      - an unchanged export gives `Changed=false` and no write;
      - `lead_too_old` makes a second call to `version`.
    - Verify: `go test -race ./internal/follow`.
11. **Loop and daemon** (`follow/loop.go`).
    - `Loop{Puller, Reloader, Clock, After}` with `Run(ctx)`, `SyncNow(ctx)` (single-flight) and `Kick()`. `Kick` re-reads the follow config and pulls only when the host changed or follow was just turned on.
    - Wiring:
      - `go loop.Run(ctx)` next to `service/process.go:1133`.
      - `Server.SetFollower`.
      - `ReloadConfig` calls `Kick` (`server.go:663`).
      - Socket routes `POST /follow/sync` and `GET /follow/status`.
    - Tests with a fake `After` and clock:
      - it pulls at start and on each tick;
      - an interval change takes effect;
      - `SyncNow` during a pull that's already running shares its result;
      - a changed pull calls `ReloadConfig` exactly once, and an unchanged one doesn't call it;
      - with no `follow`, it never pulls.
12. **CLI.**
    - New `cli/sync.go` with `leo sync [--status] [--json]`. It uses the socket when the daemon is up and an in-process Puller otherwise.
    - `leo template override <name>`, `leo delegation override <profile>`, and `leo config override {template|environment|profile} <name>`.
    - The SOURCE column shows when `HasTemplatesFile() || IsFollower()`.
    - `saveConfig` checks ownership and suggests the override command.
    - `leo validate` (`validate.go:197`) and the startup warnings gain the follow warnings: no cache, stale, invalid entries.
    - D5 goes in `ResolveHost`.
    - Test first:
      - `template remove` of a lead entry fails with `owned_by_lead` and doesn't write.
      - `override` writes the entry.
      - `list` and `list --json` show the new sources.
      - `sync --status` shows the stale state through a fake clock.
      - `sync` without a daemon uses the fake runner.
      - `delegation use` works on a follower.
13. **Web, config API and `/api/v1`.**
    - Add `CodeOwnedByLead = "owned_by_lead"` (409, data `{kind, name, lead}`). `Mutate` checks ownership after applying a change and before Validate. Map `OwnedByLeadError` at `config_cores.go:84` and in `asConfigError`.
    - `itemDocument` gains `source` and `invalid` (omitempty). So does each profile in the delegation document.
    - New routes:
      - `POST /config/{resource}/{name}/override` (templates and environments)
      - `POST /config/delegation/profiles/{p}/override`
      - `GET /config/follow`
      - `POST /config/follow/sync`
    - HTML:
      - Generalize the badges (`handlers_pages.go:163`) to the template, environment and delegation pages.
      - Lead entries show an "Override locally" button in place of edit and delete.
      - The Settings card shows the host, the `pulled_at` age, the stale state, the lead revision and version, the last error, and a **Sync now** button (`POST /web/follow/sync`).
    - `/api/v1`: `observe.Template` and `observe.Environment` gain `source` and `invalid`, both omitempty and additive (this is leoterm's contract). Add a read-only `GET /api/v1/follow`.
    - Test first:
      - Each write path (`ApplyValues`, `DeleteItem`, `RenameItem`, delegation profile PUT/DELETE, environment save, and the HTML forms) returns 409 `owned_by_lead` and leaves the file unchanged.
      - The override route works.
      - The `ConfigRoutePatterns` auth tests and the schema/route goldens are updated.
      - The v1 payloads without follow are unchanged byte for byte.
    - Verify: `go test -race ./internal/web -run 'Follow|Owned|Override|V1|ConfigAPI'`.
14. **Docs.**
    - `docs/configuration/config-reference.md`: the `follow:` block next to `templates_file` (:341), with the layering rules, the trust model and D5.
    - A new `docs/configuration/follow-lead.md`: setup, cache, stale, errors, and the launchd ssh notes.
    - `docs/configuration/config-api.md`: `owned_by_lead`, the override routes, the revision now covering the cache, and `source`/`invalid`.
    - `docs/cli/config.md` (export and override), `docs/cli/template.md` (override, SOURCE values), a new `docs/cli/sync.md`, and `docs/cli/index.md`.
    - `docs/guides/remote-cli.md`: following vs `--host`. `CLAUDE.md`: a one-line architecture entry for `internal/follow`.
15. **e2e** (`e2e/follow_lead_test.go`).
    - Set up two isolated homes, each running `leo service --supervised -c`, in the pattern of `e2e/interactive_dispatch_test.go:373`.
    - The lead has templates (some from a `templates_file`), environments, delegation and defaults.
    - The follower has `client.hosts.lead{ssh: lead-fake, leo_path: <built leo>}` and `follow{host: lead}`.
    - The PATH-prepended `e2e/testdata/fakessh` (shared with agent-move: same name and the same argv parsing) changes into the lead's home and runs `sh -c`. It logs argv and fails when a toggle file exists.
    - All waits use `leo sync`, with no sleeps on an interval.
    - Assert:
      - the follower lists the lead entries as `lead`;
      - after editing the lead's leo.yaml and running `leo sync`, the follower shows the change and reloaded once;
      - an override survives a sync;
      - with the toggle file present, `leo sync` reports `unreachable`, `--status` shows it, and `template show --resolved` plus a fakeclaude spawn still work from the cache;
      - a stub `leo_path` gives `lead_too_old: v0.1.0`;
      - the argv log shows BatchMode and the quoted leo path.
    - Run in the background: `heavy env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e E2E_RUN=TestFollowLead`.
16. **Gate** (only after checking the load average): `make test`, `make lint`, then the full isolated `make e2e` once. Then a fresh-context review, using `review.concurrency` for the loop, cache and Save path.

## Live check (needs Evan's go-ahead; no production restarts)
- **Lead (Dionysus).** Put a dev build at a separate path (for example `~/leo-dev/bin/leo`). The export only reads leo.yaml, so production leo stays untouched.
- **Follower (laptop, 10.0.4.163).**
  - Run an isolated `LEO_HOME` test daemon with the dev build. Stop it by killing its PID, never with `leo service restart`.
  - Its config: `client.hosts.dionysus{ssh: evan@10.0.2.10, leo_path: ~/leo-dev/bin/leo}`, `client.default_host: localhost` (unless D5 lands), and `follow{host: dionysus, interval: 30s}`.
- **Manual setup.**
  - The ssh key must work with BatchMode from the daemon's environment. Check with `env -i HOME=$HOME PATH=$PATH ssh -o BatchMode=yes evan@10.0.2.10 true`.
  - Leo.app needs its Local Network permission if production is used later.
  - The laptop's `CLAUDE_CONFIG_DIR` directories must exist. Any that don't will show `lead · invalid`, which is the expected result.
- **Checks.**
  - Edit a template on Dionysus; it appears on the laptop within 30s.
  - A laptop override survives a pull.
  - Make Dionysus unreachable: Evan turns Wi-Fi off, or blocks 10.0.2.10 with pf. Then `leo sync --status` shows `unreachable` and a spawn from a lead template still works.

## Other risks
- `Load` now also reads and decodes the cache and runs the probes on every call, at about 39 call sites. The files are a few KB, so this is cheap, but it should be measured in step 6.
- yaml.v3 tolerates unknown keys, so a template field that only a newer lead understands is silently dropped on an older follower. Plan: surface it as a `lead_version` greater than the local version warning, not an error.
- The daemon's ssh runs under launchd. It needs a key that works with BatchMode and no prompt, a reachable `SSH_AUTH_SOCK`, and Leo.app's Local Network grant on the laptop.
- `localView` must strip every lead entry on save. If it misses one, a lead entry is quietly written into the follower's `leo.yaml` as a local override. Step 7 has a round-trip test for each section to catch this.
