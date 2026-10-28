# Plan: secret-sourced env values

Spec: `docs/specs/2026-10-28-secret-env-values.md` (source of truth; where this plan and the spec disagree, the spec wins). Plan against `feat/secret-env` @ 28d0c61e (main 9c82e1e1 + spec).

## Design in one screen
- **Representation.** `config.EnvValue` (literal string *or* `secretenv.Ref`) and `config.EnvMap map[string]EnvValue` replace `map[string]string` in `TemplateConfig.Env`, `TaskConfig.Env` and `Config.Environments`. Custom YAML/JSON codecs: a literal marshals as exactly the string it is today, so every ref-free config, `config show --json`, follow export and config-API read is byte-identical. A ref marshals as the mapping from the spec. `defaults` has no `env:` key today; it is covered through `defaults.environments`. No `defaults.env` is added.
- **Resolution.** Resolution lives in `config.Expander`, which every spawn path already carries: the agent manager's `m.env`, `consult.Dispatcher.Env` and the interactive runtime's `Env`, and `run.RunWith(x)`. A new field `Secrets SecretResolver` is nil in production, and nil means `secretenv.System{}`, the same zero-value pattern as `Lookup`/`Home`. Tests inject fakes. Config structs always hold refs. Only the maps returned by `ResolveNamed`/`ExpandLiteral`/`ResolveEnv` carry values, so each spawn resolves exactly once, at the point it already expands `$VAR`.
- **Backends** live in a new leaf package `internal/secretenv` with injected exec/fs/env seams (struct fields, no package globals) and a 10 s per-resolution timeout.
- **Never-persist rule.** Resolved values exist only in the in-memory launch env: tmux `-e`, the exec env and the supervisor's `ProcessSpec`. agentstore persists refs (Q1).

## Code map
- Types: `config/config.go:108` (Environments), `:401` (TaskConfig.Env), `:425` (TemplateConfig.Env). Validation: `validateTemplate` `:668` (env keys `:703`), the task loop `:839`, and `validateEnvironment` in `environments.go`. `Save` is at `:1010`.
- Expansion: `config/envexpand.go`. `Expander` is at `:23`, `expandMap` at `:178` and `scoped` at `:190`. The layering is in `config/environments.go`: `ResolveNamed`, `ExpandLiteral`, `ResolveEnv`, `TaskEnv`, `TemplateEnv`, `DispatchEnv`, `EnvironmentWarnings` and `EnvironmentKeys`.
- Agent spawn/rewire: `spawnShared` is at `agent/manager.go:595`, with resolution at 596–603 and the record write at 693. `spawnWorktreeCore` is at `:887`, with resolution at 888–895 and the record write at 1013. Rewire sites are `Start` `:1245` (re-resolves at 1289, writes 1320), `Reset` `:1344` (reuses `rec.Env` at 1399–1418), `Restart` `:1448` (1486–1526) and `resolveTemplateWiring` `:1665`. `handleForRecord` is at `:1913`, `StaleAgents`/`diffWiring` at `agent/stale.go:48/86`, and boot restore at `service/agents.go:94/215-234` (uses stored `rec.Env`; `markFailedRestore` is at `:329`).
- Tasks: `run/runner.go:233` (`TaskEnv`), `:419`, and the history and notify paths. Persistent tasks go `config/target.go:13` → agent spawn. `cli/run.go:60,104` is the dry-run and `:81` the CLI run.
- Dispatch: `consult/consult.go:385`, `consult/interactive_runtime.go:144` (env → tmux `-e` at `:383`) and `consult/continuation.go:78`.
- Display: `cli/config.go:83` (`config show`), `cli/template.go:491,505,553`, `daemon/handlers.go:185` and `web/handlers_agents.go:238` (keys only), and `redact/redact.go`.
- Web: in `web/schema/`, kind detection is `schema.go:155`, `envJSON` and `ApplyEnvPatch` are in `env.go:23/55`, `json.go:265,459,554` and `values.go:59,122,225,244`. Also `web/config_patch.go:105`, `handlers_environments.go:48` and `config_resources.go:189-203`. Docs are in `docs/configuration/config-api.md` (§Secrets).
- Writers: `config/shared_templates_splice.go` has `encodeChangedEntry` `:198`, `mergeMapping` `:236` and `mergeValue` `:297`. Also `follow_save.go`.
- Follow: `config/follow_export.go` has the schema at `:16`, `FollowExport.Environments` at `:60`, the build at `:89`, `decodeSections` at `:150-175` and `Digest` at `:198`. `follow_invalid.go` has validation at `:86,98` and `probeConfigDirs` at `:158`. The cache is in `follow_cache.go` (typed JSON).
- Validate: `cli/validate.go:199-207` (`configWarningFindings`).
- Literal-value readers that must skip refs: `cli/doctor_guard.go:149` and `follow_invalid.go:158`.

