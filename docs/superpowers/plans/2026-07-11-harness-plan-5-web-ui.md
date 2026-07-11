# Harness Plan 5: Web UI for Harnesses — Dropdown, Options Sub-Form, Model Fix, Badges

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Surface `harness` and `harness_options` as first-class web-UI form fields: a harness dropdown on every config form, a per-adapter options sub-form rendered from a new `OptionsSchema()` interface method, a harness-aware model control, and harness badges on entity lists.

**Architecture:** A neutral `harness.OptionField` descriptor type (in `internal/harness` — the web layer imports harness, never the reverse) describes each adapter's `harness_options` keys. `DecodeOptions` stays the untouched validator: every web save already round-trips through `Config.Validate()`, so form/validator acceptance cannot disagree; per-adapter consistency tests lock `OptionsSchema()` to the adapters' existing `optionKeys` slices. The web layer gains a sub-form component (`internal/web/schema/harnessform.go` + `components/harness_options.html`) that renders `OptionField`s and parses `harness_options.<key>` form inputs back into the YAML-shaped map. `harness` itself becomes a plain registered schema field (`KindSelect`, new `"harnesses"` option source); changing it htmx-swaps the sub-form via a new partial endpoint.

**Tech Stack:** Go stdlib + existing deps only (`gopkg.in/yaml.v3` already vendored). htmx + `html/template` (embedded), matching the existing web UI.

**Spec:** `docs/superpowers/specs/2026-07-11-harness-plan-5-web-ui-design.md` (approved 2026-07-11). Parent: `docs/superpowers/specs/2026-07-10-harness-abstraction-design.md` §Web UI.

## Verified codebase facts (2026-07-11, main @ 9716ecd + spec commit d975e46)

**Do not re-derive these; cite this section in dispute.**

- The three adapters each hold a package-level `optionKeys []string` (claude: 7 keys, codex: `{"sandbox"}`, opencode: `{"permission"}`) and a strict `DecodeOptions` that rejects unknown keys, processing keys in sorted order. Value shapes asserted: `string`, `bool`, `[]any`-of-`string`, `map[string]any` (nested for opencode permission). **`DecodeOptions` type-asserts `[]any`, NOT `[]string`** (`internal/harness/claude/options.go:83-97`) — the web apply path must produce `[]any`.
- Claude enum: `validPermissionModes` = acceptEdits, auto, bypassPermissions, default, dontAsk, plan (`claude/options.go:21-24`). Codex enum: `validSandboxes` = read-only, workspace-write, danger-full-access (`codex/options.go:12-14`). Opencode permission verdicts: allow, ask, deny; values may be a nested `map[string]any` pattern map (`opencode/options.go:66-94`).
- Claude argv flags per option (`claude/args.go`, `args_shared.go`): `--permission-mode`, `--dangerously-skip-permissions` (bypass), `--remote-control`, `--agent`, `--allowed-tools` (comma-joined), `--disallowed-tools`, `--append-system-prompt`.
- `claude.ValidModels()` (exported, sorted) exists at `internal/harness/claude/claude.go:42`. `harness.Names()` (sorted) and `harness.Get(name) (Harness, error)` exist in `internal/harness/registry.go`.
- Cascade helpers (`internal/config/harness.go`): `DefaultsHarness()`, `ProcessHarness(p)`, `TaskHarness(t)`, `TemplateHarness(t)`, `SessionHarness(s)` (value receivers on Config, value params); `*HarnessOptions(...)` merge defaults' map under the scope's **only when the scope runs the same harness as defaults**; `SessionHarnessOptions` never inherits. `DefaultHarnessName = "claude"`.
- Web form system: registry-driven (`internal/web/schema/{schema,registry,options,values}.go`). `Excluded` map lists `harness`+`harness_options` (+dead flat claude fields, `provider`) on all five config sections; `registry_drift_test.go` enforces registered-XOR-excluded per yaml-tagged struct field, both directions.
- Save path: every form POST → `applySection` (`internal/web/handlers_config.go:54-89`): ParseForm → loadConfig → locate → `schema.Apply` → put → `validateAndSave` (`cfg.Validate()` then `config.Save`; **Validate failure means nothing is written**) → flash into `#flash-container`. Errors are a flash banner; **no per-field error UI exists anywhere**.
- `cfg.Validate()` resolves the harness per scope and calls `ValidateModel` + `DecodeOptions` on the *merged* per-scope options (via the cascade helpers), plus `SupportsChannels`/`SupportsKind` checks.
- The model field renders from a **static claude-only** `modelOptions` list (`schema/options.go:15-22`) via option source `"models"`; with harness=codex/opencode a valid model is literally unenterable (select-only). `OptionSources.For` **panics** on unknown source names (deliberate).
- The old option source `"agents"` (`schema/options.go:47-52`, lists claude sub-agents via injected `s.agentList`) has **no remaining registry consumer** — the claude `agent` sub-form field re-adopts it.
- Templates: `components/form.html` defines `config_form` (groups, Advanced `<details>`, `hx-post` → `#flash-container`) and `config_field` (switch on `kindName`). `kindName` lives in `internal/web/handlers_config.go:211-236`; funcMap wiring at `web.go:411-453`. htmx precedent for live sub-updates: the cron field's `hx-get="/web/cron/preview" hx-trigger="input changed delay:400ms" hx-target="next .cron-preview"`.
- Multiple `config_form` instances render on one page (sessions cards, settings hosts) — **any new element id must be scope-unique**.
- Form builders: `buildForm(section, target any, cfg, action) formData` (`handlers_config.go:30-47`); edit-page handlers in `handlers_pages.go` (task :372, process :427, template :495), defaults builder :213, sessions loop `handlers_sessions.go:58`. `templateOwnAgent(tmpl.HarnessOptions)` helper already exists (templates list Agent column).
- List surfaces: `processes.html` table (5 cols incl. model, `colspan="5"`), `config_templates.html` table (5 cols incl. model+agent, `colspan="5"`), `tasks.html` two tables (schedule-centric, **no model column, no colspan rows**), `sessions.html` cards (status pill + queue + stored id). `processRow`/`templateRow`/`taskRow`/`sessionRow` structs in `handlers_pages.go`/`handlers_sessions.go`.
- Known extra `harness.Harness` stub implementers to ripple the interface change through (compiler will confirm the full set): `internal/harness/registry_test.go` `fakeHarness`, `internal/config/harness_test.go` `stubHarness`; possibly stubs in `internal/web/handlers_agents_test.go`, `internal/agent/logs_driveturns_test.go`, `internal/cli/attach_driver_test.go`, `internal/daemon/handlers_agents_attach_test.go` (some of those may stub only `SessionDriver`).
- yaml.v3 (already the config codec) unmarshals nested mappings into `map[string]any` when the target is `map[string]any` — identical shapes to what config load hands `DecodeOptions`.

## Global Constraints

