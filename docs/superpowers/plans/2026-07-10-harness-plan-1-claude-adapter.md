# Harness Abstraction Plan 1: `internal/harness` + Claude Adapter (Pure Refactor)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Introduce the `internal/harness` package (LaunchSpec, Harness interface, registry) and a claude adapter, then rewire leo's three duplicated argv builders through it — with zero behavior change, locked by characterization tests.

**Architecture:** Callers (cli/agent/run packages) resolve all config cascades into a neutral `harness.LaunchSpec` + claude-specific `claude.Options`; the adapter translates spec → argv, preserving each builder's current flag order byte-for-byte. No config schema changes, no providers removal, no new harnesses — those are Plans 2–5 (spec: `docs/superpowers/specs/2026-07-10-harness-abstraction-design.md`).

**Tech Stack:** Go 1.x, stdlib testing (table-driven), `make test` (`go test -race -cover ./...`), `make lint` (go vet + staticcheck).

## Global Constraints

- **Pure refactor:** every existing test must keep passing unmodified; new characterization tests must pass against the OLD code before the rewire and the NEW code after.
- Claude adapter argv output is **byte-identical** to the current `buildProcessArgs` / `BuildTemplateArgs` / `buildArgs` for identical inputs, including flag order.
- Adapters never import `internal/config` or `internal/leomcp` — callers resolve everything config-dependent into the spec.
- Existing wrapper function signatures (`buildProcessArgs`, `BuildTemplateArgs`, `buildArgs`) stay unchanged in this plan.
- gofmt/goimports on everything; wrap errors with `%w` context; table-driven tests.
- Commit after every task (conventional commits).

## Characterization-Test Protocol (applies to Tasks 3–5)

These are golden tests for a refactor, so the normal TDD polarity inverts:

1. Write the test with the expected argv from this plan.
2. Run it against the **current** (pre-rewire) builder. It must **PASS**. If it fails, the plan's expected value is wrong — compare with the actual output, sanity-check the difference against the current builder's source, and correct the *test*, not the builder.
3. Only then perform the rewire.
4. Re-run: it must still pass, along with the package's pre-existing tests.

---

### Task 1: `internal/harness` core types + registry

**Files:**
- Create: `internal/harness/harness.go`
- Create: `internal/harness/registry.go`
- Test: `internal/harness/registry_test.go`

**Interfaces:**
- Produces: `harness.Kind` (`KindProcess`/`KindAgent`/`KindTask`), `harness.SessionMode` (`SessionNone`/`SessionResume`/`SessionPinned`), `harness.SessionState{Mode, ID}`, `harness.LaunchSpec`, `harness.Harness` interface, `harness.Register(Harness)`, `harness.Get(string) (Harness, error)`, `harness.Names() []string`, `harness.FallbackString(primary, fallback string) string`, `harness.FallbackSlice(primary, fallback []string) []string`. All later tasks consume these.

- [ ] **Step 1: Write the failing registry test**

`internal/harness/registry_test.go`:

```go
package harness

import (
	"reflect"
	"testing"
)

type fakeHarness struct{ name string }

func (f fakeHarness) Name() string                          { return f.name }
func (f fakeHarness) Binary() string                        { return f.name }
func (f fakeHarness) Args(LaunchSpec) ([]string, error)     { return nil, nil }
func (f fakeHarness) SessionArgs(SessionState) []string     { return nil }

func TestRegistryGetAndNames(t *testing.T) {
	reset := snapshotRegistry(t)
	defer reset()

	Register(fakeHarness{name: "zeta"})
	Register(fakeHarness{name: "alpha"})

	h, err := Get("alpha")
	if err != nil {
		t.Fatalf("Get(alpha): %v", err)
	}
	if h.Name() != "alpha" {
		t.Fatalf("Get(alpha).Name() = %q", h.Name())
	}

	if _, err := Get("missing"); err == nil {
		t.Fatal("Get(missing): expected error, got nil")
	}

	// Names() is sorted regardless of registration order.
	got := Names()
	want := []string{"alpha", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %v, want %v", got, want)
	}
}

func TestRegisterDuplicatePanics(t *testing.T) {
	reset := snapshotRegistry(t)
	defer reset()

	Register(fakeHarness{name: "dup"})
	defer func() {
		if recover() == nil {
			t.Fatal("duplicate Register did not panic")
		}
	}()
	Register(fakeHarness{name: "dup"})
}

func TestFallbackHelpers(t *testing.T) {
	if got := FallbackString("a", "b"); got != "a" {
		t.Fatalf("FallbackString(a,b) = %q", got)
	}
	if got := FallbackString("", "b"); got != "b" {
		t.Fatalf("FallbackString('',b) = %q", got)
	}
	if got := FallbackSlice([]string{"x"}, []string{"y"}); !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("FallbackSlice non-empty primary = %v", got)
	}
	if got := FallbackSlice(nil, []string{"y"}); !reflect.DeepEqual(got, []string{"y"}) {
		t.Fatalf("FallbackSlice empty primary = %v", got)
	}
}

// snapshotRegistry empties the package registry for a test and returns a
// restore func. Registration happens in adapter init()s in real binaries;
// tests need a clean slate.
func snapshotRegistry(t *testing.T) func() {
	t.Helper()
	saved := registry
	registry = map[string]Harness{}
	return func() { registry = saved }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/harness/`
Expected: FAIL — package does not exist / undefined identifiers.

- [ ] **Step 3: Write the implementation**

`internal/harness/harness.go`:

```go
// Package harness defines the coding-agent-neutral contract leo uses to
// drive a coding agent CLI. Adapters (claude today; codex/opencode in later
// plans) translate a fully resolved LaunchSpec into binary-specific argv.
//
// Adapters must not consult leo config: every cascade (model, workspace,
// tool lists, merged system prompt, MCP paths) is resolved by the caller
// before the spec reaches an adapter.
package harness

// Kind identifies which leo primitive a launch belongs to. Adapters may
// emit different flags per kind (one-shot task runs vs interactive
// process/agent sessions).
type Kind string

const (
	KindProcess Kind = "process"
	KindAgent   Kind = "agent"
	KindTask    Kind = "task"
)

// SessionMode says how a launch relates to an existing session.
type SessionMode string

const (
	// SessionNone starts a fresh session; the harness picks the ID.
	SessionNone SessionMode = "none"
	// SessionResume continues an existing session by ID.
	SessionResume SessionMode = "resume"
	// SessionPinned starts a fresh session with a pre-issued ID.
	SessionPinned SessionMode = "pinned"
)

// SessionState carries the resolved session decision for a launch.
type SessionState struct {
	Mode SessionMode
	ID   string
}

// LaunchSpec is the harness-neutral description of one coding-agent launch.
type LaunchSpec struct {
	Kind        Kind
	Name        string // process/agent name; empty for tasks
	Model       string // fully resolved
	MaxTurns    int    // 0 = omit the flag (harness default)
	Workspace   string
	AddDirs     []string
	Channels    []string
	DevChannels []string
	Prompt      string // opening prompt (agents) or task prompt; empty for processes
	Session     SessionState
	Options     any // adapter-specific resolved options (e.g. claude.Options)
}

// Harness translates LaunchSpecs into concrete CLI invocations.
type Harness interface {
	// Name is the registry key (config `harness:` value in later plans).
	Name() string
	// Binary is the executable to look up / exec.
	Binary() string
	// Args renders the full argv (excluding argv[0]) for a launch.
	Args(spec LaunchSpec) ([]string, error)
	// SessionArgs renders just the session-selection flags, for callers
	// that append session state after a pre-built arg list.
	SessionArgs(s SessionState) []string
}

// FallbackString returns primary if non-empty, else fallback. Callers use
// it to resolve config cascades into a LaunchSpec.
func FallbackString(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

// FallbackSlice returns primary if non-empty, else fallback.
func FallbackSlice(primary, fallback []string) []string {
	if len(primary) > 0 {
		return primary
	}
	return fallback
}
```

`internal/harness/registry.go`:

```go
package harness

import (
	"fmt"
	"sort"
)

var registry = map[string]Harness{}

// Register adds an adapter under its Name. Adapter packages call this from
// init(). Duplicate registration is a programmer error, so it panics.
func Register(h Harness) {
	if _, dup := registry[h.Name()]; dup {
		panic(fmt.Sprintf("harness: duplicate registration for %q", h.Name()))
	}
	registry[h.Name()] = h
}

// Get returns the adapter registered under name.
func Get(name string) (Harness, error) {
	h, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown harness %q (registered: %v)", name, Names())
	}
	return h, nil
}

// Names returns the registered harness names, sorted.
func Names() []string {
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/harness/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/harness/
git commit -m "feat(harness): core types and adapter registry"
```

---

### Task 2: claude adapter package (Options, identity, SessionArgs)

**Files:**
- Create: `internal/harness/claude/claude.go`
- Test: `internal/harness/claude/claude_test.go`

**Interfaces:**
- Consumes: `harness.Harness`, `harness.SessionState`, `harness.Register` (Task 1).
- Produces: `claude.Options` struct, `claude.Claude{}` implementing `harness.Harness`. Registered under name `"claude"` via `init()`. Tasks 3–5 add the per-kind arg builders to this package.

- [ ] **Step 1: Write the failing test**

`internal/harness/claude/claude_test.go`:

```go
package claude

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestIdentity(t *testing.T) {
	c := Claude{}
	if c.Name() != "claude" {
		t.Fatalf("Name() = %q", c.Name())
	}
	if c.Binary() != "claude" {
		t.Fatalf("Binary() = %q", c.Binary())
	}
}

func TestRegisteredInRegistry(t *testing.T) {
	h, err := harness.Get("claude")
	if err != nil {
		t.Fatalf("harness.Get(claude): %v", err)
	}
	if h.Name() != "claude" {
		t.Fatalf("registered harness Name() = %q", h.Name())
	}
}

func TestSessionArgs(t *testing.T) {
	tests := []struct {
		name  string
		state harness.SessionState
		want  []string
	}{
		{"none", harness.SessionState{Mode: harness.SessionNone}, nil},
		{"zero value", harness.SessionState{}, nil},
		{"resume", harness.SessionState{Mode: harness.SessionResume, ID: "abc-123"}, []string{"--resume", "abc-123"}},
		{"pinned", harness.SessionState{Mode: harness.SessionPinned, ID: "def-456"}, []string{"--session-id", "def-456"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Claude{}.SessionArgs(tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SessionArgs(%+v) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestArgsRejectsWrongOptionsType(t *testing.T) {
	_, err := Claude{}.Args(harness.LaunchSpec{Kind: harness.KindProcess, Options: "nope"})
	if err == nil {
		t.Fatal("Args with non-claude.Options: expected error")
	}
}

func TestArgsRejectsUnknownKind(t *testing.T) {
	_, err := Claude{}.Args(harness.LaunchSpec{Kind: harness.Kind("bogus"), Options: Options{}})
	if err == nil {
		t.Fatal("Args with unknown kind: expected error")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race ./internal/harness/claude/`
Expected: FAIL — package does not exist.

- [ ] **Step 3: Write the implementation**

`internal/harness/claude/claude.go`:

```go
// Package claude adapts leo's harness-neutral LaunchSpec to the Claude Code
// CLI. Flag order per Kind is load-bearing: it reproduces the pre-harness
// arg builders byte-for-byte so the characterization tests hold.
package claude

import (
	"fmt"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// Options carries the claude-specific knobs, fully resolved by the caller:
// cascades applied, system prompt merged (leomcp.MergeSystemPrompt), MCP
// paths gated (config.HasMCPServers), leo MCP flag precomputed.
type Options struct {
	PermissionMode      string
	BypassPermissions   bool   // legacy fallback; only consulted when PermissionMode == ""
	RemoteControl       bool
	RemoteControlPrefix string // when set, adds --remote-control-session-name-prefix
	AgentFile           string // --agent
	AllowedTools        []string
	DisallowedTools     []string
	AppendSystemPrompt  string
	MCPConfigPath       string   // user MCP config; empty when absent or serverless
	LeoMCPArgs          []string // precomputed leomcp.AppendArg(nil, cfg); nil when gated off
}

// Claude is the Claude Code adapter.
type Claude struct{}

func init() { harness.Register(Claude{}) }

func (Claude) Name() string   { return "claude" }
func (Claude) Binary() string { return "claude" }

func (Claude) SessionArgs(s harness.SessionState) []string {
	switch s.Mode {
	case harness.SessionResume:
		return []string{"--resume", s.ID}
	case harness.SessionPinned:
		return []string{"--session-id", s.ID}
	default:
		return nil
	}
}

func (c Claude) Args(spec harness.LaunchSpec) ([]string, error) {
	opts, ok := spec.Options.(Options)
	if !ok {
		return nil, fmt.Errorf("claude: spec.Options is %T, want claude.Options", spec.Options)
	}
	switch spec.Kind {
	case harness.KindProcess:
		return processArgs(spec, opts), nil
	case harness.KindAgent:
		return agentArgs(spec, opts), nil
	case harness.KindTask:
		return taskArgs(spec, opts), nil
	default:
		return nil, fmt.Errorf("claude: unknown launch kind %q", spec.Kind)
	}
}
```

Also create `internal/harness/claude/args.go` with stubs so the package compiles (Tasks 3–5 fill them; each panics until implemented so a premature call is loud, and each stub is replaced within its task):

```go
package claude

import "github.com/blackpaw-studio/leo/internal/harness"

func processArgs(spec harness.LaunchSpec, o Options) []string {
	panic("claude: processArgs not yet implemented (plan task 3)")
}

func agentArgs(spec harness.LaunchSpec, o Options) []string {
	panic("claude: agentArgs not yet implemented (plan task 4)")
}

func taskArgs(spec harness.LaunchSpec, o Options) []string {
	panic("claude: taskArgs not yet implemented (plan task 5)")
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test -race ./internal/harness/... `
Expected: PASS (the panic stubs are never reached by these tests).

- [ ] **Step 5: Commit**

```bash
git add internal/harness/claude/
git commit -m "feat(harness): claude adapter skeleton with session args"
```

---

### Task 3: process kind — characterize, implement, rewire `buildProcessArgs`

**Files:**
- Create: `internal/cli/process_args_test.go`
- Modify: `internal/harness/claude/args.go` (replace `processArgs` stub)
- Create: `internal/harness/claude/args_shared.go` (shared flag helpers)
- Modify: `internal/cli/service.go:304-383` (`buildProcessArgs` body)

**Interfaces:**
- Consumes: Task 1 types, Task 2 `claude.Options`/`Claude{}`.
- Produces: working `processArgs`; shared helpers `appendChannelFlags(args, channels, devChannels []string) []string`, `appendPermissionFlags(args []string, o Options) []string`, `appendToolFlags(args []string, o Options) []string` (Tasks 4–5 reuse); `buildProcessArgs` keeps its exact signature `func(cfg *config.Config, name string, proc config.ProcessConfig) []string`.

- [ ] **Step 1: Write the characterization test (against CURRENT code)**

`internal/cli/process_args_test.go`:

```go
package cli

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

// Characterization tests: lock buildProcessArgs's argv byte-for-byte across
// the harness refactor. Web is disabled in every case so leomcp.AppendArg
// is a no-op and MergeSystemPrompt passes through (no state-dir writes).
func TestBuildProcessArgsCharacterization(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name string
		cfg  *config.Config
		proc config.ProcessConfig
		want []string
	}{
		{
			name: "minimal defaults",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{"--model", "opus", "--add-dir", "/tmp/ws"},
		},
		{
			name: "kitchen sink",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:           "opus",
					AllowedTools:    []string{"Read", "Bash"},
					DisallowedTools: []string{"WebFetch"},
				},
			},
			proc: config.ProcessConfig{
				Workspace:          "/tmp/ws",
				Channels:           []string{"plugin:telegram@claude-plugins-official"},
				DevChannels:        []string{"plugin:dev@local"},
				AddDirs:            []string{"/tmp/extra"},
				RemoteControl:      boolPtr(true),
				PermissionMode:     "acceptEdits",
				Agent:              "rocket",
				AppendSystemPrompt: "be terse",
			},
			want: []string{
				"--model", "opus",
				"--channels", "plugin:telegram@claude-plugins-official",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/tmp/extra",
				"--remote-control", "--remote-control-session-name-prefix", "myproc",
				"--permission-mode", "acceptEdits",
				"--agent", "rocket",
				"--allowed-tools", "Read,Bash",
				"--disallowed-tools", "WebFetch",
				"--append-system-prompt", "be terse",
			},
		},
		{
			name: "bypass permissions legacy fallback",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "sonnet", BypassPermissions: true},
			},
			proc: config.ProcessConfig{Workspace: "/tmp/ws"},
			want: []string{
				"--model", "sonnet",
				"--add-dir", "/tmp/ws",
				"--dangerously-skip-permissions",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildProcessArgs(tt.cfg, "myproc", tt.proc)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildProcessArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run against CURRENT code — it must PASS**

Run: `go test -race -run TestBuildProcessArgsCharacterization ./internal/cli/`
Expected: PASS. Per the characterization protocol, if any case fails, diff the actual output against the current `buildProcessArgs` source (internal/cli/service.go:304-383), fix the test's `want`, and note why. Watch for: `cfg.ProcessRemoteControl(proc)` may default on/off from `Defaults.RemoteControl` — the minimal case assumes defaults produce no `--remote-control`; if the accessor defaults true, add `Defaults`/`proc` fields to pin it off and keep the case minimal.

- [ ] **Step 3: Implement `processArgs` and shared helpers in the adapter**

Create `internal/harness/claude/args_shared.go`:

```go
package claude