## Steps
Every verify command runs from the worktree. Each step commits on its own.

### 1. `internal/secretenv`: refs, validation, backends
Interfaces:
```go
type Backend string // keychain | secret-service | systemd-creds | file | command
type Ref struct {
    Backend    Backend           `yaml:"secret" json:"secret"`
    Service    string            `yaml:"service,omitempty" json:"service,omitempty"`
    Account    string            `yaml:"account,omitempty" json:"account,omitempty"`
    Attributes map[string]string `yaml:"attributes,omitempty" json:"attributes,omitempty"`
    Name       string            `yaml:"name,omitempty" json:"name,omitempty"`
    Path       string            `yaml:"path,omitempty" json:"path,omitempty"`
    Argv       []string          `yaml:"argv,omitempty" json:"argv,omitempty"`
    Unknown    []string          `yaml:"-" json:"-"` // decoded keys no backend reads
}
func (r Ref) Validate() error   // unknown backend, unknown/missing/empty keys; no I/O
func (r Ref) String() string    // deterministic descriptor: keychain(service=X, account=evan)
type Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)
type System struct {
    GOOS string; Run Runner; Getuid func() int
    LookupEnv func(string) (string, bool); Timeout time.Duration // zero values → OS defaults, DefaultTimeout = 10s
}
func (s System) Resolve(ref Ref) (string, error) // errors are *ResolutionError
type Describe struct{}                            // Resolve → "<secret "+ref.String()+">", no I/O
type ResolutionError struct{ Ref Ref; Reason string }
```
Rules:
- **keychain** (darwin): `security find-generic-password -s S [-a A] -w`. Strip the single trailing `\n` the tool prints.
- **secret-service** (linux): `secret-tool lookup k1 v1 k2 v2…`, with keys sorted.
- **systemd-creds** (linux): `$CREDENTIALS_DIRECTORY/<name>`. `name` must be a single path element.
- **file:** the path must be absolute after the caller expands it. It must be a regular file with `perm&^0o600 == 0`, owned by `Getuid()`.
- **command:** the default Runner uses `exec.CommandContext` with no shell, empty stdin, the daemon's env and `WaitDelay` set.
- **Output:** strip trailing `\n`/`\r\n` from file, command and systemd-creds output (Q5). Output is capped at 64 KiB, and a NUL byte is an error.
- **Errors:** each error names the reason only. Keychain exit 44 reads "item not found". Other keychain or secret-service failures give the exit code plus the first stderr line, at most 200 bytes. A command failure gives the exit code only. A timeout reads "timed out after 10s". An unsupported platform reads "not supported on linux". stdout never appears in an error.

Starts from (`secretenv_test.go`, failing):
- Table tests per backend assert the **exact argv** through a fake Runner.
- Platform gating via `GOOS`.
- File checks use real `t.TempDir` files: 0600 ok, 0400 ok, 0640 refused, a directory refused, a foreign owner refused via a fake `Getuid`.
- systemd-creds uses a real temp dir plus a fake `LookupEnv`, and a missing `CREDENTIALS_DIRECTORY` errors.
- Timeout uses a fake Runner that blocks on `ctx.Done()` with `Timeout=time.Millisecond`.
- Sentinel test: the fake prints `S3CR3T-SENTINEL` and exits 1, and `err.Error()` lacks it.
- One real-exec test of the default Runner: `command` with `/bin/sh -c 'printf "v\n"'` gives "v", and `exit 3` gives "exited 3".
- `Validate` table: every missing or unknown key per backend.

