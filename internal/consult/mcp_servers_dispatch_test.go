package consult

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

const mcpSecretSentinel = "SENTINEL-SECRET-TOKEN-9f3a"

// mcpDispatchFixture is a claude template whose account config (reached via
// the template's CLAUDE_CONFIG_DIR) defines chrome (with a secret header) and
// an unrelated server.
type mcpDispatchFixture struct {
	cfg  *config.Config
	home string
}

func newMCPDispatchFixture(t *testing.T, harnessOptions map[string]any) mcpDispatchFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	account := t.TempDir()
	body := `{"mcpServers":{"chrome":{"command":"npx","headers":{"Authorization":"` + mcpSecretSentinel + `"}},"other":{"url":"https://example.invalid"}}}`
	if err := os.WriteFile(filepath.Join(account, ".claude.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.HomePath = t.TempDir()
	cfg.Web.Enabled = true
	tmpl := cfg.Templates["claude"]
	tmpl.Env = config.LiteralEnv(map[string]string{"CLAUDE_CONFIG_DIR": account})
	tmpl.HarnessOptions = harnessOptions
	cfg.Templates["claude"] = tmpl
	return mcpDispatchFixture{cfg: cfg, home: cfg.HomePath}
}

func (f mcpDispatchFixture) path(id string) string {
	return filepath.Join(f.home, "state", "mcp", "dispatch-"+id+".json")
}

type capturedRun struct {
	args []string
	file []byte // MCP config file contents when the process was launched
	mode os.FileMode
}

// capturingExec records each launch's argv and, when argv names an
// --mcp-config file, that file as it was at launch.
func capturingExec(t *testing.T, calls *[]capturedRun, mu *sync.Mutex) func(context.Context, string, ...string) *exec.Cmd {
	return func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		run := capturedRun{args: append([]string(nil), args...)}
		for i, a := range args {
			if a == "--mcp-config" && i+1 < len(args) && !strings.HasPrefix(args[i+1], "{") {
				run.file, _ = os.ReadFile(args[i+1])
				if fi, err := os.Stat(args[i+1]); err == nil {
					run.mode = fi.Mode().Perm()
				}
			}
		}
		mu.Lock()
		*calls = append(*calls, run)
		mu.Unlock()
		return exec.CommandContext(ctx, "printf", "%s", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false,"num_turns":1}`)
	}
}

func configServerNames(t *testing.T, data []byte) []string {
	t.Helper()
	var doc struct {
		MCPServers map[string]json.RawMessage `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("mcp config %q: %v", data, err)
	}
	var names []string
	for n := range doc.MCPServers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func assertArgvHasOnlyMCPPath(t *testing.T, args []string, path string) {
	t.Helper()
	found := false
	for _, a := range args {
		if a == path {
			found = true
		}
		if strings.Contains(a, "mcpServers") || strings.Contains(a, mcpSecretSentinel) {
			t.Fatalf("argv leaks the MCP config: %q", args)
		}
	}
	if !found {
		t.Fatalf("argv lacks --mcp-config %s: %q", path, args)
	}
}

func startMCPDispatch(t *testing.T, f mcpDispatchFixture) (*Dispatcher, *[]capturedRun, *sync.Mutex, Started) {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	var calls []capturedRun
	var mu sync.Mutex
	d.ExecCommandContext = capturingExec(t, &calls, &mu)
	started, err := d.Start(context.Background(), f.cfg, Request{Template: "claude", Prompt: "go", Cwd: t.TempDir(), Kind: "dispatch"})
	if err != nil {
		t.Fatal(err)
	}
	return d, &calls, &mu, started
}

func TestHeadlessDispatchMCPServersUsesPrivateFileAndCleansUp(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome", "nope"}})
	d, calls, mu, started := startMCPDispatch(t, f)

	if got := d.Wait(context.Background(), []string{started.ID}, 5*time.Second)[0]; got.Status != StatusDone {
		t.Fatalf("entry = %+v", got)
	}
	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()

	assertArgvHasOnlyMCPPath(t, run.args, f.path(started.ID))
	if names := configServerNames(t, run.file); len(names) != 2 || names[0] != "chrome" || names[1] != "leo" {
		t.Fatalf("file servers = %v, want [chrome leo]", names)
	}
	if run.mode != 0o600 {
		t.Fatalf("file mode = %v, want 0600", run.mode)
	}
	if _, err := os.Stat(f.path(started.ID)); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after completion: %v", err)
	}
	if len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], `"nope"`) {
		t.Fatalf("Started.Warnings = %v", started.Warnings)
	}
	rec, err := d.Get(started.ID)
	if err != nil || len(rec.Warnings) != 1 {
		t.Fatalf("Record.Warnings = %v err=%v", rec.Warnings, err)
	}
	for _, w := range append(started.Warnings, rec.Warnings...) {
		if strings.Contains(w, mcpSecretSentinel) {
			t.Fatalf("warning leaks secret: %q", w)
		}
	}
}

