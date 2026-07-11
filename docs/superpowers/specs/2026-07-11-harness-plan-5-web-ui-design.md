# Harness Plan 5 — Web UI for Harnesses (Design)

**Date:** 2026-07-11
**Status:** Approved (design), pending implementation plan
**Parent spec:** [2026-07-10-harness-abstraction-design.md](2026-07-10-harness-abstraction-design.md) — this implements its "Web UI" section, the final plan of five.

## Summary

Surface `harness` and `harness_options` as first-class web-UI form fields. Today the schema-driven form system (`internal/web/schema`) deliberately excludes both on every section; config and validation already support them fully (Plans 1–4). This plan adds a harness dropdown, a per-adapter options sub-form rendered from a new `OptionsSchema()` interface method, a harness-aware model control, and harness badges on entity lists.

## Goals

- Harness dropdown (from `harness.Names()`) on defaults/process/template/task/session forms; selecting a harness re-renders its options sub-form via htmx.
- Each adapter's `harness_options` keys render as typed form fields, validated on save by the same `DecodeOptions` gate the CLI uses.
- The model control works for non-claude harnesses (today's static claude-only `<select>` makes valid codex/opencode models unenterable).
- Cascade display: per-key inherited placeholders from `defaults.harness_options` where the cascade applies.
- Harness badges wherever a model column already exists.

## Non-Goals

- Structured row editor for opencode's `permission` map (YAML textarea instead; row editor is a possible follow-up).
- Per-input error highlighting. No field in the form system has it; adapter errors are key-named and surface in the existing flash banner.
- Live model-list fetching from harness CLIs. Suggestions are static per adapter; `ValidateModel` gates on save.
- Driver-aware session status rework (codex thread info, opencode serve health on cards). Follow-up if wanted.

## Decisions (with rationale)

1. **Additive `OptionsSchema()` on the Harness interface; `DecodeOptions` untouched.** Every web save round-trips through `Config.Validate()` → adapter `DecodeOptions`, so form/validator *acceptance* cannot disagree; the only drift risks are the schema offering a value `DecodeOptions` rejects or omitting a key — both caught by per-adapter consistency tests (schema keys == the adapters' existing `optionKeys` slices; every enum value decodes clean; bogus values fail). Rejected alternatives: rewriting `DecodeOptions` as a generic schema-driven decoder (rewrites stable golden-tested Plan 2/3 code, and codex's pointed rejection messages plus opencode's nested permission map need escape hatches anyway); reflecting descriptors off the `Options` structs (runtime-only fields like `LeoMCP`/`ServerPort`/`ServerPassword` pollute the surface; the permission map shape isn't expressible).
2. **Descriptor type lives in `internal/harness`, not `internal/web/schema`.** Dependency direction: `web/schema` → `config` → `harness`. The parent spec's `OptionsSchema() schema.Object` sketch cannot literally reference the web package.
3. **`harness` becomes a registered schema field; `harness_options` stays in `Excluded` with an updated comment.** `harness` is a plain string struct field — a normal `KindSelect` registry entry with a new `"harnesses"` options source gives rendering, `Apply`, and drift-test coverage for free. `harness_options` (`map[string]any`) cannot be a flat registry field; it is rendered by a dedicated sub-form component instead (precedent: `client.hosts` add/remove UI, also excluded-with-component).
4. **opencode `permission` renders as a YAML textarea.** It is a nested map (tool → allow/ask/deny, or tool → {pattern → verdict}); flat KEY=VALUE lines cannot round-trip pattern maps and would silently destroy data. One monospace textarea, parsed with `yaml.Unmarshal` and validated by `DecodeOptions` on save, is lossless and adds zero new UI machinery.
5. **Bool options render as tri-state selects (inherit/on/off).** A bare checkbox cannot express "unset", and unset vs explicit-false matters for the key-wise cascade merge (`scopeHarnessOptions`). Empty selection omits the key from the map.
6. **Model control becomes text input + `<datalist>`.** Per-harness suggestions (claude: the current static list; codex/opencode: free text with format placeholder, e.g. `provider/model`). A stale suggestion list after a dropdown change is harmless — `ValidateModel` gates on save — so the primary htmx swap targets only the options sub-form; the partial response refreshes the datalist via one `hx-swap-oob` element as polish.
7. **Sessions get the picker with no inheritance display**, matching `SessionHarnessOptions` (sessions never cascaded claude flat fields; Plan 2 preserved that exactly).

## Architecture

### 1. `internal/harness`: OptionField + OptionsSchema()

```go
type OptionType int

const (
    OptionString     OptionType = iota // single-line text
    OptionText                          // multi-line text (textarea)
    OptionBool                          // tri-state in forms: unset/true/false
    OptionEnum                          // fixed value list
    OptionStringList                    // []string; forms use CSV input
    OptionYAMLMap                       // nested map; forms use a YAML textarea
)

type OptionField struct {
    Key        string     // harness_options key
    Label      string
    Help       string
    Type       OptionType
    EnumValues []string   // OptionEnum only
    Source     string     // optional named web option-source hint (e.g. "agents")
}
```

`Harness` interface gains:

```go
// OptionsSchema describes this adapter's harness_options keys for form
// rendering. It must stay consistent with DecodeOptions: same key set,
// and every EnumValues entry must decode cleanly. Enforced by tests.
OptionsSchema() []OptionField
```

Adapter schemas:

| Adapter | Key | Type | Notes |
|---|---|---|---|
| claude | `permission_mode` | Enum | acceptEdits, auto, bypassPermissions, default, dontAsk, plan |
| claude | `bypass_permissions` | Bool | |
| claude | `remote_control` | Bool | |
| claude | `agent` | String | `Source: "agents"` — reuse the existing sub-agent option source |
| claude | `allowed_tools` | StringList | CSV input |
| claude | `disallowed_tools` | StringList | CSV input |
| claude | `append_system_prompt` | Text | textarea |
| codex | `sandbox` | Enum | read-only, workspace-write, danger-full-access |
| opencode | `permission` | YAMLMap | YAML textarea |

Runtime-only Options fields (`LeoMCP`, `ServerPort`, `ServerPassword`, prefixes) never appear — the schema is hand-declared per adapter, not reflected.

`Source` is a loose by-name hint into the web layer's `OptionSources`; the web layer falls back to the plain control when it doesn't recognize the name (or when the source yields nothing).

### 2. `internal/web/schema`: harness options sub-form

New component (e.g. `harnessform.go`):

- **Render:** `HarnessOptionValues(h harness.Harness, opts, inherited map[string]any) []HarnessFieldValue` — maps each `OptionField` + current value to a renderable control. Input names are `harness_options.<key>` (cannot collide with registry field names). `inherited` supplies per-key placeholder values (nil for sessions and for scopes whose harness differs from defaults — mirror `scopeHarnessOptions` semantics).
- **Apply:** `ApplyHarnessOptions(h harness.Harness, form url.Values) (map[string]any, error)` — builds the map from submitted inputs. Empty inputs omit the key. Type fidelity is load-bearing: StringList values land as `[]any` of `string` (not `[]string`) and YAMLMap values as `map[string]any`, because `DecodeOptions` type-asserts the YAML-round-trip shapes. Bool tri-state: `""` omits, `"true"`/`"false"` store real bools. YAML textarea parse errors return key-named errors before validation.
- Empty resulting map stores as nil so `harness_options: {}` never clutters saved YAML.

Registry changes:

- `harness` registered per section: `KindSelect`, `Options: "harnesses"`, Group near Model. New option source `"harnesses"` in `options.go`: `{"", "inherit (<defaults harness>)"}` + `harness.Names()` (for `SectionDefaults`, the empty label is `claude (default)` semantics — exact label wording settled in the plan).
- `harness_options` remains in `Excluded` everywhere with the comment updated to point at the sub-form component.
- Stale `Excluded` comment about forms "arriving in a later plan" rewritten.

### 3. Templates + htmx

- `config_form` template renders the harness select like any registry field, plus a `harness-options` container `<div>` after it containing the sub-form partial (new `components/harness_options.html`).
- The harness `<select>` carries `hx-get="/web/partials/harness-options" hx-target="<that container>" hx-include="this"` plus section/scope-name params. The endpoint renders the sub-form for the chosen harness: stored values when it matches the saved harness, empty otherwise. Response includes an `hx-swap-oob` datalist refresh for the model control.
- Model control: registry `model` field switches from `KindSelect` to a datalist-backed text input (new kind or template treatment — settled in the plan). Suggestions resolved from the *saved* effective harness at page render.
- Forms without a chosen scope harness render the options sub-form for the effective (inherited) harness.

### 4. Save path

`applySection` gains an options step: after `schema.Apply` writes flat fields (including `harness`), call `ApplyHarnessOptions` with the submitted harness and set the scope's `HarnessOptions`. Then the existing `validateAndSave`: `cfg.Validate()` (adapter `DecodeOptions`, model validation, kind/channel checks) → `config.Save` only when clean → flash. A rejected option never reaches disk. Sub-form parse errors (bad YAML, non-numeric etc.) surface in the same flash, key-named.

### 5. Sessions and badges

- Session form: harness dropdown + options sub-form, `inherited` always nil.
- Harness badge/column added where a model column already exists: session cards, process list, template list, task list. Shows the *effective* harness (dim/annotated when inherited) — exact treatment settled in the plan.

## Error handling

- Bad YAML in the permission textarea → key-named flash error, config untouched.
- Unknown/invalid option values → `DecodeOptions` errors via `Validate()`, flash, config untouched.
- Harness dropdown can only submit registered names or empty; a stale form submitting a since-removed name is caught by `Validate()`.
- Partial endpoint with unknown harness name → 400.

## Testing

- **Adapter consistency tests** (per adapter, in the adapter packages): `OptionsSchema()` key set equals `optionKeys`; every `EnumValues` entry decodes cleanly via `DecodeOptions`; a bogus enum value fails; each field's zero-sample decodes per its declared type.
- **Sub-form unit tests:** render (values, inherited placeholders, tri-state bools) and apply (empty→omitted, `[]any` fidelity, YAML nesting round-trip, parse errors). Round-trip property: `ApplyHarnessOptions` output always passes `DecodeOptions` for well-formed inputs.
- **Handler tests:** partial endpoint (per harness, stored-vs-empty values, unknown harness 400); save path with a bad option → flash error and config file unchanged; save with good options → persisted YAML shape matches hand-written config.
- **Drift test** stays green across the registry changes (characterization-first when touching it).
- **Gates per task:** `go test -race ./...`, `make e2e` (build-tagged suite), golangci-lint 2.12.2 + gosec v2.25.0 with CI excludes before push.

## Rollout

Single PR on a feature branch (`feat/harness-plan-5-web-ui`), executed via subagent-driven development per the established Plan 1–4 flow. After merge + install update, the parked migration of the live `~/.leo/leo.yaml` becomes actionable (flag to Evan; do not touch before).
