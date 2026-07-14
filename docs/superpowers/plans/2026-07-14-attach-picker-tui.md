# Attach Picker TUI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the bare `promptui.Select` shown by `leo attach` (no name) with a stay-open Bubble Tea picker that lists all agents (local + every configured remote host) with fuzzy search and in-place lifecycle actions (rename, stop, suspend, resume), while attach remains the only action that exits the TUI.

**Architecture:** A new decoupled `internal/picker` package holds a pure Bubble Tea model driven by an injected `Backend` interface (one backend per host: a local backend wrapping the daemon client, and an SSH backend per `client.hosts` entry). `internal/cli` builds the backends, runs the picker, and — after the TUI exits — routes the returned agent through the existing attach path so tmux gets a clean terminal.

**Tech Stack:** Go 1.25, cobra CLI, `github.com/charmbracelet/bubbletea` v1, `github.com/charmbracelet/bubbles` (list, textinput, help, key), `github.com/charmbracelet/lipgloss` v1. `attach_picker.go` is promptui's only importer in the repo, so Task 7 removes the promptui dependency entirely (`go mod tidy`).

## Global Constraints

- Module path is `github.com/blackpaw-studio/leo`. go.mod Go directive is `go 1.25.1` — no toolchain bump needed for bubbletea v1.
- Tests run with `go test -race`. macOS CI has **no tmux and no ssh** — nothing in unit tests may shell out to tmux or ssh. Use package-level function-value seams (the established pattern, e.g. `agentExecCommand`, `agentListFn`).
- `go test ./...` skips `e2e/` (guarded by `//go:build e2e`). E2E changes must be verified with `make e2e`.
- Coding style: many small files (<400 lines), immutable updates (Bubble Tea's value-copy `Update` idiom — never mutate shared state in place; build new maps/slices), early returns, named constants for magic numbers, errors handled and wrapped with `%w`.
- The bubbles `list` component owns `/` filtering; custom keybindings must be inactive while the list is actively filtering (`list.Model.SettingFilter()` is true).
- Attach must happen **after** the tea program exits (tmux needs the terminal). `picker.Run` returns the selection; the CLI layer attaches.
- Commit messages: `<type>: <description>` (feat/fix/refactor/docs/test/chore).
- The picker package must **not** import `internal/cli` (cli imports picker — reversing it is an import cycle). Shell-quoting and SSH-argv construction that live in `internal/cli` cannot be imported; the picker replicates a one-line quote helper and receives the SSH argv builder as an injected function.
- **Status strings** produced by the daemon (`agent.Record.Status`): `"running"`, `"starting"`, `"restarting"`, `"suspended"`, `"stopped"`.
- **Glyphs:** `●` running, `⟳` starting/restarting, `◌` suspended, `✖` stopped.
- **Per-host fetch timeout:** 5s.

---

### Task 1: Unit-test `leo agent list --json` via the existing seam + add e2e coverage

**Context — pre-existing implementation:** `newAgentListCmd` (`internal/cli/agent.go:130-176`) **already** has the `--json` flag and marshals `[]agent.Record` to `agentStdout` with `json.NewEncoder(...).SetIndent("", "  ")`, and already forwards `--json` to the remote leo over SSH. The remaining work is (a) route the local list through the package-level `agentListFn` seam (defined in `attach_picker.go:21`) so the JSON output is unit-testable without a live daemon, and (b) add an e2e regression test behind the `make e2e` tag.

**Files:**
- Modify: `internal/cli/agent.go` (the `RunE` of `newAgentListCmd`, ~line 149)
- Test (unit): `internal/cli/agent_test.go`
- Test (e2e): `e2e/agent_list_json_test.go` (create)

**Interfaces:**
- Consumes: `agentListFn func(ctx context.Context, homePath string) ([]agent.Record, error)` (already declared in `internal/cli/attach_picker.go`), `agentStdout io.Writer` seam.
- Produces: `leo agent list --json` prints a JSON array of `agent.Record` to stdout (indented two spaces).

- [ ] **Step 1: Write the failing unit test**

Add to `internal/cli/agent_test.go`:

```go
func TestAgentListJSONUsesSeam(t *testing.T) {
	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return []agent.Record{
			{Name: "alpha", Template: "writer", Status: "running"},
			{Name: "beta", Status: "suspended"},
		}, nil
	}
	t.Cleanup(func() { agentListFn = oldList })

	var buf bytes.Buffer
	oldOut := agentStdout
	agentStdout = &buf
	t.Cleanup(func() { agentStdout = oldOut })

	cmd := newAgentListCmd()
	cmd.SetArgs([]string{"--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var got []agent.Record
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not a JSON array of records: %v\noutput: %s", err, buf.String())
	}
	if len(got) != 2 || got[0].Name != "alpha" || got[1].Status != "suspended" {
		t.Fatalf("unexpected decoded records: %+v", got)
	}
}
```

Ensure `bytes`, `encoding/json`, `context`, and `github.com/blackpaw-studio/leo/internal/agent` are imported in the test file (add any missing).

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race -run TestAgentListJSONUsesSeam ./internal/cli/`
Expected: FAIL — the command currently calls `daemon.AgentList` directly, so `agentListFn` is never consulted; `Execute()` tries a real socket and errors (or the buffer is empty).

- [ ] **Step 3: Route the local list through the seam**

In `internal/cli/agent.go`, inside `newAgentListCmd`'s `RunE`, replace the direct daemon call:

```go
				records, err := daemon.AgentList(cmd.Context(), cfg.HomePath)
				if err != nil {
					return fmt.Errorf("listing agents: %w", err)
				}
```

with the seam:

```go
				records, err := agentListFn(cmd.Context(), cfg.HomePath)
				if err != nil {
					return fmt.Errorf("listing agents: %w", err)
				}
```

(The `daemon` import is still used elsewhere in this file, so leave the import list unchanged.)

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -race -run TestAgentListJSONUsesSeam ./internal/cli/`
Expected: PASS

- [ ] **Step 5: Write the e2e regression test**

Create `e2e/agent_list_json_test.go`:

```go
//go:build e2e

package e2e

import (
	"encoding/json"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// TestAgentListJSON verifies `leo agent list --json` emits a well-formed JSON
// array against a live daemon. With no agents running it must still print an
// empty JSON array (not "No agents running.") and exit 0.
func TestAgentListJSON(t *testing.T) {
	ws := setupWorkspace(t, minimalConfig, nil)
	env := []string{"LEO_HOME=" + ws}

	stdout, stderr, code := runLeo(t, ws, env, "agent", "list", "--json")
	if code != 0 {
		t.Fatalf("agent list --json exited %d, stderr: %s", code, stderr)
	}

	var records []agent.Record
	if err := json.Unmarshal([]byte(stdout), &records); err != nil {
		t.Fatalf("output is not a JSON array: %v\nstdout: %s", err, stdout)
	}
}
```

- [ ] **Step 6: Run the e2e test to verify it passes**

Run: `go test -tags e2e -race -run TestAgentListJSON ./e2e/`
Expected: PASS (the flag already exists; this is a characterization/regression test).

Note: if `LEO_HOME` is not the env var the e2e harness uses to point at an isolated home, mirror whatever `minimalConfig`-based tests in `e2e/e2e_test.go` already do to select the home/config; the assertion (valid JSON array, exit 0) is what matters.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/agent.go internal/cli/agent_test.go e2e/agent_list_json_test.go
git commit -m "test: cover leo agent list --json via seam + e2e"
```

---

### Task 2: `internal/picker` core types and `Run` entry point

**Files:**
- Create: `internal/picker/picker.go`
- Test: `internal/picker/picker_test.go`

**Interfaces:**
- Produces (relied on by every later task):
  - `type Agent struct { Name, Template, Host, Status string; StartedAt time.Time; AttachOnly bool }`
  - `type Backend interface { List(ctx) ([]Agent, error); Rename(ctx, oldName, newName string) error; Stop(ctx, name string) error; Suspend(ctx, name string) error; Resume(ctx, name string) error }`
  - `type Result struct { Agent *Agent }`
  - `const LocalHost = "local"`
  - (`func Run(ctx context.Context, backends map[string]Backend) (Result, error)` is declared in the package doc below but **implemented in Task 5**, after the model exists — this task must compile and commit green on its own, with no bubbletea import.)

- [ ] **Step 1: Write the failing test**

Create `internal/picker/picker_test.go`:

```go
package picker

import (
	"context"
	"testing"
	"time"
)

func TestAgentZeroValue(t *testing.T) {
	a := Agent{Name: "x", Host: LocalHost, Status: "running", StartedAt: time.Now()}
	if a.AttachOnly {
		t.Fatalf("AttachOnly should default false")
	}
	if LocalHost != "local" {
		t.Fatalf("LocalHost = %q, want local", LocalHost)
	}
}

// staticBackend is a trivial Backend used to prove Run wires up and returns
// without a selection when the caller-cancelled context tears the program down.
type staticBackend struct{ agents []Agent }

func (s staticBackend) List(context.Context) ([]Agent, error) { return s.agents, nil }
func (staticBackend) Rename(context.Context, string, string) error { return nil }
func (staticBackend) Stop(context.Context, string) error           { return nil }
func (staticBackend) Suspend(context.Context, string) error        { return nil }
func (staticBackend) Resume(context.Context, string) error         { return nil }

func TestBackendInterfaceSatisfied(t *testing.T) {
	var _ Backend = staticBackend{}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/picker/`
Expected: FAIL — package `picker` does not exist yet (`Agent`, `Backend`, `LocalHost` undefined).

- [ ] **Step 3: Implement the core types**

Create `internal/picker/picker.go`:

```go
// Package picker renders a full-screen Bubble Tea picker over all leo agents
// (local and remote), with fuzzy search and in-place lifecycle actions. It is
// decoupled from the daemon and SSH transport via the Backend interface so the
// tea model can be unit-tested against fakes with no I/O.
package picker

import (
	"context"
	"time"
)

// LocalHost is the reserved backend key (and Agent.Host value) for agents
// served by the local daemon.
const LocalHost = "local"

// Agent is one row's worth of agent metadata, harness-agnostic.
type Agent struct {
	Name       string
	Template   string
	Host       string
	Status     string
	StartedAt  time.Time
	AttachOnly bool // remote tmux-fallback rows: attach works, lifecycle does not
}

// Backend is one host's control surface. "local" wraps the daemon client; each
// configured client.hosts entry is an SSH backend. Names passed to the action
// methods are canonical agent names as returned by List.
type Backend interface {
	List(ctx context.Context) ([]Agent, error)
	Rename(ctx context.Context, oldName, newName string) error
	Stop(ctx context.Context, name string) error
	Suspend(ctx context.Context, name string) error
	Resume(ctx context.Context, name string) error
}

// Result carries the picker outcome. Agent is nil when the user quit without
// choosing anything.
type Result struct {
	Agent *Agent
}
```

`Run` is intentionally **not** in this file yet — it needs `newModel`/`model` from
Task 5 and the bubbletea import from Task 3. It is added to this same file in
Task 5, keeping every task's commit compiling green on its own.

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test -race ./internal/picker/`
Expected: PASS (this file has no external deps; the whole package compiles standalone).

- [ ] **Step 5: Commit**

```bash
git add internal/picker/picker.go internal/picker/picker_test.go
git commit -m "feat: add picker core types (Agent, Backend, Result)"
```

---

### Task 3: Add charmbracelet dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: importable `github.com/charmbracelet/bubbletea`, `github.com/charmbracelet/bubbles/{list,textinput,help,key}`, `github.com/charmbracelet/lipgloss`.

- [ ] **Step 1: Fetch the dependencies**

```bash
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
go get github.com/charmbracelet/lipgloss@latest
```

Expected: `bubbletea` v1.x, `bubbles` v0.21+, `lipgloss` v1.x added to go.mod `require`.

- [ ] **Step 2: Verify versions are v1 (bubbletea/lipgloss) and v0.21+ (bubbles)**

Run: `go list -m github.com/charmbracelet/bubbletea github.com/charmbracelet/bubbles github.com/charmbracelet/lipgloss`
Expected: `bubbletea v1.x.y`, `bubbles v0.2x.y` (>= 0.21), `lipgloss v1.x.y`. If `go get @latest` resolved a v2 for bubbletea or lipgloss, pin explicitly instead:

```bash
go get github.com/charmbracelet/bubbletea@v1.3.10
go get github.com/charmbracelet/lipgloss@v1.1.0
go get github.com/charmbracelet/bubbles@v0.21.0
```

(Adjust patch versions to the latest available v1.x/v0.21+.)

- [ ] **Step 3: Tidy and confirm the module builds**

Run: `go mod tidy && go build ./...`
Expected: no errors; the new modules move from implicit to explicit `require` blocks.

- [ ] **Step 4: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add bubbletea, bubbles, lipgloss dependencies"
```

---

### Task 4: Rows, styles, keymap — the pure presentation layer

**Files:**
- Create: `internal/picker/rows.go`
- Create: `internal/picker/styles.go`
- Create: `internal/picker/keys.go`
- Test: `internal/picker/rows_test.go`

**Interfaces:**
- Consumes: `Agent`, `LocalHost` (Task 2).
- Produces (used by the model in Task 5):
  - `type row struct { title, desc, filter, host string; ag *Agent }` implementing `list.DefaultItem` (`Title()`, `Description()`, `FilterValue()`).
  - `func glyph(status string) string`
  - `func buildRows(byHost map[string][]Agent, byHostErr map[string]error, pending map[string]struct{}, frame int) []list.Item`
  - `func sortAgents(a []Agent)`
  - `func rowKey(host, name string) string`
  - `func validName(name string) bool`
  - `func humanDuration(d time.Duration) string`
  - `var spinnerFrames []string`, glyph constants
  - `type styles struct { ... }`, `func newStyles() styles`
  - `type keyMap struct { ... }` implementing `help.KeyMap`, `func defaultKeys() keyMap`

- [ ] **Step 1: Write the failing test**

Create `internal/picker/rows_test.go`:

```go
package picker

import (
	"testing"
	"time"
)

func TestGlyphByStatus(t *testing.T) {
	cases := map[string]string{
		"running":    glyphRunning,
		"starting":   glyphStarting,
		"restarting": glyphStarting,
		"suspended":  glyphSuspended,
		"stopped":    glyphStopped,
		"weird":      glyphStopped, // unknown → stopped glyph
	}
	for status, want := range cases {
		if got := glyph(status); got != want {
			t.Errorf("glyph(%q) = %q, want %q", status, got, want)
		}
	}
}

func TestSortAgentsByName(t *testing.T) {
	ags := []Agent{{Name: "zulu"}, {Name: "alpha"}, {Name: "mike"}}
	sortAgents(ags)
	want := []string{"alpha", "mike", "zulu"}
	for i, a := range ags {
		if a.Name != want[i] {
			t.Fatalf("sorted[%d] = %q, want %q", i, a.Name, want[i])
		}
	}
}

func TestBuildRowsGroupsHostsAndErrorRows(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Template: "writer", Host: LocalHost, Status: "running"}},
		"hestia":  {{Name: "rocket", Host: "hestia", Status: "suspended"}},
	}
	byHostErr := map[string]error{"down": errBoom}
	items := buildRows(byHost, byHostErr, map[string]struct{}{}, 0)

	// 2 agent rows + 1 error row.
	if len(items) != 3 {
		t.Fatalf("want 3 rows, got %d", len(items))
	}

	var sawError, sawAlpha bool
	for _, it := range items {
		r := it.(row)
		if r.ag == nil && r.host == "down" {
			sawError = true
			if !contains(r.desc, "boom") {
				t.Errorf("error row desc = %q, want it to mention the error", r.desc)
			}
		}
		if r.ag != nil && r.ag.Name == "alpha" {
			sawAlpha = true
			if r.filter != "alpha writer local" {
				t.Errorf("alpha filter = %q", r.filter)
			}
		}
	}
	if !sawError || !sawAlpha {
		t.Fatalf("missing rows: error=%v alpha=%v", sawError, sawAlpha)
	}
}

