# Leo Web UI Redesign Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rebuild the Leo web UI in the "Ops Terminal" visual direction with schema-driven config forms that cover every config field, plus new Sessions and Service pages.

**Architecture:** A new `internal/web/schema` package holds a per-section field registry; reflection maps yaml-tagged config struct fields to form inputs, and a drift test fails CI when a config field lacks a registry entry. The single-page tab UI becomes a sidebar shell with real per-section URLs (htmx-boosted links). All config forms render from one shared template component and save through one shared apply path.

**Tech Stack:** Go 1.24 `html/template` + `net/http` ServeMux (method+path patterns), htmx (already vendored at `internal/web/static/htmx.min.js`), hand-rolled CSS with custom-property tokens, `embed.FS` assets. No new Go dependencies.

**Spec:** `docs/superpowers/specs/2026-07-08-web-ui-redesign-design.md`
**Visual reference:** `docs/superpowers/specs/assets/2026-07-08-web-ui-direction-a.html` (Direction A mockup — open in a browser while styling)

## Global Constraints

- No new third-party Go modules. No JS framework, no build step, no CDN fetches at runtime.
- Committed dark theme only. Tokens: bg `#0b0e14`, panel `#10141d`, panel2 `#151a25`, line `#232a38`, text `#d7dee8`, dim `#77839a`, accent `#ffb454`, good `#3fdc97`, bad `#ff6b6b`, link `#82aaff`.
- Amber accent is the ONLY interactive accent. Green/red are status-only, never decorative.
- Font: JetBrains Mono woff2 (Regular + Bold) embedded and served from `/static/fonts/`, with `ui-monospace, "SF Mono", Menlo, monospace` fallback. Include the OFL license file.
- The JSON `/api/*` surface must not change (channel plugins depend on it).
- Auth (token login, cookie sessions, bearer API) must not change behavior — only the login page gets restyled.
- Never touch the production daemon during development. Live testing uses an isolated `LEO_HOME` test daemon (see `~/.claude/projects/-Users-evan--leo-agents-leo/memory/reference_isolated_leo_test_daemon.md`): separate home dir + port, e.g. `LEO_HOME=/tmp/leo-webdev leo service start` after seeding a minimal `leo.yaml` there.
- Every commit: `make test` (`go test -race -cover ./...`) and `make lint` (go vet + staticcheck) must pass.
- Commit messages end with:
  `Claude-Session: https://claude.ai/code/session_01VS4H83KEDpFSwyEtKpUthV`

## File Structure (end state)

```
internal/web/
├── schema/
│   ├── schema.go          # Kind, Section, Field types; DeriveKind
│   ├── registry.go        # per-section []Field lists + Excluded map
│   ├── values.go          # Values() render + Apply() parse via reflection
│   ├── options.go         # named select-option sources
│   ├── schema_test.go
│   ├── registry_drift_test.go
│   └── values_test.go
├── web.go                 # routes (rewritten), Server (agents cache removed)
├── handlers.go            # shared helpers, status/flash/cron/reload (slimmed)
├── handlers_pages.go      # NEW: one GET handler per sidebar page
├── handlers_config.go     # NEW: generic schema-driven save/add/delete handlers
├── handlers_agents.go     # agents page + spawn/stop/rename (restyled templates)
├── handlers_sessions.go   # NEW: sessions page actions (reset, drain-status)
├── handlers_service.go    # NEW: service page (status, log tail; restart/reload stay in handlers.go)
├── static/
│   ├── style.css          # rewritten around tokens
│   ├── htmx.min.js        # unchanged
│   └── fonts/JetBrainsMono-Regular.woff2, JetBrainsMono-Bold.woff2, OFL.txt
└── templates/
    ├── layout.html        # sidebar shell; {{template "page_content" .}}
    ├── login.html         # restyled
    ├── pages/             # NEW: tasks, agents, processes, sessions,
    │                      #      config_defaults, config_templates,
    │                      #      config_providers, config_settings, service
    ├── components/        # form.html (NEW), flash.html, prompt_editor.html,
    │                      # task_history.html, task_log.html
    └── partials/          # status.html, processes.html (poll targets only)
DELETED: templates/partials/{tasks,agents,config,config_processes,config_tasks,config_settings,config_templates}.html
```

Config structs → sections: `DefaultsConfig`→defaults, `ProcessConfig`→process, `TaskConfig`→task, `TemplateConfig`→template, `SessionConfig`→session, `ProviderConfig`→provider, `HostConfig`→client_host, `WebConfig`→web, `ClientConfig`→client.

---

### Task 1: Schema package core — types, kind derivation, defaults registry, drift test

**Files:**
- Create: `internal/web/schema/schema.go`
- Create: `internal/web/schema/registry.go`
- Test: `internal/web/schema/schema_test.go`, `internal/web/schema/registry_drift_test.go`

**Interfaces:**
- Produces: `type Kind int` (KindText, KindNumber, KindBool, KindTriBool, KindSelect, KindCSV, KindEnvMap, KindCron, KindDuration, KindTextarea), `type Section string` (SectionDefaults …), `type Field struct{Key, Label, Help, Options, Group, Warning string; Kind Kind; Advanced bool}`, `func DeriveKind(t reflect.Type) Kind`, `func FieldsFor(s Section) []Field`, `var Excluded = map[Section][]string{...}`, `func StructFor(s Section) reflect.Type`.

- [ ] **Step 1: Write failing tests for DeriveKind and the defaults drift check**

```go
// internal/web/schema/schema_test.go
package schema

import (
	"reflect"
	"testing"
)

func TestDeriveKind(t *testing.T) {
	tests := []struct {
		name string
		typ  reflect.Type
		want Kind
	}{
		{"string", reflect.TypeOf(""), KindText},
		{"int", reflect.TypeOf(0), KindNumber},
		{"bool", reflect.TypeOf(false), KindBool},
		{"ptr bool", reflect.TypeOf((*bool)(nil)), KindTriBool},
		{"string slice", reflect.TypeOf([]string{}), KindCSV},
		{"string map", reflect.TypeOf(map[string]string{}), KindEnvMap},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveKind(tt.typ); got != tt.want {
				t.Errorf("DeriveKind(%v) = %v, want %v", tt.typ, got, tt.want)
			}
		})
	}
}
```

```go
// internal/web/schema/registry_drift_test.go
package schema

import (
	"reflect"
	"testing"
)

// TestRegistryCoversConfig fails whenever a yaml-tagged field on a config
// struct has neither a registry entry nor an explicit exclusion. This is the
// drift gate: adding a config field without deciding its web treatment is a
// CI failure.
func TestRegistryCoversConfig(t *testing.T) {
	for _, section := range AllSections() {
		section := section
		t.Run(string(section), func(t *testing.T) {
			st := StructFor(section)
			fields := map[string]bool{}
			for _, f := range FieldsFor(section) {
				fields[f.Key] = true
			}
			excluded := map[string]bool{}
			for _, k := range Excluded[section] {
				excluded[k] = true
			}
			for i := 0; i < st.NumField(); i++ {
				tag := yamlKey(st.Field(i))
				if tag == "" || tag == "-" {
					continue
				}
				if fields[tag] && excluded[tag] {
					t.Errorf("field %q is both registered and excluded", tag)
				}
				if !fields[tag] && !excluded[tag] {
					t.Errorf("config field %q (%s.%s) has no registry entry and no exclusion — add one to internal/web/schema/registry.go", tag, st.Name(), st.Field(i).Name)
				}
			}
			// Reverse direction: every registry key must exist on the struct.
			for _, f := range FieldsFor(section) {
				if _, ok := fieldByYAMLKey(st, f.Key); !ok {
					t.Errorf("registry key %q has no matching struct field on %s", f.Key, st.Name())
				}
			}
		})
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test -race ./internal/web/schema/`
Expected: FAIL — package doesn't compile (`DeriveKind` undefined).

- [ ] **Step 3: Implement schema.go**

```go
// Package schema defines the single source of truth mapping Leo's config
// fields to web-UI form controls. Forms render and parse exclusively from
// this registry; registry_drift_test.go fails when a config field is
// neither registered nor explicitly excluded.
package schema

import (
	"reflect"
	"strings"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Kind is the form-control type used to render and parse a field.
type Kind int

const (
	KindAuto     Kind = iota // registry default: derive from the Go type
	KindText                 // <input type="text">
	KindNumber               // <input type="number">
	KindBool                 // toggle; hidden-false + checkbox-true pair
	KindTriBool              // *bool: select inherit / on / off
	KindSelect               // select with a named options source
	KindCSV                  // []string as comma-separated input
	KindEnvMap               // map[string]string as KEY=VALUE textarea lines
	KindCron                 // string with live cron preview
	KindDuration             // string like "30m", "2h"
	KindTextarea             // long string
)

// Section identifies which config struct a field list applies to.
type Section string

const (
	SectionDefaults   Section = "defaults"
	SectionProcess    Section = "process"
	SectionTask       Section = "task"
	SectionTemplate   Section = "template"
	SectionSession    Section = "session"
	SectionProvider   Section = "provider"
	SectionClientHost Section = "client_host"
	SectionWeb        Section = "web"
	SectionClient     Section = "client"
)

// AllSections returns every section in stable order.
func AllSections() []Section {
	return []Section{
		SectionDefaults, SectionProcess, SectionTask, SectionTemplate,
		SectionSession, SectionProvider, SectionClientHost, SectionWeb,
		SectionClient,
	}
}

// StructFor returns the config struct type a section's fields live on.
func StructFor(s Section) reflect.Type {
	switch s {
	case SectionDefaults:
		return reflect.TypeOf(config.DefaultsConfig{})
	case SectionProcess:
		return reflect.TypeOf(config.ProcessConfig{})
	case SectionTask:
		return reflect.TypeOf(config.TaskConfig{})
	case SectionTemplate:
		return reflect.TypeOf(config.TemplateConfig{})
	case SectionSession:
		return reflect.TypeOf(config.SessionConfig{})
	case SectionProvider:
		return reflect.TypeOf(config.ProviderConfig{})
	case SectionClientHost:
		return reflect.TypeOf(config.HostConfig{})
	case SectionWeb:
		return reflect.TypeOf(config.WebConfig{})
	case SectionClient:
		return reflect.TypeOf(config.ClientConfig{})
	}
	panic("schema: unknown section " + string(s))
}

// Field describes one config field's web-form treatment.
type Field struct {
	Key      string // yaml key; doubles as the form input name
	Label    string
	Help     string
	Kind     Kind   // KindAuto derives from the struct field's Go type
	Options  string // named options source for KindSelect (see options.go)
	Group    string // form section heading, e.g. "Schedule", "Runtime"
	Advanced bool   // rendered inside the collapsed advanced <details>
	Warning  string // rendered as an inline warning (e.g. lockout risk)
}

// DeriveKind maps a Go type to its default form control.
func DeriveKind(t reflect.Type) Kind {
	switch {
	case t.Kind() == reflect.String:
		return KindText
	case t.Kind() == reflect.Int:
		return KindNumber
	case t.Kind() == reflect.Bool:
		return KindBool
	case t.Kind() == reflect.Ptr && t.Elem().Kind() == reflect.Bool:
		return KindTriBool
	case t.Kind() == reflect.Slice && t.Elem().Kind() == reflect.String:
		return KindCSV
	case t.Kind() == reflect.Map && t.Elem().Kind() == reflect.String:
		return KindEnvMap
	}
	panic("schema: no derivable kind for type " + t.String())
}

// EffectiveKind resolves a field's KindAuto against its struct type.
func EffectiveKind(section Section, f Field) Kind {
	if f.Kind != KindAuto {
		return f.Kind
	}
	sf, ok := fieldByYAMLKey(StructFor(section), f.Key)
	if !ok {
		panic("schema: field " + f.Key + " not on struct for section " + string(section))
	}
	return DeriveKind(sf.Type)
}

// yamlKey extracts the yaml tag name for a struct field ("" if untagged).
func yamlKey(sf reflect.StructField) string {
	tag := sf.Tag.Get("yaml")
	if tag == "" {
		return ""
	}
	return strings.Split(tag, ",")[0]
}

// fieldByYAMLKey finds the struct field carrying the given yaml key.
func fieldByYAMLKey(st reflect.Type, key string) (reflect.StructField, bool) {
	for i := 0; i < st.NumField(); i++ {
		if yamlKey(st.Field(i)) == key {
			return st.Field(i), true
		}
	}
	return reflect.StructField{}, false
}
```

- [ ] **Step 4: Implement registry.go with the defaults section only (other sections empty for now)**