Verify: `env -u TMUX go test -race ./internal/secretenv/`

### 2. `config.EnvValue`/`EnvMap`: type, codec, validation (mechanical migration)
```go
type EnvValue struct{ literal string; ref *secretenv.Ref }
func Literal(s string) EnvValue; func Secret(r secretenv.Ref) EnvValue
func (v EnvValue) Ref() (secretenv.Ref, bool)
// yaml.(Un)Marshaler, json.(Un)Marshaler: any scalar (null → "") ↔ string;
// mapping ↔ Ref, lenient (unknown keys → Ref.Unknown, reported by Validate); sequence → decode error
type EnvMap map[string]EnvValue
func LiteralEnv(m map[string]string) EnvMap
func (m EnvMap) Keys() []string; func (m EnvMap) Literals() map[string]string; func (m EnvMap) Refs() map[string]secretenv.Ref
```
- Switch the three config fields to the new types. Also switch `leadSections.environments`, `FollowExport.Environments`, the `Digest` struct and `schema.EnvironmentEntry.Env`.
- Wire `Ref.Validate` into the template env loop, the task env loop and `validateEnvironment`. Errors read `templates.X.env.KEY: secret: <reason>`.
- `doctor_guard.go:149` and `probeConfigDirs` read `Literals()`. The `redact.Keys` callers use `Keys()`.
- Until step 3, `expandMap` fails closed on a ref with a `*SecretResolutionError` (reason "not resolvable").
- Fixtures: about 108 test sites change from `map[string]string{…}` to `config.LiteralEnv(…)`. The compiler drives the change, and no assertion changes.

Starts from (`config/envvalue_test.go`):
- Decode literal, number, bool, null, mapping and sequence.
- YAML and JSON round trips: a literal gives a JSON string, a ref gives a JSON object.
- Validate errors with their dotted paths.
- Golden test: `Load`→`Save` of an existing ref-free fixture is byte-identical.

Verify: `go build ./... && go vet ./... && env -u TMUX go test -race ./internal/config/ ./internal/agent/ ./internal/cli/ ./internal/run/ ./internal/consult/ ./internal/web/...`

### 3. Resolve through `Expander` on every spawn path
```go
type SecretResolver interface{ Resolve(ref secretenv.Ref) (string, error) }
type Expander struct{ Lookup LookupFunc; Home func() (string, error); Secrets SecretResolver } // nil → secretenv.System{}
type SecretResolutionError struct{ Scope, Key string; Ref secretenv.Ref; Reason string }
// "<scope>.<key>: secret <ref.String()>: <reason>"
```
- `expandMap(scope, EnvMap)` passes literals through `Expand` unchanged.
- For a ref, it expands `Path` with x (`~`, `$VAR`), then resolves. A resolved value is never `$`-expanded.
- `scoped()` gains a case for the new error.
- Every ref in every selected layer resolves, which matches `$VAR` (Q4).
- `EnvironmentWarnings` skips refs.
- The spawn layer (`--env`) stays `map[string]string`.
- No new plumbing: agents, tasks, persistent tasks and dispatches already pass an Expander (code map).

Starts from (fake resolver returning the sentinel):
- **config:** `ResolveEnv` resolves named and literal refs. On failure it returns `*SecretResolutionError` with scope, key and ref, and the sentinel never appears in it. `file` `~` is expanded before `Resolve`.
- **agent:** using the `manager_env_expand_test` pattern, the spawn hands the value to the supervisor spec. A failing resolver gives a spawn error, releases the name reservation and writes no record.
- **run:** `RunWith` with a fake `execCommand` puts the value in the child env. A failing resolver gives an error naming the ref, no exec, and a history row with reason failure. The task log and history file never contain the sentinel.
- **consult:** in `interactive_runtime_test`, the tmux `-e` args carry the value. A failure fails only that dispatch, and continuation re-resolves.
- **persistent task:** `ResolveTaskTarget` carries refs into the template.