func TestBuildRowsSpinnerForPending(t *testing.T) {
	byHost := map[string][]Agent{
		LocalHost: {{Name: "alpha", Host: LocalHost, Status: "running"}},
	}
	pending := map[string]struct{}{rowKey(LocalHost, "alpha"): {}}
	items := buildRows(byHost, nil, pending, 2)
	r := items[0].(row)
	if !hasPrefix(r.title, spinnerFrames[2]) {
		t.Errorf("pending row title = %q, want spinner-prefixed", r.title)
	}
}

func TestValidName(t *testing.T) {
	if validName("") || validName("bad name") || validName(" leading") {
		t.Errorf("expected invalid names to be rejected")
	}
	if !validName("auth-refactor") || !validName("agent_1") {
		t.Errorf("expected slug-like names to be accepted")
	}
}

func TestHumanDuration(t *testing.T) {
	if got := humanDuration(2*24*time.Hour + 4*time.Hour); got != "2d4h" {
		t.Errorf("humanDuration = %q, want 2d4h", got)
	}
	if got := humanDuration(3 * time.Hour); got != "3h" {
		t.Errorf("humanDuration = %q, want 3h", got)
	}
	if got := humanDuration(5 * time.Minute); got != "5m" {
		t.Errorf("humanDuration = %q, want 5m", got)
	}
}