- **`DecodeOptions` implementations are frozen.** No behavior change, no message change. `OptionsSchema()` is additive; consistency tests bind the two.
- **Existing form behavior for already-registered fields stays identical** except the model control (select → datalist input, deliberate) and the additive harness field/sub-form. Existing web tests must keep passing unmodified unless a task explicitly says otherwise.
- **The web UI must never write config that `Validate()` rejects** — the `validateAndSave` gate stays the single choke point; no new save path bypasses it.
- **Type fidelity:** values written into `HarnessOptions` maps must be the YAML-decoder shapes (`string`, `bool`, `[]any`, `map[string]any`) — never `[]string`.
- Every commit: `go test -race ./...` green, `make lint` clean, **`make e2e` green** (suite is build-tagged; `go test ./...` skips it — bit PR #97/#98). Changed packages ≥80% coverage.
- Before any push: golangci-lint **2.12.2** (brew) AND gosec **v2.25.0** with `-exclude=G104,G204,G304,G306,G602,G702,G703,G704` — CI's Lint job runs both; local `make lint` is only go vet (bit PR #99 twice).
- Tests must not require tmux or harness binaries on PATH (macOS CI runners lack tmux; no CI runner has claude/codex/opencode) — use the existing seams (`s.lookTmux`, `s.execCommand`, `s.agentList`).
- **Never restart the production leo service.** Live testing uses the isolated test daemon (separate `LEO_HOME`), orchestrator-only.
- Commit format `<type>: <description>`, no attribution lines.
- htmx/`html/template` only — no JS frameworks, no new static assets beyond what tasks specify; auto-escaping stays on (no `template.HTML` casts).

## Design decisions vs the spec (deliberate, reviewed)

1. **`OptionsSchema()` returns `[]harness.OptionField`, not the parent spec's `schema.Object`** — dependency direction (`web/schema` → `config` → `harness`) forbids the harness package referencing the web package. Approved in the Plan-5 spec.
2. **The static `modelOptions` list and the `"models"` option source are deleted**, replaced by `schema.ModelSuggestions(harnessName)` backed by `claude.ValidModels()` — kills the "keep in sync" comment debt. The model field becomes `KindDatalist` (free text + suggestions); "inherit" is expressed by the existing empty-value+placeholder convention instead of a fake `""` option.
3. **`fieldView` gains `Section`/`Scope`/`Placeholder`** so `config_field` can render scope-unique datalist ids and the harness select's hx-get URL without new template plumbing.
4. **`applySection` gains an optional `applyOptions` hook** rather than baking harness knowledge into the generic path — Web/Client/Host sections pass nil and are untouched semantically.
5. **The partial endpoint lives at `GET /web/partials/harness-options`** (matches `/web/cron/preview` placement inside the authed mux).
6. **Bool options render as tri-state selects** (inherit/on/off) because unset ≠ false under the key-wise cascade merge.

## Task Ordering

Strictly sequential: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8. (Interface before web schema; registry field before sub-form component; component before template wiring; wiring before the htmx partial; partial before save-path tests that exercise the full loop; badges after forms; docs last.)

## Not In This Plan (deferred / follow-ups)

- Structured row editor for opencode `permission` (YAML textarea per spec).
- Per-input error highlighting (no field in the form system has it).
- Live model-list fetching from harness CLIs.
- Driver-aware session status (codex thread info, opencode serve health on cards).
- Renaming `ClaudeArgs` fields/JSON keys (Plan-4 deferral, still parked).
- Migrating Evan's live `~/.leo/leo.yaml` (parked until this plan lands + install update — his explicit call; **flag to him after merge, do not touch**).
- opencode stale-port serve crash-loop escape hatch (Plan-4 follow-up, tracked separately).

---

### Task 1: `OptionsSchema()` on the Harness interface + all three adapter schemas + consistency tests

**Files:**
- Create: `internal/harness/options.go`
- Create: `internal/harness/schematest/schematest.go` (shared consistency-test helper)
- Modify: `internal/harness/harness.go` (interface method after `DecodeOptions`)
- Modify: `internal/harness/claude/options.go`, `internal/harness/codex/options.go`, `internal/harness/opencode/options.go` (one `OptionsSchema()` method each)
- Modify: `internal/harness/registry_test.go` (`fakeHarness` gains the method), `internal/config/harness_test.go` (`stubHarness` gains it), plus any other implementer the compiler flags (see Verified facts)
- Test: `internal/harness/claude/options_test.go`, `internal/harness/codex/options_test.go`, `internal/harness/opencode/options_test.go` (append), `internal/harness/schematest/schematest_test.go`

**Interfaces:**
- Produces (consumed by Tasks 3–5):

```go
// internal/harness/options.go
package harness

// OptionType says how a harness_options key renders in the web UI and how
// its submitted form value converts back into the map shape DecodeOptions
// expects. New types are added only when an adapter needs them (YAGNI).
type OptionType int

const (
	OptionString     OptionType = iota // single-line text → string
	OptionText                         // multi-line text (textarea) → string
	OptionBool                         // tri-state select unset/true/false → bool
	OptionEnum                         // fixed value list → string
	OptionStringList                   // comma-separated input → []any of string
	OptionYAMLMap                      // YAML textarea → map[string]any
)

// OptionField describes one harness_options key for web-form rendering. It
// must stay consistent with the adapter's DecodeOptions: same key set, and
// every EnumValues entry must decode cleanly — locked per adapter by
// schematest.Run.
type OptionField struct {
	Key        string // harness_options key; doubles as the form input suffix
	Label      string
	Help       string
	Type       OptionType
	EnumValues []string // OptionEnum only
	// Source optionally names a web option-source ("agents") whose values
	// populate a select for this field. Loose by-name hint: the web layer
	// falls back to Type's plain control when it can't resolve the name.
	Source string
}
```

- Interface addition in `internal/harness/harness.go`, directly after `DecodeOptions`:

```go
	// OptionsSchema describes this adapter's harness_options keys for web
	// form rendering, in render order. Must accept exactly the keys
	// DecodeOptions accepts (schematest.Run locks the two together).
	OptionsSchema() []OptionField
```

**Steps:**

- [ ] **Step 1: Write the shared consistency-test helper (this is the failing-test scaffold for all three adapters)**

```go
// internal/harness/schematest/schematest.go
// Package schematest asserts a harness adapter's OptionsSchema() and
// DecodeOptions agree. Imported by each adapter's tests — not production
// code — so the contract lives in exactly one place.
package schematest

import (
	"reflect"
	"sort"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Run asserts: (1) OptionsSchema keys == optionKeys (the adapter's accepted
// set); (2) every EnumValues entry decodes cleanly and a bogus enum value
// fails; (3) one well-formed sample per field decodes cleanly. samples
// overrides the built-in per-type sample for keys whose valid values are
// constrained beyond their type (e.g. opencode "permission").
func Run(t *testing.T, h harness.Harness, optionKeys []string, samples map[string]any) {
	t.Helper()
	fields := h.OptionsSchema()

	got := make([]string, 0, len(fields))
	for _, f := range fields {
		got = append(got, f.Key)
	}
	sort.Strings(got)
	want := append([]string(nil), optionKeys...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("OptionsSchema keys = %v, want %v", got, want)
	}

	for _, f := range fields {
		if f.Type == harness.OptionEnum {
			if len(f.EnumValues) == 0 {
				t.Errorf("enum field %q has no EnumValues", f.Key)
				continue
			}
			for _, v := range f.EnumValues {
				if _, err := h.DecodeOptions(map[string]any{f.Key: v}); err != nil {
					t.Errorf("enum value %q for %q rejected by DecodeOptions: %v", v, f.Key, err)
				}
			}
			if _, err := h.DecodeOptions(map[string]any{f.Key: "bogus-enum-value"}); err == nil {
				t.Errorf("bogus enum value for %q accepted — schema and validator disagree", f.Key)
			}
		}
		if _, err := h.DecodeOptions(map[string]any{f.Key: sampleFor(f, samples)}); err != nil {
			t.Errorf("sample for %q (type %d) rejected by DecodeOptions: %v", f.Key, f.Type, err)
		}
	}
}

func sampleFor(f harness.OptionField, samples map[string]any) any {
	if s, ok := samples[f.Key]; ok {
		return s
	}
	switch f.Type {
	case harness.OptionBool:
		return true
	case harness.OptionEnum:
		return f.EnumValues[0]
	case harness.OptionStringList:
		return []any{"x"}
	case harness.OptionYAMLMap:
		return map[string]any{}
	default:
		return "x"
	}
}
```

Also create `internal/harness/schematest/schematest_test.go` covering `sampleFor` per type and a `Run` happy-path against a tiny inline fake (a struct implementing `harness.Harness` with one enum field — copy `fakeHarness` from `registry_test.go` shape, methods returning zero values, plus a real `DecodeOptions`/`OptionsSchema` pair).

- [ ] **Step 2: Append the consistency test call to each adapter's options_test.go**

```go
// internal/harness/claude/options_test.go — append
func TestOptionsSchemaMatchesDecodeOptions(t *testing.T) {
	schematest.Run(t, Claude{}, optionKeys, nil)
}
```

```go
// internal/harness/codex/options_test.go — append
func TestOptionsSchemaMatchesDecodeOptions(t *testing.T) {
	schematest.Run(t, Codex{}, optionKeys, nil)
}
```

```go
// internal/harness/opencode/options_test.go — append
func TestOptionsSchemaMatchesDecodeOptions(t *testing.T) {
	schematest.Run(t, Opencode{}, optionKeys, map[string]any{
		"permission": map[string]any{"bash": "allow"},
	})
}
```

(Import `github.com/blackpaw-studio/leo/internal/harness/schematest` in each.)

- [ ] **Step 3: Run the new tests — expect COMPILE FAILURE** (`OptionsSchema` undefined). Run: `go test ./internal/harness/... 2>&1 | head -20`.

- [ ] **Step 4: Create `internal/harness/options.go` (code above), add the interface method to `harness.go` (comment + signature above), and implement per adapter:**

```go
// internal/harness/claude/options.go — append (add "github.com/blackpaw-studio/leo/internal/harness" to imports)

// OptionsSchema describes the claude harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Claude) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "permission_mode", Label: "Permission mode", Type: harness.OptionEnum,
			EnumValues: []string{"acceptEdits", "auto", "bypassPermissions", "default", "dontAsk", "plan"},
			Help:       "--permission-mode for the spawned claude"},
		{Key: "bypass_permissions", Label: "Bypass permissions", Type: harness.OptionBool,
			Help: "--dangerously-skip-permissions"},
		{Key: "remote_control", Label: "Remote control", Type: harness.OptionBool,
			Help: "--remote-control (claude.ai remote control)"},
		{Key: "agent", Label: "Agent", Type: harness.OptionString, Source: "agents",
			Help: "--agent: named claude sub-agent"},
		{Key: "allowed_tools", Label: "Allowed tools", Type: harness.OptionStringList,
			Help: "--allowed-tools, comma-separated"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Type: harness.OptionStringList,
			Help: "--disallowed-tools, comma-separated"},
		{Key: "append_system_prompt", Label: "Append system prompt", Type: harness.OptionText,
			Help: "--append-system-prompt"},
	}
}
```

```go
// internal/harness/codex/codex.go or options.go — append (import harness)

// OptionsSchema describes the codex harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Codex) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "sandbox", Label: "Sandbox", Type: harness.OptionEnum,
			EnumValues: []string{"read-only", "workspace-write", "danger-full-access"},
			Help:       "codex exec sandbox policy (default read-only)"},
	}
}
```

```go
// internal/harness/opencode/options.go — append

// OptionsSchema describes the opencode harness_options for web forms. Keys
// mirror optionKeys; TestOptionsSchemaMatchesDecodeOptions locks the two.
func (Opencode) OptionsSchema() []harness.OptionField {
	return []harness.OptionField{
		{Key: "permission", Label: "Permission", Type: harness.OptionYAMLMap,
			Help: "YAML map: tool → allow/ask/deny, or tool → {pattern: verdict}"},
	}
}
```

Enum lists are written literally (not derived from the `valid*` maps) so render order is stable; the consistency test guards drift.

- [ ] **Step 5: Ripple the interface change through every stub implementer the compiler flags.** Run `go build ./... && go vet ./...`; for each flagged fake/stub add:

```go
func (f fakeHarness) OptionsSchema() []OptionField { return nil }
```

(adjust receiver/package-qualifier per site; `stubHarness` in `internal/config/harness_test.go` uses `[]harness.OptionField`).

- [ ] **Step 6: Run the full gate.** `go test -race ./... && make lint && make e2e` — all green; adapter packages stay ≥80% (`go test -cover ./internal/harness/...`).

- [ ] **Step 7: Commit**

```bash
git add internal/harness
git commit -m "feat(harness): OptionsSchema() field descriptors + adapter schemas + consistency tests"
```

---

### Task 2: `harness` as a registered schema field + `"harnesses"` option source

**Files:**
- Modify: `internal/web/schema/registry.go` (register `harness` on all five config sections; shrink `Excluded`; rewrite its comment)
- Modify: `internal/web/schema/options.go` (add `"harnesses"` source; add non-panicking `TryFor`)
- Test: `internal/web/schema/registry_test.go` or the package's existing test files (extend), `internal/web/schema/options_test.go`

**Interfaces:**
- Consumes: `harness.Names()` (Task-independent, exists on main).
- Produces: registry field `{Key: "harness", Kind: KindSelect, Options: "harnesses", Group: "Harness"}` on Defaults/Process/Task/Template/Session; `OptionSources.TryFor(name string) []Option` (nil for unknown names — consumed by Task 3's Source-hint resolution).

**Steps:**

- [ ] **Step 1: Write failing tests**

```go
// internal/web/schema/options_test.go — append
func TestHarnessesOptionSource(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	opts := src.For("harnesses")
	if len(opts) < 4 { // inherit + claude + codex + opencode
		t.Fatalf("harnesses source = %v, want inherit + at least 3 registered harnesses", opts)
	}
	if opts[0].Value != "" || opts[0].Label != "inherit" {
		t.Errorf("first option = %+v, want empty-value inherit", opts[0])
	}
	names := map[string]bool{}
	for _, o := range opts[1:] {
		names[o.Value] = true
	}
	for _, want := range []string{"claude", "codex", "opencode"} {
		if !names[want] {
			t.Errorf("harnesses source missing %q", want)
		}
	}
}

func TestTryForUnknownSourceReturnsNil(t *testing.T) {
	src := OptionSources{Cfg: &config.Config{}}
	if got := src.TryFor("no-such-source"); got != nil {
		t.Fatalf("TryFor(unknown) = %v, want nil", got)
	}
}

func TestHarnessFieldRegisteredOnConfigSections(t *testing.T) {
	for _, section := range []Section{SectionDefaults, SectionProcess, SectionTask, SectionTemplate, SectionSession} {
		found := false
		for _, f := range FieldsFor(section) {
			if f.Key == "harness" {
				found = true
				if EffectiveKind(section, f) != KindSelect || f.Options != "harnesses" {
					t.Errorf("%s harness field = %+v, want KindSelect/harnesses", section, f)
				}
			}
		}
		if !found {
			t.Errorf("section %s has no harness field", section)
		}
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`TryFor` undefined; harness field missing). `go test ./internal/web/schema/ 2>&1 | head`.

- [ ] **Step 3: Implement.** In `options.go`: import `"github.com/blackpaw-studio/leo/internal/harness"`; restructure `For`/add `TryFor`:

```go
// For returns the options for a registry Options name. Unknown names panic:
// a typo in the registry should fail loudly in tests, not render empty.
func (o OptionSources) For(name string) []Option {
	opts := o.TryFor(name)
	if opts == nil {
		panic("schema: unknown options source " + name)
	}
	return opts
}

// TryFor resolves a named source like For but returns nil for unknown
// names. Harness OptionField.Source values are loose by-name hints, so
// unresolvable ones must fall back to a plain control, not panic.
func (o OptionSources) TryFor(name string) []Option {
	switch name {
	case "models":
		return modelOptions
	case "harnesses":
		return namedKeys(harness.Names(), "inherit")
	case "permission_modes":
		return []Option{{"", "inherit"}, {"default", "default"}, {"acceptEdits", "acceptEdits"},
			{"auto", "auto"}, {"bypassPermissions", "bypassPermissions"},
			{"dontAsk", "dontAsk"}, {"plan", "plan"}}
	case "runtimes":
		return []Option{{"oneshot", "oneshot"}, {"persistent", "persistent"}}
	case "sessions":
		return namedKeys(keysOf(o.Cfg.Sessions), "derived per task")
	case "templates":
		return namedKeys(keysOf(o.Cfg.Templates), "none")
	case "agents":
		var names []string
		if o.Agents != nil {
			names = o.Agents()
		}
		return namedKeys(names, "none")
	}
	return nil
}
```

(`"models"` stays for now — Task 4 deletes it together with the datalist flip.) In `registry.go`: add the shared builder and insert it per section:

```go
func fHarness() Field {
	return Field{Key: "harness", Label: "Harness", Kind: KindSelect, Options: "harnesses", Group: "Harness",
		Help: "Coding agent CLI driving this scope; options below are harness-specific"}
}
```

Insert `fHarness()` immediately **before** the model field in each of the five sections (Defaults: before its inline model entry; Process/Task/Template/Session: before `fModel(...)`). Remove `"harness"` from all five `Excluded` lists (keep `"harness_options"`), and replace the `Excluded` comment block's harness paragraph with:

```go
	// harness_options is excluded from the flat registry on every scope
	// below: it's a map rendered by the dedicated harness-options sub-form
	// (internal/web/schema/harnessform.go + components/harness_options.html),
	// not a flat field — same excluded-with-own-UI pattern as client.hosts.
	// harness itself IS registered (KindSelect over harness.Names()).
```

(keep the existing provider + flat-claude-fields paragraphs verbatim).

- [ ] **Step 4: Run the package + drift test.** `go test ./internal/web/schema/ -run 'TestRegistryCoversConfig|TestHarness|TestTryFor' -v` — PASS. Then the full web package: `go test ./internal/web/...` — existing form tests must pass unmodified (the new field renders through existing KindSelect machinery).

- [ ] **Step 5: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web/schema
git commit -m "feat(web): harness dropdown field + harnesses option source"
```

---

### Task 3: Harness options sub-form component (render + apply) and model helpers

**Files:**
- Create: `internal/web/schema/harnessform.go`
- Create: `internal/web/schema/harnessform_test.go`
- Modify: `internal/web/schema/options.go` (delete `modelOptions` + the `"models"` case; add `ModelSuggestions`/`ModelPlaceholder`)
- Modify: `internal/web/schema/registry.go` (model fields: `KindSelect/Options:"models"` → `KindDatalist`, drop Options)
- Modify: `internal/web/schema/schema.go` (add `KindDatalist` after `KindTextarea`), `internal/web/schema/values.go` (`isTextKind` gains `KindDatalist`)

**Interfaces:**
- Consumes: `harness.OptionField`/`OptionType` (Task 1), `OptionSources.TryFor` (Task 2).
- Produces (consumed by Tasks 4–6):

```go
type HarnessFieldValue struct {
	harness.OptionField
	InputName string   // "harness_options.<key>"
	Value     string   // rendered current value ("" = unset)
	Inherited string   // defaults-cascade placeholder ("" = none)
	Opts      []Option // enum values or resolved Source list; nil = plain control
}

func HarnessOptionValues(h harness.Harness, own, inherited map[string]any, src OptionSources) []HarnessFieldValue
func ApplyHarnessOptions(h harness.Harness, form url.Values) (map[string]any, error)
func ModelSuggestions(harnessName string) []Option // claude → ValidModels(); others → nil
func ModelPlaceholder(harnessName string) string   // codex/opencode format hints; "" otherwise
const HarnessOptionPrefix = "harness_options."
```

**Steps:**

- [ ] **Step 1: Write the failing tests** (`harnessform_test.go`) — table-driven, AAA:

```go
package schema

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/codex"
	"github.com/blackpaw-studio/leo/internal/harness/opencode"
)

func TestHarnessOptionValuesRendersOwnAndInherited(t *testing.T) {
	own := map[string]any{"permission_mode": "plan", "allowed_tools": []any{"Bash", "Read"}}
	inherited := map[string]any{"bypass_permissions": true, "permission_mode": "auto"}
	vals := HarnessOptionValues(claude.Claude{}, own, inherited, OptionSources{})
	byKey := map[string]HarnessFieldValue{}
	for _, v := range vals {
		byKey[v.Key] = v
	}
	if got := byKey["permission_mode"]; got.Value != "plan" || got.Inherited != "auto" {
		t.Errorf("permission_mode = %+v, want Value=plan Inherited=auto", got)
	}
	if got := byKey["allowed_tools"].Value; got != "Bash, Read" {
		t.Errorf("allowed_tools Value = %q, want CSV join", got)
	}
	if got := byKey["bypass_permissions"]; got.Value != "" || got.Inherited != "true" {
		t.Errorf("bypass_permissions = %+v, want unset with Inherited=true", got)
	}
	if got := byKey["permission_mode"].InputName; got != "harness_options.permission_mode" {
		t.Errorf("InputName = %q", got)
	}
}

func TestHarnessOptionValuesYAMLMapRoundTrips(t *testing.T) {
	own := map[string]any{"permission": map[string]any{"bash": map[string]any{"git *": "allow"}}}
	vals := HarnessOptionValues(opencode.Opencode{}, own, nil, OptionSources{})
	if len(vals) != 1 {
		t.Fatalf("opencode fields = %d, want 1", len(vals))
	}
	form := url.Values{"harness_options.permission": {vals[0].Value}}
	got, err := ApplyHarnessOptions(opencode.Opencode{}, form)
	if err != nil {
		t.Fatalf("apply rendered YAML: %v", err)
	}
	if !reflect.DeepEqual(got, own) {
		t.Errorf("round-trip = %#v, want %#v", got, own)
	}
}

func TestApplyHarnessOptionsShapesAndOmissions(t *testing.T) {
	form := url.Values{
		"harness_options.permission_mode":    {"acceptEdits"},
		"harness_options.bypass_permissions": {"true"},
		"harness_options.allowed_tools":      {"Bash, Read"},
		"harness_options.agent":              {""},   // empty → omitted
		"harness_options.remote_control":     {""},   // tri-state inherit → omitted
	}
	got, err := ApplyHarnessOptions(claude.Claude{}, form)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"permission_mode":    "acceptEdits",
		"bypass_permissions": true,
		"allowed_tools":      []any{"Bash", "Read"}, // []any, NOT []string — DecodeOptions asserts this
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %#v, want %#v", got, want)
	}
	if _, err := (claude.Claude{}).DecodeOptions(got); err != nil {
		t.Errorf("apply output rejected by DecodeOptions: %v", err)
	}
}

func TestApplyHarnessOptionsEmptyFormReturnsNil(t *testing.T) {
	got, err := ApplyHarnessOptions(codex.Codex{}, url.Values{})
	if err != nil || got != nil {
		t.Fatalf("got %v, %v; want nil, nil", got, err)
	}
}

func TestApplyHarnessOptionsBadYAMLIsKeyNamedError(t *testing.T) {
	form := url.Values{"harness_options.permission": {": not yaml ["}}
	if _, err := ApplyHarnessOptions(opencode.Opencode{}, form); err == nil {
		t.Fatal("want error for bad YAML")
	} else if want := "permission"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q does not name the key %q", err, want)
	}
}

func TestModelSuggestionsAndPlaceholder(t *testing.T) {
	if got := ModelSuggestions("claude"); len(got) == 0 {
		t.Error("claude suggestions empty")
	}
	if got := ModelSuggestions("codex"); got != nil {
		t.Errorf("codex suggestions = %v, want nil", got)
	}
	if ModelPlaceholder("codex") == "" || ModelPlaceholder("opencode") == "" {
		t.Error("codex/opencode placeholders must hint the model format")
	}
	if ModelPlaceholder("claude") != "" {
		t.Error("claude placeholder must be empty (Inherited placeholder wins)")
	}
}
```

(Also a case: `ApplyHarnessOptions` bool input `"bogus"` → key-named error. Add `strings` import.)

- [ ] **Step 2: Run — expect FAIL / compile error.** `go test ./internal/web/schema/ -run 'Harness|Model' 2>&1 | head`.

- [ ] **Step 3: Implement `harnessform.go`:**

```go
package schema

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/blackpaw-studio/leo/internal/harness"
	"gopkg.in/yaml.v3"
)

// HarnessOptionPrefix namespaces harness-options form inputs so they can
// never collide with registry field names.
const HarnessOptionPrefix = "harness_options."

// HarnessFieldValue is one harness_options key resolved for rendering.
type HarnessFieldValue struct {
	harness.OptionField
	InputName string   // HarnessOptionPrefix + Key
	Value     string   // rendered current value ("" = unset)
	Inherited string   // defaults-cascade placeholder ("" = none)
	Opts      []Option // enum values or a resolved Source list; nil = plain control
}

// HarnessOptionValues renders h's options schema against a scope's own
// harness_options map. inherited supplies the defaults-cascade values shown
// as placeholders — pass nil when the cascade doesn't apply (the defaults
// form itself, sessions, or a scope whose harness differs from defaults;
// mirrors config's scopeHarnessOptions rules).
func HarnessOptionValues(h harness.Harness, own, inherited map[string]any, src OptionSources) []HarnessFieldValue {
	var out []HarnessFieldValue
	for _, f := range h.OptionsSchema() {
		v := HarnessFieldValue{
			OptionField: f,
			InputName:   HarnessOptionPrefix + f.Key,
			Value:       renderOptionValue(own[f.Key], f.Type),
			Inherited:   renderOptionValue(inherited[f.Key], f.Type),
		}
		switch {
		case f.Type == harness.OptionEnum:
			for _, ev := range f.EnumValues {
				v.Opts = append(v.Opts, Option{ev, ev})
			}
		case f.Source != "":
			if opts := src.TryFor(f.Source); len(opts) > 1 { // >1: source resolved beyond its empty entry
				v.Opts = opts[1:] // drop the source's own empty entry; the template renders inherit itself
			}
		}
		out = append(out, v)
	}
	return out
}

// renderOptionValue converts a stored harness_options value to its form
// representation. Tolerant of hand-edited config (never panics): unknown
// shapes fall back to fmt.Sprint.
func renderOptionValue(v any, t harness.OptionType) string {
	if v == nil {
		return ""
	}
	switch t {
	case harness.OptionBool:
		if b, ok := v.(bool); ok {
			return strconv.FormatBool(b)
		}
	case harness.OptionStringList:
		if items, ok := v.([]any); ok {
			parts := make([]string, 0, len(items))
			for _, it := range items {
				parts = append(parts, fmt.Sprint(it))
			}
			return strings.Join(parts, ", ")
		}
	case harness.OptionYAMLMap:
		b, err := yaml.Marshal(v)
		if err != nil {
			return fmt.Sprint(v)
		}
		return strings.TrimSpace(string(b))
	}
	return fmt.Sprint(v)
}

// ApplyHarnessOptions parses the harness_options.* inputs in form against
// h's schema. Empty inputs omit their key; an all-empty form returns nil so
// saved YAML never carries an empty harness_options: {}. Value shapes match
// the YAML decoder's (string, bool, []any, map[string]any) because the
// adapters' DecodeOptions type-assert exactly those.
func ApplyHarnessOptions(h harness.Harness, form url.Values) (map[string]any, error) {
	var out map[string]any
	set := func(k string, v any) {
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	for _, f := range h.OptionsSchema() {
		raw := strings.TrimSpace(form.Get(HarnessOptionPrefix + f.Key))
		if raw == "" {
			continue
		}
		switch f.Type {
		case harness.OptionBool:
			b, err := strconv.ParseBool(raw)
			if err != nil {
				return nil, fmt.Errorf("harness option %s: %q is not true/false", f.Key, raw)
			}
			set(f.Key, b)
		case harness.OptionStringList:
			items := parseCSV(raw)
			vals := make([]any, 0, len(items))
			for _, it := range items {
				vals = append(vals, it)
			}
			set(f.Key, vals)
		case harness.OptionYAMLMap:
			var m map[string]any
			if err := yaml.Unmarshal([]byte(raw), &m); err != nil {
				return nil, fmt.Errorf("harness option %s: %v", f.Key, err)
			}
			if len(m) > 0 {
				set(f.Key, m)
			}
		default: // OptionString, OptionText, OptionEnum
			set(f.Key, raw)
		}
	}
	return out, nil
}
```

In `options.go`: delete `modelOptions` (and its stale keep-in-sync comment) and the `"models"` case from `TryFor`; add (import `"github.com/blackpaw-studio/leo/internal/harness/claude"`):

```go
// ModelSuggestions returns datalist suggestions for the model input under
// the given harness. Claude's authoritative list comes straight from the
// adapter (no more keep-in-sync copy); other harnesses are free-form —
// ValidateModel gates on save.
func ModelSuggestions(harnessName string) []Option {
	if harnessName != "claude" {
		return nil
	}
	var opts []Option
	for _, m := range claude.ValidModels() {
		opts = append(opts, Option{m, m})
	}
	return opts
}

// ModelPlaceholder returns the format hint shown in an empty model input.
// Claude returns "" so the standard "inherit: <model>" placeholder wins.
func ModelPlaceholder(harnessName string) string {
	switch harnessName {
	case "codex":
		return "e.g. gpt-5.3-codex"
	case "opencode":
		return "provider/model, e.g. anthropic/claude-sonnet-5"
	}
	return ""
}
```

In `schema.go`: append `KindDatalist // free-text input with a <datalist> of suggestions` to the Kind const block (after `KindTextarea`). In `values.go`: add `KindDatalist` to `isTextKind`'s case list. In `registry.go`: change both model field literals (`fModel` + SectionDefaults' inline entry) from `Kind: KindSelect, Options: "models"` to `Kind: KindDatalist` (no Options).

- [ ] **Step 4: Run the schema package.** `go test ./internal/web/schema/ -v 2>&1 | tail -20` — all PASS including drift. Fix any existing test that asserted the `"models"` source or `KindSelect` model (adapt the assertion — the behavior change is deliberate, spec §Decisions 2/6).

- [ ] **Step 5: Run the web package.** `go test ./internal/web/...`. Any failure here is a render test asserting the old model `<select>` — adapt those assertions to the datalist input (Task 4 wires the actual template; until then KindDatalist falls through `kindName`'s default to a plain text input, which is valid interim behavior).

- [ ] **Step 6: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web/schema
git commit -m "feat(web): harness options sub-form component + harness-aware model helpers"
```

---

### Task 4: Wire the sub-form and datalist into buildForm and the templates

**Files:**
- Modify: `internal/web/handlers_config.go` (`fieldView` + `formData` extensions, `buildFormWithHarness`, `harnessView`, `kindName` datalist case)
- Modify: `internal/web/handlers_pages.go` (defaults/task/process/template call sites), `internal/web/handlers_sessions.go` (sessions loop call site)
- Modify: `internal/web/web.go` (funcMap gains `optTypeName`)
- Create: `internal/web/templates/components/harness_options.html`
- Modify: `internal/web/templates/components/form.html` (sub-form block + datalist control)
- Test: extend the existing web handler/render tests (same files the current form tests live in)

**Interfaces:**
- Consumes: `schema.HarnessOptionValues`, `ApplyHarnessOptions` (Task 6 uses that), `ModelSuggestions`, `ModelPlaceholder`, `HarnessFieldValue` (Task 3); config cascade helpers (Verified facts).
- Produces (consumed by Task 5):

```go
type fieldView struct {
	schema.FieldValue
	Opts        []schema.Option
	Section     schema.Section // for the harness select's hx-get URL
	Scope       string         // scope-unique element-id suffix
	Placeholder string         // per-harness model format hint
}

type formData struct {
	Action      string
	Scope       string
	Fields      []fieldView
	Harness     *harnessFormData // nil = section has no harness sub-form
	SubmitLabel string
	DeleteURL   string
}

type harnessFormData struct {
	Section schema.Section
	Scope   string
	Harness string // effective harness the sub-form is rendered for
	Fields  []schema.HarnessFieldValue
}

func (s *Server) buildFormWithHarness(section schema.Section, target any, cfg *config.Config, action, scope string) formData
```

Scope strings: `"defaults"`, `"process-<name>"`, `"task-<name>"`, `"template-<name>"`, `"session-<name>"`.

**Steps:**

- [ ] **Step 1: Characterization guard.** Run `go test ./internal/web/... -count=1` on the pre-task tree and note it green; this task must only ADD rendered output for harness sections (plus the model control change already landed in Task 3).

- [ ] **Step 2: Write failing tests** — in the web package, alongside the existing form/handler tests:

```go
func TestBuildFormWithHarnessProcess(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Harness: "claude",
			HarnessOptions: map[string]any{"permission_mode": "auto"}},
		Processes: map[string]config.ProcessConfig{"builder": {
			Workspace:      "/w",
			HarnessOptions: map[string]any{"permission_mode": "plan"},
		}},
	}
	s := newTestServer(t, cfg) // reuse the package's existing test-server constructor
	p := cfg.Processes["builder"]
	fd := s.buildFormWithHarness(schema.SectionProcess, &p, cfg, "/web/config/process/builder", "process-builder")

	if fd.Harness == nil || fd.Harness.Harness != "claude" {
		t.Fatalf("Harness sub-form = %+v, want claude", fd.Harness)
	}
	if fd.Scope != "process-builder" || fd.Harness.Scope != "process-builder" {
		t.Errorf("scope not threaded: %q / %q", fd.Scope, fd.Harness.Scope)
	}
	byKey := map[string]schema.HarnessFieldValue{}
	for _, f := range fd.Harness.Fields {
		byKey[f.Key] = f
	}
	if got := byKey["permission_mode"]; got.Value != "plan" || got.Inherited != "auto" {
		t.Errorf("permission_mode = %+v (own overrides, inherited placeholder)", got)
	}
	for _, f := range fd.Fields {
		if f.Key == "model" {
			if len(f.Opts) == 0 {
				t.Error("claude model field has no datalist suggestions")
			}
			if f.Scope != "process-builder" {
				t.Errorf("model field Scope = %q", f.Scope)
			}
		}
	}
}