func TestHeadlessDispatchUnknownOnlyNameStillLaunchesWithInlineBridge(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"nope"}})
	d, calls, mu, started := startMCPDispatch(t, f)
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if run.file != nil {
		t.Fatal("no servers added: nothing should be written")
	}
	if len(started.Warnings) != 1 {
		t.Fatalf("Warnings = %v", started.Warnings)
	}
	if _, err := os.Stat(filepath.Join(f.home, "state", "mcp")); !os.IsNotExist(err) {
		t.Fatalf("state/mcp should not exist: %v", err)
	}
}

func TestHeadlessDispatchWithoutMCPServersIsUnchanged(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	d, calls, mu, started := startMCPDispatch(t, f)
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if !strings.Contains(strings.Join(run.args, " "), `--strict-mcp-config --mcp-config {"mcpServers"`) || len(started.Warnings) != 0 {
		t.Fatalf("args = %q warnings = %v", run.args, started.Warnings)
	}
}

func TestHeadlessDispatchOnlyMCPServersKeyGetsLeanNone(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome"}})
	d, calls, mu, started := startMCPDispatch(t, f)
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	assertArgvHasOnlyMCPPath(t, run.args, f.path(started.ID))
	if !strings.Contains(strings.Join(run.args, " "), "--strict-mcp-config") {
		t.Fatalf("args = %q", run.args)
	}
}

func TestHeadlessDispatchInheritKeepsArgvAndWarns(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp": "inherit", "mcp_servers": []any{"chrome"}})
	d, calls, mu, started := startMCPDispatch(t, f)
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if joined := strings.Join(run.args, " "); strings.Contains(joined, "--strict-mcp-config") || strings.Contains(joined, filepath.Join("state", "mcp")) {
		t.Fatalf("inherit argv changed: %q", run.args)
	}
	if len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], "inherit") {
		t.Fatalf("Warnings = %v", started.Warnings)
	}
}

func TestHeadlessContinuationRewritesAndRemovesMCPFile(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome"}})
	d, calls, mu, started := startMCPDispatch(t, f)
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	sent, err := d.SendWithConfig(context.Background(), f.cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)

	mu.Lock()
	second := (*calls)[1]
	mu.Unlock()
	assertArgvHasOnlyMCPPath(t, second.args, f.path(started.ID))
	if names := configServerNames(t, second.file); len(names) != 2 || names[0] != "chrome" || names[1] != "leo" {
		t.Fatalf("continuation file servers = %v", names)
	}
	if _, err := os.Stat(f.path(started.ID)); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after the continuation: %v", err)
	}
}

func TestInteractiveStartReturnsMCPServerWarnings(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome", "nope"}})
	d := NewDispatcher(newFakeRecorder())
	d.SetInteractiveRuntime(&fakeInteractiveRuntime{arm: true, empty: true, alive: true})

	started, err := d.Start(context.Background(), f.cfg, Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive})
	if err != nil {
		t.Fatal(err)
	}

	if len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], `"nope"`) {
		t.Fatalf("Started.Warnings = %v", started.Warnings)
	}
	if _, err := os.Stat(f.path(started.ID)); !os.IsNotExist(err) {
		t.Fatalf("Start itself must not write the file for an interactive run: %v", err)
	}
}

func startMCPDispatchWith(t *testing.T, f mcpDispatchFixture, additional []string) (*Dispatcher, *[]capturedRun, *sync.Mutex, Started, error) {
	return startMCPDispatchIn(t, f, additional, t.TempDir())
}

func startMCPDispatchIn(t *testing.T, f mcpDispatchFixture, additional []string, cwd string) (*Dispatcher, *[]capturedRun, *sync.Mutex, Started, error) {
	t.Helper()
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	var calls []capturedRun
	var mu sync.Mutex
	d.ExecCommandContext = capturingExec(t, &calls, &mu)
	started, err := d.Start(context.Background(), f.cfg, Request{Template: "claude", Prompt: "go", Cwd: cwd, Kind: "dispatch", AdditionalMCPServers: additional})
	return d, &calls, &mu, started, err
}