// test helpers
var errBoom = boomError("boom")

type boomError string

func (b boomError) Error() string { return string(b) }

func contains(s, sub string) bool  { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }
func hasPrefix(s, p string) bool   { return len(s) >= len(p) && s[:len(p)] == p }
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/picker/`
Expected: FAIL — `glyph`, `buildRows`, `sortAgents`, `rowKey`, `validName`, `humanDuration`, `spinnerFrames`, glyph constants undefined.

- [ ] **Step 3: Implement `rows.go`**

Create `internal/picker/rows.go`:

```go
package picker

import (
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/charmbracelet/bubbles/list"
)

// Status glyphs.
const (
	glyphRunning   = "●"
	glyphStarting  = "⟳"
	glyphSuspended = "◌"
	glyphStopped   = "✖"
)

// spinnerFrames animates a pending (in-flight action) row.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// nameRe is the client-side validation for a rename target: a leading
// alphanumeric followed by alphanumerics, dashes, or underscores. The daemon
// re-normalizes to a leo- slug; this only guards against empty/whitespace input.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// row is one list item. ag is nil for synthetic error rows (a host whose List
// failed); those cannot be acted on or attached.
type row struct {
	title  string
	desc   string
	filter string
	host   string
	ag     *Agent
}

func (r row) Title() string       { return r.title }
func (r row) Description() string  { return r.desc }
func (r row) FilterValue() string { return r.filter }

// glyph maps a status string to its display glyph. Unknown statuses render as
// stopped so a row is never blank.
func glyph(status string) string {
	switch status {
	case "running":
		return glyphRunning
	case "starting", "restarting":
		return glyphStarting
	case "suspended":
		return glyphSuspended
	default:
		return glyphStopped
	}
}

// rowKey is the stable per-agent key used to track in-flight actions.
func rowKey(host, name string) string { return host + "/" + name }

// validName reports whether a rename target is acceptable client-side.
func validName(name string) bool { return nameRe.MatchString(name) }

// sortAgents orders agents by name in place.
func sortAgents(a []Agent) {
	sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name })
}

// buildRows flattens the per-host agent map into list items, one host group at
// a time (hosts sorted, agents sorted within each host). A host with a fetch
// error contributes a single non-selectable error row. Rows whose action is
// in flight (present in pending) render a spinner in place of the glyph.
func buildRows(byHost map[string][]Agent, byHostErr map[string]error, pending map[string]struct{}, frame int) []list.Item {
	hosts := sortedHosts(byHost, byHostErr)
	var items []list.Item
	for _, h := range hosts {
		if err := byHostErr[h]; err != nil {
			items = append(items, row{
				title:  glyphStopped + "  " + h,
				desc:   "error: " + err.Error(),
				filter: h,
				host:   h,
			})
			continue
		}
		ags := append([]Agent(nil), byHost[h]...)
		sortAgents(ags)
		for i := range ags {
			a := ags[i]
			g := glyph(a.Status)
			if _, ok := pending[rowKey(h, a.Name)]; ok {
				g = spinnerFrames[frame%len(spinnerFrames)]
			}
			ac := a // stable pointer for the selected-row result
			items = append(items, row{
				title:  g + "  " + a.Name,
				desc:   fmt.Sprintf("%s · %s · %s", dash(a.Template), h, ageLabel(a)),
				filter: a.Name + " " + a.Template + " " + h,
				host:   h,
				ag:     &ac,
			})
		}
	}
	return items
}

// sortedHosts returns the union of host keys from both maps, sorted, with
// LocalHost first so local agents lead the list.
func sortedHosts(byHost map[string][]Agent, byHostErr map[string]error) []string {
	seen := map[string]struct{}{}
	for h := range byHost {
		seen[h] = struct{}{}
	}
	for h := range byHostErr {
		seen[h] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i] == LocalHost {
			return true
		}
		if hosts[j] == LocalHost {
			return false
		}
		return hosts[i] < hosts[j]
	})
	return hosts
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ageLabel renders the right-hand column: uptime for live agents, a
// "suspended … ago" hint for suspended agents, and a plain label otherwise.
func ageLabel(a Agent) string {
	switch a.Status {
	case "stopped":
		return "stopped"
	case "suspended":
		if a.StartedAt.IsZero() {
			return "suspended"
		}
		return "suspended " + humanDuration(time.Since(a.StartedAt)) + " ago"
	default:
		if a.StartedAt.IsZero() {
			return a.Status
		}
		return humanDuration(time.Since(a.StartedAt))
	}
}

// humanDuration renders a compact duration: "2d4h", "3h", "5m", "10s".
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
```

- [ ] **Step 4: Implement `styles.go`**

Create `internal/picker/styles.go`:

```go
package picker

import "github.com/charmbracelet/lipgloss"

// styles holds the lipgloss styles for the status bar. Row/list styling is
// handled by the bubbles list default delegate.
type styles struct {
	statusOK  lipgloss.Style
	statusErr lipgloss.Style
	prompt    lipgloss.Style
}

func newStyles() styles {
	return styles{
		statusOK:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),  // green
		statusErr: lipgloss.NewStyle().Foreground(lipgloss.Color("196")), // red
		prompt:    lipgloss.NewStyle().Foreground(lipgloss.Color("221")), // yellow
	}
}
```

- [ ] **Step 5: Implement `keys.go`**

Create `internal/picker/keys.go`:

```go
package picker

import (
	"github.com/charmbracelet/bubbles/key"
)

// keyMap is the picker's custom keybindings. It implements help.KeyMap so the
// bubbles help component can render the footer.
type keyMap struct {
	Attach  key.Binding
	Suspend key.Binding
	Resume  key.Binding
	Stop    key.Binding
	Rename  key.Binding
	Filter  key.Binding
	Quit    key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Attach:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
		Suspend: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "suspend")),
		Resume:  key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "resume")),
		Stop:    key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		Rename:  key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rename")),
		Filter:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Quit:    key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp / FullHelp satisfy help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Attach, k.Suspend, k.Resume, k.Stop, k.Rename, k.Filter, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Attach, k.Suspend, k.Resume},
		{k.Stop, k.Rename, k.Filter, k.Quit},
	}
}
```

- [ ] **Step 6: Run the tests to verify they pass**

Run: `go test -race -run 'TestGlyph|TestSortAgents|TestBuildRows|TestValidName|TestHumanDuration' ./internal/picker/`
Expected: PASS. (If the package does not yet build because `model` is undefined, that is expected — land Task 5 next; these three files compile independently of the model.)

- [ ] **Step 7: Commit**

```bash
git add internal/picker/rows.go internal/picker/styles.go internal/picker/keys.go internal/picker/rows_test.go
git commit -m "feat: add picker rows, styles, and keymap"
```

---

### Task 5: The Bubble Tea model (`model.go`)

**Files:**
- Create: `internal/picker/model.go`
- Test: `internal/picker/model_test.go`

**Interfaces:**
- Consumes: everything from Tasks 2 and 4 (`Agent`, `Backend`, `Result`, `row`, `buildRows`, `glyph`, `rowKey`, `validName`, `styles`, `keyMap`, `LocalHost`).
- Produces: `type model struct { ... }`, `func newModel(ctx context.Context, backends map[string]Backend) model`, the message types `rowsMsg`, `actionMsg`, `tickMsg`, the `actionKind` enum, **and `func Run(ctx context.Context, backends map[string]Backend) (Result, error)` (added to `picker.go` in this task — deferred from Task 2 so every commit compiles)**. `model` implements `tea.Model` and exposes its `result Result` field (read by `Run`).

- [ ] **Step 1: Write the failing test**

Create `internal/picker/model_test.go`:

```go
package picker

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeBackend records calls so tests can assert an action was dispatched.
type fakeBackend struct {
	agents    []Agent
	listErr   error
	calls     []string
	renameOld string
	renameNew string
}