Verify: `env -u TMUX go test -race ./internal/config/ ./internal/agent/ ./internal/run/ ./internal/consult/`

### 4. agentstore holds references, never values (Q1)
```go
// agentstore.Record
EnvSecrets map[string]secretenv.Ref `json:"env_secrets,omitempty"` // these keys are absent from Env
// agent
func secretLayer(names []string, cfg *config.Config, literal config.EnvMap, overrides ...map[string]string) map[string]secretenv.Ref
func persistedEnv(env map[string]string, secrets map[string]secretenv.Ref) map[string]string
func hydrateEnv(rec agentstore.Record, x config.Expander) (map[string]string, error)
// service
func WithRestoreExpander(x config.Expander) RestoreOption
```
- Spawn record writes (693, 1013), `Start` and `Restart` persist `persistedEnv` plus `EnvSecrets`. The launch spec keeps the full env.
- `Reset` and boot restore call `hydrateEnv`. A boot-restore failure goes through `markFailedRestore` with the error text, and other agents still restore.
- `StaleAgents` resolves with `secretenv.Describe` and compares secret keys by descriptor against `rec.EnvSecrets`, with no backend I/O.
- `handleForRecord` keeps `rec.Env` without secrets. Drivers read only config-dir keys; document this.

Starts from:
- After a spawn with a ref, the `agents.json` bytes lack the sentinel and contain `env_secrets`, and the `ProcessSpec` env has the value.
- Boot restore with a fake resolver puts the value in the spawn spec. A failing resolver leaves the record `Stopped` with a reason naming the key and ref, and the other records spawn.
- `Reset` re-adds the secret.
- `StaleAgents` reports no drift for an unchanged ref, and the resolver is never called (it fails the test if called). A changed ref gives `EnvChanged [KEY]`.

Verify: `env -u TMUX go test -race ./internal/agent/ ./internal/agentstore/ ./internal/service/`

### 5. CLI display surfaces never resolve
- Add `redact.EnvDisplay(literals map[string]string, refs map[string]secretenv.Ref) map[string]string`. Literals go through `redact.Value`, and refs display as `<secret …>`.
- `cli/template.go:491,505,553` (literal and effective views) uses it.
- The `cli/run.go:60,104` dry-run uses `Expander{Secrets: secretenv.Describe{}}`.
- `config show` gets the change for free from the codec.

Starts from:
- `config show` (YAML and `--json`) prints the ref mapping.
- The dry-run, with a resolver that calls `t.Fatal` if used, prints `<secret keychain(service=X)>`.
- `template show` (text and JSON) shows the descriptor.

Verify: `env -u TMUX go test -race ./internal/cli/ ./internal/redact/ ./internal/daemon/`

### 6. Web UI and config API keep references (Q3)
**API read.** A ref entry reads as `{"set": true, "ref": {…}}`, whatever its key. Literal entries are unchanged.

**API patch:**
- `{"set": true}` keeps the current entry, literal or ref.
- `{"ref": {…}}` sets a ref. An invalid ref gives a 422 on `env`.
- A string sets a literal.
- `null` deletes the entry.

**Form:**
- The textarea lists literals only. Refs show as a read-only list beneath it.
- On submit, refs the textarea doesn't name are carried over.
- A `KEY=` line for a ref key replaces that ref with a literal.

Files: the `web/schema` sites in the code map, `config_patch.go`, `handlers_environments.go`, `config_resources.go`, the env-field template partial, and the §Secrets section of `docs/configuration/config-api.md`.

Starts from:
- GET of an environment, a template and a task with a ref returns that shape.
- An echo-back PATCH leaves the ref bytes in `leo.yaml` unchanged.
- A PATCH with an invalid ref gives a 422.
- A form POST without the ref key keeps the ref.
- The rendered HTML shows the descriptor.
- No web path reaches a resolver.

Verify: `env -u TMUX go test -race ./internal/web/...`

### 7. Writers round-trip references
Covers `config.Save`, the `templates_file` splice writer (`mergeValue` must handle a scalar↔mapping kind change) and `follow_save`.

