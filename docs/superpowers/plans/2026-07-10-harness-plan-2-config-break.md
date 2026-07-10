# Harness Plan 2: Config Break — `harness` / `harness_options`, Providers Removal

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the cascading `harness:` field and adapter-validated `harness_options:` config block, migrate the claude-only flat fields into it with precise migration errors, delete the providers feature wholesale, and route all four claude argv builders through the harness registry.

**Architecture:** The `Harness` interface (Plan 1) gains `ValidateModel`, `DecodeOptions`, and `SupportsChannels`. Config gains `harness` + `harness_options` on defaults/processes/templates/tasks/sessions, with merge helpers that preserve today's cascade semantics exactly. Old flat claude fields and all provider fields become `Deprecated*` detection-only struct fields that `Validate()` rejects (yaml.v3 is lenient — deleting a field would silently ignore it, not error). The persistent-session argv builder becomes `KindSession` in the claude adapter, closing the last un-consolidated builder.

**Tech Stack:** Go, yaml.v3, existing test seams. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-07-10-harness-abstraction-design.md`

## Global Constraints

- **Byte-identical claude argv.** For any pre-break config re-expressed via `harness_options`, every builder's output must be byte-identical to today's. Characterization tests are updated to the new config shape but their argv assertions must not change.
- **Characterization polarity:** when a task says "characterization test", the test must PASS against the code as it exists before that task's rewire (write test → run → PASS → rewire → run → PASS). New functionality uses normal TDD (RED → GREEN).
- **Migration errors are precise.** Exact format for moved claude fields: `<scope>.<field> has moved to <scope>.harness_options.<field> (claude harness) — see docs/configuration/harnesses.md`. Exact format for provider fields: `<scope>.provider has been removed along with providers — see docs/configuration/harnesses.md`. For the section: `providers: this section has been removed — see docs/configuration/harnesses.md`.
- **Preserved cascade quirks** (do NOT "fix" these; they keep argv byte-identical):
  1. Sessions never inherit defaults' claude fields → `SessionHarnessOptions` does NOT merge `defaults.harness_options`.
  2. Implicit persistent-task sessions read the task's own fields without defaults cascade, and their model is `task.model` only (no fall-through to `defaults.model`).
  3. Templates default `remote_control` to `true` and ignore the defaults layer for it → `BuildTemplateArgs` re-derives RemoteControl from the template's own `harness_options` only.
- Every commit: `go test -race ./...` green, `make lint` clean. Changed packages hold ≥80% coverage.
- Commit format: `<type>: <description>` (feat/fix/refactor/test/docs/chore). No attribution lines.
- Existing validation error wording that survives must stay byte-identical (e.g. the model error text `%q is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])`).

## Task Ordering

Tasks are strictly sequential: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9. Task 7 (field renames) MUST come after Tasks 5–6 (which remove the last readers of the flat fields).

## Not In This Plan (later plans)

- codex/opencode adapters, `ParseEvents`, per-harness binary threading through runner/service exec paths (Plan 3). Until then `harness: codex` simply fails validation as unregistered — correct behavior.
- Session drivers / tmux extraction (Plan 4).
- Web UI harness dropdown + `OptionsSchema()` forms (Plan 5). Plan 2 only removes now-dead form fields so the web UI can't write rejected config.

---

### Task 1: Harness interface v2 + claude implementations

**Files:**
- Modify: `internal/harness/harness.go` (interface)
- Modify: `internal/harness/registry_test.go` (fake harness gains new methods; convert to table-driven — deferred finding from Plan 1)
- Create: `internal/harness/claude/options.go`
- Create: `internal/harness/claude/options_test.go`
- Modify: `internal/harness/claude/claude.go` (ValidateModel, SupportsChannels)
- Modify: `internal/harness/claude/claude_test.go`

**Interfaces:**
- Produces: `Harness.ValidateModel(model string) error`, `Harness.DecodeOptions(raw map[string]any) (any, error)`, `Harness.SupportsChannels() bool`; claude exports `ValidModels() []string`.
- Consumed by: Task 2 (config validation), Task 5/6 (builders).

- [ ] **Step 1: Extend the interface** in `internal/harness/harness.go`:

```go
// Harness adapts leo's neutral launch model to one coding-agent CLI.
type Harness interface {
	Name() string
	Binary() string
	Args(spec LaunchSpec) ([]string, error)
	SessionArgs(s SessionState) []string

	// ValidateModel reports whether the model name is acceptable for this
	// harness. Empty string is always valid (harness default). The error
	// text is embedded verbatim in config validation output, so phrase it
	// as `%q is not valid (…)` with no leading field path.
	ValidateModel(model string) error

	// DecodeOptions strictly decodes a harness_options map into this
	// adapter's typed options struct. Unknown keys and mistyped values are
	// errors. Runtime-only fields (MCP paths, prefixes) are left zero for
	// the caller to fill.
	DecodeOptions(raw map[string]any) (any, error)

	// SupportsChannels reports whether channel plugins can load in this
	// harness. Only Claude Code hosts channel plugins; others message via
	// leo's MCP tools.
	SupportsChannels() bool
}
```

- [ ] **Step 2: Update `registry_test.go`** — the in-test fake harness implements the three new methods (return nil / `Options{}`-like empty struct / false). While here, convert `TestRegistryGetAndNames` and `TestFallbackHelpers` to table-driven form (Plan 1 deferred finding). Keep `snapshotRegistry` and the duplicate-panic test as-is.

- [ ] **Step 3: Run harness tests to verify they FAIL to compile** (claude adapter doesn't implement the interface yet):

Run: `go test -race ./internal/harness/...`
Expected: compile error — `claude.Claude` does not implement `harness.Harness` (missing ValidateModel).

- [ ] **Step 4: Write failing tests for the claude implementations** in `internal/harness/claude/options_test.go` and `claude_test.go`. Table-driven; cover at least:

```go
func TestValidateModel(t *testing.T) {
	tests := []struct {
		model   string
		wantErr bool
	}{
		{"", false}, {"sonnet", false}, {"opus", false}, {"haiku", false},
		{"sonnet[1m]", false}, {"opus[1m]", false},
		{"gpt-5", true}, {"claude-3-opus", true},
	}
	for _, tt := range tests {
		err := Claude{}.ValidateModel(tt.model)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateModel(%q) err=%v, wantErr=%v", tt.model, err, tt.wantErr)
		}
		if tt.wantErr && err.Error() != fmt.Sprintf("%q is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])", tt.model) {
			t.Errorf("ValidateModel(%q) wrong message: %v", tt.model, err)
		}
	}
}
```

DecodeOptions table must cover: every valid key decodes to the right `Options` field (`permission_mode`, `bypass_permissions`, `remote_control`, `agent` → `AgentFile`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`); `[]any{"a","b"}` converts to `[]string`; unknown key errors and the message lists valid keys; wrong type per key errors (`permission_mode: 5`, `allowed_tools: "x"`, `remote_control: "yes"`); invalid `permission_mode` value errors with the exact existing wording (`permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)`); nil/empty map decodes to zero `Options`; a two-bad-keys map errors deterministically on the lexicographically first (decode iterates sorted keys). Also `TestSupportsChannels` asserting `true`.