func TestBuildFormWithHarnessNonClaudeModelHint(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"c": {Harness: "codex"}}}
	s := newTestServer(t, cfg)
	p := cfg.Processes["c"]
	fd := s.buildFormWithHarness(schema.SectionProcess, &p, cfg, "/a", "process-c")
	if fd.Harness == nil || fd.Harness.Harness != "codex" {
		t.Fatalf("want codex sub-form, got %+v", fd.Harness)
	}
	// scope harness (codex) != defaults harness (claude default) → no inherited placeholders
	for _, f := range fd.Harness.Fields {
		if f.Inherited != "" {
			t.Errorf("field %s inherited %q, want none across harnesses", f.Key, f.Inherited)
		}
	}
	for _, f := range fd.Fields {
		if f.Key == "model" && (f.Opts != nil || f.Placeholder == "") {
			t.Errorf("codex model field = opts %v placeholder %q, want nil + hint", f.Opts, f.Placeholder)
		}
	}
}

func TestSessionsFormNeverInheritsHarnessOptions(t *testing.T) {
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{HarnessOptions: map[string]any{"permission_mode": "auto"}},
		Sessions: map[string]config.SessionConfig{"r": {Workspace: "/w"}},
	}
	s := newTestServer(t, cfg)
	sc := cfg.Sessions["r"]
	fd := s.buildFormWithHarness(schema.SectionSession, &sc, cfg, "/a", "session-r")
	for _, f := range fd.Harness.Fields {
		if f.Inherited != "" {
			t.Errorf("session field %s shows inherited %q; sessions never cascade", f.Key, f.Inherited)
		}
	}
}
```

Plus a render test: execute the `config_form` template with a `formData` containing a Harness sub-form and assert the HTML contains `name="harness_options.permission_mode"`, the "Harness options" group label, `list="dl-model-process-builder"`, and `<datalist id="dl-model-process-builder">`. (Use the package's existing template-render test pattern; if none renders `config_form` directly, render via a full page handler test.)

- [ ] **Step 3: Run — expect FAIL.** `go test ./internal/web/ -run 'BuildFormWithHarness|SessionsFormNever' 2>&1 | head`.

- [ ] **Step 4: Implement.** In `handlers_config.go` — extend the two structs exactly as the Interfaces block shows, then:

```go
// buildFormWithHarness wraps buildForm for the five config sections that
// carry harness/harness_options: it threads a scope-unique id suffix,
// resolves the effective harness, attaches the options sub-form, and makes
// the model field harness-aware (datalist suggestions / format hint).
func (s *Server) buildFormWithHarness(section schema.Section, target any, cfg *config.Config, action, scope string) formData {
	fd := s.buildForm(section, target, cfg, action)
	fd.Scope = scope
	for i := range fd.Fields {
		fd.Fields[i].Section = section
		fd.Fields[i].Scope = scope
	}

	own, harnessName, inherited := harnessView(target, cfg)
	h, err := harness.Get(harnessName)
	if err != nil {
		// Unregistered harness in a hand-edited config: render the flat form
		// without a sub-form rather than 500ing the page; Validate() reports
		// the real error on save.
		return fd
	}
	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	fd.Harness = &harnessFormData{
		Section: section,
		Scope:   scope,
		Harness: harnessName,
		Fields:  schema.HarnessOptionValues(h, own, inherited, src),
	}
	for i := range fd.Fields {
		if fd.Fields[i].Key == "model" {
			fd.Fields[i].Opts = schema.ModelSuggestions(harnessName)
			fd.Fields[i].Placeholder = schema.ModelPlaceholder(harnessName)
		}
	}
	return fd
}