func (f *fakeBackend) List(context.Context) ([]Agent, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.agents, nil
}
func (f *fakeBackend) Rename(_ context.Context, oldName, newName string) error {
	f.calls = append(f.calls, "rename:"+oldName+"->"+newName)
	f.renameOld, f.renameNew = oldName, newName
	return nil
}
func (f *fakeBackend) Stop(_ context.Context, name string) error {
	f.calls = append(f.calls, "stop:"+name)
	return nil
}
func (f *fakeBackend) Suspend(_ context.Context, name string) error {
	f.calls = append(f.calls, "suspend:"+name)
	return nil
}
func (f *fakeBackend) Resume(_ context.Context, name string) error {
	f.calls = append(f.calls, "resume:"+name)
	return nil
}

// drive feeds a message to the model, runs the returned command to completion
// (recursively feeding produced messages except tea.Quit), and returns the
// resulting model. Batched/tick commands other than Quit are executed once.
func drive(t *testing.T, m model, msg tea.Msg) (model, bool) {
	t.Helper()
	next, cmd := m.Update(msg)
	nm := next.(model)
	quit := false
	if cmd != nil {
		if isQuit(cmd) {
			return nm, true
		}
		// Execute the command and feed non-quit messages back in.
		out := cmd()
		if out != nil {
			if _, isQ := out.(tea.QuitMsg); isQ {
				return nm, true
			}
			nm, quit = drive(t, nm, out)
		}
	}
	return nm, quit
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func sized(m model) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(model)
}

func loaded(m model, host string, ags []Agent, err error) model {
	next, _ := m.Update(rowsMsg{host: host, agents: ags, err: err})
	return next.(model)
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestFilterNarrowsRows(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{
		{Name: "alpha", Host: LocalHost, Status: "running"},
		{Name: "beta", Host: LocalHost, Status: "running"},
	}, nil)

	if got := len(m.list.VisibleItems()); got != 2 {
		t.Fatalf("pre-filter visible = %d, want 2", got)
	}

	// Enter filter mode and type "alp".
	next, _ := m.Update(keyRunes("/"))
	m = next.(model)
	for _, ch := range []string{"a", "l", "p"} {
		next, _ = m.Update(keyRunes(ch))
		m = next.(model)
	}

	if got := len(m.list.VisibleItems()); got != 1 {
		t.Fatalf("filtered visible = %d, want 1", got)
	}
}

func TestSuspendKeyDispatchesSuspend(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	m, _ = drive(t, m, keyRunes("s"))

	if len(fb.calls) != 1 || fb.calls[0] != "suspend:alpha" {
		t.Fatalf("calls = %v, want [suspend:alpha]", fb.calls)
	}
}

func TestStopRequiresConfirm(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	// x arms the confirm prompt but does NOT call Stop yet.
	next, _ := m.Update(keyRunes("x"))
	m = next.(model)
	if m.confirming == nil {
		t.Fatalf("x should arm confirm")
	}
	if len(fb.calls) != 0 {
		t.Fatalf("Stop must not fire before confirm; calls = %v", fb.calls)
	}

	// y confirms and dispatches Stop.
	m, _ = drive(t, m, keyRunes("y"))
	if len(fb.calls) != 1 || fb.calls[0] != "stop:alpha" {
		t.Fatalf("calls = %v, want [stop:alpha]", fb.calls)
	}
}

func TestRenameRoundTrip(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	// r opens the rename input, pre-filled with the current name.
	next, _ := m.Update(keyRunes("r"))
	m = next.(model)
	if !m.renaming {
		t.Fatalf("r should enter rename mode")
	}

	// Clear and type a new name.
	m.rename.SetValue("auth-refactor")

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if fb.renameNew != "auth-refactor" || fb.renameOld != "alpha" {
		t.Fatalf("rename old=%q new=%q, want alpha->auth-refactor", fb.renameOld, fb.renameNew)
	}
}

func TestEnterOnSuspendedResumesThenAttaches(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "suspended"}}, nil)

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.calls) != 1 || fb.calls[0] != "resume:alpha" {
		t.Fatalf("calls = %v, want [resume:alpha]", fb.calls)
	}
	if !quit {
		t.Fatalf("resume-then-attach should quit the program")
	}
	if m.result.Agent == nil || m.result.Agent.Name != "alpha" {
		t.Fatalf("result = %+v, want alpha selected", m.result)
	}
}

func TestEnterOnRunningSelectsAndQuits(t *testing.T) {
	m := newModel(context.Background(), map[string]Backend{LocalHost: &fakeBackend{}})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !quit || m.result.Agent == nil || m.result.Agent.Name != "alpha" {
		t.Fatalf("running Enter should quit with result; quit=%v result=%+v", quit, m.result)
	}
}

func TestHostFetchFailureRendersErrorRow(t *testing.T) {
	m := newModel(context.Background(), map[string]Backend{"hestia": &fakeBackend{}})
	m = sized(m)
	m = loaded(m, "hestia", nil, errors.New("connection refused"))

	items := m.list.Items()
	if len(items) != 1 {
		t.Fatalf("want 1 error row, got %d", len(items))
	}
	r := items[0].(row)
	if r.ag != nil {
		t.Fatalf("error row must have nil agent")
	}
	if !contains(r.desc, "connection refused") {
		t.Fatalf("error row desc = %q", r.desc)
	}
	_ = time.Now
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race ./internal/picker/`
Expected: FAIL — `model`, `newModel`, `rowsMsg`, `actionMsg`, `tickMsg`, `actionKind` undefined.

- [ ] **Step 3: Implement `model.go`**

Create `internal/picker/model.go`:

```go
package picker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// hostTimeout bounds each per-host List/action call so one unreachable
	// remote cannot hang the picker.
	hostTimeout = 5 * time.Second
	// spinnerInterval is the tick period for the in-flight action spinner.
	spinnerInterval = 120 * time.Millisecond
	// footerLines is the vertical space reserved below the list for the status
	// bar and help footer.
	footerLines = 3
)

// actionKind identifies which Backend method a dispatch invokes.
type actionKind int

const (
	actionSuspend actionKind = iota
	actionResume
	actionStop
	actionRename
	actionResumeAttach // resume a suspended agent, then quit and attach
)

// rowsMsg carries the result of a host's List call.
type rowsMsg struct {
	host   string
	agents []Agent
	err    error
}

// actionMsg carries the result of a dispatched lifecycle action.
type actionMsg struct {
	host string
	name string
	kind actionKind
	err  error
}

// tickMsg advances the spinner.
type tickMsg struct{}

// confirmState holds the target of a pending stop confirmation.
type confirmState struct {
	host string
	name string
}

// statusLine is the transient status-bar message.
type statusLine struct {
	text  string
	isErr bool
}

type model struct {
	ctx      context.Context
	backends map[string]Backend

	list   list.Model
	help   help.Model
	keys   keyMap
	styles styles

	byHost    map[string][]Agent
	byHostErr map[string]error
	pending   map[string]struct{}
	frame     int

	confirming *confirmState
	renaming   bool
	rename     textinput.Model
	renameHost string
	renameOld  string

	status statusLine
	result Result
}

func newModel(ctx context.Context, backends map[string]Backend) model {
	delegate := list.NewDefaultDelegate()
	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	ti := textinput.New()
	ti.Prompt = ""

	return model{
		ctx:       ctx,
		backends:  backends,
		list:      l,
		help:      help.New(),
		keys:      defaultKeys(),
		styles:    newStyles(),
		byHost:    map[string][]Agent{},
		byHostErr: map[string]error{},
		pending:   map[string]struct{}{},
		rename:    ti,
	}
}

func (m model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.backends))
	for host, b := range m.backends {
		cmds = append(cmds, loadCmd(m.ctx, host, b))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-footerLines)
		return m, nil

	case rowsMsg:
		m.byHost[msg.host] = msg.agents
		if msg.err != nil {
			m.byHostErr[msg.host] = msg.err
		} else {
			delete(m.byHostErr, msg.host)
		}
		return m, m.rebuild()

	case actionMsg:
		return m.onActionDone(msg)

	case tickMsg:
		if len(m.pending) == 0 {
			return m, nil // stop animating when nothing is in flight
		}
		m.frame++
		return m, tea.Batch(m.rebuild(), tickCmd())

	case tea.KeyMsg:
		// While the user is typing a filter, the list owns every key.
		if m.list.SettingFilter() {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		if m.renaming {
			return m.updateRename(msg)
		}
		if m.confirming != nil {
			return m.updateConfirm(msg)
		}
		return m.updateKey(msg)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// updateKey handles the top-level keybindings when not filtering/renaming/confirming.
func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Esc clears an applied filter first; only quits when the list is unfiltered.
	if msg.String() == "esc" {
		if m.list.FilterState() != list.Unfiltered {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		return m, tea.Quit
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Attach):
		return m.enterSelected()
	case key.Matches(msg, m.keys.Suspend):
		return m.startAction(actionSuspend)
	case key.Matches(msg, m.keys.Resume):
		return m.startAction(actionResume)
	case key.Matches(msg, m.keys.Stop):
		return m.beginConfirm()
	case key.Matches(msg, m.keys.Rename):
		return m.beginRename()
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) selectedRow() (row, bool) {
	it := m.list.SelectedItem()
	if it == nil {
		return row{}, false
	}
	r, ok := it.(row)
	return r, ok
}

// enterSelected implements Enter semantics: running/starting attach immediately;
// suspended resumes first then attaches; stopped shows a hint.
func (m model) enterSelected() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	switch r.ag.Status {
	case "suspended":
		return m.dispatch(r.host, r.ag.Name, actionResumeAttach)
	case "stopped":
		m.status = statusLine{text: "stopped — press u to resume", isErr: true}
		return m, nil
	default: // running / starting / restarting
		agentCopy := *r.ag
		m.result = Result{Agent: &agentCopy}
		return m, tea.Quit
	}
}