- [ ] **Step 5: Run to verify FAIL**: `go test -race ./internal/harness/claude/` — expected: undefined `DecodeOptions` etc.

- [ ] **Step 6: Implement.** In `claude.go` add:

```go
// validModels is the hardcoded Claude Code model list, moved here from
// internal/config so model policy lives with the adapter.
var validModels = map[string]bool{
	"sonnet": true, "opus": true, "haiku": true,
	"sonnet[1m]": true, "opus[1m]": true,
}

// ValidModels returns the accepted model names, sorted.
func ValidModels() []string {
	names := make([]string, 0, len(validModels))
	for name := range validModels {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (Claude) ValidateModel(model string) error {
	if model == "" || validModels[model] {
		return nil
	}
	return fmt.Errorf("%q is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])", model)
}

func (Claude) SupportsChannels() bool { return true }
```

Create `internal/harness/claude/options.go`:

```go
package claude

import (
	"fmt"
	"sort"
	"strings"
)

// harness_options keys accepted by the claude adapter. These mirror the
// pre-break flat config field names one-to-one.
var optionKeys = []string{
	"agent",
	"allowed_tools",
	"append_system_prompt",
	"bypass_permissions",
	"disallowed_tools",
	"permission_mode",
	"remote_control",
}

var validPermissionModes = map[string]bool{
	"acceptEdits": true, "auto": true, "bypassPermissions": true,
	"default": true, "dontAsk": true, "plan": true,
}

// DecodeOptions strictly decodes a harness_options map into Options.
// Runtime fields (RemoteControlPrefix, MCPConfigPath, LeoMCPArgs) stay zero.
// Keys are processed in sorted order so multi-error maps fail deterministically.
func (Claude) DecodeOptions(raw map[string]any) (any, error) {
	var o Options
	keys := make([]string, 0, len(raw))
	for k := range raw {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		val := raw[key]
		var err error
		switch key {
		case "permission_mode":
			o.PermissionMode, err = stringOption(key, val)
			if err == nil && o.PermissionMode != "" && !validPermissionModes[o.PermissionMode] {
				err = fmt.Errorf("permission_mode %q is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)", o.PermissionMode)
			}
		case "bypass_permissions":
			o.BypassPermissions, err = boolOption(key, val)
		case "remote_control":
			o.RemoteControl, err = boolOption(key, val)
		case "agent":
			o.AgentFile, err = stringOption(key, val)
		case "allowed_tools":
			o.AllowedTools, err = stringSliceOption(key, val)
		case "disallowed_tools":
			o.DisallowedTools, err = stringSliceOption(key, val)
		case "append_system_prompt":
			o.AppendSystemPrompt, err = stringOption(key, val)
		default:
			err = fmt.Errorf("unknown option %q (valid: %s)", key, strings.Join(optionKeys, ", "))
		}
		if err != nil {
			return nil, err
		}
	}
	return o, nil
}

func stringOption(key string, val any) (string, error) {
	s, ok := val.(string)
	if !ok {
		return "", fmt.Errorf("option %q must be a string, got %T", key, val)
	}
	return s, nil
}

func boolOption(key string, val any) (bool, error) {
	b, ok := val.(bool)
	if !ok {
		return false, fmt.Errorf("option %q must be a boolean, got %T", key, val)
	}
	return b, nil
}

func stringSliceOption(key string, val any) ([]string, error) {
	items, ok := val.([]any)
	if !ok {
		return nil, fmt.Errorf("option %q must be a list of strings, got %T", key, val)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("option %q must be a list of strings, got %T element", key, item)
		}
		out = append(out, s)
	}
	return out, nil
}
```

- [ ] **Step 7: Run tests to verify PASS**: `go test -race -cover ./internal/harness/...` — all green, claude package coverage stays ≥80%.

- [ ] **Step 8: Commit**

```bash
git add internal/harness/
git commit -m "feat(harness): ValidateModel, DecodeOptions, SupportsChannels on the interface"
```

---

### Task 2: Config — harness fields, cascade, merged options, validation

**Files:**
- Modify: `internal/config/config.go` (struct fields, Validate hooks)
- Create: `internal/config/harness.go`
- Create: `internal/config/harness_test.go`

**Interfaces:**
- Consumes: Task 1's `harness.Get`, `harness.Names`, `h.ValidateModel`, `h.DecodeOptions`, `h.SupportsChannels`.
- Produces: `Config.DefaultsHarness() string`; `Config.ProcessHarness/TaskHarness/TemplateHarness/SessionHarness(...)  string`; `Config.ProcessHarnessOptions/TaskHarnessOptions/TemplateHarnessOptions/SessionHarnessOptions(...) map[string]any`; `DefaultHarnessName = "claude"` const. Tasks 5–6 depend on these exact names.

- [ ] **Step 1: Add fields.** In `internal/config/config.go` add to `DefaultsConfig`, `ProcessConfig`, `TemplateConfig`, `TaskConfig`, and `SessionConfig`:

```go
	Harness        string         `yaml:"harness,omitempty"`
	HarnessOptions map[string]any `yaml:"harness_options,omitempty"`
```

(Existing flat fields stay untouched in this task — they are still read by the builders until Tasks 5–6 and are renamed in Task 7.)