```go
package schema

// FieldsFor returns the ordered field list for a section's form.
func FieldsFor(s Section) []Field {
	return registry[s]
}

// Excluded names yaml keys deliberately absent from the web UI, per section.
// Every entry is a reviewed decision — the drift test enforces that a config
// field is either registered here or excluded here.
var Excluded = map[Section][]string{
	// hosts is rendered as its own add/remove entries UI, not a flat field.
	SectionClient: {"hosts"},
}

var registry = map[Section][]Field{
	SectionDefaults: {
		{Key: "model", Label: "Model", Kind: KindSelect, Options: "models", Group: "Model"},
		{Key: "provider", Label: "Provider", Kind: KindSelect, Options: "providers", Group: "Model",
			Help: "Third-party Anthropic-compatible endpoint; empty = Anthropic"},
		{Key: "max_turns", Label: "Max turns", Group: "Limits"},
		{Key: "permission_mode", Label: "Permission mode", Kind: KindSelect, Options: "permission_modes", Group: "Permissions"},
		{Key: "bypass_permissions", Label: "Bypass permissions", Group: "Permissions",
			Help: "Legacy switch; ignored when a permission mode is set"},
		{Key: "allowed_tools", Label: "Allowed tools", Group: "Permissions"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Group: "Permissions"},
		{Key: "remote_control", Label: "Remote control", Group: "Behavior", Advanced: true},
		{Key: "append_system_prompt", Label: "Append system prompt", Kind: KindTextarea, Group: "Behavior", Advanced: true},
		{Key: "stale_resume_hours", Label: "Stale resume (hours)", Group: "Behavior", Advanced: true,
			Help: "Skip --resume when the stored session is older than this"},
		{Key: "idle_suspend_after", Label: "Idle suspend after", Kind: KindDuration, Group: "Behavior", Advanced: true,
			Help: "Auto-suspend idle ephemeral agents, e.g. \"2h\"; empty disables"},
	},
}
```

NOTE: field keys above are taken from `internal/config/config.go:185-201` (DefaultsConfig). If compilation or the drift test reports a mismatch, trust the struct.

- [ ] **Step 5: Run tests — DeriveKind and the defaults drift subtest must pass; other sections' subtests will fail (empty registry)**

Run: `go test -race ./internal/web/schema/ -run 'TestDeriveKind|TestRegistryCoversConfig/defaults' -v`
Expected: PASS for both. (`TestRegistryCoversConfig` for other sections still fails — that is Task 2.)

- [ ] **Step 6: Commit**

```bash
git add internal/web/schema/
git commit -m "feat(web): schema package core — field kinds, defaults registry, drift test"
```

---

### Task 2: Complete the registry for all sections

**Files:**
- Modify: `internal/web/schema/registry.go`

**Interfaces:**
- Consumes: `Field`, `Section`, `Excluded` from Task 1.
- Produces: `FieldsFor` returns non-empty lists for all nine sections; the full drift test passes.

- [ ] **Step 1: Run the full drift test to enumerate every missing field**

Run: `go test -race ./internal/web/schema/ -run TestRegistryCoversConfig -v 2>&1 | head -80`
Expected: FAIL listing each unregistered yaml key per section. Use this output as the authoritative worklist.

- [ ] **Step 2: Register every field**

Shared entries repeat across process/task/template/session — define them as helper funcs to keep DRY:

```go
func fModel(group string) Field {
	return Field{Key: "model", Label: "Model", Kind: KindSelect, Options: "models", Group: group}
}
func fProvider(group string) Field {
	return Field{Key: "provider", Label: "Provider", Kind: KindSelect, Options: "providers", Group: group,
		Help: "Third-party Anthropic-compatible endpoint; empty = inherit"}
}
func fPermissions() []Field {
	return []Field{
		{Key: "permission_mode", Label: "Permission mode", Kind: KindSelect, Options: "permission_modes", Group: "Permissions"},
		{Key: "allowed_tools", Label: "Allowed tools", Group: "Permissions", Help: "Comma-separated tool names"},
		{Key: "disallowed_tools", Label: "Disallowed tools", Group: "Permissions", Help: "Comma-separated tool names"},
	}
}
```

Then build the remaining sections. Required coverage (yaml keys, from `internal/config/`):

- **process** (`ProcessConfig`): enabled, workspace, channels, dev_channels, model, provider, max_turns, bypass_permissions (TriBool — derived), remote_control (TriBool — derived), mcp_config, add_dirs, env, agent (KindSelect Options:"agents"), allowed_tools, disallowed_tools, append_system_prompt (KindTextarea), permission_mode, stale_resume_hours (it is `*int` on ProcessConfig — register it with explicit `Kind: KindNumber`; Task 3's Apply/Values treat a `*int` with an empty form value as nil, so KindAuto's DeriveKind never sees the pointer type).
  Groups: "General" (enabled, workspace, agent), "Model" (model, provider, max_turns), "Channels" (channels, dev_channels), "Permissions", "Advanced" (Advanced:true — mcp_config, add_dirs, env, append_system_prompt, remote_control, bypass_permissions, stale_resume_hours).
- **task** (`TaskConfig`): workspace, schedule (KindCron, Group "Schedule"), timezone, prompt_file, model, provider, max_turns, enabled, silent, timeout (KindDuration), retries, channels, dev_channels, notify_on_fail, permission_mode, allowed_tools, disallowed_tools, append_system_prompt (KindTextarea), runtime (KindSelect Options:"runtimes", Help: "persistent injects into a supervised session instead of spawning claude -p"), session (KindSelect Options:"sessions", Help: "named session from the sessions: block; empty derives one per task"), lazy (Help: "start the session on first firing instead of at boot"), queue_max (Help: "max queued firings; 0 = default (5)").
  Groups: "Schedule" (schedule, timezone, enabled), "Prompt" (prompt_file), "Model" (model, provider, max_turns), "Execution" (timeout, retries, silent, runtime, session, lazy, queue_max), "Notifications" (channels, dev_channels, notify_on_fail), "Permissions", advanced: append_system_prompt, workspace.
- **template** (`TemplateConfig`): workspace, channels, dev_channels, model, provider, max_turns, remote_control, mcp_config, add_dirs, env, agent (Options:"agents"), allowed_tools, disallowed_tools, append_system_prompt (KindTextarea), permission_mode, idle_suspend_after (KindDuration).
- **session** (`SessionConfig`): workspace, model, provider, agent (Options:"agents"), permission_mode, allowed_tools, disallowed_tools, append_system_prompt (KindTextarea), add_dirs, channels, env, idle_timeout (KindDuration).
- **provider** (`ProviderConfig`): base_url (Help: "Anthropic-Messages-compatible endpoint"), api_key_env (Help: "environment variable holding the API key"), api_key_cmd (Help: "command that prints the API key"), default_model.
- **client_host** (`HostConfig`): ssh (Help: "user@host"), ssh_args, leo_path, tmux_path.
- **web** (`WebConfig`): enabled, port, bind (Warning: "Changing port or bind can lock you out of this UI; requires service restart", Help: "empty = 127.0.0.1 (loopback only)"), allowed_hosts (Warning: "Removing your own address here will block your browser").
- **client**: default_host (Help: "default remote host for leo agent commands").

Check the drift-test output — the struct is authoritative; register anything it lists that this plan text missed, and remove anything it flags as nonexistent.

- [ ] **Step 3: Run the full schema test suite**

Run: `go test -race ./internal/web/schema/ -v`
Expected: PASS, all sections.

- [ ] **Step 4: Commit**

```bash
git add internal/web/schema/registry.go
git commit -m "feat(web): complete schema registry — full config coverage across all sections"
```

---

### Task 3: Values (render) and Apply (parse) with round-trip tests

**Files:**
- Create: `internal/web/schema/values.go`
- Test: `internal/web/schema/values_test.go`

**Interfaces:**
- Consumes: `Field`, `Section`, `FieldsFor`, `EffectiveKind`, `fieldByYAMLKey` from Tasks 1–2.
- Produces:
  - `type FieldValue struct{ Field; Kind Kind; Value string; Checked bool; Inherited string }`
  - `func Values(target any, section Section, defaults any) []FieldValue` — target is a pointer to the section's struct; defaults may be nil or `*config.DefaultsConfig` for cascade placeholders.
  - `func Apply(target any, section Section, form url.Values) error` — mutates target in place; returns the first per-field parse error (e.g. non-numeric max_turns) so handlers can flash it.

**Semantics (this is the blank-field-erase fix — implement exactly):**
- Every registered field always renders, so on Apply every registered field is always written. Empty text/CSV/textarea/duration/cron → zero value (clear). Empty number → 0 (or nil for `*int`).
- KindBool inputs render as a hidden `false` input followed by a checkbox `true` input with the same name; parse with `vals := form[key]; checked := len(vals) > 0 && vals[len(vals)-1] == "true"`.
- KindTriBool renders a select with values `"" / "true" / "false"` → nil / &true / &false.
- KindEnvMap: one `KEY=VALUE` per textarea line; blank lines ignored; a line without `=` is a parse error naming the line.
- KindCSV: split on comma, trim spaces, drop empties; empty input → nil slice (not an empty non-nil slice, so `omitempty` keeps yaml clean).
- Values fills `Inherited` for string/select fields that are empty on target but set on defaults (same yaml key looked up on the defaults struct) — templates show it as `placeholder="inherit: sonnet"`.

- [ ] **Step 1: Write failing round-trip tests**

```go
// internal/web/schema/values_test.go
package schema

import (
	"net/url"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// renderToForm converts Values output back into url.Values the way a browser
// submitting the rendered form would.
func renderToForm(fvs []FieldValue) url.Values {
	form := url.Values{}
	for _, fv := range fvs {
		switch fv.Kind {
		case KindBool:
			form.Add(fv.Key, "false")
			if fv.Checked {
				form.Add(fv.Key, "true")
			}
		default:
			form.Set(fv.Key, fv.Value)
		}
	}
	return form
}

func TestRoundTripTask(t *testing.T) {
	lazy := config.TaskConfig{
		Schedule:   "30 8,15 * * 1-5",
		PromptFile: "prompts/trade.md",
		Model:      "sonnet",
		Provider:   "zai",
		Enabled:    true,
		Silent:     true,
		Timeout:    "45m",
		Retries:    2,
		Channels:   []string{"plugin:telegram@claude-plugins-official"},
		Runtime:    "persistent",
		Session:    "daily",
		Lazy:       true,
		QueueMax:   9,
	}
	form := renderToForm(Values(&lazy, SectionTask, nil))
	var got config.TaskConfig
	if err := Apply(&got, SectionTask, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if !reflect.DeepEqual(lazy, got) {
		t.Errorf("round trip mismatch:\n want %+v\n got  %+v", lazy, got)
	}
}

func TestRoundTripProcessTriBool(t *testing.T) {
	bTrue := true
	proc := config.ProcessConfig{
		Enabled:           true,
		Model:             "opus",
		BypassPermissions: &bTrue,
		Env:               map[string]string{"FOO": "bar", "BAZ": "qux"},
	}
	form := renderToForm(Values(&proc, SectionProcess, nil))
	var got config.ProcessConfig
	if err := Apply(&got, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.BypassPermissions == nil || !*got.BypassPermissions {
		t.Errorf("BypassPermissions = %v, want &true", got.BypassPermissions)
	}
	if !reflect.DeepEqual(proc.Env, got.Env) {
		t.Errorf("Env = %v, want %v", got.Env, proc.Env)
	}
}

func TestTriBoolNilSurvives(t *testing.T) {
	var proc config.ProcessConfig
	form := renderToForm(Values(&proc, SectionProcess, nil))
	var got config.ProcessConfig
	if err := Apply(&got, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got.BypassPermissions != nil || got.RemoteControl != nil {
		t.Errorf("nil tri-bools did not survive round trip: bypass=%v remote=%v",
			got.BypassPermissions, got.RemoteControl)
	}
}

func TestApplyClearsEmptied(t *testing.T) {
	proc := config.ProcessConfig{MCPConfig: "mcp.json", Model: "opus"}
	form := renderToForm(Values(&proc, SectionProcess, nil))
	form.Set("mcp_config", "") // user cleared the field
	if err := Apply(&proc, SectionProcess, form); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if proc.MCPConfig != "" {
		t.Errorf("cleared field survived: %q", proc.MCPConfig)
	}
	if proc.Model != "opus" {
		t.Errorf("untouched field lost: %q", proc.Model)
	}
}

func TestApplyRejectsBadNumber(t *testing.T) {
	var task config.TaskConfig
	form := renderToForm(Values(&task, SectionTask, nil))
	form.Set("max_turns", "lots")
	if err := Apply(&task, SectionTask, form); err == nil {
		t.Fatal("want error for non-numeric max_turns, got nil")
	}
}

func TestApplyRejectsBadEnvLine(t *testing.T) {
	var proc config.ProcessConfig
	form := renderToForm(Values(&proc, SectionProcess, nil))
	form.Set("env", "FOO=bar\nnot-an-assignment\n")
	if err := Apply(&proc, SectionProcess, form); err == nil {
		t.Fatal("want error for env line without '=', got nil")
	}
}

func TestInheritedPlaceholder(t *testing.T) {
	defaults := config.DefaultsConfig{Model: "sonnet"}
	var task config.TaskConfig
	for _, fv := range Values(&task, SectionTask, &defaults) {
		if fv.Key == "model" {
			if fv.Inherited != "sonnet" {
				t.Errorf("Inherited = %q, want %q", fv.Inherited, "sonnet")
			}
			return
		}
	}
	t.Fatal("model field not rendered")
}
```