// harnessView resolves a form target's own options map, effective harness,
// and the inherited-placeholder map per the cascade rules (mirrors
// config.scopeHarnessOptions: defaults' options cascade only into scopes
// running the same harness; sessions and defaults itself never show
// inherited placeholders).
func harnessView(target any, cfg *config.Config) (own map[string]any, name string, inherited map[string]any) {
	sameHarnessDefaults := func(n string) map[string]any {
		if n == cfg.DefaultsHarness() {
			return cfg.Defaults.HarnessOptions
		}
		return nil
	}
	switch v := target.(type) {
	case *config.DefaultsConfig:
		return v.HarnessOptions, cfg.DefaultsHarness(), nil
	case *config.ProcessConfig:
		name = cfg.ProcessHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.TaskConfig:
		name = cfg.TaskHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.TemplateConfig:
		name = cfg.TemplateHarness(*v)
		return v.HarnessOptions, name, sameHarnessDefaults(name)
	case *config.SessionConfig:
		return v.HarnessOptions, cfg.SessionHarness(*v), nil
	}
	return nil, config.DefaultHarnessName, nil
}
```

Add to `kindName`: `case schema.KindDatalist: return "datalist"`. Add `optTypeName` (new small func in `handlers_config.go`, mirroring `kindName`) and register it in `web.go`'s funcMap next to `"kindName"`:

```go
// optTypeName maps a harness.OptionType to the string
// components/harness_options.html switches on.
func optTypeName(t harness.OptionType) string {
	switch t {
	case harness.OptionBool:
		return "bool"
	case harness.OptionEnum:
		return "enum"
	case harness.OptionStringList:
		return "list"
	case harness.OptionYAMLMap:
		return "yamlmap"
	case harness.OptionText:
		return "text"
	default:
		return "string"
	}
}
```

Call-site swaps (buildForm → buildFormWithHarness + scope):
- `handlers_pages.go:218` (defaults): `s.buildFormWithHarness(schema.SectionDefaults, &cfg.Defaults, cfg, "/web/config/defaults", "defaults")`
- `handlers_pages.go:387` (task): scope `"task-"+name`
- `handlers_pages.go:442` (process): scope `"process-"+name`
- `handlers_pages.go:510` (template): scope `"template-"+name`
- `handlers_sessions.go:58` (session loop): scope `"session-"+name`
(Web/Client/Host call sites stay on plain `buildForm`.)

Templates — `components/harness_options.html` (new):

```html
{{define "harness_options"}}
{{$scope := .Scope}}
{{range .Fields}}
<div class="frow">
  <label for="ho-{{.Key}}-{{$scope}}">{{.Label}}</label>
  <div class="fctl">
    {{if eq (optTypeName .Type) "bool"}}
      <select id="ho-{{.Key}}-{{$scope}}" name="{{.InputName}}">
        <option value="" {{if eq .Value ""}}selected{{end}}>{{if .Inherited}}inherit ({{.Inherited}}){{else}}inherit{{end}}</option>
        <option value="true" {{if eq .Value "true"}}selected{{end}}>on</option>
        <option value="false" {{if eq .Value "false"}}selected{{end}}>off</option>
      </select>
    {{else if .Opts}}
      <select id="ho-{{.Key}}-{{$scope}}" name="{{.InputName}}">
        <option value="" {{if eq .Value ""}}selected{{end}}>{{if .Inherited}}inherit ({{.Inherited}}){{else}}inherit{{end}}</option>
        {{$v := .Value}}{{range .Opts}}<option value="{{.Value}}" {{if eq .Value $v}}selected{{end}}>{{.Label}}</option>{{end}}
      </select>
    {{else if eq (optTypeName .Type) "yamlmap"}}
      <textarea id="ho-{{.Key}}-{{$scope}}" name="{{.InputName}}" rows="4" class="mono"
                {{if .Inherited}}placeholder="inherit:&#10;{{.Inherited}}"{{end}}>{{.Value}}</textarea>
    {{else if eq (optTypeName .Type) "text"}}
      <textarea id="ho-{{.Key}}-{{$scope}}" name="{{.InputName}}" rows="3"
                {{if .Inherited}}placeholder="inherit: {{.Inherited}}"{{end}}>{{.Value}}</textarea>
    {{else}}
      <input type="text" id="ho-{{.Key}}-{{$scope}}" name="{{.InputName}}" value="{{.Value}}"
             {{if .Inherited}}placeholder="inherit: {{.Inherited}}"{{end}}>
    {{end}}
    {{if .Help}}<div class="form-help">{{.Help}}</div>{{end}}
  </div>