- [ ] **Step 2: Write failing tests** in `internal/config/harness_test.go` (table-driven). Cover:
  - `DefaultsHarness()` returns `"claude"` when unset, the set value otherwise.
  - Each scope cascade: scope value → defaults → `"claude"`.
  - Merge: defaults `{permission_mode: "plan", agent: "a.md"}` + process `{agent: "b.md"}` → merged `{permission_mode: "plan", agent: "b.md"}`; inputs not mutated (assert originals unchanged after call — immutability).
  - Different-harness scope gets only its own options: defaults `harness: claude` with options + task `harness: other` → task merged map contains only the task's own keys. (Register a stub harness in the test via `harness.Register` if needed, or set the task's harness to a name and only assert the map contents — merging logic must not require the name to resolve.)
  - Sessions: `SessionHarnessOptions` ignores defaults' options entirely (preserved quirk #1).
  - Validation: unknown harness name at defaults and per-scope → error `processes.foo.harness "codex" is not a registered harness (available: claude)`; per-scope error only fires when the scope explicitly sets a bad name (a bad defaults name errors once, at `defaults.harness`).
  - Validation: bad `harness_options` (unknown key, bad type, bad permission_mode value) at each scope → error prefixed `<scope>.harness_options: `.
  - Validation: `channels` (or `dev_channels`) set on a scope whose harness doesn't support channels → error. Test via a stub registered harness with `SupportsChannels() == false`.
  - Model delegation: `defaults.model: "gpt-5"` (no provider) errors with today's exact text `defaults.model "gpt-5" is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])`; same per scope.

Run: `go test -race ./internal/config/` — expected: FAIL (undefined helpers).

- [ ] **Step 3: Implement `internal/config/harness.go`:**

```go
package config

import (
	"github.com/blackpaw-studio/leo/internal/harness"
	// Adapters self-register in init; config validation must be able to
	// resolve them. Plan 3 adds codex/opencode imports here.
	_ "github.com/blackpaw-studio/leo/internal/harness/claude"
)

// DefaultHarnessName is the harness assumed when config specifies none.
const DefaultHarnessName = "claude"

func harnessOrDefault(scope, def string) string {
	if scope != "" {
		return scope
	}
	if def != "" {
		return def
	}
	return DefaultHarnessName
}

func (c *Config) DefaultsHarness() string { return harnessOrDefault(c.Defaults.Harness, "") }

func (c *Config) ProcessHarness(p ProcessConfig) string {
	return harnessOrDefault(p.Harness, c.Defaults.Harness)
}

func (c *Config) TaskHarness(t TaskConfig) string {
	return harnessOrDefault(t.Harness, c.Defaults.Harness)
}

func (c *Config) TemplateHarness(t TemplateConfig) string {
	return harnessOrDefault(t.Harness, c.Defaults.Harness)
}

func (c *Config) SessionHarness(s SessionConfig) string {
	return harnessOrDefault(s.Harness, c.Defaults.Harness)
}

// mergeHarnessOptions returns a new map with override entries layered over
// base. Neither input is mutated; the result is never nil.
func mergeHarnessOptions(base, override map[string]any) map[string]any {
	merged := make(map[string]any, len(base)+len(override))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range override {
		merged[k] = v
	}
	return merged
}

// scopeHarnessOptions layers defaults.harness_options under the scope's own
// options — but only when the scope runs the same harness as defaults;
// options for one harness must not leak into another.
func (c *Config) scopeHarnessOptions(scopeHarness string, opts map[string]any) map[string]any {
	if scopeHarness != c.DefaultsHarness() {
		return mergeHarnessOptions(nil, opts)
	}
	return mergeHarnessOptions(c.Defaults.HarnessOptions, opts)
}

func (c *Config) ProcessHarnessOptions(p ProcessConfig) map[string]any {
	return c.scopeHarnessOptions(c.ProcessHarness(p), p.HarnessOptions)
}

func (c *Config) TaskHarnessOptions(t TaskConfig) map[string]any {
	return c.scopeHarnessOptions(c.TaskHarness(t), t.HarnessOptions)
}

func (c *Config) TemplateHarnessOptions(t TemplateConfig) map[string]any {
	return c.scopeHarnessOptions(c.TemplateHarness(t), t.HarnessOptions)
}

// SessionHarnessOptions intentionally does NOT inherit defaults: persistent
// sessions never cascaded the claude flat fields from defaults, and the
// migration preserves that behavior exactly.
func (c *Config) SessionHarnessOptions(s SessionConfig) map[string]any {
	return mergeHarnessOptions(nil, s.HarnessOptions)
}
```

- [ ] **Step 4: Wire into `Validate()`** in config.go. Add near the top of Validate:

```go
	// resolveHarness returns the adapter for a scope, emitting at most one
	// error per bad name: defaults errors at defaults.harness; a scope only
	// errors when it *explicitly* names an unregistered harness.
	available := strings.Join(harness.Names(), ", ")
	defaultsHarness, defaultsHarnessErr := harness.Get(c.DefaultsHarness())
	if defaultsHarnessErr != nil {
		errs = append(errs, fmt.Sprintf("defaults.harness %q is not a registered harness (available: %s)", c.DefaultsHarness(), available))
	}
	resolveHarness := func(scope, explicit string) (harness.Harness, bool) {
		if explicit == "" {
			return defaultsHarness, defaultsHarnessErr == nil
		}
		h, err := harness.Get(explicit)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s.harness %q is not a registered harness (available: %s)", scope, explicit, available))
			return nil, false
		}
		return h, true
	}
```

Then, replacing each hardcoded `validModels` model check (defaults, processes, templates, sessions, tasks) and adding options/channels checks — pattern for processes (repeat for the other scopes with their field names; keep the existing `Provider != ""` model-relaxation guard for now, Task 3 deletes it):

```go
		if h, ok := resolveHarness("processes."+name, proc.Harness); ok {
			if proc.Model != "" && proc.Provider == "" {
				if err := h.ValidateModel(proc.Model); err != nil {
					errs = append(errs, fmt.Sprintf("processes.%s.model %v", name, err))
				}
			}
			if _, err := h.DecodeOptions(c.ProcessHarnessOptions(proc)); err != nil {
				errs = append(errs, fmt.Sprintf("processes.%s.harness_options: %v", name, err))
			}
			if !h.SupportsChannels() && (len(proc.Channels) > 0 || len(proc.DevChannels) > 0) {
				errs = append(errs, fmt.Sprintf("processes.%s.channels: the %s harness does not support channel plugins; use leo's MCP tools for messaging", name, h.Name()))
			}
		}
```