- [ ] **Step 2: Run tests to verify failure**

Run: `go test -race ./internal/web/schema/ -run 'TestRoundTrip|TestTriBool|TestApply|TestInherited' -v`
Expected: FAIL — `Values`/`Apply` undefined.

- [ ] **Step 3: Implement values.go**

```go
package schema

import (
	"fmt"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// FieldValue is a Field resolved against a concrete struct value, ready for
// template rendering.
type FieldValue struct {
	Field
	Kind      Kind   // effective (never KindAuto)
	Value     string // rendered current value ("" for unset)
	Checked   bool   // KindBool only
	Inherited string // effective default when Value is empty (placeholder)
}

// Values renders every registered field of section against target (a pointer
// to the section's struct). defaults, when non-nil, is a pointer to
// config.DefaultsConfig used to compute Inherited placeholders.
func Values(target any, section Section, defaults any) []FieldValue {
	v := reflect.ValueOf(target).Elem()
	var out []FieldValue
	for _, f := range FieldsFor(section) {
		sf, _ := fieldByYAMLKey(v.Type(), f.Key)
		fv := FieldValue{Field: f, Kind: effectiveKindFor(section, f, sf.Type)}
		val := v.FieldByIndex(sf.Index)
		switch fv.Kind {
		case KindBool:
			fv.Checked = val.Bool()
		case KindTriBool:
			if !val.IsNil() {
				fv.Value = strconv.FormatBool(val.Elem().Bool())
			}
		case KindNumber:
			fv.Value = renderNumber(val)
		case KindCSV:
			fv.Value = strings.Join(val.Interface().([]string), ", ")
		case KindEnvMap:
			fv.Value = renderEnvMap(val.Interface().(map[string]string))
		default: // text-ish kinds
			fv.Value = val.String()
		}
		if defaults != nil && fv.Value == "" && isTextKind(fv.Kind) {
			fv.Inherited = inheritedFrom(defaults, f.Key)
		}
		out = append(out, fv)
	}
	return out
}

// Apply parses form into target, writing every registered field of section.
// All registered fields always render, so absence in a submitted form only
// happens for KindBool (unchecked checkbox), which the hidden-false input
// covers. Returns the first parse error encountered.
func Apply(target any, section Section, form url.Values) error {
	v := reflect.ValueOf(target).Elem()
	for _, f := range FieldsFor(section) {
		sf, _ := fieldByYAMLKey(v.Type(), f.Key)
		val := v.FieldByIndex(sf.Index)
		raw := form.Get(f.Key)
		switch effectiveKindFor(section, f, sf.Type) {
		case KindBool:
			vals := form[f.Key]
			val.SetBool(len(vals) > 0 && vals[len(vals)-1] == "true")
		case KindTriBool:
			switch raw {
			case "true", "false":
				b := raw == "true"
				val.Set(reflect.ValueOf(&b))
			default:
				val.Set(reflect.Zero(sf.Type))
			}
		case KindNumber:
			if err := applyNumber(val, sf, f.Key, raw); err != nil {
				return err
			}
		case KindCSV:
			val.Set(reflect.ValueOf(parseCSV(raw)))
		case KindEnvMap:
			m, err := parseEnvLines(raw)
			if err != nil {
				return fmt.Errorf("%s: %w", f.Key, err)
			}
			val.Set(reflect.ValueOf(m))
		default:
			val.SetString(strings.TrimSpace(raw))
		}
	}
	return nil
}

func effectiveKindFor(section Section, f Field, t reflect.Type) Kind {
	if f.Kind != KindAuto {
		return f.Kind
	}
	return DeriveKind(t)
}

func isTextKind(k Kind) bool {
	switch k {
	case KindText, KindSelect, KindCron, KindDuration, KindTextarea:
		return true
	}
	return false
}

func renderNumber(val reflect.Value) string {
	if val.Kind() == reflect.Ptr {
		if val.IsNil() {
			return ""
		}
		val = val.Elem()
	}
	if val.Int() == 0 {
		return ""
	}
	return strconv.FormatInt(val.Int(), 10)
}

func applyNumber(val reflect.Value, sf reflect.StructField, key, raw string) error {
	raw = strings.TrimSpace(raw)
	isPtr := sf.Type.Kind() == reflect.Ptr
	if raw == "" {
		if isPtr {
			val.Set(reflect.Zero(sf.Type))
		} else {
			val.SetInt(0)
		}
		return nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return fmt.Errorf("%s: %q is not a number", key, raw)
	}
	if isPtr {
		val.Set(reflect.ValueOf(&n))
	} else {
		val.SetInt(int64(n))
	}
	return nil
}

func parseCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseEnvLines(raw string) (map[string]string, error) {
	var m map[string]string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return nil, fmt.Errorf("line %q is not KEY=VALUE", line)
		}
		if m == nil {
			m = map[string]string{}
		}
		m[strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	return m, nil
}

func renderEnvMap(m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		fmt.Fprintf(&b, "%s=%s\n", k, m[k])
	}
	return b.String()
}

// inheritedFrom reads the same yaml key off the defaults struct, returning
// its string form when set.
func inheritedFrom(defaults any, key string) string {
	dv := reflect.ValueOf(defaults).Elem()
	sf, ok := fieldByYAMLKey(dv.Type(), key)
	if !ok || sf.Type.Kind() != reflect.String {
		return ""
	}
	return dv.FieldByIndex(sf.Index).String()
}
```

- [ ] **Step 4: Run the full schema package tests**

Run: `go test -race ./internal/web/schema/ -v`
Expected: PASS. If `TestRoundTripTask` fails on a specific field, check that field's registry Kind against its struct type before touching values.go.

- [ ] **Step 5: Commit**

```bash
git add internal/web/schema/values.go internal/web/schema/values_test.go
git commit -m "feat(web): schema Values/Apply — reflection form render+parse with clear semantics"
```

---

### Task 4: Options sources + on-demand agent list

**Files:**
- Create: `internal/web/schema/options.go`
- Test: `internal/web/schema/options_test.go`
- Modify: `internal/web/web.go` (remove the boot-time `agents` cache field and `s.agents = s.fetchAgentList()` at web.go:71,123; keep `fetchAgentList` but move it behind the provider below)

**Interfaces:**
- Consumes: registry `Options` names used in Tasks 1–2: `models`, `providers`, `permission_modes`, `agents`, `runtimes`, `sessions`, `templates`.
- Produces:
  - `type Option struct{ Value, Label string }`
  - `type OptionSources struct{ Cfg *config.Config; Agents func() []string }`
  - `func (o OptionSources) For(name string) []Option`

- [ ] **Step 1: Write failing tests**

```go
// internal/web/schema/options_test.go
package schema

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestOptionSources(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.ProviderConfig{"zai": {BaseURL: "https://api.z.ai/v1"}},
		Sessions:  map[string]config.SessionConfig{"daily": {}},
		Templates: map[string]config.TemplateConfig{"dev": {}},
	}
	src := OptionSources{Cfg: cfg, Agents: func() []string { return []string{"rocket"} }}

	if opts := src.For("models"); len(opts) < 3 {
		t.Errorf("models: want sonnet/opus/haiku at least, got %v", opts)
	}
	provs := src.For("providers")
	if len(provs) != 2 || provs[0].Value != "" || provs[1].Value != "zai" {
		t.Errorf("providers: want [inherit, zai], got %v", provs)
	}
	if opts := src.For("sessions"); len(opts) != 2 || opts[1].Value != "daily" {
		t.Errorf("sessions: got %v", opts)
	}
	if opts := src.For("agents"); len(opts) != 2 || opts[1].Value != "rocket" {
		t.Errorf("agents: got %v", opts)
	}
	if opts := src.For("runtimes"); len(opts) != 2 {
		t.Errorf("runtimes: want oneshot+persistent, got %v", opts)
	}
}
```

- [ ] **Step 2: Run to verify failure, then implement options.go**

Run: `go test -race ./internal/web/schema/ -run TestOptionSources -v` → FAIL.

```go
package schema

import (
	"sort"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Option is one <option> in a KindSelect control.
type Option struct{ Value, Label string }

// OptionSources resolves named option lists against a loaded config. Agents
// is injected because listing claude sub-agents shells out (see web.Server).
type OptionSources struct {
	Cfg    *config.Config
	Agents func() []string
}

// For returns the options for a registry Options name. Unknown names panic:
// a typo in the registry should fail loudly in tests, not render empty.
func (o OptionSources) For(name string) []Option {
	switch name {
	case "models":
		return []Option{{"", "inherit"}, {"sonnet", "sonnet"}, {"opus", "opus"}, {"haiku", "haiku"}}
	case "permission_modes":
		return []Option{{"", "inherit"}, {"default", "default"}, {"acceptEdits", "acceptEdits"},
			{"auto", "auto"}, {"bypassPermissions", "bypassPermissions"},
			{"dontAsk", "dontAsk"}, {"plan", "plan"}}
	case "runtimes":
		return []Option{{"oneshot", "oneshot"}, {"persistent", "persistent"}}
	case "providers":
		return namedKeys(keysOf(o.Cfg.Providers), "inherit (Anthropic)")
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
	panic("schema: unknown options source " + name)
}

func keysOf[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func namedKeys(keys []string, emptyLabel string) []Option {
	opts := []Option{{"", emptyLabel}}
	for _, k := range keys {
		opts = append(opts, Option{k, k})
	}
	return opts
}
```

Note: model names beyond sonnet/opus/haiku are validated by `Config.Validate()`; check `internal/config/config.go` `Validate()` for the authoritative list and mirror it (plus the empty inherit option) rather than inventing one.

- [ ] **Step 3: Replace the boot-time agent cache in web.go**

In `internal/web/web.go`: delete the `agents []string` field (line 71) and `s.agents = s.fetchAgentList()` (line 123). Add instead a lazily-refreshed cache with a 60s TTL so the dropdown stays fresh without shelling out on every render:

```go
// agentList returns the claude sub-agent names, refreshing at most once per
// minute. fetchAgentList shells out to `claude agents` (~100ms).
func (s *Server) agentList() []string {
	s.agentMu.Lock()
	defer s.agentMu.Unlock()
	if time.Since(s.agentsFetched) > time.Minute {
		s.agentCache = s.fetchAgentList()
		s.agentsFetched = time.Now()
	}
	return s.agentCache
}
```

Add fields to Server: `agentMu sync.Mutex; agentCache []string; agentsFetched time.Time`. Fix any compile errors from removed `s.agents` references (handlers_agents.go and handlers.go use it for dropdowns — switch them to `s.agentList()`).

- [ ] **Step 4: Full test + lint, commit**

Run: `go test -race ./internal/web/... && make lint`
Expected: PASS.

```bash
git add internal/web/schema/options.go internal/web/schema/options_test.go internal/web/web.go internal/web/handlers.go internal/web/handlers_agents.go
git commit -m "feat(web): schema option sources; agent dropdown refreshes on demand"
```

---

### Task 5: Shell — fonts, token CSS, sidebar layout, real routes

This is the visual cutover. After this task every page renders inside the new shell at its own URL, with existing content (tables/forms) unstyled-but-functional; Tasks 6–14 then rebuild each page.