import "strings"

func appendChannelFlags(args []string, channels, devChannels []string) []string {
	for _, ch := range channels {
		args = append(args, "--channels", ch)
	}
	for _, ch := range devChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	return args
}

func appendPermissionFlags(args []string, o Options) []string {
	if o.PermissionMode != "" {
		return append(args, "--permission-mode", o.PermissionMode)
	}
	if o.BypassPermissions {
		return append(args, "--dangerously-skip-permissions")
	}
	return args
}

func appendToolFlags(args []string, o Options) []string {
	if len(o.AllowedTools) > 0 {
		args = append(args, "--allowed-tools", strings.Join(o.AllowedTools, ","))
	}
	if len(o.DisallowedTools) > 0 {
		args = append(args, "--disallowed-tools", strings.Join(o.DisallowedTools, ","))
	}
	return args
}
```

Replace the `processArgs` stub in `internal/harness/claude/args.go`:

```go
// processArgs reproduces internal/cli.buildProcessArgs flag order exactly.
func processArgs(spec harness.LaunchSpec, o Options) []string {
	var args []string
	args = append(args, "--model", spec.Model)
	args = appendChannelFlags(args, spec.Channels, spec.DevChannels)
	args = append(args, "--add-dir", spec.Workspace)
	for _, dir := range spec.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if o.RemoteControl {
		args = append(args, "--remote-control")
		if o.RemoteControlPrefix != "" {
			args = append(args, "--remote-control-session-name-prefix", o.RemoteControlPrefix)
		}
	}
	args = appendPermissionFlags(args, o)
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	args = append(args, o.LeoMCPArgs...)
	if o.AgentFile != "" {
		args = append(args, "--agent", o.AgentFile)
	}
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return args
}
```

- [ ] **Step 4: Rewire `buildProcessArgs` to build a spec and call the adapter**

Replace the body of `buildProcessArgs` in `internal/cli/service.go` (keep the signature). Add imports `"log"`, `harness "github.com/blackpaw-studio/leo/internal/harness"`, `claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"`:

```go
// buildProcessArgs builds claude CLI args for a named process by resolving
// the config cascade into a harness.LaunchSpec.
func buildProcessArgs(cfg *config.Config, name string, proc config.ProcessConfig) []string {
	mcpConfig := ""
	if p := cfg.ProcessMCPConfigPath(proc); config.HasMCPServers(p) {
		mcpConfig = p
	}
	spec := harness.LaunchSpec{
		Kind:        harness.KindProcess,
		Name:        name,
		Model:       cfg.ProcessModel(proc),
		Workspace:   cfg.ProcessWorkspace(proc),
		AddDirs:     proc.AddDirs,
		Channels:    proc.Channels,
		DevChannels: proc.DevChannels,
		Options: claudeharness.Options{
			PermissionMode:      harness.FallbackString(proc.PermissionMode, cfg.Defaults.PermissionMode),
			BypassPermissions:   cfg.ProcessBypassPermissions(proc),
			RemoteControl:       cfg.ProcessRemoteControl(proc),
			RemoteControlPrefix: name,
			AgentFile:           proc.Agent,
			AllowedTools:        harness.FallbackSlice(proc.AllowedTools, cfg.Defaults.AllowedTools),
			DisallowedTools:     harness.FallbackSlice(proc.DisallowedTools, cfg.Defaults.DisallowedTools),
			AppendSystemPrompt:  leomcp.MergeSystemPrompt(cfg, harness.FallbackString(proc.AppendSystemPrompt, cfg.Defaults.AppendSystemPrompt)),
			MCPConfigPath:       mcpConfig,
			LeoMCPArgs:          leomcp.AppendArg(nil, cfg),
		},
	}
	args, err := claudeharness.Claude{}.Args(spec)
	if err != nil {
		// Unreachable with a well-formed spec; log loudly rather than
		// silently launching claude with no args.
		log.Printf("[%s] building claude args: %v", name, err)
		return nil
	}
	return args
}
```

Note the subtle preserved behavior: the old code emitted `--dangerously-skip-permissions` only when `permMode == ""` AND `cfg.ProcessBypassPermissions(proc)` — `appendPermissionFlags` reproduces exactly that precedence.

- [ ] **Step 5: Run the characterization test and the full cli + harness packages**