For defaults: validate `c.Defaults.HarnessOptions` against `defaultsHarness` (when resolvable) and delegate `defaults.model` the same way. The old wording `defaults.model %q is not valid (…)` must come out byte-identical: format as `fmt.Sprintf("defaults.model %v", err)` — verify against the existing test expectations. Delete the now-unused `validModels` map and `ValidModels()` from config **only if** `grep -rn "config.ValidModels\|ValidModels()" --include="*.go"` shows no callers outside internal/config; if the web schema uses it, re-point it to `claudeharness.ValidModels()` instead.

Note `TaskConfig` and `SessionConfig` model checks in Validate get the same delegation; sessions/tasks also get the options + channels checks (sessions use `c.SessionHarnessOptions(sc)`).

- [ ] **Step 5: Run tests**: `go test -race -cover ./internal/config/ ./internal/harness/...` — all green. Fix any pre-existing config tests whose model-error expectations shifted (they must NOT shift — if one does, the delegation format string is wrong).

- [ ] **Step 6: Full suite + commit**

```bash
go test -race ./... && make lint
git add internal/config/
git commit -m "feat(config): harness + harness_options fields with cascade and adapter-driven validation"
```

---

### Task 3: Providers removal — core

**Files:**
- Delete: `internal/provider/provider.go`, `internal/provider/provider_test.go`
- Delete: `internal/config/provider.go`, `internal/config/provider_test.go`
- Modify: `internal/config/config.go` (Providers/Provider fields → deprecated detection; model funcs lose provider step; provider validation block removed)
- Modify: `internal/cli/service.go`, `internal/run/runner.go`, `internal/agent/manager.go`, `internal/service/process.go`, `internal/service/session.go` (remove provider.Env call sites)
- Modify: `internal/agent/store.go` (drop `Record.Provider`)
- Modify: tests listed in Step 6

**Interfaces:**
- Produces: `TemplateModel`/`SessionModel` relocated into `internal/config/config.go` (same signatures, no provider step). `SessionModel(s) == s.Model` now.
- Removes: `provider.Env`, all `*Provider(...)` cascade funcs, `ProviderDefaultModel`, `ProviderKeyEnvNames`, `config.ProviderConfig`.

- [ ] **Step 1: Detection fields.** In config.go change:

```go
	// Providers was removed with the harness abstraction. The field survives
	// only so Validate() can emit a precise removal error (yaml.v3 silently
	// ignores unknown keys).
	Providers map[string]any `yaml:"providers,omitempty"`
```

and on all five scope structs rename `Provider` → `DeprecatedProvider` keeping the yaml tag `provider,omitempty`.

- [ ] **Step 2: Write failing validation tests** (in `internal/config/config_test.go` or a new `migration_test.go`):

```go
func TestValidateRejectsRemovedProviders(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"providers section", func(c *Config) { c.Providers = map[string]any{"corp": map[string]any{}} },
			"providers: this section has been removed — see docs/configuration/harnesses.md"},
		{"defaults.provider", func(c *Config) { c.Defaults.DeprecatedProvider = "corp" },
			"defaults.provider has been removed along with providers — see docs/configuration/harnesses.md"},
		// ... one case per scope: processes.p, tasks.t, templates.x, sessions.s
	}
	// build a minimal valid config, apply mutate, assert Validate() output contains wantErr
}
```

Run to verify FAIL (compile or assertion).

- [ ] **Step 3: Implement removal.**
  - Delete `internal/provider/` entirely and `internal/config/provider.go` + both test files.
  - Relocate into config.go, simplified (no provider fall-through):

```go
// TemplateModel resolves the model for a template: template → defaults → built-in.
func (c *Config) TemplateModel(t TemplateConfig) string {
	if t.Model != "" {
		return t.Model
	}
	if c.Defaults.Model != "" {
		return c.Defaults.Model
	}
	return DefaultModel
}

// SessionModel resolves the model for a persistent session. Empty means
// claude picks its own default.
func (c *Config) SessionModel(s SessionConfig) string { return s.Model }
```

  - `ProcessModel` / `TaskModel`: delete the `ProviderDefaultModel` step (scope → defaults → `DefaultModel`).
  - In `Validate()`: delete the provider-block validation (base_url/api key checks), `checkProviderRef` and all five call sites, and every `&& X.Provider == ""` model-relaxation guard added/kept in Task 2. Add the removal errors:

```go
	if len(c.Providers) > 0 {
		errs = append(errs, "providers: this section has been removed — see docs/configuration/harnesses.md")
	}
	// per scope, e.g.:
	if c.Defaults.DeprecatedProvider != "" {
		errs = append(errs, "defaults.provider has been removed along with providers — see docs/configuration/harnesses.md")
	}
```

(and the equivalent inside each scope loop: `processes.%s.provider …`, `tasks.%s.provider …`, `templates.%s.provider …`, `sessions.%s.provider …`).