</div>
{{end}}
{{end}}
```

`components/form.html` — two edits. (1) After the non-advanced fields loop (after line 7's `{{end}}{{end}}`), insert:

```html
  {{if .Harness}}
  <div class="form-group-label">Harness options</div>
  <div class="harness-options">{{template "harness_options" .Harness}}</div>
  {{end}}
```

(2) In `config_field`, add a datalist branch before the final `{{else}}`:

```html
    {{else if eq (kindName .Kind) "datalist"}}
      <input type="text" id="f-{{.Key}}" name="{{.Key}}" value="{{.Value}}" list="dl-{{.Key}}-{{.Scope}}"
             {{if .Inherited}}placeholder="inherit: {{.Inherited}}"{{else if .Placeholder}}placeholder="{{.Placeholder}}"{{end}}>
      <datalist id="dl-{{.Key}}-{{.Scope}}">{{range .Opts}}<option value="{{.Value}}"></option>{{end}}</datalist>
```

- [ ] **Step 5: Run.** `go test ./internal/web/... -count=1` — new tests PASS, existing tests PASS (any old model-`<select>` assertions were already adapted in Task 3).

- [ ] **Step 6: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web
git commit -m "feat(web): render harness options sub-form + harness-aware model datalist"
```

---

### Task 5: htmx partial — re-render the sub-form when the harness dropdown changes