func TestHeadlessDispatchAdditionalMCPServersOnlyAddsToBridge(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	d, calls, mu, started, err := startMCPDispatchWith(t, f, []string{"chrome"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	assertArgvHasOnlyMCPPath(t, run.args, f.path(started.ID))
	if names := configServerNames(t, run.file); len(names) != 2 || names[0] != "chrome" || names[1] != "leo" {
		t.Fatalf("file servers = %v, want [chrome leo]", names)
	}
	if run.mode != 0o600 {
		t.Fatalf("file mode = %v, want 0600", run.mode)
	}
	rec, err := d.Get(started.ID)
	if err != nil || len(rec.AdditionalMCPServers) != 1 || rec.AdditionalMCPServers[0] != "chrome" {
		t.Fatalf("Record.AdditionalMCPServers = %v err=%v", rec.AdditionalMCPServers, err)
	}
}

func TestHeadlessDispatchAdditionalMCPServersUnionWithTemplate(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome"}})
	d, calls, mu, started, err := startMCPDispatchWith(t, f, []string{"chrome", "other"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if names := configServerNames(t, run.file); len(names) != 3 || names[0] != "chrome" || names[1] != "leo" || names[2] != "other" {
		t.Fatalf("file servers = %v, want [chrome leo other]", names)
	}
}

func TestHeadlessDispatchAdditionalUnknownNameWarnsAndLaunches(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	d, calls, mu, started, err := startMCPDispatchWith(t, f, []string{"chrome", "nope"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if names := configServerNames(t, run.file); len(names) != 2 {
		t.Fatalf("file servers = %v", names)
	}
	if len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], `"nope"`) {
		t.Fatalf("Started.Warnings = %v", started.Warnings)
	}
	rec, _ := d.Get(started.ID)
	if len(rec.Warnings) != 1 {
		t.Fatalf("Record.Warnings = %v", rec.Warnings)
	}
}

func TestHeadlessDispatchAdditionalIgnoredUnderInheritWarns(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp": "inherit"})
	d, calls, mu, started, err := startMCPDispatchWith(t, f, []string{"chrome"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	mu.Lock()
	run := (*calls)[0]
	mu.Unlock()
	if joined := strings.Join(run.args, " "); strings.Contains(joined, "--strict-mcp-config") || strings.Contains(joined, filepath.Join("state", "mcp")) {
		t.Fatalf("inherit argv changed: %q", run.args)
	}
	if len(started.Warnings) != 1 || !strings.Contains(started.Warnings[0], "additional_mcp_servers ignored under mcp: inherit") {
		t.Fatalf("Warnings = %v", started.Warnings)
	}
}

func TestHeadlessDispatchWithoutAdditionalIsByteIdentical(t *testing.T) {
	f := newMCPDispatchFixture(t, map[string]any{"mcp_servers": []any{"chrome"}})
	cwd := t.TempDir()
	d1, calls1, mu1, s1, err := startMCPDispatchIn(t, f, nil, cwd)
	if err != nil {
		t.Fatal(err)
	}
	d1.Wait(context.Background(), []string{s1.ID}, 5*time.Second)
	d2, calls2, mu2, s2, err := startMCPDispatchIn(t, f, []string{}, cwd)
	if err != nil {
		t.Fatal(err)
	}
	d2.Wait(context.Background(), []string{s2.ID}, 5*time.Second)

	mu1.Lock()
	a := (*calls1)[0]
	mu1.Unlock()
	mu2.Lock()
	b := (*calls2)[0]
	mu2.Unlock()
	norm := func(args []string, id string) string { return strings.ReplaceAll(strings.Join(args, "\x00"), id, "ID") }
	if norm(a.args, s1.ID) != norm(b.args, s2.ID) || string(a.file) != string(b.file) {
		t.Fatalf("argv/config differ:\n%q\n%q", a.args, b.args)
	}
	if rec, _ := d1.Get(s1.ID); rec.AdditionalMCPServers != nil {
		t.Fatalf("record should carry none: %v", rec.AdditionalMCPServers)
	}
}

func TestStartRejectsInvalidAdditionalMCPServers(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	for _, bad := range [][]string{{"a/b"}, {""}} {
		_, _, _, _, err := startMCPDispatchWith(t, f, bad)
		if err == nil || !errors.As(err, new(*ValidationError)) {
			t.Fatalf("names %q: err = %v, want a validation error", bad, err)
		}
	}
}