- [ ] **Step 4: Remove the env-injection call sites.**
  - `internal/cli/service.go`: foreground path (~139–145) — delete the `provider.Env` block; change `processEnviron(proc config.ProcessConfig, extraEnv map[string]string)` to `processEnviron(proc config.ProcessConfig)` and drop the extraEnv loop. `buildAllProcessSpecs` (~211–231) — delete the provEnv block and merge loop (`procEnv := mergeChannelsIntoEnv(proc)` stands alone). Line ~579: `env.Capture(cfg.ProviderKeyEnvNames()...)` → `env.Capture()`.
  - `internal/run/runner.go`: delete the `provider.Env` call (~156) and pass only `leoEnv` where `mergeEnvMaps(provEnv, leoEnv)` merged them; drop the now-single-use merge if it becomes trivial. Update `executeCommand` threading accordingly.
  - `internal/agent/manager.go`: delete the three provEnv blocks (spawn ~202–205, resume ~331–335, restore ~583) and every use of the resolved env; delete `Record.Provider` (store.go:74) and all reads/writes of it.
  - `internal/service/process.go`: `sessionSpecs.add` (~520–527) — delete the provEnv resolution and merge.
  - `internal/service/session.go`: `SessionSpecsFromConfig` — delete both provEnv blocks and their warn-and-skip paths; explicit sessions keep `Env: sc.Env` (copied), implicit task sessions get `Env: nil`; implicit model becomes `model := task.Model` (preserved quirk #2 — no defaults fall-through).
  - Remove all now-unused imports (`internal/provider` from five files).

- [ ] **Step 5: Run full suite**: `go test -race ./...` — expect FAILURES only in the provider-specific tests listed next.

- [ ] **Step 6: Delete/update provider tests:**
  - Delete: `TestBuildAllProcessSpecsProviderEnv`, `TestBuildAllProcessSpecsSkipsUnresolvableProvider` (internal/cli/service_test.go); `TestSpawnInjectsProviderEnvWithoutPersistingKey`, `TestSpawnFailsWhenProviderUnresolvable` (internal/agent/manager_test.go); `TestRestoreAgentsResolvesProviderEnv`, `TestRestoreAgentsSkipsUnresolvableProvider` (internal/service/agents_test.go); `TestSessionSpecsProviderEnv`, `TestSessionSpecsSkipsUnresolvableProvider`, `TestImplicitSessionProviderEnv` (internal/service/session_test.go).
  - Update: provider-env assertions inside `internal/run/runner_test.go` (~lines 985, 1002); any config test exercising provider cascade/relaxation.
  - Web tests still reference providers — they keep passing until Task 4; do NOT touch web code in this task.

- [ ] **Step 7: Verify green + commit**

```bash
go test -race ./... && make lint
git add -A
git commit -m "feat(config)!: remove providers — endpoints are the harness's own concern"
```

---

### Task 4: Providers removal — web UI

**Files:**
- Modify: `internal/web/web.go` (4 routes), `internal/web/handlers.go` (`handleProviderAdd`, `handleProviderDelete`), `internal/web/handlers_config.go` (`handleConfigProviderSave`), `internal/web/handlers_pages.go` (`buildProvidersData`, `providerCard`, `providersPageData`)
- Modify: `internal/web/schema/schema.go` (`SectionProvider`), `internal/web/schema/registry.go` (provider entry), `internal/web/schema/options.go` ("providers" case)
- Modify: `internal/web/templates/layout.html` (nav link + page slot)
- Delete: `internal/web/templates/pages/config_providers.html`
- Modify: `internal/web/handlers_config_test.go`, `internal/web/web_test.go`, `internal/web/schema/options_test.go`

**Interfaces:** none new — pure deletion.

- [ ] **Step 1: Write the failing test** — in `web_test.go`, assert `GET /config/providers` returns 404 (adjust the route-list test at ~line 198 to drop the path).

- [ ] **Step 2: Delete** everything listed under Files: routes, handlers, page builder + data types, schema section/registry/options entries, nav link, page-slot branch, template file.

- [ ] **Step 3: Delete provider web tests**: `TestProviderAddRejectsInvalidName`, `TestProviderCRUD`, `TestProviderAddRejectsDuplicate`, `TestProviderAddRejectsEmptyName`, `TestProviderDeleteRefusedWhileReferenced`, `TestProviderDeleteNotFound`, `TestPageConfigProvidersEmptyState`, `TestPageConfigProvidersListsCards`; the "providers" options-source cases in `schema/options_test.go`.

- [ ] **Step 4: Verify + commit**

```bash
go test -race ./internal/web/... && go test -race ./... && make lint
git add -A
git commit -m "feat(web): remove providers page"
```

---

### Task 5: Rewire process/agent/task builders to registry + harness_options

**Files:**
- Modify: `internal/cli/service.go` (`buildProcessArgs`)
- Modify: `internal/agent/args.go` (`BuildTemplateArgs`)
- Modify: `internal/run/runner.go` (`buildArgs`)
- Modify: `internal/cli/process_args_test.go`, `internal/agent/args_test.go`, `internal/run/args_test.go` (characterization configs move to `HarnessOptions`)

**Interfaces:**
- Consumes: Task 2 accessors; `harness.Get`; `h.DecodeOptions`.
- Produces: builders no longer read any flat claude field (`PermissionMode`, `BypassPermissions`, `RemoteControl`, `Agent`, `AllowedTools`, `DisallowedTools`, `AppendSystemPrompt`) from config structs — Task 7 depends on this.

**Note:** binaries and error handling stay claude-wired (`claudeharness.Claude{}.Binary()`, log-and-return-nil) — Plan 3 threads `h.Binary()` through execution when a second harness actually exists. The `harness.Get` + `DecodeOptions` calls cannot fail for a config that passed `Validate()`; treat errors with the existing log-and-return-nil pattern.

- [ ] **Step 1: Update characterization tests FIRST** and prove them against a *hybrid* expectation: for each existing characterization case in the three test files, re-express the fixture config via `HarnessOptions` (e.g. `PermissionMode: "plan"` → `HarnessOptions: map[string]any{"permission_mode": "plan"}`), keep every argv assertion byte-identical, and add cascade cases: defaults-level option inherited by scope; scope override wins; template `remote_control` absent → `--remote-control` present (default true); template's own `remote_control: false` suppresses it even when `defaults.harness_options.remote_control: true` (preserved quirk #3 — this case's expectation encodes the new *defined* behavior: template ignores the defaults layer for remote_control).

Run: `go test -race ./internal/cli/ ./internal/agent/ ./internal/run/` — expected: FAIL (builders still read flat fields, which the fixtures no longer set).

- [ ] **Step 2: Rewire `buildProcessArgs`** (internal/cli/service.go):

```go
func buildProcessArgs(cfg *config.Config, name string, proc config.ProcessConfig) []string {
	h, err := harness.Get(cfg.ProcessHarness(proc))
	if err != nil {
		log.Printf("[%s] resolving harness: %v", name, err)
		return nil
	}
	decoded, err := h.DecodeOptions(cfg.ProcessHarnessOptions(proc))
	if err != nil {
		log.Printf("[%s] decoding harness options: %v", name, err)
		return nil
	}
	opts, ok := decoded.(claudeharness.Options)
	if !ok {
		// Non-claude processes arrive with Plan 4 (session drivers).
		log.Printf("[%s] harness %q cannot run supervised processes yet", name, h.Name())
		return nil
	}
	mcpConfig := ""
	if p := cfg.ProcessMCPConfigPath(proc); config.HasMCPServers(p) {
		mcpConfig = p
	}
	opts.RemoteControlPrefix = name
	opts.AppendSystemPrompt = leomcp.MergeSystemPrompt(cfg, opts.AppendSystemPrompt)
	opts.MCPConfigPath = mcpConfig
	opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)

	spec := harness.LaunchSpec{
		Kind:        harness.KindProcess,
		Name:        name,
		Model:       cfg.ProcessModel(proc),
		Workspace:   cfg.ProcessWorkspace(proc),
		AddDirs:     proc.AddDirs,
		Channels:    proc.Channels,
		DevChannels: proc.DevChannels,
		Options:     opts,
	}
	args, err := h.Args(spec)
	if err != nil {
		log.Printf("[%s] building %s args: %v", name, h.Name(), err)
		return nil
	}
	return args
}
```