**Files:**
- Create: `internal/web/static/fonts/JetBrainsMono-Regular.woff2`, `JetBrainsMono-Bold.woff2`, `OFL.txt`
- Rewrite: `internal/web/static/style.css`
- Rewrite: `internal/web/templates/layout.html`
- Create: `internal/web/handlers_pages.go`
- Create: `internal/web/templates/pages/tasks.html`, `agents.html`, `processes.html`, `sessions.html`, `config_defaults.html`, `config_templates.html`, `config_providers.html`, `config_settings.html`, `service.html` (each initially wraps the equivalent existing partial's content; placeholder `<p class="dim">coming in a later task</p>` ONLY for sessions.html and service.html which have no equivalent yet)
- Modify: `internal/web/web.go` (routes), `internal/web/handlers.go` (`handleDashboard` → redirect)
- Modify: `internal/web/templates/login.html` (restyle with new tokens)
- Test: `internal/web/web_test.go`, `internal/web/middleware_test.go`, `internal/web/auth_test.go` (update route expectations)

**Interfaces:**
- Produces: `func (s *Server) handlePage(page, title string, build func(*http.Request) (any, error)) http.HandlerFunc`.
- Template dispatch convention: Go templates cannot dispatch `{{template}}` by a dynamic name, and ParseFS registers templates by base name (two `tasks.html` files in different dirs would collide). So: every file under `templates/pages/` wraps its content in `{{define "page_<name>"}}...{{end}}` (e.g. `page_tasks`, `page_config_defaults`), and layout.html dispatches with one explicit `{{if eq .Page ...}}` chain over those defined names (shown in Step 4). Adding a page means adding one file + one line in the chain.

- [ ] **Step 1: Fetch fonts**

```bash
cd /tmp && curl -LO https://github.com/JetBrains/JetBrainsMono/releases/download/v2.304/JetBrainsMono-2.304.zip && unzip -o JetBrainsMono-2.304.zip -d jbmono
mkdir -p <repo>/internal/web/static/fonts
cp jbmono/fonts/webfonts/JetBrainsMono-Regular.woff2 jbmono/fonts/webfonts/JetBrainsMono-Bold.woff2 <repo>/internal/web/static/fonts/
cp jbmono/OFL.txt <repo>/internal/web/static/fonts/OFL.txt
```

Verify both woff2 files are 60–120KB each (`ls -la`). If the URL 404s, find the latest release tag at https://github.com/JetBrains/JetBrainsMono/releases and adjust.

- [ ] **Step 2: Write the failing route test**

Add to `internal/web/web_test.go` (follow the existing test setup helpers in that file for constructing a Server with a temp config):

```go
func TestPageRoutes(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // reuse/adapt the file's existing helper for an authenticated client
	pages := []string{"/tasks", "/agents", "/processes", "/sessions",
		"/config/defaults", "/config/templates", "/config/providers",
		"/config/settings", "/service"}
	for _, p := range pages {
		resp := getWithCookie(t, srv, p, cookie)
		if resp.StatusCode != 200 {
			t.Errorf("GET %s = %d, want 200", p, resp.StatusCode)
		}
	}
	resp := getWithCookie(t, srv, "/", cookie)
	if resp.StatusCode != 303 && resp.StatusCode != 302 {
		t.Errorf("GET / = %d, want redirect to /tasks", resp.StatusCode)
	}
}
```

(If `web_test.go` has no such helpers, write them once in this step: spin up the Server via `New(...)` against a temp `leo.yaml`, POST /login with the test token, capture the cookie.)

Run: `go test -race ./internal/web/ -run TestPageRoutes -v` → FAIL (404s).

- [ ] **Step 3: Rewrite style.css around tokens**

Full token block and core components (extend as pages need, but all colors/spacing MUST come from these tokens — hardcoded hex outside `:root` is a review reject):

```css
:root {
  /* color */
  --bg: #0b0e14; --panel: #10141d; --panel-2: #151a25;
  --line: #232a38; --text: #d7dee8; --dim: #77839a;
  --accent: #ffb454; --good: #3fdc97; --bad: #ff6b6b; --link: #82aaff;
  --good-bg: rgb(63 220 151 / 0.12); --bad-bg: rgb(255 107 107 / 0.12);
  --dim-bg: rgb(119 131 154 / 0.14); --accent-bg: rgb(255 180 84 / 0.12);
  /* type */
  --font-mono: "JetBrains Mono", ui-monospace, "SF Mono", Menlo, monospace;
  --fs-xs: 10.5px; --fs-sm: 12px; --fs-base: 13px; --fs-lg: 15px;
  --ls-label: 0.16em;
  /* space & shape */
  --sp-1: 4px; --sp-2: 8px; --sp-3: 12px; --sp-4: 16px; --sp-5: 24px;
  --radius: 4px; --sidebar-w: 200px;
}
@font-face {
  font-family: "JetBrains Mono"; font-style: normal; font-weight: 400;
  font-display: swap; src: url("/static/fonts/JetBrainsMono-Regular.woff2") format("woff2");
}
@font-face {
  font-family: "JetBrains Mono"; font-style: normal; font-weight: 700;
  font-display: swap; src: url("/static/fonts/JetBrainsMono-Bold.woff2") format("woff2");
}
* { box-sizing: border-box; }
body {
  margin: 0; background: var(--bg); color: var(--text);
  font-family: var(--font-mono); font-size: var(--fs-base); line-height: 1.5;
}
a { color: var(--link); text-decoration: none; }
a:focus-visible, button:focus-visible, input:focus-visible, select:focus-visible,
textarea:focus-visible { outline: 2px solid var(--accent); outline-offset: 1px; }

/* shell */
.shell { display: grid; grid-template-columns: var(--sidebar-w) 1fr; min-height: 100vh; }
.sidebar {
  background: var(--panel); border-right: 1px solid var(--line);
  padding: var(--sp-4) 0; position: sticky; top: 0; height: 100vh; overflow-y: auto;
}
.sidebar .brand { padding: 0 var(--sp-4) var(--sp-4); font-weight: 700; font-size: var(--fs-lg); }
.sidebar .brand em { color: var(--accent); font-style: normal; }
.nav-sect {
  padding: var(--sp-4) var(--sp-4) var(--sp-1); font-size: var(--fs-xs);
  letter-spacing: var(--ls-label); text-transform: uppercase; color: var(--dim);
}
.nav-item {
  display: block; padding: var(--sp-2) var(--sp-4); color: var(--dim);
  border-left: 2px solid transparent;
}
.nav-item:hover { color: var(--text); }
.nav-item.on { color: var(--text); background: var(--panel-2); border-left-color: var(--accent); }
.main { padding: var(--sp-4) var(--sp-5) var(--sp-5); min-width: 0; }

/* status line */
.statusline {
  display: flex; flex-wrap: wrap; gap: var(--sp-4) var(--sp-5);
  padding: var(--sp-2) var(--sp-3); margin-bottom: var(--sp-4);
  background: var(--panel-2); border: 1px solid var(--line);
  border-radius: var(--radius); color: var(--dim); font-size: var(--fs-sm);
}
.statusline b { color: var(--text); font-weight: 600; }
.statusline .ok { color: var(--good); }

/* headings & tables */
h1.page-title {
  font-size: var(--fs-xs); letter-spacing: var(--ls-label); text-transform: uppercase;
  color: var(--dim); font-weight: 600; margin: 0 0 var(--sp-3);
}
.table-wrap { overflow-x: auto; }
table { width: 100%; border-collapse: collapse; font-variant-numeric: tabular-nums; }
th {
  text-align: left; font-size: var(--fs-xs); letter-spacing: 0.14em;
  text-transform: uppercase; color: var(--dim); font-weight: 600;
  padding: var(--sp-1) var(--sp-3); border-bottom: 1px solid var(--line);
}
td { padding: var(--sp-2) var(--sp-3); border-bottom: 1px solid var(--line); }
tbody tr:hover td { background: var(--panel-2); }

/* pills, buttons, forms */
.pill { display: inline-block; padding: 1px var(--sp-2); border-radius: 3px; font-size: var(--fs-sm); }
.pill.ok { color: var(--good); background: var(--good-bg); }
.pill.err { color: var(--bad); background: var(--bad-bg); }
.pill.off { color: var(--dim); background: var(--dim-bg); }
.btn {
  display: inline-block; padding: 3px var(--sp-3); border: 1px solid var(--line);
  border-radius: 3px; background: transparent; color: var(--dim);
  font: inherit; font-size: var(--fs-sm); cursor: pointer;
}
.btn:hover { color: var(--text); border-color: var(--dim); }
.btn.hot { border-color: var(--accent); color: var(--accent); }
.btn.hot:hover { background: var(--accent-bg); }
.btn.danger { border-color: var(--bad); color: var(--bad); }
input, select, textarea {
  background: var(--bg); border: 1px solid var(--line); color: var(--text);
  font: inherit; font-size: var(--fs-sm); padding: var(--sp-1) var(--sp-2);
  border-radius: 3px; width: 100%;
}
input::placeholder, textarea::placeholder { color: var(--dim); opacity: 0.7; }

/* cards */
.card {
  border: 1px solid var(--line); border-radius: var(--radius);
  background: var(--panel); margin-bottom: var(--sp-4);
}
.card-head {
  display: flex; justify-content: space-between; align-items: center; gap: var(--sp-3);
  padding: var(--sp-2) var(--sp-3); border-bottom: 1px solid var(--line);
}
.card-head b { color: var(--link); }

/* flash + banner (keep existing class names used by flash.html) */
.flash { padding: var(--sp-2) var(--sp-3); border-radius: var(--radius); margin-bottom: var(--sp-3); font-size: var(--fs-sm); }
.flash-success { color: var(--good); background: var(--good-bg); border: 1px solid var(--good); }
.flash-error { color: var(--bad); background: var(--bad-bg); border: 1px solid var(--bad); }
.flash-warning { color: var(--accent); background: var(--accent-bg); border: 1px solid var(--accent); }

/* mobile */
.menu-btn { display: none; }
@media (max-width: 768px) {
  .shell { grid-template-columns: 1fr; }
  .sidebar { position: fixed; z-index: 10; width: var(--sidebar-w); height: 100vh;
             transform: translateX(-100%); transition: transform 0.15s ease; }
  .sidebar.open { transform: none; }
  .menu-btn { display: inline-block; }
  .main { padding: var(--sp-3); }
}
@media (prefers-reduced-motion: reduce) { * { transition: none !important; } }
```

Preserve any class names still referenced by templates you are NOT rewriting in this task (`grep -h 'class="' internal/web/templates -r | ...` to check; the old status/processes/flash/task-history/log/prompt-editor classes must keep working — port their old rules onto the new tokens rather than deleting them).

- [ ] **Step 4: Rewrite layout.html**

```html
<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1">
    <title>Leo — {{.Title}}</title>
    <link rel="stylesheet" href="/static/style.css">
    <script src="/static/htmx.min.js"></script>
</head>
<body hx-boost="true">
    <div class="shell">
        <nav class="sidebar" id="sidebar" aria-label="Main navigation">
            <div class="brand">leo<em>_</em></div>
            <div class="nav-sect">Operate</div>
            <a class="nav-item {{if eq .Page "tasks"}}on{{end}}" href="/tasks">Tasks</a>
            <a class="nav-item {{if eq .Page "agents"}}on{{end}}" href="/agents">Agents</a>
            <a class="nav-item {{if eq .Page "processes"}}on{{end}}" href="/processes">Processes</a>
            <a class="nav-item {{if eq .Page "sessions"}}on{{end}}" href="/sessions">Sessions</a>
            <div class="nav-sect">Configure</div>
            <a class="nav-item {{if eq .Page "config_defaults"}}on{{end}}" href="/config/defaults">Defaults</a>
            <a class="nav-item {{if eq .Page "config_templates"}}on{{end}}" href="/config/templates">Templates</a>
            <a class="nav-item {{if eq .Page "config_providers"}}on{{end}}" href="/config/providers">Providers</a>
            <a class="nav-item {{if eq .Page "config_settings"}}on{{end}}" href="/config/settings">Settings</a>
            <a class="nav-item {{if eq .Page "service"}}on{{end}}" href="/service">Service</a>
            <form method="post" action="/logout" class="logout-form" hx-boost="false">
                <button type="submit" class="btn">Sign out</button>
            </form>
        </nav>
        <main class="main">
            <button class="menu-btn btn" onclick="document.getElementById('sidebar').classList.toggle('open')" aria-label="Toggle navigation">☰</button>
            {{template "status.html" .}}
            <div id="flash-container"></div>
            {{if eq .Page "tasks"}}{{template "page_tasks" .}}
            {{else if eq .Page "agents"}}{{template "page_agents" .}}
            {{else if eq .Page "processes"}}{{template "page_processes" .}}
            {{else if eq .Page "sessions"}}{{template "page_sessions" .}}
            {{else if eq .Page "config_defaults"}}{{template "page_config_defaults" .}}
            {{else if eq .Page "config_templates"}}{{template "page_config_templates" .}}
            {{else if eq .Page "config_providers"}}{{template "page_config_providers" .}}
            {{else if eq .Page "config_settings"}}{{template "page_config_settings" .}}
            {{else if eq .Page "service"}}{{template "page_service" .}}{{end}}
        </main>
    </div>
    <script>
    /* keep toggleHistory/toggleLog/confirmDelete helpers from the old layout verbatim */
    </script>
</body>
</html>
```

Keep the three JS helper functions from the old layout.html (`toggleHistory`, `toggleLog`, `confirmDelete` — old file lines 37–66) verbatim in the script block; task-history and log viewing still use them.

- [ ] **Step 5: Create handlers_pages.go and page stubs; rewire routes**

```go
package web

import "net/http"

// pageData is the payload every full-page render receives. Pages add their
// own data via the Data field. Status carries what partials/status.html
// renders today — extract that struct from buildDashboardData (handlers.go:1123)
// into a named type and reuse it here and in handlePartialStatus.
type pageData struct {
	Page          string
	Title         string
	Status        any // the extracted status struct; same data both render paths
	RestartNeeded bool
	Data          any
}

// handlePage returns a GET handler that renders the named page in the shell.
// build may be nil for pages with no extra data yet.
func (s *Server) handlePage(page, title string, build func(*http.Request) (any, error)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var data any
		if build != nil {
			var err error
			if data, err = build(r); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		pd := pageData{Page: page, Title: title, RestartNeeded: s.restartNeeded.Load(), Data: data}
		s.fillStatus(&pd) // extract from buildDashboardData: process states + task counts + next run
		if err := s.templates.ExecuteTemplate(w, "layout.html", pd); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	}
}
```

`fillStatus` is a refactor of the status portion of `buildDashboardData()` (handlers.go:1123) — extract, don't duplicate. In web.go, replace the `GET /` + `/partials/config/*` registrations:

```go
mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" { http.NotFound(w, r); return }
	http.Redirect(w, r, "/tasks", http.StatusSeeOther)
})
mux.HandleFunc("GET /tasks", s.handlePage("tasks", "Tasks", s.buildTasksData))
mux.HandleFunc("GET /agents", s.handlePage("agents", "Agents", s.buildAgentsData))
mux.HandleFunc("GET /processes", s.handlePage("processes", "Processes", s.buildProcessesData))
mux.HandleFunc("GET /sessions", s.handlePage("sessions", "Sessions", nil))
mux.HandleFunc("GET /config/defaults", s.handlePage("config_defaults", "Defaults", s.buildDefaultsData))
mux.HandleFunc("GET /config/templates", s.handlePage("config_templates", "Templates", s.buildTemplatesData))
mux.HandleFunc("GET /config/providers", s.handlePage("config_providers", "Providers", s.buildProvidersData))
mux.HandleFunc("GET /config/settings", s.handlePage("config_settings", "Settings", s.buildSettingsData))
mux.HandleFunc("GET /service", s.handlePage("service", "Service", nil))
```

For THIS task, the `build*Data` funcs just re-expose what the old partial handlers loaded (`buildDashboardData` pieces); the pages/*.html stubs wrap the old partial templates' inner content (copy the table/form markup across into `{{define "page_<name>"}}` blocks). Sessions and service get `{{define "page_sessions"}}<h1 class="page-title">Sessions</h1><p class="dim">coming soon</p>{{end}}` style stubs. Delete the old tab partial registrations (`/partials/config/processes|tasks|settings|templates`, `/partials/tasks`, `/partials/agents` full-tab variants) and their handler funcs ONLY where nothing else uses them — the poll targets `/partials/status` and `/partials/processes` stay. Keep all `/web/*` mutation routes unchanged in this task.

- [ ] **Step 6: Update middleware/auth tests, run everything**

Any test asserting `GET /` returns 200 now expects 303→/tasks. Any test hitting `/partials/config/*` or `/partials/tasks` moves to the new page URL. Run: `go test -race ./internal/web/... && make lint` → PASS.

- [ ] **Step 7: Visual smoke check against the isolated test daemon**

```bash
mkdir -p /tmp/leo-webdev/state && cp <repo>/testdata-or-handwritten-minimal-leo.yaml /tmp/leo-webdev/leo.yaml
# minimal leo.yaml: web enabled on port 8371, one dummy task, one disabled process
make build && LEO_HOME=/tmp/leo-webdev ./bin/leo service start
```

Screenshot `http://127.0.0.1:8371/tasks` (login via the token at /tmp/leo-webdev/state/api.token) at 1440, 768, and 375px widths with Playwright. Compare against `docs/superpowers/specs/assets/2026-07-08-web-ui-direction-a.html`: sidebar, statusline, table styling, mobile drawer. Iterate CSS until it matches the mockup's feel.

- [ ] **Step 8: Commit**

```bash
git add internal/web/
git commit -m "feat(web): Ops Terminal shell — sidebar layout, per-section routes, token CSS, JetBrains Mono"
```

---

### Task 6: Form component + generic save path + Defaults page (exemplar)

**Files:**
- Create: `internal/web/templates/components/form.html`
- Create: `internal/web/handlers_config.go`
- Modify: `internal/web/templates/pages/config_defaults.html`
- Modify: `internal/web/web.go` (route swap for defaults save)
- Modify: `internal/web/handlers.go` (delete old `handleConfigDefaults`)
- Test: `internal/web/handlers_config_test.go`

**Interfaces:**
- Consumes: `schema.Values`, `schema.Apply`, `schema.OptionSources`, `schema.FieldValue` (Tasks 3–4); `s.validateAndSave`, `s.reloadConfigOrWarn`, `s.renderFlash`, `appendReloadWarning` (existing, handlers.go).
- Produces:
  - Template `config_form` rendering `formData{Action string, Fields []fieldView, SubmitLabel string}` where `fieldView{schema.FieldValue; Opts []schema.Option}`.
  - `func (s *Server) buildForm(section schema.Section, target any, cfg *config.Config, action string) formData`
  - `func (s *Server) applySection(w http.ResponseWriter, r *http.Request, section schema.Section, locate func(cfg *config.Config) (any, bool), put func(cfg *config.Config, v any), okMsg string, needsRestart bool)` — the ONE save path all config posts go through.

- [ ] **Step 1: Write failing handler test**

```go
// internal/web/handlers_config_test.go
package web

import (
	"net/url"
	"strings"
	"testing"
)

func TestDefaultsSaveRoundTrip(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // helper from Task 5
	form := url.Values{}
	form.Set("model", "opus")
	form.Set("max_turns", "50")
	form.Set("permission_mode", "acceptEdits")
	form.Set("provider", "")
	form.Set("stale_resume_hours", "12")
	// KindBool pattern: hidden false + optional true
	form.Add("bypass_permissions", "false")
	form.Add("remote_control", "false")
	resp := postFormWithCookie(t, srv, "/web/config/defaults", form, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	cfg := reloadTestConfig(t, srv) // read the temp leo.yaml back
	if cfg.Defaults.Model != "opus" || cfg.Defaults.MaxTurns != 50 ||
		cfg.Defaults.StaleResumeHours != 12 {
		t.Errorf("saved defaults wrong: %+v", cfg.Defaults)
	}
}

func TestDefaultsSaveRejectsBadModel(t *testing.T) {
	srv, cookie := newAuthedTestServer(t)
	form := url.Values{}
	form.Set("model", "gpt-9000")
	resp := postFormWithCookie(t, srv, "/web/config/defaults", form, cookie)
	body := readBody(t, resp)
	if !strings.Contains(body, "flash-error") {
		t.Errorf("want validation flash, got: %s", body)
	}
	cfg := reloadTestConfig(t, srv)
	if cfg.Defaults.Model == "gpt-9000" {
		t.Error("invalid model was persisted")
	}
}
```

Run: `go test -race ./internal/web/ -run TestDefaultsSave -v` → FAIL.

- [ ] **Step 2: Implement handlers_config.go**

```go
package web

import (
	"net/http"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// fieldView pairs a resolved field value with its select options.
type fieldView struct {
	schema.FieldValue
	Opts []schema.Option
}

// formData feeds components/form.html.
type formData struct {
	Action      string
	Fields      []fieldView
	SubmitLabel string
	DeleteURL   string // optional; renders a delete button
}

// buildForm renders section's registry against target for display.
func (s *Server) buildForm(section schema.Section, target any, cfg *config.Config, action string) formData {
	src := schema.OptionSources{Cfg: cfg, Agents: s.agentList}
	var defaults any
	if section != schema.SectionDefaults && section != schema.SectionProvider &&
		section != schema.SectionClientHost && section != schema.SectionWeb &&
		section != schema.SectionClient {
		defaults = &cfg.Defaults
	}
	fd := formData{Action: action, SubmitLabel: "Save"}
	for _, fv := range schema.Values(target, section, defaults) {
		view := fieldView{FieldValue: fv}
		if fv.Options != "" {
			view.Opts = src.For(fv.Options)
		}
		fd.Fields = append(fd.Fields, view)
	}
	return fd
}

// applySection is the single save path for every config form. locate returns
// a POINTER to a copy of the section struct; put writes the (mutated) copy
// back into cfg. needsRestart marks process-affecting sections.
func (s *Server) applySection(w http.ResponseWriter, r *http.Request,
	section schema.Section,
	locate func(cfg *config.Config) (any, bool),
	put func(cfg *config.Config, v any),
	okMsg string, needsRestart bool,
) {
	if err := r.ParseForm(); err != nil {
		s.renderFlash(w, "error", "Invalid form: "+err.Error())
		return
	}
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", "Failed to load config: "+err.Error())
		return
	}
	target, ok := locate(cfg)
	if !ok {
		s.renderFlash(w, "error", "Not found")
		return
	}
	if err := schema.Apply(target, section, r.Form); err != nil {
		s.renderFlash(w, "error", err.Error())
		return
	}
	put(cfg, target)
	if errMsg := s.validateAndSave(cfg); errMsg != "" {
		s.renderFlash(w, "error", errMsg)
		return
	}
	warn := s.reloadConfigOrWarn()
	if needsRestart {
		s.restartNeeded.Store(true)
	}
	typ, msg := appendReloadWarning("success", okMsg, warn)
	s.renderFlash(w, typ, msg)
}

func (s *Server) handleConfigDefaultsSave(w http.ResponseWriter, r *http.Request) {
	s.applySection(w, r, schema.SectionDefaults,
		func(cfg *config.Config) (any, bool) { return &cfg.Defaults, true },
		func(cfg *config.Config, v any) {}, // pointer into cfg — already applied
		"Defaults saved", true)
}
```

In web.go replace `mux.HandleFunc("POST /web/config/defaults", s.handleConfigDefaults)` with `s.handleConfigDefaultsSave` and delete the old func from handlers.go.

- [ ] **Step 3: Write components/form.html**

```html
{{define "config_form"}}
<form class="config-form" hx-post="{{.Action}}" hx-target="#flash-container" hx-swap="innerHTML">
  {{$group := ""}}
  {{range .Fields}}{{if not .Advanced}}
    {{if ne .Group $group}}{{$group = .Group}}<div class="form-group-label">{{.Group}}</div>{{end}}
    {{template "config_field" .}}
  {{end}}{{end}}
  <details class="form-advanced"><summary>Advanced</summary>
  {{range .Fields}}{{if .Advanced}}{{template "config_field" .}}{{end}}{{end}}
  </details>
  <div class="form-actions">
    <button type="submit" class="btn hot">{{.SubmitLabel}}</button>
    {{if .DeleteURL}}
    <form hx-delete="{{.DeleteURL}}" hx-target="#flash-container" hx-trigger="confirmed"
          onsubmit="event.preventDefault(); confirmDelete(this)">
      <button type="submit" class="btn danger">Delete</button>
    </form>
    {{end}}
  </div>
</form>
{{end}}

{{define "config_field"}}
<div class="frow">
  <label for="f-{{.Key}}">{{.Label}}</label>
  <div class="fctl">
    {{if eq .Kind 3}}{{/* KindBool */}}
      <input type="hidden" name="{{.Key}}" value="false">
      <input type="checkbox" class="toggle" id="f-{{.Key}}" name="{{.Key}}" value="true" {{if .Checked}}checked{{end}}>
    {{else if eq .Kind 4}}{{/* KindTriBool */}}
      <select id="f-{{.Key}}" name="{{.Key}}">
        <option value="" {{if eq .Value ""}}selected{{end}}>inherit</option>
        <option value="true" {{if eq .Value "true"}}selected{{end}}>on</option>
        <option value="false" {{if eq .Value "false"}}selected{{end}}>off</option>
      </select>
    {{else if eq .Kind 5}}{{/* KindSelect */}}
      <select id="f-{{.Key}}" name="{{.Key}}">
        {{$v := .Value}}{{range .Opts}}<option value="{{.Value}}" {{if eq .Value $v}}selected{{end}}>{{.Label}}</option>{{end}}
      </select>
    {{else if eq .Kind 7}}{{/* KindEnvMap */}}
      <textarea id="f-{{.Key}}" name="{{.Key}}" rows="3" placeholder="KEY=VALUE, one per line">{{.Value}}</textarea>
    {{else if eq .Kind 10}}{{/* KindTextarea */}}
      <textarea id="f-{{.Key}}" name="{{.Key}}" rows="3">{{.Value}}</textarea>
    {{else if eq .Kind 8}}{{/* KindCron */}}
      <input type="text" id="f-{{.Key}}" name="{{.Key}}" value="{{.Value}}"
             hx-get="/web/cron/preview" hx-trigger="keyup changed delay:400ms"
             hx-target="next .cron-preview" hx-include="this">
      <div class="cron-preview dim"></div>
    {{else if eq .Kind 2}}{{/* KindNumber */}}
      <input type="number" id="f-{{.Key}}" name="{{.Key}}" value="{{.Value}}">
    {{else}}
      <input type="text" id="f-{{.Key}}" name="{{.Key}}" value="{{.Value}}"
             {{if .Inherited}}placeholder="inherit: {{.Inherited}}"{{end}}>
    {{end}}
    {{if .Help}}<div class="fhelp">{{.Help}}</div>{{end}}
    {{if .Warning}}<div class="fwarn">⚠ {{.Warning}}</div>{{end}}
  </div>