Starts from:
- `Load`→`Save` keeps the ref.
- Editing another field of a shared template leaves the env ref block and its comments byte-identical.
- Splicing literal→ref and ref→literal both work.
- A follower's local override holding a ref survives a save.

Verify: `env -u TMUX go test -race ./internal/config/`

### 8. Follow-lead carries references only (Q7)
- `BuildFollowExport` emits `schema: 2` if and only if a synced section contains a ref. Parsing accepts schema 2 or lower.
- On a follower, `validateEnvironment`/`validateTemplate` mark a bad ref `lead · invalid` for that entry only.
- A platform mismatch never invalidates an entry; it fails only at resolution.

Starts from:
- An export with refs contains the ref object and `schema: 2`.
- A ref-free export is byte-identical to today's (`schema: 1`).
- `ParseFollowExport` round-trips refs.
- The cache file holds the ref.
- With `GOOS=linux`, a lead keychain ref stays valid.
- A lead env with an unknown backend becomes invalid while the other entries still apply.

Verify: `env -u TMUX go test -race ./internal/config/ ./internal/follow/`

### 9. `leo validate` resolves every reference (Q6)
```go
type ScopedRef struct{ Scope, Key string; Ref secretenv.Ref }
func (c *Config) SecretReferences() []ScopedRef   // effective config: environments, templates.*.env, tasks.*.env; sorted
func secretFindings(cfg *config.Config, x config.Expander) []Finding // cli; Expander built in RunE
```
- Identical refs resolve once per run.
- A failure is an ERROR finding naming the scope, key and ref.
- An unsupported platform, or systemd-creds with `CREDENTIALS_DIRECTORY` unset, is a WARN.

Starts from:
- Fake resolver cases ok, fail and unsupported produce the right findings.
- Text and `--json` output never contain the sentinel.
- Resolver call counts show the dedupe.

Verify: `env -u TMUX go test -race ./internal/cli/ ./internal/config/`

### 10. Docs
Edit `docs/configuration/environments.md` (or a new `secret-env.md` linked from `config-reference.md`). Cover:
- syntax, the backends and the platform rules
- failure semantics
- rotation: it takes effect on the next spawn, restart or boot restore, and running agents keep their env
- keychain items: create them with `security add-generic-password -s S -a A -w -T /usr/bin/security`, which avoids an ACL prompt that would hit the timeout
- `command` under launchd: argv[0] must be an absolute path because of the minimal PATH
- systemd: an example using `LoadCredentialEncrypted=`

Docs only; no test.

### 11. Integration gate
1. Check the load average.
2. Run `heavy make test` (in the background) and `make lint`.
3. Run `env -i HOME=$HOME PATH=$PATH TMUX_TMPDIR=$(mktemp -d) make e2e`. The e2e build tag matters here, and running it this way keeps the env from leaking into the live daemon.
4. Get a fresh-context **review.security** with the severity bar stated in the prompt.
5. Live on Dionysus, using an isolated `LEO_HOME` test daemon. Never `leo service restart`; kill the test PID instead.
   - Add keychain item `leo-secret-env-test`.
   - Spawn an agent from a template with the ref; `tmux show-environment` has the value.
   - Run a task whose prompt reports `${#KEY}`; the length matches.
   - Delete the item. The spawn fails with `templates.X.env.KEY: secret keychain(service=leo-secret-env-test): item not found`.
   - `grep -rF <value> $LEO_HOME` finds nothing.
   - `leo validate` reports the missing item, and `leo config show` shows the ref.