// startAction dispatches a lifecycle action against the selected row.
func (m model) startAction(kind actionKind) (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	return m.dispatch(r.host, r.ag.Name, kind)
}

// dispatch marks the row pending and fires the async action command, animating
// the spinner if this is the first in-flight action.
func (m model) dispatch(host, name string, kind actionKind) (tea.Model, tea.Cmd) {
	b, ok := m.backends[host]
	if !ok {
		m.status = statusLine{text: "unknown host " + host, isErr: true}
		return m, nil
	}
	startTick := len(m.pending) == 0

	newPending := make(map[string]struct{}, len(m.pending)+1)
	for k := range m.pending {
		newPending[k] = struct{}{}
	}
	var newName string
	if kind == actionRename {
		newName = strings.TrimSpace(m.rename.Value())
	}
	newPending[rowKey(host, name)] = struct{}{}
	m.pending = newPending

	cmds := []tea.Cmd{actionCmd(m.ctx, host, b, kind, name, newName), m.rebuild()}
	if startTick {
		cmds = append(cmds, tickCmd())
	}
	return m, tea.Batch(cmds...)
}

// onActionDone clears the pending marker and either quits-and-attaches (resume
// attach), or shows the result and reloads that host.
func (m model) onActionDone(msg actionMsg) (tea.Model, tea.Cmd) {
	newPending := make(map[string]struct{}, len(m.pending))
	for k := range m.pending {
		if k != rowKey(msg.host, msg.name) {
			newPending[k] = struct{}{}
		}
	}
	m.pending = newPending

	if msg.kind == actionResumeAttach && msg.err == nil {
		m.result = Result{Agent: &Agent{Name: msg.name, Host: msg.host, Status: "running"}}
		return m, tea.Quit
	}

	if msg.err != nil {
		m.status = statusLine{text: verbLabel(msg.kind) + " " + msg.name + ": " + msg.err.Error(), isErr: true}
	} else {
		m.status = statusLine{text: verbLabel(msg.kind) + " " + msg.name + " ok", isErr: false}
	}
	// Refresh the acted-on host so its rows reflect the new state.
	return m, tea.Batch(m.rebuild(), loadCmd(m.ctx, msg.host, m.backends[msg.host]))
}

// beginConfirm arms the inline stop confirmation for the selected row.
func (m model) beginConfirm() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	m.confirming = &confirmState{host: r.host, name: r.ag.Name}
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		c := m.confirming
		m.confirming = nil
		return m.dispatch(c.host, c.name, actionStop)
	case "n", "N", "esc", "ctrl+c":
		m.confirming = nil
		return m, nil
	}
	return m, nil
}

// beginRename opens the inline text input pre-filled with the current name.
func (m model) beginRename() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	m.renaming = true
	m.renameHost = r.host
	m.renameOld = r.ag.Name
	m.rename.SetValue(r.ag.Name)
	m.rename.CursorEnd()
	return m, m.rename.Focus()
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		newName := strings.TrimSpace(m.rename.Value())
		if !validName(newName) {
			m.status = statusLine{text: "invalid name — use letters, digits, dash, underscore", isErr: true}
			return m, nil
		}
		host, old := m.renameHost, m.renameOld
		m.renaming = false
		m.rename.Blur()
		return m.dispatch(host, old, actionRename)
	case "esc", "ctrl+c":
		m.renaming = false
		m.rename.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.rename, cmd = m.rename.Update(msg)
	return m, cmd
}

// rebuild refreshes the list items from the current per-host state.
func (m *model) rebuild() tea.Cmd {
	return m.list.SetItems(buildRows(m.byHost, m.byHostErr, m.pending, m.frame))
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.list.View())
	b.WriteString("\n")
	switch {
	case m.renaming:
		b.WriteString(m.styles.prompt.Render("rename "+m.renameOld+" to: ") + m.rename.View())
	case m.confirming != nil:
		b.WriteString(m.styles.statusErr.Render(fmt.Sprintf("stop %s? (y/n)", m.confirming.name)))
	case m.status.text != "":
		st := m.styles.statusOK
		if m.status.isErr {
			st = m.styles.statusErr
		}
		b.WriteString(st.Render(m.status.text))
	}
	b.WriteString("\n")
	b.WriteString(m.help.View(m.keys))
	return b.String()
}

// loadCmd fetches one host's agents with a per-host timeout.
func loadCmd(ctx context.Context, host string, b Backend) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, hostTimeout)
		defer cancel()
		ags, err := b.List(cctx)
		return rowsMsg{host: host, agents: ags, err: err}
	}
}