</div>
{{end}}
```

Raw Kind integers in `eq` comparisons are brittle — instead add a template func `kindName` (registered in parseTemplates) that maps schema.Kind → string ("bool", "tribool", "select", "envmap", "textarea", "cron", "number", "text", "csv", "duration") and compare on names: `{{if eq (kindName .Kind) "bool"}}`. Implement it that way, not with integers. Add matching CSS: `.frow { display: grid; grid-template-columns: 180px 1fr; gap: var(--sp-3); padding: var(--sp-2) var(--sp-3); align-items: start; } .fhelp { color: var(--dim); font-size: var(--fs-xs); margin-top: 2px; } .fwarn { color: var(--accent); font-size: var(--fs-xs); margin-top: 2px; } .form-group-label { padding: var(--sp-3) var(--sp-3) var(--sp-1); font-size: var(--fs-xs); letter-spacing: var(--ls-label); text-transform: uppercase; color: var(--dim); }` plus a `.toggle` checkbox restyle (accent-color: var(--accent)).

- [ ] **Step 4: Point the Defaults page at the component**

```html
{{define "page_config_defaults"}}
<h1 class="page-title">Defaults</h1>
<p class="dim">Baseline settings inherited by every process, task, template, and session unless overridden.</p>
<div class="card">{{template "config_form" .Data}}</div>
{{end}}
```

`buildDefaultsData` returns `s.buildForm(schema.SectionDefaults, &cfg.Defaults, cfg, "/web/config/defaults")`.

- [ ] **Step 5: Run tests, verify in browser, commit**

Run: `go test -race ./internal/web/... && make lint` → PASS. In the test daemon, open /config/defaults: every DefaultsConfig field visible, save works, invalid model flashes an error without persisting.

```bash
git add internal/web/
git commit -m "feat(web): schema-driven form component + generic save path; full defaults coverage"
```

---

### Task 7: Tasks page — list, per-task edit page, add-then-edit flow

**Files:**
- Modify: `internal/web/templates/pages/tasks.html` (list)
- Create: `internal/web/templates/pages/task_edit.html` (`{{define "page_task_edit"}}`; add it to layout.html's dispatch chain as `.Page "task_edit"`)
- Modify: `internal/web/handlers_config.go` (task save/add/delete via applySection), `internal/web/web.go` (routes)
- Delete: old `handleConfigTask` body in handlers.go, `templates/partials/config_tasks.html`
- Test: `internal/web/handlers_config_test.go` (extend), keep `handlers_task_prompt_test.go` green

**Interfaces:**
- Consumes: `applySection`, `buildForm` (Task 6); existing `handleTaskToggle`, `handleTaskRun`, prompt editor component + handlers, `describeCron`, history/log components.
- Produces: routes `GET /tasks` (list), `GET /tasks/{name}` (edit), `POST /web/task/add` (rewritten: name + schedule only → 303 to `/tasks/{name}`), `POST /web/config/task/{name}` (schema save), `DELETE /web/task/{name}/delete` (kept).

- [ ] **Step 1: Extend tests**

```go
func TestTaskSaveCoversNewFields(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // seed config must contain task "demo"
	form := taskFormBase()               // helper: render current form via GET or build minimal valid url.Values
	form.Set("runtime", "persistent")
	form.Set("session", "")
	form.Set("queue_max", "7")
	form.Add("lazy", "false")
	form.Add("lazy", "true")
	resp := postFormWithCookie(t, srv, "/web/config/task/demo", form, cookie)
	if resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	cfg := reloadTestConfig(t, srv)
	task := cfg.Tasks["demo"]
	if task.Runtime != "persistent" || task.QueueMax != 7 || !task.Lazy {
		t.Errorf("new fields not saved: %+v", task)
	}
}