## Redaction checklist
| Surface | Step | Covered by |
|---|---|---|
| Logs (daemon, supervisor, task log) | 3, 4, 11 | run test: the task log lacks the sentinel; restore failure text names the ref; live `grep -rF` of `$LEO_HOME` |
| `leo config show` (YAML, JSON) | 2, 5 | show prints the ref mapping; no resolver is reachable from `config show` |
| Web UI | 6 | the env form renders the descriptor; no resolver is called |
| Config API | 6 | GET/PATCH shapes; echo-back keeps the ref; invalid ref gives 422 |
| History | 3 | `RunWith` success and failure: the history row lacks the sentinel |
| Error messages | 1, 3 | `ResolutionError`/`SecretResolutionError` exclude stdout (and command stderr); sentinel tests |
| Follow export | 8 | export JSON carries the ref object; the build path has no resolver |
| Follow cache | 8 | cache bytes contain the ref only |
| agents.json (beyond the spec list) | 4 | agentstore bytes lack the sentinel |
| dry-run, `template show`, stale-agent prompt | 4, 5 | descriptor or keys only; the resolver calls `t.Fatal` if used |
| `leo validate` output | 9 | sentinel absent from text and `--json` |
| /api/v1, agent list, template list | — | names and keys only today (`observe/snapshot.go:157`, `agent/manager.go:365`); unchanged |

## Risks
- **Fixture churn in step 2** (about 108 test sites). It is mechanical, but it touches most packages. Land it as one commit with no behaviour change.
- **Old follower, new lead.** Schema 2 gives a clean `follower_too_old` instead of a confusing decode error (Q7).
- **Downgrade.** An older leo restoring a record with `env_secrets` launches the agent without those keys. Call this out in the release notes.
- **Process-level exposure is unchanged.** Values reach tmux as `-e K=V` argv and stay visible to `tmux show-environment`, exactly as literal values do today (`service/process.go:2155`). This is an accepted risk and outside the spec.
- **launchd context.** A keychain ACL prompt or a locked keychain in the search list ends in a timeout or a clear error, never a hang. `command` runs with a minimal PATH. The step 10 docs and the live check cover both.
- **Mocked exec seams hide argv bugs** (a past incident). The real-exec Runner test and the Dionysus live run are mandatory.
- **Resolution cost.** Each ref costs one exec per spawn. `StaleAgents` resolves nothing, and `validate` dedupes.
- **`$TMUX` masks unstubbed seams.** Always run tests under `env -u TMUX`.

## Open questions (recommended answer in bold)
- **Q1. Does agentstore keep resolved values?** The spec doesn't list `agents.json`. **No.** Persist refs in `env_secrets` and re-resolve on boot restore and reset (step 4). Otherwise every agent's token sits in plaintext in `state/agents.json`, and a rotation is missed on boot restore. If declined, drop step 4.
- **Q2. Do crash-restarts of a supervised session re-resolve?** **No.** They reuse the env resolved at spawn, start, restart or boot restore, as `$VAR` expansion does today. A revoked token then needs `leo agent restart`.
- **Q3. How do the config API and web UI show a ref?** **As in step 6.** `{"set": true, "ref": {…}}` is a superset of today's `{"set"}` shape, so existing clients keep working when they echo it back. The web form shows refs read-only and preserves them. Refs are edited in YAML or through the API.
- **Q4. Do shadowed refs (a key overridden by a later layer) resolve?** **Yes, matching `$VAR`.** That is simpler, and it agrees with `validate`. Cross-platform configs swap whole environments through local overrides, not single keys.
- **Q5. Does systemd-creds trim trailing newlines?** **Yes, like `file`.** A credential written with `echo` would otherwise carry `\n`.
- **Q6. What severity does `leo validate` use?** **ERROR for a failed resolution. WARN for an unsupported platform, or for systemd-creds with `CREDENTIALS_DIRECTORY` unset (validate runs outside the unit).**
- **Q7. When is the follow export schema bumped?** **Only when the export contains a ref.** Ref-free leads stay compatible with old followers.

## Decisions (orchestrator, 2026-10-28)
- Q1, Q2, Q3, Q5, Q6, Q7: accepted as recommended.
- **Q4 overridden: shadowed refs are NOT resolved.** Only the effective value of each key, after all layers merge, is resolved; `leo validate` checks the same effective set. Reason: the spec says a backend on the wrong platform must not break a shared or lead config. A keychain ref that a Linux host overrides locally must never execute there or fail its spawns. Add a test: a lower layer holds a ref whose fake resolver fails, a higher layer overrides it with a literal, and the spawn succeeds with zero resolver calls for that key.