Run: `go test -race ./internal/cli/ ./internal/harness/...`
Expected: PASS, including all pre-existing cli tests.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/claude/ internal/cli/
git commit -m "refactor(harness): route process args through claude adapter"
```

---

### Task 4: agent kind — characterize, implement, rewire `BuildTemplateArgs`

**Files:**
- Create or extend: `internal/agent/args_test.go` (extend if it exists — check first with `ls internal/agent/`)
- Modify: `internal/harness/claude/args.go` (replace `agentArgs` stub)
- Modify: `internal/agent/args.go` (rewrite `BuildTemplateArgs` body; keep signature)

**Interfaces:**
- Consumes: Task 1–3 (types, Options, shared flag helpers).
- Produces: working `agentArgs`; `BuildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace, prompt string) []string` unchanged signature.

- [ ] **Step 1: Write the characterization test (against CURRENT code)**

Append to (or create) `internal/agent/args_test.go`:

```go
package agent

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildTemplateArgsCharacterization(t *testing.T) {
	boolPtr := func(b bool) *bool { return &b }

	tests := []struct {
		name   string
		cfg    *config.Config
		tmpl   config.TemplateConfig
		prompt string
		want   []string
	}{
		{
			name: "minimal — remote control defaults on, max turns default",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			tmpl: config.TemplateConfig{},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--remote-control",
				"--name", "myagent",
				"--max-turns", "15",
			},
		},
		{
			name: "full template with opening prompt",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus", PermissionMode: "acceptEdits"},
			},
			tmpl: config.TemplateConfig{
				Model:              "sonnet",
				Channels:           []string{"plugin:telegram@claude-plugins-official"},
				AddDirs:            []string{"/tmp/extra"},
				RemoteControl:      boolPtr(false),
				Agent:              "rocket",
				AllowedTools:       []string{"Read"},
				DisallowedTools:    []string{"WebFetch"},
				AppendSystemPrompt: "be terse",
				MaxTurns:           50,
			},
			prompt: "hello world",
			want: []string{
				"--model", "sonnet",
				"--channels", "plugin:telegram@claude-plugins-official",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/tmp/extra",
				"--name", "myagent",
				"--permission-mode", "acceptEdits",
				"--agent", "rocket",
				"--allowed-tools", "Read",
				"--disallowed-tools", "WebFetch",
				"--append-system-prompt", "be terse",
				"--max-turns", "50",
				"hello world",
			},
		},
		{
			name: "unsafe add_dir dropped",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			tmpl: config.TemplateConfig{
				AddDirs: []string{"/ok/dir", "/bad;dir"},
			},
			want: []string{
				"--model", "opus",
				"--add-dir", "/tmp/ws",
				"--add-dir", "/ok/dir",
				"--remote-control",
				"--name", "myagent",
				"--max-turns", "15",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BuildTemplateArgs(tt.cfg, tt.tmpl, "myagent", "/tmp/ws", tt.prompt)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("BuildTemplateArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run against CURRENT code — it must PASS**

Run: `go test -race -run TestBuildTemplateArgsCharacterization ./internal/agent/`
Expected: PASS. Same protocol as Task 3 if it doesn't (compare against internal/agent/args.go and fix the `want`). The "unsafe add_dir dropped" case relies on `config.ValidateAddDir` rejecting `;` (`addDirRejectedChars` in config.go:37).

- [ ] **Step 3: Implement `agentArgs` in the adapter**

Replace the `agentArgs` stub in `internal/harness/claude/args.go`. Order differences vs process kind are deliberate — `--name` after remote-control, `--agent` before tool flags, leo MCP args at the END (after append-system-prompt), then `--max-turns`, then the positional prompt:

```go
// agentArgs reproduces internal/agent.BuildTemplateArgs flag order exactly.
// Note: templates have no bypass-permissions fallback — callers must leave
// Options.BypassPermissions false for KindAgent.
func agentArgs(spec harness.LaunchSpec, o Options) []string {
	var args []string
	args = append(args, "--model", spec.Model)
	args = appendChannelFlags(args, spec.Channels, spec.DevChannels)
	args = append(args, "--add-dir", spec.Workspace)
	for _, dir := range spec.AddDirs {
		args = append(args, "--add-dir", dir)
	}
	if o.RemoteControl {
		args = append(args, "--remote-control")
	}
	args = append(args, "--name", spec.Name)
	args = appendPermissionFlags(args, o)
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	if o.AgentFile != "" {
		args = append(args, "--agent", o.AgentFile)
	}
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	args = append(args, o.LeoMCPArgs...)
	if spec.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(spec.MaxTurns))
	}
	if spec.Prompt != "" {
		args = append(args, spec.Prompt)
	}
	return args
}
```

Add `"strconv"` to `args.go` imports.

- [ ] **Step 4: Rewire `BuildTemplateArgs`**

Rewrite the body in `internal/agent/args.go` (keep the doc comment about positional-prompt semantics and the defense-in-depth add_dirs note; keep the signature):

```go
func BuildTemplateArgs(cfg *config.Config, tmpl config.TemplateConfig, agentName, workspace, prompt string) []string {
	// Defense in depth: Config.Validate() also rejects these, but skip
	// anything unsafe here in case spawn-time receives an unvalidated
	// config. Log noisily so the silent drop isn't invisible.
	safeDirs := make([]string, 0, len(tmpl.AddDirs))
	for _, dir := range tmpl.AddDirs {
		if err := config.ValidateAddDir(dir); err != nil {
			log.Printf("[agent:%s] skipping unsafe add_dirs entry %q: %v", agentName, dir, err)
			continue
		}
		safeDirs = append(safeDirs, dir)
	}

	rc := true
	if tmpl.RemoteControl != nil {
		rc = *tmpl.RemoteControl
	}

	mcpConfig := ""
	if tmpl.MCPConfig != "" {
		p := tmpl.MCPConfig
		if !filepath.IsAbs(p) {
			p = filepath.Join(workspace, p)
		}
		if config.HasMCPServers(p) {
			mcpConfig = p
		}
	}

	maxTurns := tmpl.MaxTurns
	if maxTurns == 0 {
		maxTurns = cfg.Defaults.MaxTurns
	}
	if maxTurns == 0 {
		maxTurns = config.DefaultMaxTurns
	}

	spec := harness.LaunchSpec{
		Kind:        harness.KindAgent,
		Name:        agentName,
		Model:       cfg.TemplateModel(tmpl),
		MaxTurns:    maxTurns,
		Workspace:   workspace,
		AddDirs:     safeDirs,
		Channels:    tmpl.Channels,
		DevChannels: tmpl.DevChannels,
		Prompt:      prompt,
		Options: claudeharness.Options{
			PermissionMode:     harness.FallbackString(tmpl.PermissionMode, cfg.Defaults.PermissionMode),
			RemoteControl:      rc,
			AgentFile:          tmpl.Agent,
			AllowedTools:       harness.FallbackSlice(tmpl.AllowedTools, cfg.Defaults.AllowedTools),
			DisallowedTools:    harness.FallbackSlice(tmpl.DisallowedTools, cfg.Defaults.DisallowedTools),
			AppendSystemPrompt: leomcp.MergeSystemPrompt(cfg, harness.FallbackString(tmpl.AppendSystemPrompt, cfg.Defaults.AppendSystemPrompt)),
			MCPConfigPath:      mcpConfig,
			LeoMCPArgs:         leomcp.AppendArg(nil, cfg),
		},
	}
	args, err := claudeharness.Claude{}.Args(spec)
	if err != nil {
		log.Printf("[agent:%s] building claude args: %v", agentName, err)
		return nil
	}
	return args
}
```

Update imports: drop `"strconv"`/`"strings"` if now unused, add `harness "github.com/blackpaw-studio/leo/internal/harness"` and `claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"`.

- [ ] **Step 5: Run agent package tests (characterization + pre-existing)**

Run: `go test -race ./internal/agent/ ./internal/harness/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/claude/ internal/agent/
git commit -m "refactor(harness): route template args through claude adapter"
```

---

### Task 5: task kind — characterize, implement, rewire `buildArgs`

**Files:**
- Create or extend: `internal/run/args_test.go` (check for an existing test file covering buildArgs first: `grep -rn "buildArgs" internal/run/*_test.go`)
- Modify: `internal/harness/claude/args.go` (replace `taskArgs` stub)
- Modify: `internal/run/runner.go:910-984` (rewrite `buildArgs` body; keep signature)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces: working `taskArgs`; `buildArgs(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID string, leoMCPOK bool) []string` unchanged signature.

- [ ] **Step 1: Write the characterization test (against CURRENT code)**

`internal/run/args_test.go` (extend existing file if `buildArgs` tests exist — keep theirs, add these):

```go
package run

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestBuildArgsCharacterization(t *testing.T) {
	tests := []struct {
		name      string
		cfg       *config.Config
		task      config.TaskConfig
		prompt    string
		sessionID string
		leoMCPOK  bool
		want      []string
	}{
		{
			name: "minimal",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus"},
			},
			task:   config.TaskConfig{Workspace: "/tmp/ws"},
			prompt: "do the thing",
			want: []string{
				"-p", "do the thing",
				"--model", "opus",
				"--max-turns", "15",
				"--output-format", "stream-json",
				"--verbose",
				"--add-dir", "/tmp/ws",
			},
		},
		{
			name: "resume with dev channels, tools, bypass fallback",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{
					Model:             "opus",
					BypassPermissions: true,
					AllowedTools:      []string{"Read", "Bash"},
				},
			},
			task: config.TaskConfig{
				Workspace:          "/tmp/ws",
				Model:              "sonnet",
				MaxTurns:           30,
				DevChannels:        []string{"plugin:dev@local"},
				AppendSystemPrompt: "be terse",
			},
			prompt:    "nightly run",
			sessionID: "sess-789",
			want: []string{
				"-p", "nightly run",
				"--model", "sonnet",
				"--max-turns", "30",
				"--output-format", "stream-json",
				"--verbose",
				"--dangerously-load-development-channels", "plugin:dev@local",
				"--resume", "sess-789",
				"--dangerously-skip-permissions",
				"--add-dir", "/tmp/ws",
				"--allowed-tools", "Read,Bash",
				"--append-system-prompt", "be terse",
			},
		},
		{
			name: "task permission mode wins over bypass",
			cfg: &config.Config{
				HomePath: "/tmp/leo-home",
				Defaults: config.DefaultsConfig{Model: "opus", BypassPermissions: true},
			},
			task: config.TaskConfig{
				Workspace:      "/tmp/ws",
				PermissionMode: "plan",
			},
			prompt: "plan it",
			want: []string{
				"-p", "plan it",
				"--model", "opus",
				"--max-turns", "15",
				"--output-format", "stream-json",
				"--verbose",
				"--permission-mode", "plan",
				"--add-dir", "/tmp/ws",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.cfg, tt.task, "mytask", tt.prompt, tt.sessionID, tt.leoMCPOK)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildArgs argv mismatch\n got: %q\nwant: %q", got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run against CURRENT code — it must PASS**

Run: `go test -race -run TestBuildArgsCharacterization ./internal/run/`
Expected: PASS (fix `want` per the protocol otherwise; the reference is internal/run/runner.go:910-984).

- [ ] **Step 3: Implement `taskArgs` in the adapter**

Replace the `taskArgs` stub in `internal/harness/claude/args.go`. Task-kind quirks preserved exactly: `-p` prompt leads, `--output-format stream-json --verbose` always, **no `--channels`** (regular channels ride the LEO_CHANNELS env var for tasks), session resume mid-list, workspace `--add-dir` AFTER the MCP flags:

```go
// taskArgs reproduces internal/run.buildArgs flag order exactly.
func taskArgs(spec harness.LaunchSpec, o Options) []string {
	args := []string{
		"-p", spec.Prompt,
		"--model", spec.Model,
		"--max-turns", strconv.Itoa(spec.MaxTurns),
		"--output-format", "stream-json",
		"--verbose",
	}
	for _, ch := range spec.DevChannels {
		args = append(args, "--dangerously-load-development-channels", ch)
	}
	args = append(args, Claude{}.SessionArgs(spec.Session)...)
	args = appendPermissionFlags(args, o)
	if o.MCPConfigPath != "" {
		args = append(args, "--mcp-config", o.MCPConfigPath)
	}
	args = append(args, o.LeoMCPArgs...)
	args = append(args, "--add-dir", spec.Workspace)
	args = appendToolFlags(args, o)
	if o.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", o.AppendSystemPrompt)
	}
	return args
}
```

- [ ] **Step 4: Rewire `buildArgs`**

Rewrite the body in `internal/run/runner.go` (keep the signature and the long leoMCPOK doc comment above the LeoMCPArgs line, condensed):

```go
func buildArgs(cfg *config.Config, task config.TaskConfig, taskName, prompt, sessionID string, leoMCPOK bool) []string {
	mcpConfig := ""
	if p := cfg.TaskMCPConfigPath(task); config.HasMCPServers(p) {
		mcpConfig = p
	}
	// leoMCPOK is the leoMCPEnv gate (web disabled, or no readable API
	// token), evaluated once by the caller (Run/Preview) rather than here —
	// see the pre-refactor comment history for why.
	var leoMCPArgs []string
	if leoMCPOK {
		leoMCPArgs = leomcp.AppendArg(nil, cfg)
	}
	session := harness.SessionState{}
	if sessionID != "" {
		session = harness.SessionState{Mode: harness.SessionResume, ID: sessionID}
	}
	spec := harness.LaunchSpec{
		Kind:        harness.KindTask,
		Name:        taskName,
		Model:       cfg.TaskModel(task),
		MaxTurns:    cfg.TaskMaxTurns(task),
		Workspace:   cfg.TaskWorkspace(task),
		DevChannels: task.DevChannels,
		Prompt:      prompt,
		Session:     session,
		Options: claudeharness.Options{
			PermissionMode:     harness.FallbackString(task.PermissionMode, cfg.Defaults.PermissionMode),
			BypassPermissions:  cfg.Defaults.BypassPermissions,
			AllowedTools:       harness.FallbackSlice(task.AllowedTools, cfg.Defaults.AllowedTools),
			DisallowedTools:    harness.FallbackSlice(task.DisallowedTools, cfg.Defaults.DisallowedTools),
			AppendSystemPrompt: leomcp.MergeSystemPrompt(cfg, harness.FallbackString(task.AppendSystemPrompt, cfg.Defaults.AppendSystemPrompt)),
			MCPConfigPath:      mcpConfig,
			LeoMCPArgs:         leoMCPArgs,
		},
	}
	args, err := claudeharness.Claude{}.Args(spec)
	if err != nil {
		log.Printf("[task:%s] building claude args: %v", taskName, err)
		return nil
	}
	return args
}
```

Add imports to runner.go: `harness "github.com/blackpaw-studio/leo/internal/harness"`, `claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"` (and `"log"` if not present). If `appendDevChannelFlags` (runner.go:989) is now only used by `notifyFailure`, leave it — it still has that caller.

- [ ] **Step 5: Run run-package tests (characterization + pre-existing)**

Run: `go test -race ./internal/run/ ./internal/harness/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/harness/claude/ internal/run/
git commit -m "refactor(harness): route task args through claude adapter"
```

---

### Task 6: session state + binary seam at exec points

**Files:**
- Modify: `internal/cli/service.go` (`resolveSessionArgs` → `resolveSessionState`; both call sites; `exec.LookPath("claude")` ×2 and `syscall.Exec` argv0)
- Modify: `internal/run/runner.go:520` (`execCommand("claude", ...)`)
- Test: extend `internal/cli/process_args_test.go`

**Interfaces:**
- Consumes: `harness.SessionState`, `claude.Claude{}.SessionArgs`, `claude.Claude{}.Binary()`.
- Produces: `resolveSessionState(store *session.Store, sessionKey, workspace string, maxAge time.Duration, logPrefix string) harness.SessionState` — same resolution ladder as today's `resolveSessionArgs`, returning state instead of argv.

- [ ] **Step 1: Write the failing test for `resolveSessionState`**

Append to `internal/cli/process_args_test.go`:

```go
func TestResolveSessionStateFreshPinsNewID(t *testing.T) {
	// Empty store + no claude project dir for the workspace → a fresh
	// pinned session ID is minted and persisted.
	home := t.TempDir()
	store := session.NewStore(home)

	st := resolveSessionState(store, "process:x", filepath.Join(home, "no-such-ws"), 0, "")
	if st.Mode != harness.SessionPinned {
		t.Fatalf("Mode = %q, want pinned", st.Mode)
	}
	if st.ID == "" {
		t.Fatal("expected a minted session ID")
	}
	storedID, _, err := store.Get("process:x")
	if err != nil || storedID != st.ID {
		t.Fatalf("store.Get = %q, %v; want %q", storedID, err, st.ID)
	}
}

func TestResolveSessionStateStoredIDResumes(t *testing.T) {
	home := t.TempDir()
	store := session.NewStore(home)
	if err := store.Set("process:x", "stored-id"); err != nil {
		t.Fatal(err)
	}
	st := resolveSessionState(store, "process:x", filepath.Join(home, "no-such-ws"), 0, "")
	if st.Mode != harness.SessionResume || st.ID != "stored-id" {
		t.Fatalf("state = %+v, want resume/stored-id", st)
	}
}
```

Add imports as needed (`"path/filepath"`, `harness "github.com/blackpaw-studio/leo/internal/harness"`, `"github.com/blackpaw-studio/leo/internal/session"`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test -race -run TestResolveSessionState ./internal/cli/`
Expected: FAIL — `resolveSessionState` undefined.

- [ ] **Step 3: Refactor `resolveSessionArgs` into `resolveSessionState`**

In `internal/cli/service.go`, change only the return construction of the existing function (internal/cli/service.go:256-284) — the doc comment, the resolution ladder, and all warnings stay:

```go
func resolveSessionState(store *session.Store, sessionKey, workspace string, maxAge time.Duration, logPrefix string) harness.SessionState {
	storedID, _, getErr := store.Get(sessionKey)
	if getErr != nil {
		warn.Printf("  %sCould not read session store: %v\n", logPrefix, getErr)
	}

	latestID, _, latestErr := session.LatestSession(workspace, maxAge)
	if latestErr != nil {
		warn.Printf("  %sCould not inspect claude project directory: %v\n", logPrefix, latestErr)
	}

	switch {
	case latestID != "":
		if latestID != storedID {
			if err := store.Set(sessionKey, latestID); err != nil {
				warn.Printf("  %sCould not update session ID: %v\n", logPrefix, err)
			}
		}
		return harness.SessionState{Mode: harness.SessionResume, ID: latestID}
	case storedID != "":
		return harness.SessionState{Mode: harness.SessionResume, ID: storedID}
	default:
		sid := session.NewID()
		if err := store.Set(sessionKey, sid); err != nil {
			warn.Printf("  %sCould not store session ID: %v\n", logPrefix, err)
		}
		return harness.SessionState{Mode: harness.SessionPinned, ID: sid}
	}
}
```

Update both call sites. Foreground path (service.go:125-127) becomes:

```go
	claudeArgs = append(claudeArgs,
		claudeharness.Claude{}.SessionArgs(
			resolveSessionState(store, sessionKey, cfg.ProcessWorkspace(proc), cfg.ProcessStaleResume(proc), ""),
		)...,
	)
```

The second call site lives in `buildAllProcessSpecs` (near internal/cli/service.go:218; find with `grep -n resolveSessionArgs internal/cli/`): apply the identical transformation — wrap the `resolveSessionState(...)` result in `claudeharness.Claude{}.SessionArgs(...)` where the old `resolveSessionArgs(...)` argv was used. No other logic in that function changes.

- [ ] **Step 4: Route the binary name through the adapter**

Mechanical swaps, no behavior change (`claudeharness.Claude{}.Binary()` returns `"claude"`):

- `internal/cli/service.go:70` and `:129`: `exec.LookPath("claude")` → `exec.LookPath(claudeharness.Claude{}.Binary())`
- `internal/cli/service.go:141`: `syscall.Exec(claudePath, append([]string{"claude"}, claudeArgs...), procEnv)` → `syscall.Exec(claudePath, append([]string{claudeharness.Claude{}.Binary()}, claudeArgs...), procEnv)`
- `internal/run/runner.go:520`: `cmd := execCommand("claude", args...)` → hoist a package-visible reference: at the top of the file near `var execCommand`, add `var claudeBinary = claudeharness.Claude{}.Binary()`, then `cmd := execCommand(claudeBinary, args...)`

(`internal/service/process.go`'s `buildClaudeShellCmd` already takes `claudePath` as a parameter threaded from `runService`'s LookPath — no change needed there.)

- [ ] **Step 5: Run tests**

Run: `go test -race ./internal/cli/ ./internal/run/ ./internal/harness/...`
Expected: PASS, including both new session-state tests and all pre-existing tests.

- [ ] **Step 6: Commit**

```bash
git add internal/cli/ internal/run/
git commit -m "refactor(harness): session state and binary name via claude adapter"
```

---

### Task 7: full verification

**Files:** none new.

- [ ] **Step 1: Full test suite with race + cover**

Run: `make test`
Expected: PASS across all packages, coverage not lower than before the refactor (the new harness packages are fully covered by their own tests).

- [ ] **Step 2: Lint**

Run: `make lint`
Expected: clean (go vet + staticcheck). Fix any unused-import fallout from the rewires (`strconv`/`strings` in internal/agent/args.go are the likely candidates).

- [ ] **Step 3: Build**

Run: `make build`
Expected: `bin/leo` builds.

- [ ] **Step 4: Behavioral spot-check (no daemon restart!)**

Run: `bin/leo run --help` and, if a preview mode exists (`grep -n "Preview" internal/cli/run.go`), preview a configured task's assembled command to eyeball the argv. Do **NOT** restart any leo service or daemon — standing rule, and this refactor doesn't require it to verify.

- [ ] **Step 5: Commit any fixups**

```bash
git add -A && git commit -m "refactor(harness): plan 1 verification fixups" --allow-empty
```

---

## Self-Review Notes (done at plan-writing time)

- **Spec coverage:** This plan covers only the spec's "LaunchSpec consolidation" + adapter-registry foundation, per the agreed Plan 1/5 decomposition. Config changes, providers removal, codex/opencode, drivers, and web UI are explicitly Plans 2–5.
- **Type consistency:** `claude.Options`, `harness.LaunchSpec`, `resolveSessionState`, and the three rewired builders use consistent names across Tasks 2–6.
- **Known soft spots called out inline:** `cfg.ProcessRemoteControl` default polarity (Task 3 Step 2), possible pre-existing test files (Tasks 4–5 Step 1), second `resolveSessionArgs` call site located by grep (Task 6 Step 3). Each has an explicit resolution protocol rather than a guess.