func TestTaskAddRedirectsToEdit(t *testing.T) {
	srv, cookie := newAuthedTestServer(t)
	form := url.Values{"name": {"fresh-task"}, "schedule": {"0 9 * * *"}}
	resp := postFormWithCookieNoRedirect(t, srv, "/web/task/add", form, cookie)
	if resp.StatusCode != 303 || resp.Header.Get("Location") != "/tasks/fresh-task" {
		t.Errorf("add: status=%d loc=%q", resp.StatusCode, resp.Header.Get("Location"))
	}
	cfg := reloadTestConfig(t, srv)
	if _, ok := cfg.Tasks["fresh-task"]; !ok {
		t.Error("task not created")
	}
}
```

Run → FAIL (routes/behavior missing).

- [ ] **Step 2: Handlers**

```go
func (s *Server) handleConfigTaskSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionTask,
		func(cfg *config.Config) (any, bool) {
			t, ok := cfg.Tasks[name]
			return &t, ok
		},
		func(cfg *config.Config, v any) { cfg.Tasks[name] = *(v.(*config.TaskConfig)) },
		fmt.Sprintf("Task %q saved", name), false)
}
```

Rewrite `handleTaskAdd` (handlers.go:1294): validate name (reuse its existing name checks), require non-empty schedule, create `config.TaskConfig{Schedule: sched, PromptFile: "prompts/" + name + ".md", Enabled: false}`, validateAndSave, then `http.Redirect(w, r, "/tasks/"+url.PathEscape(name), http.StatusSeeOther)` (the add form is a plain non-htmx form — set `hx-boost="false"` on it so the browser follows the redirect). Keep `handleTaskDelete` but on success emit `HX-Redirect: /tasks` header so htmx navigates back to the list.

- [ ] **Step 3: Templates**

```html
{{define "page_tasks"}}
<h1 class="page-title">Tasks</h1>
<div class="card">
  <div class="table-wrap">
  <table>
    <thead><tr><th>name</th><th>schedule</th><th>next</th><th>last</th><th></th></tr></thead>
    <tbody>
    {{range .Data.Tasks}}
    <tr>
      <td><a href="/tasks/{{.Name}}">{{.Name}}</a></td>
      <td>{{cronDesc .Schedule}} <span class="dim">{{.Schedule}}</span></td>
      <td>{{relativeTime .NextRun}}</td>
      <td>{{if .HasRun}}<span class="pill {{if eq .LastExit 0}}ok{{else}}err{{end}}">{{if eq .LastExit 0}}ok{{else}}exit {{.LastExit}}{{end}}</span>{{else}}<span class="dim">—</span>{{end}}</td>
      <td class="row-actions">
        <button class="btn hot" hx-post="/web/task/{{.Name}}/run" hx-target="#flash-container">run</button>
        <button class="btn" hx-post="/web/task/{{.Name}}/toggle" hx-target="#flash-container">{{if .Enabled}}disable{{else}}enable{{end}}</button>
      </td>
    </tr>
    {{end}}
    </tbody>
  </table>
  </div>
</div>
<div class="card">
  <div class="card-head"><b>New task</b></div>
  <form method="post" action="/web/task/add" hx-boost="false" class="frow-inline">
    <input type="text" name="name" placeholder="task-name" required>
    <input type="text" name="schedule" placeholder="0 9 * * *" required>
    <button type="submit" class="btn hot">Create</button>
  </form>
</div>
{{end}}
```

The row data comes from `buildTasksData` — reuse the fields the old `tasks.html` partial had (name, schedule, next run from scheduler List(), last exit from history store). Disabled tasks: after a toggle the row is stale — have the toggle response also set `HX-Refresh: true` OR (simpler, less jarring) keep today's behavior of swapping in the flash and let the next page load reflect it. Do what the old handler did; don't invent new swap mechanics.

Edit page:

```html
{{define "page_task_edit"}}
<h1 class="page-title"><a href="/tasks">Tasks</a> / {{.Data.Name}}</h1>
<div class="card">{{template "config_form" .Data.Form}}</div>
<div class="card">
  <div class="card-head"><b>Prompt</b> <span class="dim">{{.Data.PromptFile}}</span></div>
  {{template "prompt_editor.html" .Data.Prompt}}
</div>
<div class="card">
  <div class="card-head"><b>Recent runs</b></div>
  <div id="history-{{.Data.Name}}" hx-get="/partials/task/{{.Data.Name}}/history" hx-trigger="load" hx-swap="innerHTML"></div>
</div>
{{end}}
```

`GET /tasks/{name}` handler: load cfg, 404 unknown names, build `struct{Name, PromptFile string; Form formData; Prompt <existing prompt-editor data type>}` with `Form: s.buildForm(schema.SectionTask, &taskCopy, cfg, "/web/config/task/"+name)` and `Form.DeleteURL = "/web/task/"+name+"/delete"`. Reuse the existing prompt-editor data builder from `handleTaskPromptGet` (extract a shared helper rather than duplicating file-reading logic).

- [ ] **Step 4: Run tests, browser-check, commit**

`go test -race ./internal/web/... && make lint` → PASS. Browser: list renders all seeded tasks; edit shows every TaskConfig field incl. runtime/session/lazy/queue_max; create → lands on edit page; cron preview updates as you type; prompt save works; delete returns to list.

```bash
git add internal/web/ && git commit -m "feat(web): tasks list + schema-driven task edit page with add-then-edit flow"
```

---

### Task 8: Processes page

**Files:**
- Modify: `internal/web/templates/pages/processes.html`
- Create: `internal/web/templates/pages/process_edit.html` (`page_process_edit`, add to layout dispatch)
- Modify: `internal/web/handlers_config.go`, `internal/web/web.go`
- Delete: old `handleConfigProcess` + `handleProcessAdd` form-parsing bodies in handlers.go, `templates/partials/config_processes.html`
- Test: extend `internal/web/handlers_config_test.go`

**Interfaces:**
- Consumes: `applySection`, `buildForm`; existing `handleProcessInterrupt`, `handleProcessRestart`, `ProcessStateProvider.States()`; partial `partials/processes.html` (5s poll) stays for the status strip.
- Produces: `GET /processes` (status cards + table of configured processes), `GET /processes/{name}` (edit), `POST /web/process/add` (name-only → 303 `/processes/{name}`), `POST /web/config/process/{name}` (schema save), `DELETE /web/process/{name}` (kept, adds `HX-Redirect: /processes`).

- [ ] **Step 1: Test — the bypass tri-state regression**

```go
func TestProcessBypassTriState(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // seed: process "assistant"
	form := processFormBase()
	form.Set("bypass_permissions", "true")
	form.Set("permission_mode", "")
	resp := postFormWithCookie(t, srv, "/web/config/process/assistant", form, cookie)
	if resp.StatusCode != 200 { t.Fatalf("save: %d", resp.StatusCode) }
	cfg := reloadTestConfig(t, srv)
	bp := cfg.Processes["assistant"].BypassPermissions
	if bp == nil || !*bp {
		t.Errorf("bypass_permissions = %v, want &true (was impossible to set true from web before)", bp)
	}
	// inherit round-trips to nil
	form.Set("bypass_permissions", "")
	postFormWithCookie(t, srv, "/web/config/process/assistant", form, cookie)
	cfg = reloadTestConfig(t, srv)
	if cfg.Processes["assistant"].BypassPermissions != nil {
		t.Error("inherit did not clear bypass_permissions")
	}
}
```

Run → FAIL.

- [ ] **Step 2: Handlers** — mirror Task 7's shapes exactly:

```go
func (s *Server) handleConfigProcessSave(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.applySection(w, r, schema.SectionProcess,
		func(cfg *config.Config) (any, bool) { p, ok := cfg.Processes[name]; return &p, ok },
		func(cfg *config.Config, v any) { cfg.Processes[name] = *(v.(*config.ProcessConfig)) },
		fmt.Sprintf("Process %q saved", name), true)
}
```

`handleProcessAdd` rewrite: name validation from the old handler, create `config.ProcessConfig{Enabled: false, Workspace: <old default>}`, save, 303 to `/processes/{name}`. Delete the old handler's per-field parsing and the `bypass_permissions` special-case block (handlers.go:559-565) — the schema path replaces it; permission_mode/bypass interplay is now user-visible tri-state (validation in `Config.Validate()` still applies).

- [ ] **Step 3: Templates**

```html
{{define "page_processes"}}
<h1 class="page-title">Processes</h1>
<div id="process-cards" hx-get="/partials/processes" hx-trigger="every 5s" hx-swap="innerHTML">
  {{template "processes.html" .}}