**Files:**
- Create: `internal/web/handlers_harness.go`
- Create: `internal/web/handlers_harness_test.go`
- Modify: `internal/web/web.go` (route next to `/web/cron/preview`)
- Modify: `internal/web/templates/components/form.html` (hx attributes on the harness select)
- Create: `internal/web/templates/components/harness_options_partial.html`

**Interfaces:**
- Consumes: `buildFormWithHarness`'s helpers (`harnessView`), `schema.HarnessOptionValues/ModelSuggestions` (Tasks 3–4).
- Produces: `GET /web/partials/harness-options?section=<s>&scope=<name>&harness=<h>` → HTML fragment: the `harness_options` sub-form for the selected harness plus an `hx-swap-oob` datalist refresh for the model input.

**Steps:**

- [ ] **Step 1: Write failing handler tests** (`handlers_harness_test.go`, using the package's existing authed-request test helpers):

```go
func TestHarnessOptionsPartialRendersSelectedHarness(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"b": {
		HarnessOptions: map[string]any{"permission_mode": "plan"},
	}}}
	s := newTestServer(t, cfg)

	// Same harness as stored (claude via default) → stored values render.
	body := getBody(t, s, "/web/partials/harness-options?section=process&scope=b&harness=claude")
	if !strings.Contains(body, `name="harness_options.permission_mode"`) {
		t.Errorf("claude partial missing permission_mode field: %s", body)
	}
	if !strings.Contains(body, `value="plan" selected`) {
		t.Errorf("claude partial does not preselect stored value plan: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob`) || !strings.Contains(body, `dl-model-process-b`) {
		t.Errorf("partial missing OOB datalist refresh: %s", body)
	}

	// Different harness → blank slate for that harness's fields.
	body = getBody(t, s, "/web/partials/harness-options?section=process&scope=b&harness=codex")
	if !strings.Contains(body, `name="harness_options.sandbox"`) {
		t.Errorf("codex partial missing sandbox field: %s", body)
	}
	if strings.Contains(body, "permission_mode") {
		t.Errorf("codex partial leaked claude fields: %s", body)
	}
}

func TestHarnessOptionsPartialRejectsUnknown(t *testing.T) {
	s := newTestServer(t, &config.Config{})
	for _, path := range []string{
		"/web/partials/harness-options?section=process&scope=nope&harness=claude", // unknown scope
		"/web/partials/harness-options?section=bogus&scope=x&harness=claude",      // unknown section
		"/web/partials/harness-options?section=defaults&harness=doesnotexist",     // unknown harness
	} {
		if code := getStatus(t, s, path); code != http.StatusBadRequest && code != http.StatusNotFound {
			t.Errorf("%s → %d, want 400/404", path, code)
		}
	}
}