(The `cfg.ProcessBypassPermissions` / `cfg.ProcessRemoteControl` helpers lose their last callers here — delete them and their tests, since the merged options map now carries both cascades.)

- [ ] **Step 3: Rewire `BuildTemplateArgs`** (internal/agent/args.go) with the same pattern, plus the remote-control quirk:

```go
	decoded, err := h.DecodeOptions(cfg.TemplateHarnessOptions(tmpl))
	// ... error handling as above ...
	opts, ok := decoded.(claudeharness.Options)
	// ... as above ...

	// Agents default remote_control to true, and only the template's own
	// options can turn it off — the defaults layer never applied to
	// templates pre-migration and still doesn't (see plan: preserved quirks).
	opts.RemoteControl = true
	if v, ok := tmpl.HarnessOptions["remote_control"].(bool); ok {
		opts.RemoteControl = v
	}
	opts.AppendSystemPrompt = leomcp.MergeSystemPrompt(cfg, opts.AppendSystemPrompt)
	opts.MCPConfigPath = mcpConfig
	opts.LeoMCPArgs = leomcp.AppendArg(nil, cfg)
```

Keep the AddDirs safety filter, MCP relative-path resolution, and maxTurns fallback exactly as they are.

- [ ] **Step 4: Rewire `buildArgs`** (internal/run/runner.go) the same way: `harness.Get(cfg.TaskHarness(task))` → `DecodeOptions(cfg.TaskHarnessOptions(task))` → fill `AppendSystemPrompt` (MergeSystemPrompt), `MCPConfigPath`, `LeoMCPArgs` (gated on `leoMCPOK` exactly as today). Note the old task path took `BypassPermissions` from defaults only — the merged map reproduces that (tasks previously had no bypass field; a task-level `harness_options.bypass_permissions` now also works, which is fine and documented in Task 9).