</div>
<div class="card">
  <div class="table-wrap"><table>
    <thead><tr><th>name</th><th>workspace</th><th>model</th><th>enabled</th><th></th></tr></thead>
    <tbody>
    {{range .Data.Processes}}
    <tr>
      <td><a href="/processes/{{.Name}}">{{.Name}}</a></td>
      <td class="dim">{{.Workspace}}</td>
      <td>{{if .Model}}{{.Model}}{{else}}<span class="dim">inherit</span>{{end}}</td>
      <td>{{if .Enabled}}<span class="pill ok">on</span>{{else}}<span class="pill off">off</span>{{end}}</td>
      <td><a class="btn" href="/processes/{{.Name}}">edit</a></td>
    </tr>
    {{end}}
    </tbody>
  </table></div>
</div>
<div class="card">
  <div class="card-head"><b>New process</b></div>
  <form method="post" action="/web/process/add" hx-boost="false" class="frow-inline">
    <input type="text" name="name" placeholder="process-name" required>
    <button type="submit" class="btn hot">Create</button>
  </form>
</div>
{{end}}

{{define "page_process_edit"}}
<h1 class="page-title"><a href="/processes">Processes</a> / {{.Data.Name}}</h1>
<div class="card">{{template "config_form" .Data.Form}}</div>
{{end}}
```

Restyle `partials/processes.html` cards onto the new tokens (status dot = `--good`/`--bad`/`--dim`, keep Interrupt/Restart buttons and 5s poll semantics unchanged).

- [ ] **Step 4: Run, browser-check, commit**

`go test -race ./internal/web/... && make lint` → PASS. Browser: interrupt/restart still work; every ProcessConfig field editable incl. provider, env, stale_resume_hours, tri-state bypass/remote_control.

```bash
git add internal/web/ && git commit -m "feat(web): processes page — schema forms, tri-state bypass fix, add-then-edit"
```

---

### Task 9: Templates page

Same shape as Task 8 with s/process/template/ — spelled out where it differs.

**Files:**
- Modify: `internal/web/templates/pages/config_templates.html`
- Create: `internal/web/templates/pages/template_edit.html` (`page_template_edit`, add to layout dispatch)
- Modify: `internal/web/handlers_config.go`, `internal/web/web.go`
- Delete: old `handleConfigTemplate` + `handleTemplateAdd` parsing bodies (keep name-validation + workspace-default logic from `handleTemplateAdd`, handlers.go:1481), `templates/partials/config_templates.html`
- Test: verify `handlers_templates_test.go` still passes after rewiring; add one save test asserting `idle_suspend_after` and `provider` persist (fields previously missing):

```go
func TestTemplateSaveNewFields(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // seed: template "dev"
	form := templateFormBase()
	form.Set("idle_suspend_after", "2h")
	form.Set("provider", "")
	resp := postFormWithCookie(t, srv, "/web/config/template/dev", form, cookie)
	if resp.StatusCode != 200 { t.Fatalf("save: %d", resp.StatusCode) }
	cfg := reloadTestConfig(t, srv)
	if cfg.Templates["dev"].IdleSuspendAfter != "2h" {
		t.Errorf("idle_suspend_after not saved: %+v", cfg.Templates["dev"])
	}
}
```

Routes: `GET /config/templates` (list: name, workspace, model, agent + edit link + new-template name-only form), `GET /config/templates/{name}` (edit page: `buildForm(schema.SectionTemplate, &tplCopy, cfg, "/web/config/template/"+name)` with DeleteURL), `POST /web/config/template/{name}` via applySection (`needsRestart: false` — templates only affect future agent spawns), `POST /web/template/add` → 303 to edit, `DELETE /web/template/{name}` + `HX-Redirect: /config/templates`.

- [ ] **Step 1: Write the save test above, run → FAIL**
- [ ] **Step 2: Implement handlers + templates per the Task 8 pattern**
- [ ] **Step 3: `go test -race ./internal/web/... && make lint` → PASS; browser-check; commit**

```bash
git add internal/web/ && git commit -m "feat(web): templates page — schema forms incl. idle_suspend_after and provider"
```

---

### Task 10: Providers page

**Files:**
- Modify: `internal/web/templates/pages/config_providers.html`
- Modify: `internal/web/handlers_config.go`, `internal/web/web.go`
- Test: extend `internal/web/handlers_config_test.go`

**Interfaces:**
- Produces: `GET /config/providers` (one card per provider, inline form each — providers are few, no separate edit page), `POST /web/config/provider/{name}`, `POST /web/provider/add` (name-only, stays on page via flash + `HX-Refresh: true`), `DELETE /web/provider/{name}`.

- [ ] **Step 1: Failing test**

```go
func TestProviderCRUD(t *testing.T) {
	srv, cookie := newAuthedTestServer(t)
	// add
	resp := postFormWithCookie(t, srv, "/web/provider/add", url.Values{"name": {"zai"}}, cookie)
	if resp.StatusCode != 200 { t.Fatalf("add: %d", resp.StatusCode) }
	// edit
	form := url.Values{"base_url": {"https://api.z.ai/api/anthropic"}, "api_key_env": {"ZAI_API_KEY"}, "api_key_cmd": {""}, "default_model": {"glm-4.6"}}
	resp = postFormWithCookie(t, srv, "/web/config/provider/zai", form, cookie)
	if resp.StatusCode != 200 { t.Fatalf("save: %d", resp.StatusCode) }
	cfg := reloadTestConfig(t, srv)
	if cfg.Providers["zai"].BaseURL != "https://api.z.ai/api/anthropic" {
		t.Errorf("provider not saved: %+v", cfg.Providers["zai"])
	}
	// delete
	resp = deleteWithCookie(t, srv, "/web/provider/zai", cookie)
	cfg = reloadTestConfig(t, srv)
	if _, ok := cfg.Providers["zai"]; ok { t.Error("provider not deleted") }
}
```

Run → FAIL.

- [ ] **Step 2: Handlers**

Save mirrors Task 8's map pattern with `cfg.Providers` and `schema.SectionProvider`; `needsRestart: true` (providers inject env into spawned processes). Add: validate name non-empty + not already present, `cfg.Providers[name] = config.ProviderConfig{}` (initialize the map if nil), validateAndSave, flash success + `w.Header().Set("HX-Refresh", "true")`. Delete: refuse (`flash error`) when any process/task/template/session/defaults references the provider name — check all `Provider` fields before removing; also `HX-Refresh: true` on success. Note `Config.Validate()` may already reject dangling provider refs — check it; if it does, let validateAndSave carry the refusal and skip the manual scan.

- [ ] **Step 3: Template**

```html
{{define "page_config_providers"}}
<h1 class="page-title">Providers</h1>
<p class="dim">Third-party Anthropic-Messages-compatible endpoints. Processes, tasks, templates, and sessions opt in via their provider field.</p>
{{range .Data.Providers}}
<div class="card">
  <div class="card-head"><b>{{.Name}}</b></div>
  {{template "config_form" .Form}}
</div>
{{else}}
<p class="dim">No providers configured.</p>
{{end}}
<div class="card">
  <div class="card-head"><b>New provider</b></div>
  <form hx-post="/web/provider/add" hx-target="#flash-container" class="frow-inline">
    <input type="text" name="name" placeholder="provider-name" required>
    <button type="submit" class="btn hot">Create</button>
  </form>
</div>
{{end}}
```

`buildProvidersData` returns sorted `[]struct{Name string; Form formData}` where each Form's Action is `/web/config/provider/{name}` and DeleteURL `/web/provider/{name}`.

- [ ] **Step 4: Run, browser-check, commit**

```bash
git add internal/web/ && git commit -m "feat(web): providers page — full CRUD for third-party endpoints"
```

---

### Task 11: Settings page — web config + client hosts

**Files:**
- Modify: `internal/web/templates/pages/config_settings.html`
- Modify: `internal/web/handlers_config.go`, `internal/web/web.go`
- Delete: `handlePartialConfigSettings` and the old read-only web table (this fixes the wrong `0.0.0.0` bind display — the schema Help text documents the real 127.0.0.1 default)
- Test: extend `internal/web/handlers_config_test.go`

**Interfaces:**
- Produces: `GET /config/settings`; `POST /web/config/web` (SectionWeb → `&cfg.Web`, needsRestart true); `POST /web/config/client` (SectionClient → `&cfg.Client`, needsRestart false); `POST /web/host/add`, `POST /web/config/host/{name}`, `DELETE /web/host/{name}` (SectionClientHost over `cfg.Client.Hosts`, same map pattern as providers, `HX-Refresh: true`).

- [ ] **Step 1: Failing test**

```go
func TestWebConfigSave(t *testing.T) {
	srv, cookie := newAuthedTestServer(t)
	form := url.Values{"port": {"8371"}, "bind": {"0.0.0.0"}, "allowed_hosts": {"10.0.4.16, 10.0.2.10"}}
	form.Add("enabled", "false"); form.Add("enabled", "true")
	resp := postFormWithCookie(t, srv, "/web/config/web", form, cookie)
	if resp.StatusCode != 200 { t.Fatalf("save: %d", resp.StatusCode) }
	cfg := reloadTestConfig(t, srv)
	if cfg.Web.Port != 8371 || len(cfg.Web.AllowedHosts) != 2 {
		t.Errorf("web config not saved: %+v", cfg.Web)
	}
}
```

Run → FAIL.

- [ ] **Step 2: Implement** — three applySection wirings plus the hosts map CRUD (copy the provider handlers, substituting `cfg.Client.Hosts`, `schema.SectionClientHost`, no reference-check on delete). Page template: three cards — "Web UI" (web form; the registry Warning strings from Task 2 render on port/bind/allowed_hosts), "Remote client" (default_host), "Remote hosts" (per-host cards + add form, providers-page pattern). Also render a "Config reload" card keeping the existing `POST /web/config/reload` button ("Reload config from disk").

- [ ] **Step 3: Run, browser-check (verify the lockout warnings show), commit**

```bash
git add internal/web/ && git commit -m "feat(web): settings page — editable web config with lockout warnings, client hosts CRUD"
```

---

### Task 12: Sessions page — config CRUD + runtime status + reset

**Files:**
- Modify: `internal/web/templates/pages/sessions.html`
- Create: `internal/web/handlers_sessions.go`
- Modify: `internal/web/handlers_config.go` (session config save via applySection), `internal/web/web.go`
- Test: `internal/web/handlers_sessions_test.go`

**Interfaces:**
- Consumes: `cfg.Sessions` map; `session.NewStore(cfg.HomePath).Get("session:" + name)` (`internal/session`); `daemon.IsRunning(cfg.HomePath)`, `daemon.ResetSession(ctx, homePath, name, reason)`, `daemon.SessionDepth(ctx, homePath, name)` (`internal/daemon` — same calls `internal/cli/session.go:161-218` makes); tmux liveness via `s.execCommand(tmuxBin, tmux.Args("has-session", "-t", tmux.Target("leo-session-"+name))...)` mirroring `internal/cli/session.go:229-239`.
- Produces: `GET /sessions`; `POST /web/config/session/{name}` (applySection, needsRestart false — sessions boot lazily); `POST /web/session/add` (+`HX-Refresh`), `DELETE /web/session/{name}`; `POST /web/session/{name}/reset` (flash result; NO confirm-skip — wire through `confirmDelete`-style JS confirm since reset kills tmux + drops queued work).

- [ ] **Step 1: Failing tests** — session config save (mirror `TestProviderCRUD` shape against `/web/config/session/{name}` with fields `idle_timeout`, `model`) and a reset test using the `execCommand` seam:

```go
func TestSessionReset(t *testing.T) {
	srv, cookie := newAuthedTestServer(t) // seed: sessions: daily: {} in leo.yaml
	var killed []string
	srv.execCommand = func(name string, args ...string) *exec.Cmd {
		killed = append(killed, name+" "+strings.Join(args, " "))
		return exec.Command("true")
	}
	resp := postFormWithCookie(t, srv, "/web/session/daily/reset", url.Values{}, cookie)
	if resp.StatusCode != 200 { t.Fatalf("reset: %d", resp.StatusCode) }
	found := false
	for _, c := range killed {
		if strings.Contains(c, "kill-session") && strings.Contains(c, "leo-session-daily") { found = true }
	}
	if !found { t.Errorf("tmux kill-session not invoked: %v", killed) }
}
```

(Daemon reset call: guard with `daemon.IsRunning` exactly like the CLI does; in tests the daemon isn't running so the guard skips it — assert only the tmux+store effects. Clearing the stored id: `session.NewStore(cfg.HomePath).Delete("session:daily")` — assert via a pre-seeded store entry that is gone afterward.)

Run → FAIL.

- [ ] **Step 2: Implement handlers_sessions.go**

```go
package web

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"time"

	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/session"
	"github.com/blackpaw-studio/leo/internal/tmux"
	"github.com/blackpaw-studio/leo/internal/web/schema"
)

// sessionRow is one configured persistent session with live status attached.
type sessionRow struct {
	Name      string
	StoredID  string
	TmuxLive  bool
	Depth     int  // queued + in-flight; -1 when unknown (daemon call failed)
	Form      formData
}