func TestHarnessOptionsPartialEmptyHarnessMeansInherit(t *testing.T) {
	cfg := &config.Config{Defaults: config.DefaultsConfig{Harness: "codex"},
		Tasks: map[string]config.TaskConfig{"n": {Schedule: "@daily", PromptFile: "p.md"}}}
	s := newTestServer(t, cfg)
	body := getBody(t, s, "/web/partials/harness-options?section=task&scope=n&harness=")
	if !strings.Contains(body, `name="harness_options.sandbox"`) {
		t.Errorf("inherit resolution failed — want codex fields, got: %s", body)
	}
}
```

(`getBody`/`getStatus`: use whatever authed GET helper the web tests already have; if none fits, add tiny local helpers wrapping `httptest` + the session cookie the package's tests already mint.)

- [ ] **Step 2: Run — expect FAIL (404 route).** `go test ./internal/web/ -run HarnessOptionsPartial 2>&1 | head`.

- [ ] **Step 3: Implement `handlers_harness.go`:**

```go
package web

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// harnessPartialData feeds harness_options_partial.html: the sub-form for
// the newly selected harness plus the OOB datalist refresh for the model
// input (a stale datalist is harmless — ValidateModel gates on save — but
// refreshing it keeps suggestions honest).
type harnessPartialData struct {
	Form      harnessFormData
	ModelOpts []schema.Option
}