// actionCmd runs one lifecycle action with a per-host timeout.
func actionCmd(ctx context.Context, host string, b Backend, kind actionKind, name, newName string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, hostTimeout)
		defer cancel()
		var err error
		switch kind {
		case actionSuspend:
			err = b.Suspend(cctx, name)
		case actionResume, actionResumeAttach:
			err = b.Resume(cctx, name)
		case actionStop:
			err = b.Stop(cctx, name)
		case actionRename:
			err = b.Rename(cctx, name, newName)
		}
		return actionMsg{host: host, name: name, kind: kind, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func verbLabel(k actionKind) string {
	switch k {
	case actionSuspend:
		return "suspend"
	case actionResume, actionResumeAttach:
		return "resume"
	case actionStop:
		return "stop"
	case actionRename:
		return "rename"
	default:
		return "action"
	}
}
```

- [ ] **Step 4: Add `Run` to `internal/picker/picker.go`**

Append to `internal/picker/picker.go` (and add `fmt` plus `tea "github.com/charmbracelet/bubbletea"` to its imports):

```go
// Run starts the picker over the given backends (keyed by host name) and blocks
// until the user attaches or quits. Attach happens in the caller AFTER Run
// returns, so tmux inherits a clean terminal.
func Run(ctx context.Context, backends map[string]Backend) (Result, error) {
	m := newModel(ctx, backends)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	final, err := p.Run()
	if err != nil {
		return Result{}, err
	}
	fm, ok := final.(model)
	if !ok {
		return Result{}, fmt.Errorf("picker: unexpected final model type %T", final)
	}
	return fm.result, nil
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test -race ./internal/picker/`
Expected: PASS for all model, rows, and picker tests.

If `TestFilterNarrowsRows` is flaky because the list needs a non-zero height for filtering, confirm the `sized()` helper (WindowSizeMsg 80x24) ran before the filter keys; the list must have a size for `VisibleItems` to reflect the filter.

- [ ] **Step 6: Vet the package**

Run: `go vet ./internal/picker/`
Expected: no findings.

- [ ] **Step 7: Commit**

```bash
git add internal/picker/model.go internal/picker/model_test.go internal/picker/picker.go
git commit -m "feat: add picker bubbletea model and Run entry point"
```

---

### Task 6: Local and SSH backends

**Files:**
- Create: `internal/picker/backend_local.go`
- Create: `internal/picker/backend_ssh.go`
- Test: `internal/picker/backend_ssh_test.go`

**Interfaces:**
- Consumes: `Agent`, `Backend`, `LocalHost` (Task 2); `internal/daemon` client funcs; `internal/agent.Record`, `agent.SessionName`; `internal/tmux`.
- Produces:
  - `func NewLocalBackend(homePath string) *LocalBackend` (implements `Backend`; sets `Agent.Host = LocalHost`).
  - `func NewSSHBackend(host, leoPath, tmuxPath string, sshArgs func(tail ...string) []string) *SSHBackend` (implements `Backend`; sets `Agent.Host = host`). Exposes an `exec func(name string, args ...string) *exec.Cmd` field defaulting to `exec.Command` for test seams.

- [ ] **Step 1: Write the failing test for the SSH backend**

Create `internal/picker/backend_ssh_test.go`:

```go
package picker

import (
	"context"
	"encoding/base64"
	"fmt"
	"os/exec"
	"strings"
	"testing"
)

// fakeExec returns an exec seam that ignores args and emits the given stdout /
// exit code via a base64 pipe (byte-exact, survives embedded newlines).
func fakeExec(captured *[]string, stdout string, exitCode int) func(string, ...string) *exec.Cmd {
	return func(name string, args ...string) *exec.Cmd {
		if captured != nil {
			*captured = append([]string{name}, args...)
		}
		encoded := base64.StdEncoding.EncodeToString([]byte(stdout))
		script := fmt.Sprintf("printf '%%s' '%s' | base64 -d; exit %d", encoded, exitCode)
		return exec.Command("sh", "-c", script)
	}
}

func newTestSSHBackend(exec func(string, ...string) *exec.Cmd) *SSHBackend {
	b := NewSSHBackend("hestia", "$HOME/.local/bin/leo", "tmux",
		func(tail ...string) []string {
			return append([]string{"user@hestia"}, tail...)
		})
	b.exec = exec
	return b
}

func TestSSHBackendListParsesJSON(t *testing.T) {
	jsonOut := `[{"name":"rocket","template":"assistant","status":"running"},` +
		`{"name":"blog","status":"suspended"}]`
	b := newTestSSHBackend(fakeExec(nil, jsonOut, 0))

	ags, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ags) != 2 {
		t.Fatalf("want 2 agents, got %d", len(ags))
	}
	if ags[0].Name != "rocket" || ags[0].Host != "hestia" || ags[0].Template != "assistant" {
		t.Fatalf("agent[0] = %+v", ags[0])
	}
	if ags[1].Status != "suspended" {
		t.Fatalf("agent[1].Status = %q", ags[1].Status)
	}
}

func TestSSHBackendListFallsBackToTmux(t *testing.T) {
	// First invocation (leo agent list --json) fails; List retries via tmux.
	var calls int
	b := NewSSHBackend("hestia", "$HOME/.local/bin/leo", "tmux",
		func(tail ...string) []string { return append([]string{"user@hestia"}, tail...) })
	b.exec = func(name string, args ...string) *exec.Cmd {
		calls++
		if calls == 1 {
			return exec.Command("sh", "-c", "echo 'command not found: leo' 1>&2; exit 127")
		}
		return exec.Command("sh", "-c", "printf 'leo-rocket\nleo-blog\nunrelated\n'")
	}

	ags, err := b.List(context.Background())
	if err != nil {
		t.Fatalf("List fallback: %v", err)
	}
	if len(ags) != 2 {
		t.Fatalf("want 2 leo- sessions, got %d: %+v", len(ags), ags)
	}
	for _, a := range ags {
		if !a.AttachOnly {
			t.Errorf("fallback rows must be AttachOnly: %+v", a)
		}
	}
}

func TestSSHBackendStopQuotesName(t *testing.T) {
	var captured []string
	b := newTestSSHBackend(fakeExec(&captured, "", 0))

	if err := b.Stop(context.Background(), "rocket"); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	joined := strings.Join(captured, " ")
	// The remote agent name must be single-token shell-quoted so the remote
	// login shell re-parse cannot split or glob it.
	if !strings.Contains(joined, "'rocket'") {
		t.Fatalf("stop must quote the agent name; argv: %s", joined)
	}
	if !strings.Contains(joined, "agent stop") {
		t.Fatalf("stop must dispatch `agent stop`; argv: %s", joined)
	}
}

func TestSSHBackendRenameQuotesBothNames(t *testing.T) {
	var captured []string
	b := newTestSSHBackend(fakeExec(&captured, "", 0))

	if err := b.Rename(context.Background(), "rocket", "launcher"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "'rocket'") || !strings.Contains(joined, "'launcher'") {
		t.Fatalf("rename must quote both names; argv: %s", joined)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test -race -run TestSSHBackend ./internal/picker/`
Expected: FAIL — `NewSSHBackend`, `SSHBackend`, its `exec` field undefined.

- [ ] **Step 3: Implement the SSH backend**

Create `internal/picker/backend_ssh.go`:

```go
package picker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// SSHBackend dispatches list/lifecycle operations to a remote leo binary over
// SSH. The sshArgs closure builds the full ssh argv (host + configured ssh args
// + ControlMaster opts) for a given remote command tail; it is injected by the
// cli layer so all SSH multiplexing policy stays there. exec is a seam so tests
// never touch a real ssh.
type SSHBackend struct {
	host     string
	leoPath  string
	tmuxPath string
	sshArgs  func(tail ...string) []string
	exec     func(name string, args ...string) *exec.Cmd
}

// NewSSHBackend builds an SSH backend for a named host. leoPath/tmuxPath are the
// remote binary paths (e.g. config.HostConfig.RemoteLeoPath()).
func NewSSHBackend(host, leoPath, tmuxPath string, sshArgs func(tail ...string) []string) *SSHBackend {
	return &SSHBackend{
		host:     host,
		leoPath:  leoPath,
		tmuxPath: tmuxPath,
		sshArgs:  sshArgs,
		exec:     exec.Command,
	}
}

// shellQuoteArg wraps a value in single quotes, escaping embedded single quotes.
// ssh flattens everything after the host into one string handed to the remote
// login shell (which re-parses it), so any argument that must survive that
// re-parse intact — an agent name, a tmux format — has to be single-token
// quoted. This mirrors internal/cli.shellQuoteArg; the picker cannot import cli
// (that would be an import cycle), so the one-liner is duplicated here.
func shellQuoteArg(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// List fetches the remote agents via `leo agent list --json`. On failure it
// degrades to `tmux -L leo list-sessions`, returning attach-only rows so an old
// remote leo (predating --json) or a partial outage still shows something.
func (b *SSHBackend) List(ctx context.Context) ([]Agent, error) {
	out, err := b.run(ctx, b.leoPath, "agent", "list", "--json")
	if err != nil {
		return b.listViaTmux(ctx)
	}
	var records []agent.Record
	if jsonErr := json.Unmarshal(out, &records); jsonErr != nil {
		return b.listViaTmux(ctx)
	}
	agents := make([]Agent, 0, len(records))
	for _, r := range records {
		agents = append(agents, Agent{
			Name:      r.Name,
			Template:  r.Template,
			Host:      b.host,
			Status:    r.Status,
			StartedAt: r.StartedAt,
		})
	}
	return agents, nil
}

// listViaTmux enumerates leo- sessions on the remote and returns attach-only
// rows. The format arg is single-quoted so the `#` cannot start a remote shell
// comment (see internal/cli.remoteAttachChoices for the same gotcha).
func (b *SSHBackend) listViaTmux(ctx context.Context) ([]Agent, error) {
	tail := append([]string{b.tmuxPath}, tmux.Args("list-sessions", "-F", shellQuoteArg("#{session_name}"))...)
	out, err := b.run(ctx, tail...)
	if err != nil {
		return nil, fmt.Errorf("listing remote sessions on %s: %w", b.host, err)
	}
	var agents []Agent
	for _, line := range strings.Split(string(out), "\n") {
		name := strings.TrimSpace(line)
		if !strings.HasPrefix(name, "leo-") {
			continue
		}
		agents = append(agents, Agent{
			Name:       name,
			Host:       b.host,
			Status:     "running",
			AttachOnly: true,
		})
	}
	return agents, nil
}

func (b *SSHBackend) Rename(ctx context.Context, oldName, newName string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "rename", shellQuoteArg(oldName), shellQuoteArg(newName))
	return err
}

func (b *SSHBackend) Stop(ctx context.Context, name string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "stop", shellQuoteArg(name))
	return err
}

func (b *SSHBackend) Suspend(ctx context.Context, name string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "suspend", shellQuoteArg(name))
	return err
}

func (b *SSHBackend) Resume(ctx context.Context, name string) error {
	_, err := b.run(ctx, b.leoPath, "agent", "resume", shellQuoteArg(name))
	return err
}

// run executes `ssh <args...> <tail...>` and returns stdout, wrapping stderr on
// failure.
func (b *SSHBackend) run(ctx context.Context, tail ...string) ([]byte, error) {
	args := b.sshArgs(tail...)
	cmd := b.exec("ssh", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return out, nil
}
```

Note: `b.run(ctx, ...)` takes `ctx` for symmetry but does not thread it into `exec.Command` (the model already wraps each call in a `hostTimeout` context; wiring `exec.CommandContext` is a possible follow-up). Keep the `ctx` parameter so the seam signature stays stable.

- [ ] **Step 4: Run the SSH backend test to verify it passes**

Run: `go test -race -run TestSSHBackend ./internal/picker/`
Expected: PASS.

- [ ] **Step 5: Implement the local backend (no separate test — covered via e2e in Task 8 and by the model's fake in Task 5)**

Create `internal/picker/backend_local.go`:

```go
package picker

import (
	"context"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/daemon"
)

// LocalBackend wraps the local daemon client. The daemon funcs are stored as
// fields so a test can inject fakes without a live socket. Agent names returned
// by List are canonical, so lifecycle calls pass them straight through (no
// shorthand resolution needed).
type LocalBackend struct {
	homePath string
	list     func(ctx context.Context, workDir string) ([]agent.Record, error)
	stop     func(ctx context.Context, workDir, name string) error
	suspend  func(ctx context.Context, workDir, name string) error
	resume   func(ctx context.Context, workDir, name string) (agent.Record, error)
	rename   func(ctx context.Context, workDir, query, newName string) (agent.Record, error)
}

// NewLocalBackend builds a local backend bound to the given leo home.
func NewLocalBackend(homePath string) *LocalBackend {
	return &LocalBackend{
		homePath: homePath,
		list:     daemon.AgentList,
		stop:     daemon.AgentStop,
		suspend:  daemon.AgentSuspend,
		resume:   daemon.AgentResume,
		rename:   daemon.AgentRename,
	}
}

func (b *LocalBackend) List(ctx context.Context) ([]Agent, error) {
	records, err := b.list(ctx, b.homePath)
	if err != nil {
		return nil, err
	}
	agents := make([]Agent, 0, len(records))
	for _, r := range records {
		agents = append(agents, Agent{
			Name:      r.Name,
			Template:  r.Template,
			Host:      LocalHost,
			Status:    r.Status,
			StartedAt: r.StartedAt,
		})
	}
	return agents, nil
}

func (b *LocalBackend) Rename(ctx context.Context, oldName, newName string) error {
	_, err := b.rename(ctx, b.homePath, oldName, newName)
	return err
}

func (b *LocalBackend) Stop(ctx context.Context, name string) error {
	return b.stop(ctx, b.homePath, name)
}

func (b *LocalBackend) Suspend(ctx context.Context, name string) error {
	return b.suspend(ctx, b.homePath, name)
}

func (b *LocalBackend) Resume(ctx context.Context, name string) error {
	_, err := b.resume(ctx, b.homePath, name)
	return err
}
```

- [ ] **Step 6: Verify the whole package builds and tests pass**

Run: `go test -race ./internal/picker/`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add internal/picker/backend_local.go internal/picker/backend_ssh.go internal/picker/backend_ssh_test.go
git commit -m "feat: add local and ssh picker backends"
```

---

### Task 7: Rewire `runAttachPicker` to the new picker

**Files:**
- Modify: `internal/cli/attach_picker.go`
- Modify: `internal/cli/attach_picker_test.go`

**Interfaces:**
- Consumes: `picker.Run`, `picker.Backend`, `picker.LocalBackend`/`NewLocalBackend`, `picker.SSHBackend`/`NewSSHBackend`, `picker.LocalHost`, `picker.Agent`, `picker.Result` (Tasks 2–6); existing `buildSSHArgs`, `runRemoteAttach`, `attachChosenSession`, `agent.SessionName`, `agentListFn`, `stdinIsTerminal` in the cli package.
- Produces: rewritten `runAttachPicker`; new helpers `buildPickerBackends(cfg *config.Config) map[string]picker.Backend` and `attachPickedAgent(ctx, cfg, a picker.Agent, opts attachOptions) error`. Removes the single-candidate auto-attach and the promptui path.

- [ ] **Step 1: Update the tests first**

In `internal/cli/attach_picker_test.go`:

1. **Delete** `TestRunAttachPickerAutoAttachesSingle` entirely (single-candidate auto-attach is removed by design).
2. **Replace** `TestRunAttachPickerErrorsWhenNoSessions` with a daemon-down fail-fast test:

```go
func TestRunAttachPickerFailsFastWhenDaemonDown(t *testing.T) {
	stubStdinIsTerminal(t, true)

	oldList := agentListFn
	agentListFn = func(ctx context.Context, homePath string) ([]agent.Record, error) {
		return nil, fmt.Errorf("connecting to daemon: dial unix: connect: no such file or directory")
	}
	t.Cleanup(func() { agentListFn = oldList })

	cfg := &config.Config{HomePath: t.TempDir()}
	err := runAttachPicker(context.Background(), cfg, config.HostResolution{Localhost: true}, attachOptions{})
	if err == nil || !strings.Contains(err.Error(), "leo service") {
		t.Fatalf("want daemon-down fail-fast error mentioning leo service, got %v", err)
	}
}
```

3. Keep `TestRunAttachPickerRejectsNonTTY` and `TestLocalAttachChoicesSortsAgents` as-is if `localAttachChoices` is retained — but Task 7 removes `localAttachChoices`, `remoteAttachChoices`, `attachChoice`, and the promptui import from the picker flow. **Decision:** the remote enumeration and `attachChoice` type are still consumed by `attachChosenSession`. Keep `attachChoice`, `attachChoiceKind`, `attachChosenSession`, and `agentListFn`; **remove** `localAttachChoices` and `remoteAttachChoices` and their tests (`TestLocalAttachChoicesSortsAgents`, `TestRemoteAttachChoicesFiltersLeoPrefix`, `TestRemoteAttachChoicesQuotesFormatString`, `TestRemoteAttachChoicesHandlesNoServer`) since the new SSH backend supersedes them. Add `fmt` to the test imports if not present.

Add the import for the `agent` package to the test file if the deleted tests removed its only use — the new fail-fast test above uses `agent.Record`.

- [ ] **Step 2: Run the tests to verify they fail (compile error / behavior)**

Run: `go test -race -run TestRunAttachPicker ./internal/cli/`
Expected: FAIL to compile — `runAttachPicker` still references removed helpers / promptui, and `TestRunAttachPickerFailsFastWhenDaemonDown` expects a "leo service" error the current code doesn't emit.

- [ ] **Step 3: Rewrite `attach_picker.go`**

Replace the entire contents of `internal/cli/attach_picker.go` with:

```go
package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/picker"
)

// agentListFn is a testability seam for daemon.AgentList — tests override this
// to simulate the daemon's agent list without spinning up a real socket. Used
// by `leo agent list` (via the seam) and by the attach picker's daemon-down
// fail-fast probe.
var agentListFn = daemon.AgentList

// attachChoiceKind distinguishes what an attach target resolves to, so the
// attach path can route non-claude agents through their SessionDriver instead
// of assuming every target is a tmux session.
type attachChoiceKind int

const (
	attachChoiceAgent attachChoiceKind = iota
	attachChoiceRemote
)

// attachChoice is a resolved attach target: a human label, the tmux session
// name to fall back to for claude targets, and enough identity (kind + bare
// name) to resolve a non-claude harness's driver attach spec.
type attachChoice struct {
	label   string
	session string
	kind    attachChoiceKind
	name    string // bare agent name; empty for remote rows
}

// runAttachPicker handles `leo attach` / `leo agent attach` with no positional
// arg. It opens the full-screen Bubble Tea picker over all agents (local daemon
// + every configured client.hosts entry), then attaches to the chosen agent
// after the TUI exits. The picker is always shown when no name is given — the
// former single-candidate auto-attach is intentionally gone, because the picker
// is now the management surface too.
func runAttachPicker(ctx context.Context, cfg *config.Config, _ config.HostResolution, opts attachOptions) error {
	if !stdinIsTerminal() {
		return fmt.Errorf("no session name given and stdin is not a terminal — pass a name explicitly")
	}

	// Fail fast if the local daemon is unreachable, before entering alt-screen —
	// a blank picker over a dead daemon is worse than a clear error.
	if _, err := agentListFn(ctx, cfg.HomePath); err != nil {
		return fmt.Errorf("cannot reach the leo daemon (is 'leo service' running?): %w", err)
	}

	backends := buildPickerBackends(cfg)
	result, err := picker.Run(ctx, backends)
	if err != nil {
		return fmt.Errorf("picker: %w", err)
	}
	if result.Agent == nil {
		return nil // quit without attaching
	}
	return attachPickedAgent(ctx, cfg, *result.Agent, opts)
}

// buildPickerBackends assembles one backend per host: the local daemon under
// picker.LocalHost, plus an SSH backend for every configured client.hosts entry.
func buildPickerBackends(cfg *config.Config) map[string]picker.Backend {
	backends := map[string]picker.Backend{
		picker.LocalHost: picker.NewLocalBackend(cfg.HomePath),
	}
	for name := range cfg.Client.Hosts {
		res, err := cfg.ResolveHost(name)
		if err != nil {
			continue // skip a host that fails to resolve rather than aborting the picker
		}
		r := res // capture per-iteration copy for the closure
		backends[name] = picker.NewSSHBackend(
			name,
			r.Host.RemoteLeoPath(),
			r.Host.RemoteTmuxPath(),
			func(tail ...string) []string { return buildSSHArgs(r, tail...) },
		)
	}
	return backends
}

// attachPickedAgent routes the chosen agent to the correct attach path. Local
// agents go through the existing attachChosenSession flow (driver-aware); remote
// agents delegate the whole `agent attach <name>` invocation to the remote leo
// over SSH so it does its own resolution and driver routing.
func attachPickedAgent(ctx context.Context, cfg *config.Config, a picker.Agent, opts attachOptions) error {
	if a.Host == picker.LocalHost {
		choice := attachChoice{
			label:   a.Name,
			session: agent.SessionName(a.Name),
			kind:    attachChoiceAgent,
			name:    a.Name,
		}
		return attachChosenSession(ctx, cfg, config.HostResolution{Localhost: true}, choice, opts)
	}
	res, err := cfg.ResolveHost(a.Host)
	if err != nil {
		return fmt.Errorf("resolving host %q: %w", a.Host, err)
	}
	return runRemoteAttach(res, "agent", "attach", a.Name)
}

// stdinIsTerminal reports whether os.Stdin is an interactive TTY. Indirected as
// a var so tests can simulate a pipe or terminal.
var stdinIsTerminal = defaultStdinIsTerminal

func defaultStdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
```

Note: this removes `localAttachChoices`, `remoteAttachChoices`, the `bytes`/`errors`/`sort`/`strings`/`tmux`/`promptui` imports, and the `agentExecCommand` usage in this file. `attachChosenSession` and `attachChoice` remain (defined here). Verify `attachChosenSession` still lives in this file — it does not; per the original it was defined here. **Move `attachChosenSession` into this rewrite** (it was in the original `attach_picker.go`). Include it verbatim:

```go
// attachChosenSession dispatches a resolved attachChoice: a non-claude agent
// (localhost only) routes through its SessionDriver; everything else keeps the
// tmux attach flow.
func attachChosenSession(ctx context.Context, cfg *config.Config, res config.HostResolution, choice attachChoice, opts attachOptions) error {
	if res.Localhost {
		switch choice.kind {
		case attachChoiceAgent:
			if spec, err := agentAttachSpecFn(ctx, cfg.HomePath, choice.name); err == nil && spec.Harness != "" && spec.Harness != "claude" {
				return attachViaDriver(res, toAttachSpec(spec), opts)
			}
		case attachChoiceRemote:
			// No per-row identity to resolve against — always tmux.
		}
	}
	return attachTmuxSession(res, choice.session, opts)
}
```

(Add this function to the rewritten file, immediately after `attachPickedAgent`.)

- [ ] **Step 4: Run the cli tests to verify they pass**

Run: `go test -race ./internal/cli/`
Expected: PASS. Fix any leftover references to removed symbols (`localAttachChoices`, `remoteAttachChoices`, `promptui` in `attach_picker.go`, unused imports) until the package builds and tests are green.

- [ ] **Step 5: Confirm nothing else in the package references the removed helpers**

Run: `grep -rn "localAttachChoices\|remoteAttachChoices" internal/`
Expected: no matches (all call sites removed). If any remain, remove or update them.

- [ ] **Step 6: Drop the now-unused promptui dependency**

`attach_picker.go` was the only importer of `github.com/manifoldco/promptui` in the repo (verified by `grep -rln "manifoldco/promptui" --include="*.go" .`). Remove it:

```bash
go mod tidy
```

Expected: the `manifoldco/promptui` require line disappears from go.mod. If `go mod tidy` keeps it, a stray import remains — grep again and remove it.

- [ ] **Step 7: Commit**

```bash
git add internal/cli/attach_picker.go internal/cli/attach_picker_test.go go.mod go.sum
git commit -m "feat: open the bubbletea picker for nameless leo attach"
```

---

### Task 8: Full verification — test, lint, e2e, and fix fallout

**Files:**
- Modify: any file surfaced by the checks below.

**Interfaces:** none new.

- [ ] **Step 1: Run the full race test suite**

Run: `make test`
Expected: PASS across all packages. Investigate and fix any failure (common suspects: leftover imports in `internal/cli`, tests referencing the deleted `localAttachChoices`/`remoteAttachChoices` helpers, or a bubbletea version mismatch). Note `agentExecCommand` is declared in `agent.go` and still used by attach/forward/terminfo code — do not remove it.

- [ ] **Step 2: Run the linters**

Run: `make lint`
Expected: clean `go vet` + `staticcheck`. Fix findings (e.g. unused parameters — the `_ config.HostResolution` in `runAttachPicker` is intentional; if staticcheck flags the unused `res` parameter name, the blank identifier already handles it).

- [ ] **Step 3: Run the e2e suite**

Run: `make e2e`
Expected: PASS, including `TestAgentListJSON`. The picker itself is interactive and not exercised by e2e; the e2e gate here protects the `leo agent list --json` contract the SSH backend depends on.

- [ ] **Step 4: Manual smoke build**

Run: `make build`
Expected: `bin/leo` builds. (Manual interactive verification of the picker — `bin/leo attach` with agents running — is out of scope for automated checks; note it for the reviewer.)

- [ ] **Step 5: Commit any fixes**

```bash
git add -A
git commit -m "fix: resolve attach-picker test/lint/e2e fallout"
```

(Skip this commit if Steps 1–4 produced no changes.)

---

## Self-Review

**Spec coverage:**
- Entry / picker-always-opens / no auto-attach → Task 7 (`runAttachPicker` rewrite, removed single-candidate branch).
- Full list of all statuses incl. suspended/stopped, glyphs, row format → Tasks 4 (`glyph`, `buildRows`, `ageLabel`) + local/ssh backends (Task 6) surfacing all records.
- Fuzzy filter across name/template/host → Task 4 (`row.FilterValue`) + Task 5 (`TestFilterNarrowsRows`), bubbles list built-in.
- Keybindings (Enter/s/u/x/r///q/Esc), inline confirm, inline rename, async dispatch, spinner, status bar, help footer → Task 5 (`model.Update`, `beginConfirm`, `beginRename`, spinner tick, `View`) + Task 4 `keyMap`.
- Enter semantics (running attach / suspended resume-then-attach / stopped hint) → Task 5 (`enterSelected`, `onActionDone`), tested.
- Remotes via `ssh <host> leo agent list --json` + `ssh <host> leo agent <op> <name>`, 5s timeout, error rows, tmux fallback → Task 6 (`SSHBackend`) + Task 5 (`hostTimeout`, error-row rendering).
- Single-token shell quoting gotcha → Task 6 (`shellQuoteArg`, quoted names/format, tests).
- `internal/picker` package with the exact `Agent`/`Backend`/`Result`/`Run` shapes and the listed files → Tasks 2, 4, 5, 6.
- New deps → Task 3.
- `leo agent list --json` → Task 1 (already implemented; test + e2e added).
- Daemon-down fail-fast before alt-screen → Task 7 (`agentListFn` probe).
- Attach after program exit → Task 7 (`attachPickedAgent` runs post-`picker.Run`).
- Testing: model via fake Backend (all listed cases), ssh via exec seam, no tmux in unit tests, e2e for `--json` → Tasks 5, 6, 1.

**Placeholder scan:** No "TBD"/"similar to Task N"/"add appropriate…" — every code step shows complete code. `Run` is deliberately deferred from Task 2 to Task 5 (where `newModel`/`model` exist) so every task's commit compiles and tests green in strict order.

**Type consistency:** `Backend` methods (`List/Rename/Stop/Suspend/Resume`), `Agent` fields, `Result.Agent`, `LocalHost`, `actionKind` values (`actionSuspend/actionResume/actionStop/actionRename/actionResumeAttach`), `rowKey`, `buildRows` signature, `NewLocalBackend`/`NewSSHBackend` signatures, and `attachChoice`/`attachChoiceKind`/`attachChosenSession` are spelled identically across Tasks 2–7.