func (s *Server) buildSessionsData(r *http.Request) (any, error) {
	cfg, err := s.loadConfig()
	if err != nil {
		return nil, err
	}
	store := session.NewStore(cfg.HomePath)
	daemonUp := daemon.IsRunning(cfg.HomePath)
	var rows []sessionRow
	for _, name := range sortedKeys(cfg.Sessions) {
		sc := cfg.Sessions[name]
		row := sessionRow{Name: name, Depth: -1}
		row.StoredID, _, _ = store.Get("session:" + name)
		row.TmuxLive = s.tmuxSessionLive("leo-session-" + name)
		if daemonUp {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			if resp, err := daemon.SessionDepth(ctx, cfg.HomePath, name); err == nil {
				row.Depth = resp.Depth
			}
			cancel()
		}
		row.Form = s.buildForm(schema.SectionSession, &sc, cfg, "/web/config/session/"+name)
		row.Form.DeleteURL = "/web/session/" + name
		rows = append(rows, row)
	}
	return struct{ Sessions []sessionRow }{rows}, nil
}

func (s *Server) tmuxSessionLive(target string) bool {
	tmuxBin, err := exec.LookPath("tmux")
	if err != nil {
		return false
	}
	return s.execCommand(tmuxBin, tmux.Args("has-session", "-t", tmux.Target(target))...).Run() == nil
}

func (s *Server) handleSessionReset(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	cfg, err := s.loadConfig()
	if err != nil {
		s.renderFlash(w, "error", "Failed to load config: "+err.Error())
		return
	}
	if _, ok := cfg.Sessions[name]; !ok {
		s.renderFlash(w, "error", fmt.Sprintf("Session %q not found", name))
		return
	}
	cleared := 0
	if daemon.IsRunning(cfg.HomePath) {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		if resp, derr := daemon.ResetSession(ctx, cfg.HomePath, name, "web reset"); derr == nil {
			cleared = resp.Cleared
		}
		cancel()
	}
	if tmuxBin, lerr := exec.LookPath("tmux"); lerr == nil {
		_ = s.execCommand(tmuxBin, tmux.Args("kill-session", "-t", tmux.Target("leo-session-"+name))...).Run()
	}
	if err := session.NewStore(cfg.HomePath).Delete("session:" + name); err != nil {
		s.renderFlash(w, "error", "Clear stored session id: "+err.Error())
		return
	}
	s.renderFlash(w, "success", fmt.Sprintf("Session %q reset (%d queued invocation(s) dropped)", name, cleared))
}
```

(`sortedKeys` helper if one doesn't already exist; check `daemon.ResetSession`/`SessionDepth` signatures in `internal/daemon` and adjust — the CLI at `internal/cli/session.go` is the working reference. Add session-config save/add/delete to handlers_config.go with the providers map pattern over `cfg.Sessions`.)

- [ ] **Step 3: Template**

```html
{{define "page_sessions"}}
<h1 class="page-title">Sessions</h1>
<p class="dim">Persistent Claude sessions used by runtime: persistent tasks. Reset kills the tmux session and drops queued work.</p>
{{range .Data.Sessions}}
<div class="card">
  <div class="card-head">
    <b>{{.Name}}</b>
    <span>
      {{if .TmuxLive}}<span class="pill ok">running</span>{{else}}<span class="pill off">down</span>{{end}}
      {{if ge .Depth 0}}<span class="dim">queue {{.Depth}}</span>{{end}}
      <button class="btn danger" hx-post="/web/session/{{.Name}}/reset" hx-target="#flash-container"
              hx-trigger="confirmed" onclick="event.preventDefault(); confirmDelete(this.closest('button'))">reset</button>
    </span>
  </div>
  {{if .StoredID}}<div class="frow"><label>session id</label><span class="dim">{{.StoredID}}</span></div>{{end}}
  {{template "config_form" .Form}}
</div>
{{else}}
<p class="dim">No persistent sessions configured.</p>
{{end}}
<div class="card">
  <div class="card-head"><b>New session</b></div>
  <form hx-post="/web/session/add" hx-target="#flash-container" class="frow-inline">
    <input type="text" name="name" placeholder="session-name" required>
    <button type="submit" class="btn hot">Create</button>
  </form>
</div>
{{end}}
```

(`confirmDelete` operates on forms — adapt it or add a `confirmThen(el)` helper in layout.html that works for buttons; keep one shared confirm helper, not two divergent ones.)

- [ ] **Step 4: Run, browser-check, commit**

```bash
git add internal/web/ && git commit -m "feat(web): sessions page — persistent session config, live status, reset"
```

---

### Task 13: Service page

**Files:**
- Modify: `internal/web/templates/pages/service.html`
- Create: `internal/web/handlers_service.go`
- Modify: `internal/web/web.go` (route `GET /web/service/logtail` partial + page data wiring)
- Test: `internal/web/handlers_service_test.go`

**Interfaces:**
- Consumes: `service.LogPathFor(cfg.HomePath)` (`internal/service/service.go:34`); existing `handleServiceRestart` (`POST /web/service/restart`) and `handleConfigReload` (`POST /web/config/reload`); `ProcessStateProvider.States()`.
- Produces: `GET /service` page: daemon process table (name/status/uptime/restarts from `States()`, including ephemeral agents), Reload + Restart buttons, log tail card. `GET /web/service/logtail?n=200` returns the last N lines of service.log wrapped in `<pre class="logtail">` (N capped at 1000, default 200).

- [ ] **Step 1: Failing test**

```go
func TestServiceLogTail(t *testing.T) {
	srv, cookie := newAuthedTestServer(t)
	logPath := service.LogPathFor(testHomePath(t, srv)) // helper: the temp LEO home used by the test server
	os.MkdirAll(filepath.Dir(logPath), 0o755)
	os.WriteFile(logPath, []byte("line1\nline2\nline3\n"), 0o644)
	resp := getWithCookie(t, srv, "/web/service/logtail?n=2", cookie)
	body := readBody(t, resp)
	if strings.Contains(body, "line1") || !strings.Contains(body, "line3") {
		t.Errorf("tail wrong: %s", body)
	}
}
```

Run → FAIL.

- [ ] **Step 2: Implement** — read the file, split lines, keep the last N (simple whole-file read is fine; service.log is size-bounded in practice — if it exceeds 5MB read only the final 5MB using `f.Seek(-5<<20, io.SeekEnd)` guarded by file size), HTML-escape via template rendering (never `fmt.Fprintf` raw log content into HTML). Missing file → `<pre class="logtail dim">no log file yet</pre>`, not an error.

Page template:

```html
{{define "page_service"}}
<h1 class="page-title">Service</h1>
<div class="card">
  <div class="card-head"><b>Supervisor</b>
    <span>
      <button class="btn" hx-post="/web/config/reload" hx-target="#flash-container">Reload config</button>
      <button class="btn danger" hx-post="/web/service/restart" hx-target="#flash-container"
              hx-confirm="Restart the leo service? All supervised processes and agents restart.">Restart service</button>
    </span>
  </div>
  <div class="table-wrap"><table>
    <thead><tr><th>process</th><th>status</th><th>uptime</th><th>restarts</th></tr></thead>
    <tbody>
    {{range .Data.States}}
    <tr><td>{{.Name}}{{if .Ephemeral}} <span class="dim">(agent)</span>{{end}}</td>
        <td><span class="pill {{if eq .Status "running"}}ok{{else}}off{{end}}">{{.Status}}</span></td>
        <td>{{uptime .StartedAt}}</td><td>{{.Restarts}}</td></tr>
    {{end}}
    </tbody>
  </table></div>
</div>
<div class="card">
  <div class="card-head"><b>Service log</b>
    <button class="btn" hx-get="/web/service/logtail?n=200" hx-target="#service-log" hx-swap="innerHTML">refresh</button>
  </div>
  <div id="service-log" hx-get="/web/service/logtail?n=200" hx-trigger="load" hx-swap="innerHTML"></div>
</div>
{{end}}
```

Add `.logtail { max-height: 400px; overflow: auto; font-size: var(--fs-xs); padding: var(--sp-3); margin: 0; white-space: pre-wrap; }`. Note the restart button uses htmx's built-in `hx-confirm` — appropriate here because restart bounces every process AND every running agent (see memory: `leo update bounces ALL agents`); the confirm text must say so.

- [ ] **Step 3: Run, browser-check, commit**

```bash
git add internal/web/ && git commit -m "feat(web): service page — supervisor status, reload/restart, log tail"
```

---

### Task 14: Agents page restyle, cleanup sweep, visual QA

**Files:**
- Modify: `internal/web/templates/pages/agents.html` (restyle spawn form + agent cards onto tokens; actions unchanged: spawn/stop/rename)
- Delete: `internal/web/templates/partials/tasks.html`, `agents.html`, `config.html`, `config_processes.html`, `config_tasks.html`, `config_settings.html`, `config_templates.html` (verify nothing references them: `grep -rn 'config_processes\|config_tasks\|config_settings\|config_templates\|"tasks.html"\|"agents.html"\|"config.html"' internal/web/`)
- Modify: `internal/web/handlers.go` — delete now-unused handlers (`handleDashboard`'s old body, `handlePartialConfig*`, `handlePartialTasks`, `handlePartialAgents` if the agents page now renders via handlePage, old `handleConfigTask`/`handleConfigProcess`/`handleConfigTemplate`, `parseCommaSeparated`/`parseEnvMap`/`parseOptionalBool` if no remaining callers)
- Modify: `internal/web/static/style.css` — delete any rule no template references (`grep -o 'class="[^"]*"' -r internal/web/templates | tr ' ' '\n' | sort -u` as the checklist)
- Modify: `docs/` — update any doc that shows the old web UI tabs (grep `docs/ -rn 'Manage Tasks\|Manage Processes\|Manage Templates'`)

- [ ] **Step 1: Restyle agents page** — spawn form as a card (template select via `s.agentList()`-independent template options — templates come from cfg.Templates; name/repo input keeps its help text), running agents as cards with status stripe, stop + rename actions preserved with their existing htmx targets (`handlers_agents.go` behaviors unchanged; `handlers_agents_test.go` must still pass without modification — if it fails, the regression is in your template wiring, not the test).

- [ ] **Step 2: Dead-code sweep** — run the greps above; `go vet` catches unused funcs only in some cases, so also `grep -n 'func (s \*Server)' internal/web/*.go` and trace each handler to a route registration; delete unrouted ones EXCEPT `handleProcessSendKeys`/`handleProcessMessage` (`/web/process/{name}/send` + `/message` are used by external callers — keep them routed and untouched).

- [ ] **Step 3: Full suite + lint**

Run: `go test -race -cover ./... && make lint`
Expected: PASS across the repo (not just internal/web — config/daemon/cli tests must be untouched).

- [ ] **Step 4: Visual QA against the isolated test daemon**

Seed `/tmp/leo-webdev/leo.yaml` with: 2 processes (one enabled), 3 tasks (one disabled, one failing exit code in history if convenient), 1 template, 1 provider, 1 session, client host entry. Screenshot with Playwright at widths 375, 768, 1440 for pages: /tasks, /tasks/{name}, /processes, /config/defaults, /config/providers, /config/settings, /sessions, /service, /login. Checklist per screenshot:
  - no horizontal body scroll (tables scroll inside .table-wrap)
  - sidebar drawer opens/closes at 375
  - amber only on interactive elements; green/red only on status
  - focus ring visible tabbing through a form
  - login page styled on tokens (not the old look)
Fix issues, re-shoot until clean.

- [ ] **Step 5: Update CLAUDE.md web bullet + commit**

The repo `CLAUDE.md` "Web UI" line still describes tabs — update to sidebar/pages + schema forms in one sentence. Final commit:

```bash
git add -A && git commit -m "feat(web): agents page restyle, dead code sweep, docs — complete Ops Terminal redesign"
```

---

## Self-Review Notes (already applied)

- Spec §5 "add forms capture a subset" → Tasks 7/8/9 add-then-edit flows; providers/sessions/hosts create-then-inline-edit (Tasks 10–12).
- Spec §3 drift test → Task 1 (gate) + Task 2 (coverage); exclusions live in one reviewed map.
- Spec §5 agent-dropdown boot cache → Task 4 Step 3 (60s TTL on-demand).
- Spec §8 map-of-struct sections → providers pattern (Task 10) reused for hosts (11) and sessions (12).
- Spec §2 real URLs / middleware risk → Task 5 Step 6 updates middleware/auth tests; `/` redirects.
- Kind integer comparisons in templates explicitly replaced by `kindName` func (Task 6 Step 3 note).
- `enabled` on TaskConfig is registered (KindBool) AND the list has a toggle button — both write through different paths (schema save vs handleTaskToggle); they don't conflict because both round-trip the full config through load-mutate-save.