// handleHarnessOptionsPartial re-renders the harness-options sub-form when
// a form's harness dropdown changes. Stored option values render only when
// the selected harness matches the scope's stored effective harness —
// switching harnesses starts from a blank slate (the stored map still
// belongs to the old harness until the user saves).
func (s *Server) handleHarnessOptionsPartial(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	section := schema.Section(q.Get("section"))
	scopeName := q.Get("scope")
	selected := q.Get("harness")

	cfg, err := s.loadConfig()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	target, scope, ok := locateHarnessScope(cfg, section, scopeName)
	if !ok {
		http.Error(w, "unknown section or scope", http.StatusNotFound)
		return
	}

	stored, storedName, _ := harnessView(target, cfg)
	name := selected
	if name == "" {
		if section == schema.SectionDefaults {
			name = config.DefaultHarnessName
		} else {
			name = cfg.DefaultsHarness()
		}
	}
	h, err := harness.Get(name)
	if err != nil {
		http.Error(w, "unknown harness", http.StatusBadRequest)
		return
	}
	if name != storedName {
		stored = nil // blank slate across harnesses
	}
	// Recompute the inherited-placeholder map against the SELECTED harness
	// (harnessView computed it against the stored one — switching TO the
	// defaults harness must light the placeholders up, and away must drop
	// them). Sessions and the defaults form itself never show any.
	var inherited map[string]any
	if section != schema.SectionDefaults && section != schema.SectionSession &&
		name == cfg.DefaultsHarness() {
		inherited = cfg.Defaults.HarnessOptions
	}

	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	data := harnessPartialData{
		Form: harnessFormData{Section: section, Scope: scope, Harness: name,
			Fields: schema.HarnessOptionValues(h, stored, inherited, src)},
		ModelOpts: schema.ModelSuggestions(name),
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.templates.ExecuteTemplate(w, "harness_options_partial", data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// locateHarnessScope maps a (section, scope-name) pair to the config struct
// backing its form, plus the scope id suffix used for element ids.
func locateHarnessScope(cfg *config.Config, section schema.Section, name string) (any, string, bool) {
	switch section {
	case schema.SectionDefaults:
		return &cfg.Defaults, "defaults", true
	case schema.SectionProcess:
		if p, ok := cfg.Processes[name]; ok {
			return &p, "process-" + name, true
		}
	case schema.SectionTask:
		if t, ok := cfg.Tasks[name]; ok {
			return &t, "task-" + name, true
		}
	case schema.SectionTemplate:
		if t, ok := cfg.Templates[name]; ok {
			return &t, "template-" + name, true
		}
	case schema.SectionSession:
		if sc, ok := cfg.Sessions[name]; ok {
			return &sc, "session-" + name, true
		}
	}
	return nil, "", false
}
```

Note the `inherited` recomputation above mirrors `harnessView` but against the *selected* harness (`harnessView` computed it against the stored one).

Template `components/harness_options_partial.html`:

```html
{{define "harness_options_partial"}}
{{template "harness_options" .Form}}
<datalist id="dl-model-{{.Form.Scope}}" hx-swap-oob="true">{{range .ModelOpts}}<option value="{{.Value}}"></option>{{end}}</datalist>
{{end}}
```

Route in `web.go`, next to the cron preview:

```go
	mux.HandleFunc("GET /web/partials/harness-options", s.handleHarnessOptionsPartial)
```

`form.html` `config_field` select branch — thread the hx attributes onto the harness select only:

```html
    {{else if eq (kindName .Kind) "select"}}
      <select id="f-{{.Key}}" name="{{.Key}}"
        {{if eq .Key "harness"}} hx-get="/web/partials/harness-options?section={{.Section}}&scope={{.Scope}}"
        hx-trigger="change" hx-include="this" hx-target="next .harness-options" hx-swap="innerHTML"{{end}}>
        {{$v := .Value}}{{range .Opts}}<option value="{{.Value}}" {{if eq .Value $v}}selected{{end}}>{{.Label}}</option>{{end}}
      </select>
```

(`scope` in the URL is the map-key name — `b`, not `process-b`; `locateHarnessScope` rebuilds the id suffix. For defaults the scope param is ignored.) **Note:** `hx-include="this"` submits the select's own `name="harness"` value as the `harness` query param.

- [ ] **Step 4: Run.** `go test ./internal/web/ -run HarnessOptionsPartial -v` — PASS; then the full package.

- [ ] **Step 5: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web
git commit -m "feat(web): htmx harness-options partial — dropdown swaps the sub-form"
```

---

### Task 6: Save path — parse harness_options on every config save, gate through Validate

**Files:**
- Modify: `internal/web/handlers_config.go` (`applySection` gains an `applyOptions` hook; the five harness-section handlers pass closures; the three non-harness handlers pass nil)
- Test: `internal/web/handlers_config_test.go` (or wherever the existing save-handler tests live — extend there)

**Interfaces:**
- Consumes: `schema.ApplyHarnessOptions` (Task 3), config cascade helpers.
- Produces: every harness-section POST parses `harness_options.*` inputs into the scope's `HarnessOptions` before `validateAndSave`; a rejected option never reaches disk.

**Steps:**

- [ ] **Step 1: Write failing tests** (POST through the real handlers, using the package's existing form-POST test helpers):

```go
func TestSaveProcessWithHarnessOptions(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"b": {Workspace: "/w"}}}
	s, cfgPath := newTestServerWithConfigFile(t, cfg) // reuse/extend the package's save-test scaffold
	// Every registered SectionProcess field renders in the real form, so a
	// real browser POST carries them all; schema.Apply zeroes absent fields.
	// baselineProcessForm fills every registered key with the current config
	// value (build it from schema.FieldsFor(SectionProcess) if the package's
	// existing save tests don't already have such a helper), then override:
	form := baselineProcessForm()
	form.Set("harness", "codex")
	form.Set("harness_options.sandbox", "workspace-write")
	postForm(t, s, "/web/config/process/b", form)

	saved := loadConfigFile(t, cfgPath)
	p := saved.Processes["b"]
	if p.Harness != "codex" {
		t.Errorf("Harness = %q, want codex", p.Harness)
	}
	if got := p.HarnessOptions["sandbox"]; got != "workspace-write" {
		t.Errorf("HarnessOptions = %#v", p.HarnessOptions)
	}
}

func TestSaveRejectsBadHarnessOptionAndWritesNothing(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"b": {Workspace: "/w"}}}
	s, cfgPath := newTestServerWithConfigFile(t, cfg)
	before := readFile(t, cfgPath)

	form := baselineProcessForm() // helper from Step 1 above
	form.Set("harness", "codex")
	form.Set("harness_options.sandbox", "bogus")
	body := postForm(t, s, "/web/config/process/b", form)

	if !strings.Contains(body, "sandbox") || !strings.Contains(body, "not valid") {
		t.Errorf("flash does not carry the adapter's key-named error: %s", body)
	}
	if after := readFile(t, cfgPath); after != before {
		t.Error("config file changed despite validation failure")
	}
}

func TestSaveEmptyOptionsOmitsHarnessOptionsKey(t *testing.T) {
	cfg := &config.Config{Processes: map[string]config.ProcessConfig{"b": {Workspace: "/w",
		HarnessOptions: map[string]any{"permission_mode": "plan"}}}}
	s, cfgPath := newTestServerWithConfigFile(t, cfg)

	form := baselineProcessForm() // all harness_options.* inputs empty
	postForm(t, s, "/web/config/process/b", form)

	if raw := readFile(t, cfgPath); strings.Contains(raw, "harness_options") {
		t.Errorf("cleared options must drop the key entirely, got:\n%s", raw)
	}
}

func TestSaveOpencodePermissionYAMLRoundTrip(t *testing.T) {
	cfg := &config.Config{Sessions: map[string]config.SessionConfig{"r": {Workspace: "/w"}}}
	s, cfgPath := newTestServerWithConfigFile(t, cfg)

	form := baselineSessionForm()
	form.Set("harness", "opencode")
	form.Set("model", "anthropic/claude-sonnet-5") // opencode requires provider/model
	form.Set("harness_options.permission", "bash: allow\nwebfetch:\n  \"github.com/*\": allow")
	postForm(t, s, "/web/config/session/r", form)

	saved := loadConfigFile(t, cfgPath)
	perm, _ := saved.Sessions["r"].HarnessOptions["permission"].(map[string]any)
	if perm["bash"] != "allow" {
		t.Errorf("permission = %#v", perm)
	}
	nested, _ := perm["webfetch"].(map[string]any)
	if nested["github.com/*"] != "allow" {
		t.Errorf("nested pattern map lost: %#v", perm)
	}
}
```

(Adapt helper names to the file's existing scaffolding — the package already tests these handlers end-to-end; extend, don't reinvent. If `baselineProcessForm` doesn't exist, build it from `schema.FieldsFor(SectionProcess)` defaults.)

- [ ] **Step 2: Run — expect FAIL** (options silently dropped; no `harness_options` parsing). 

- [ ] **Step 3: Implement.** `applySection` signature gains the hook, invoked between `schema.Apply` and `put`:

```go
func (s *Server) applySection(w http.ResponseWriter, r *http.Request,
	section schema.Section,
	locate func(cfg *config.Config) (any, bool),
	put func(cfg *config.Config, v any),
	applyOptions func(cfg *config.Config, target any, form url.Values) error, // nil = section has no harness options
	okMsg string, needsRestart bool,
) {
	// ... existing body through schema.Apply ...
	if applyOptions != nil {
		if err := applyOptions(cfg, target, r.Form); err != nil {
			s.renderFlash(w, "error", err.Error())
			return
		}
	}
	put(cfg, target)
	// ... existing tail unchanged ...
}
```

Shared decode helper + one closure per harness section:

```go
// applyScopeHarnessOptions decodes the harness_options.* inputs against the
// scope's effective harness (resolved AFTER schema.Apply wrote the
// submitted harness field, so a harness change and its options land
// atomically in one save).
func applyScopeHarnessOptions(form url.Values, harnessName string) (map[string]any, error) {
	h, err := harness.Get(harnessName)
	if err != nil {
		return nil, fmt.Errorf("harness %q is not registered", harnessName)
	}
	return schema.ApplyHarnessOptions(h, form)
}
```

Handler updates (pattern shown for process; defaults/task/template/session are identical with their own cascade helper; web/client/host pass `nil`):

```go
func (s *Server) handleConfigProcessSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionProcess,
		func(cfg *config.Config) (any, bool) {
			p, ok := cfg.Processes[name]
			return &p, ok
		},
		func(cfg *config.Config, v any) { cfg.Processes[name] = *(v.(*config.ProcessConfig)) },
		func(cfg *config.Config, target any, form url.Values) error {
			p := target.(*config.ProcessConfig)
			opts, err := applyScopeHarnessOptions(form, cfg.ProcessHarness(*p))
			if err != nil {
				return err
			}
			p.HarnessOptions = opts
			return nil
		},
		fmt.Sprintf("Process %q saved", name), true)
}
```

(Defaults closure resolves via `cfg.DefaultsHarness()` — note `target` is `&cfg.Defaults` itself, already carrying the submitted harness. Session closure uses `cfg.SessionHarness(*sc)`.)

- [ ] **Step 4: Run.** `go test ./internal/web/... -count=1` — new tests PASS, all existing save tests PASS (nil hooks are behavior-identical).

- [ ] **Step 5: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web
git commit -m "feat(web): save harness_options through the Validate gate"
```

---

### Task 7: Harness badges on lists and session cards

**Files:**
- Modify: `internal/web/handlers_pages.go` (`processRow`/`templateRow`/`taskRow` gain `Harness string` + `HarnessInherited bool`; builders fill them via the cascade helpers)
- Modify: `internal/web/handlers_sessions.go` (`sessionRow` likewise)
- Modify: `internal/web/templates/pages/processes.html`, `config_templates.html`, `tasks.html`, `sessions.html`
- Test: extend the pages' existing builder/render tests

**Interfaces:**
- Consumes: `cfg.ProcessHarness/TaskHarness/TemplateHarness/SessionHarness` (Verified facts).
- Produces: display-only; no new exports.

**Steps:**

- [ ] **Step 1: Write failing tests** — extend the existing `buildProcessesData`/`buildTemplatesData`/`buildTasksData`/`buildSessionsData` tests:

```go
// e.g. in the processes builder test: a process with no harness under
// defaults.harness=codex reports Harness="codex", HarnessInherited=true;
// an explicit harness: claude process reports "claude", false.
```

Concretely, per builder: one scope with explicit `Harness: "claude"` → row `{Harness: "claude", HarnessInherited: false}`; one with empty harness and `Defaults.Harness: "codex"` → `{Harness: "codex", HarnessInherited: true}`. Assert both.

- [ ] **Step 2: Run — expect FAIL (fields don't exist).**

- [ ] **Step 3: Implement.** Row structs gain the two fields; builders fill, e.g.:

```go
rows = append(rows, processRow{
	Name: name, Workspace: proc.Workspace, Model: proc.Model, Enabled: proc.Enabled,
	Harness:          dd.Config.ProcessHarness(proc),
	HarnessInherited: proc.Harness == "",
})
```

(templates/tasks/sessions analogous; `buildTasksData` fills both Enabled and Disabled rows.) Templates — add a `harness` column right after `model` where one exists, and after `schedule` in `tasks.html`; render:

```html
<td>{{if .HarnessInherited}}<span class="dim">{{.Harness}}</span>{{else}}{{.Harness}}{{end}}</td>
```

Bump the two `colspan="5"` empty-state cells (processes, templates) to `colspan="6"`. In `sessions.html`, add to the card-head row-actions span, before the status pill:

```html
      <span class="pill">{{.Harness}}</span>
```

and change the page intro copy `Persistent Claude sessions used by runtime: persistent tasks.` → `Persistent coding-agent sessions used by runtime: persistent tasks.`

- [ ] **Step 4: Run.** `go test ./internal/web/... -count=1` — PASS (adapt any render test asserting the old `<thead>` literals).

- [ ] **Step 5: Full gate + commit**

```bash
go test -race ./... && make lint && make e2e
git add internal/web
git commit -m "feat(web): harness badges on process/template/task lists + session cards"
```

---

### Task 8: Docs + repo-wide sweep

**Files:**
- Modify: `docs/configuration/harnesses.md` (new "Web UI" section)
- Modify: any file the sweep in Step 2 flags (stale "later plan" comments)

**Steps:**

- [ ] **Step 1: Write the docs.** Append a `## Web UI` section to `docs/configuration/harnesses.md` covering, in this order: the harness dropdown on defaults/process/task/template/session forms (values from the registered adapters; empty = inherit); the harness-options sub-form (typed fields per adapter — enumerate the claude/codex/opencode fields from the Task 1 table); the opencode permission YAML textarea; the model input's per-harness suggestions/format hints and that `ValidateModel` gates on save; inherited-value placeholders (same-harness defaults cascade; sessions never inherit); and that every save round-trips `Config.Validate()` so the web UI can't write config the CLI would reject. **Docs-accuracy bar (Plan-4 Task-9 standard): every concrete claim verified against the merged source, not the plan text.**

- [ ] **Step 2: Stale-reference sweep.** Run:

```bash
grep -rn "later plan\|arrive in a later\|Plan 5" internal/ docs/configuration/ --include='*.go' --include='*.md' | grep -vi "plans/"
```

Fix anything describing harness/harness_options as web-UI-pending (the `registry.go` comment was rewritten in Task 2 — verify; check `docs/configuration/config-reference.md` for a harness_options mention that claims no web UI exists).

- [ ] **Step 3: Full local CI-parity gate** (all four, fresh):

```bash
go test -race ./... && make lint && make e2e
golangci-lint run ./...           # version 2.12.2 (brew)
gosec -exclude=G104,G204,G304,G306,G602,G702,G703,G704 ./...   # v2.25.0
```

Expected: zero findings across all five commands.

- [ ] **Step 4: Commit**

```bash
git add docs internal
git commit -m "docs: web UI section for harnesses + stale-reference sweep"
```

---

## Final wave (orchestrator, after all tasks)

1. Whole-branch review on Opus (spec compliance + code quality), fix wave if needed.
2. Optional live smoke on the **isolated test daemon** (separate `LEO_HOME`; NEVER restart production): open the web UI, flip a process to codex, watch the sub-form swap, save, verify YAML. Orchestrator-only.
3. Push `feat/harness-plan-5-web-ui`, open PR, watch CI (Lint = golangci-lint AND gosec; macos+ubuntu; known flaky tests: `TestRunInterruptStopsImmediatelyWithoutRetryOrNotify`, `TestRunSuperviseLoopRestartsAndCallsOnSessionEnd` — rerun before debugging).
4. Merge is Evan's call. After merge + install update: **flag the parked live `~/.leo/leo.yaml` migration to Evan — do not touch it.**