- [ ] **Step 5: Run characterization + full suite**: `go test -race ./...` — all green, argv assertions untouched from Step 1.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor: builders resolve adapter via harness registry and harness_options"
```

---

### Task 6: Persistent sessions — KindSession + builder consolidation

**Files:**
- Modify: `internal/harness/harness.go` (add `KindSession`)
- Modify: `internal/harness/claude/args.go` (+ `sessionArgs`), `internal/harness/claude/args_test.go`
- Modify: `internal/service/session.go` (`buildSessionClaudeArgs` becomes a LaunchSpec wrapper; `SessionSpecsFromConfig` reads harness_options)
- Modify: `internal/service/session_test.go`

**Interfaces:**
- Consumes: `SessionHarnessOptions` / `TaskHarnessOptions` own-map semantics from Task 2; `DecodeOptions`.
- Produces: `harness.KindSession`; claude adapter handles it in `Args()`. `SessionSpec` struct unchanged (it stays the runtime descriptor; only its *population* and *rendering* change).

- [ ] **Step 1: Characterization test for the argv** — in `internal/harness/claude/args_test.go`, add golden cases for `KindSession` derived from the CURRENT `buildSessionClaudeArgs` output (write the expected argv by hand from the current implementation — flag order: `--model` (omitted when empty), `--resume` (when set), `--permission-mode`, `--channels`×N, `--agent`, `--add-dir <workdir>`, `--add-dir`×N, `--allowed-tools` (comma-joined), `--disallowed-tools`, `--append-system-prompt`). Include: full-featured case, minimal case (only workdir → `["--add-dir", wd]`), empty-model case.

Run: `go test -race ./internal/harness/claude/` — expected: FAIL (`KindSession` undefined).

- [ ] **Step 2: Implement.** In harness.go add `KindSession Kind = "session"`. In claude `Args()` dispatch add `case harness.KindSession: return sessionArgs(spec, opts), nil`. In args.go:

```go
// sessionArgs reproduces the pre-harness buildSessionClaudeArgs
// byte-for-byte for persistent task sessions. No MCP flags: channel
// plugins load at session boot and delivery happens in-session.
func sessionArgs(spec harness.LaunchSpec, o Options) []string {
	var a []string
	if spec.Model != "" {
		a = append(a, "--model", spec.Model)
	}
	a = append(a, Claude{}.SessionArgs(spec.Session)...)
	if o.PermissionMode != "" {
		a = append(a, "--permission-mode", o.PermissionMode)
	}
	for _, ch := range spec.Channels {
		a = append(a, "--channels", ch)
	}
	if o.AgentFile != "" {
		a = append(a, "--agent", o.AgentFile)
	}
	a = append(a, "--add-dir", spec.Workspace)
	for _, d := range spec.AddDirs {
		a = append(a, "--add-dir", d)
	}
	if len(o.AllowedTools) > 0 {
		a = append(a, "--allowed-tools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		a = append(a, "--disallowed-tools", strings.Join(o.DisallowedTools, ","))
	}
	if o.AppendSystemPrompt != "" {
		a = append(a, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return a
}
```

Run claude tests: PASS.

- [ ] **Step 3: Rewire `buildSessionClaudeArgs`** to a thin wrapper (keep name and signature — `SuperviseSession`'s resume=false trick continues to work by zeroing `ResumeID` on its copy):

```go
func buildSessionClaudeArgs(spec SessionSpec) []string {
	ls := harness.LaunchSpec{
		Kind:      harness.KindSession,
		Name:      spec.Name,
		Model:     spec.Model,
		Workspace: spec.Workdir,
		AddDirs:   spec.AddDirs,
		Channels:  spec.Channels,
		Options: claudeharness.Options{
			PermissionMode:     spec.PermissionMode,
			AgentFile:          spec.Agent,
			AllowedTools:       spec.AllowedTools,
			DisallowedTools:    spec.DisallowedTools,
			AppendSystemPrompt: spec.AppendPrompt,
		},
	}
	if spec.ResumeID != "" {
		ls.Session = harness.SessionState{Mode: harness.SessionResume, ID: spec.ResumeID}
	}
	args, err := claudeharness.Claude{}.Args(ls)
	if err != nil {
		// Unreachable with a well-formed spec; never launch flagless silently.
		fmt.Fprintf(os.Stderr, "warning: session %q: building claude args: %v\n", spec.Name, err)
		return nil
	}
	return args
}
```

Existing `session_test.go` argv expectations must pass unchanged — they are the characterization net for this step.

- [ ] **Step 4: Rewire `SessionSpecsFromConfig` population.** Both loops decode options instead of reading flat fields. Add a small helper in session.go:

```go
// claudeSessionOptions decodes a session-scoped harness_options map. A
// config that passed Validate() cannot fail here; on the defensive path we
// warn and skip the session rather than boot claude with dropped flags.
func claudeSessionOptions(opts map[string]any) (claudeharness.Options, error) {
	decoded, err := claudeharness.Claude{}.DecodeOptions(opts)
	if err != nil {
		return claudeharness.Options{}, err
	}
	o, ok := decoded.(claudeharness.Options)
	if !ok {
		return claudeharness.Options{}, fmt.Errorf("unexpected options type %T", decoded)
	}
	return o, nil
}
```

Explicit sessions: `o, err := claudeSessionOptions(cfg.SessionHarnessOptions(sc))`; on error warn+skip (same shape as the removed provider warn+skip); populate `Agent: o.AgentFile, PermissionMode: o.PermissionMode, AllowedTools: o.AllowedTools, DisallowedTools: o.DisallowedTools, AppendPrompt: o.AppendSystemPrompt`. Implicit task sessions: do NOT use `cfg.TaskHarnessOptions` (that merges defaults) — implicit sessions read the task's OWN fields without defaults cascade (preserved quirk #2), so decode the raw map: `claudeSessionOptions(task.HarnessOptions)`. Model stays `task.Model` (from Task 3).

- [ ] **Step 5: Update `session_test.go`** fixtures to `HarnessOptions` maps; argv/spec assertions unchanged. Add one test asserting defaults' options do NOT leak into an explicit session spec and one asserting a task-level `harness_options.permission_mode` reaches the implicit session while a defaults-level one does not.

- [ ] **Step 6: Full suite + commit**

```bash
go test -race ./... && make lint
git add -A
git commit -m "refactor(service): persistent session argv via claude adapter KindSession"
```

---

### Task 7: Claude flat-field migration errors

**Files:**
- Modify: `internal/config/config.go` (field renames + migration errors)
- Create: `internal/config/migration_test.go` (or extend Task 3's file)
- Modify: `internal/web/schema/registry.go` (drop dead form fields), plus any schema/web tests referencing them
- Modify: `internal/templates/*` and `internal/setup/*` IF they emit deprecated keys (Step 4)
- Modify: every remaining test fixture that still sets a flat field

**Interfaces:**
- Consumes: Tasks 5–6 removed all runtime readers. Before starting, the implementer MUST verify: `grep -rn "\.PermissionMode\|\.BypassPermissions\|\.RemoteControl\b\|\.AllowedTools\|\.DisallowedTools\|\.AppendSystemPrompt" --include="*.go" internal/ | grep -v harness | grep -v _test` shows no reads of the CONFIG struct fields outside Validate() (hits on `claude.Options`/`SessionSpec` fields are fine — those structs keep their names).
- Produces: config structs whose only claude-specific surface is `harness_options`.

- [ ] **Step 1: Write failing migration tests.** Table-driven over every (scope × field) pair; each case sets exactly one deprecated field on a minimal valid config and asserts Validate() contains the exact message, e.g.:

```
processes.builder.permission_mode has moved to processes.builder.harness_options.permission_mode (claude harness) — see docs/configuration/harnesses.md
```

Fields per scope — defaults: `permission_mode`, `bypass_permissions`, `remote_control`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`; processes: those six plus `agent` (bools are `*bool`/pointer on processes/templates so explicit `false` is detected); templates: `permission_mode`, `remote_control`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`; tasks: `permission_mode`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`; sessions: `permission_mode`, `agent`, `allowed_tools`, `disallowed_tools`, `append_system_prompt`. Also: `defaults.bypass_permissions: false` and `defaults.remote_control: false` explicitly set MUST error (Step 2 makes them `*bool`).

- [ ] **Step 2: Rename the fields.** On each config struct rename flat claude fields to `Deprecated*` (e.g. `PermissionMode` → `DeprecatedPermissionMode`), KEEPING the yaml tags. Change `DefaultsConfig.BypassPermissions`/`RemoteControl` from `bool` to `*bool` so an explicit `false` is detectable (they were only cascade sources; nothing reads them anymore).

- [ ] **Step 3: Emit the errors.** Add a helper and call it per scope in Validate():

```go
type movedField struct {
	set  bool
	name string
}

func appendMovedFieldErrs(errs []string, scope string, fields []movedField) []string {
	for _, f := range fields {
		if f.set {
			errs = append(errs, fmt.Sprintf("%s.%s has moved to %s.harness_options.%s (claude harness) — see docs/configuration/harnesses.md", scope, f.name, scope, f.name))
		}
	}
	return errs
}
```

Call with e.g. for processes:

```go
		errs = appendMovedFieldErrs(errs, "processes."+name, []movedField{
			{proc.DeprecatedPermissionMode != "", "permission_mode"},
			{proc.DeprecatedBypassPermissions != nil, "bypass_permissions"},
			{proc.DeprecatedRemoteControl != nil, "remote_control"},
			{proc.DeprecatedAgent != "", "agent"},
			{len(proc.DeprecatedAllowedTools) > 0, "allowed_tools"},
			{len(proc.DeprecatedDisallowedTools) > 0, "disallowed_tools"},
			{proc.DeprecatedAppendSystemPrompt != "", "append_system_prompt"},
		})
```

Delete the old flat-field validation that becomes dead (the `permission_mode` value check in Validate — value validation now lives in `DecodeOptions`).

- [ ] **Step 4: Scrub generators and forms.**
  - `grep -rn "permission_mode\|allowed_tools\|disallowed_tools\|append_system_prompt\|bypass_permissions\|remote_control" internal/templates/ internal/setup/ internal/web/schema/` — any generated YAML (setup wizard, embedded templates) moves those keys under `harness_options:`; any web schema registry entry for a deprecated field on defaults/process/template/task/session sections is removed (the web UI must not write config that Validate rejects; harness_options forms arrive in Plan 5).
  - Fix every remaining test fixture across the repo that still sets a flat field (`go build ./... && go test -race ./...` will enumerate them after the rename).

- [ ] **Step 5: Full suite green + commit**

```bash
go test -race ./... && make lint
git add -A
git commit -m "feat(config)!: claude flat fields move to harness_options with precise migration errors"
```

---

### Task 8: Harness-aware prereq, web seam, alias cleanup

**Files:**
- Modify: `internal/prereq/prereq.go`, `internal/prereq/prereq_test.go` (if present)
- Modify: `internal/cli/validate.go`, `internal/setup/setup.go`
- Modify: `internal/web/web.go` (fetchAgentList)
- Modify: `internal/cli/service.go`, `internal/cli/process_args_test.go` (drop self-named `harness` import aliases)

**Interfaces:**
- Produces: `prereq.CheckBinary(name string) BinaryResult` (fields `Path`, `Version`, `OK` — same shape as today's `ClaudeResult`). `CheckClaude()` becomes `CheckBinary(claudeharness.Claude{}.Binary())` at the call sites and the old func/type are deleted (solo-user project: rename, don't shim).

- [ ] **Step 1: Failing test** — table-driven test for `CheckBinary` using the existing `lookPath`/`runCommand` seams: found+version, found+version-cmd-fails (OK with empty Version), not found.

- [ ] **Step 2: Generalize prereq.** Rename `ClaudeResult` → `BinaryResult`, `CheckClaude()` → `CheckBinary(name string)` (parameterizes the current hardcoded `"claude"`). Update `internal/setup/setup.go` (`checkClaudeFn = prereq.CheckClaude` → a closure `func() prereq.BinaryResult { return prereq.CheckBinary(claudeharness.Claude{}.Binary()) }`) and `internal/cli/validate.go`: instead of checking only claude, collect the set of harnesses referenced by the loaded config (defaults + every process/task/template/session via the Task 2 accessors), resolve each via `harness.Get`, and `CheckBinary(h.Binary())` each — reporting per-harness lines in the existing output style.

- [ ] **Step 3: Web seam.** In `internal/web/web.go` `fetchAgentList`: `exec.LookPath("claude")` → `exec.LookPath(claudeharness.Claude{}.Binary())` (the function shells out to `claude agents`, which is inherently claude-specific — the literal binary name is the only seam to close here).

- [ ] **Step 4: Alias cleanup.** Remove the redundant `harness "…/internal/harness"` self-named aliases in `internal/cli/service.go` and `internal/cli/process_args_test.go` (plain import).

- [ ] **Step 5: Full suite + commit**

```bash
go test -race ./... && make lint
git add -A
git commit -m "refactor: harness-aware prereq checks; close remaining hardcoded claude seams"
```

---

### Task 9: Docs

**Files:**
- Create: `docs/configuration/harnesses.md`
- Delete: `docs/configuration/providers.md`
- Modify: `docs/configuration/config-reference.md`, `docs/configuration/persistent-tasks.md`, `CLAUDE.md` (repo root), `README.md` (if it mentions providers)

(Leave `docs/superpowers/specs/2026-07-08-provider-config-design.md` and its plan in place — they are historical records.)

- [ ] **Step 1: Write `docs/configuration/harnesses.md`** covering, in this order:
  1. **What a harness is** — the coding-agent CLI leo drives; `claude` is the only adapter today; codex/opencode land next; adapters are compiled-in Go (PRs welcome), no runtime plugins.
  2. **Config shape** — `harness:` cascades defaults → process/template/task/session; `harness_options:` is adapter-specific, validated strictly (unknown keys rejected); defaults-level `harness_options` merge into same-harness scopes (per-key, scope wins). Sessions never inherit defaults' options; template `remote_control` is template-own-only, default `true`. Include a full YAML example (claude, with `permission_mode`, `remote_control`, `agent`, tool filters under `harness_options`).
  3. **Claude option reference** — table of the seven keys with types and meanings, noting `bypass_permissions` is a legacy fallback consulted only when `permission_mode` is empty, and that task-level `bypass_permissions` is now honored (previously defaults-only).
  4. **Migration table** — every old flat field → new location, plus: `providers:`/`provider:` removed entirely; custom Anthropic-compatible endpoints now via per-scope `env:` (`ANTHROPIC_BASE_URL`, `ANTHROPIC_AUTH_TOKEN`) with a short example; `channels` only valid on channel-supporting harnesses.
  5. **Validation behavior** — every migration mistake produces a named error pointing here.

- [ ] **Step 2: Update the other docs** — remove provider sections from config-reference.md and persistent-tasks.md; replace the flat claude fields in every YAML example repo-wide (`grep -rn "permission_mode\|append_system_prompt\|allowed_tools" docs/ README.md CLAUDE.md`) with `harness_options` form; update root CLAUDE.md's config-section list (drop `providers`, add `harness`/`harness_options`, drop the provider cascade paragraph, note channels-on-claude-only validation).

- [ ] **Step 3: Verify + commit**

```bash
go test -race ./... && make lint
git add -A
git commit -m "docs: harness configuration reference; remove providers docs"
```

---

## Rollout Notes (not tasks)

- **Evan's live `~/.leo/leo.yaml` breaks on upgrade** — flat claude fields and any `provider`/`providers` keys must move under `harness_options` before the new binary loads it. Do this at release time, not merge time: migrate the file alongside deploying the binary (offer to do it, gated on his go — and remember: no service restarts without asking).
- Dry-run verification after merge: `bin/leo run <task> --dry-run` against a migrated config must emit identical argv to the pre-break binary.
- Plan 3 (codex/opencode one-shot adapters) builds directly on `DecodeOptions`/`ValidateModel` and adds `ParseEvents` + per-harness binary threading; the `internal/config/harness.go` blank-import block is where new adapters register.
