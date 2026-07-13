package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// fakeDaemonTurnsDriver is a minimal harness.SessionDriver whose Attach
// returns a fixed AttachSpec, used to exercise handleAgentAttachSpec without
// a real driven process.
type fakeDaemonTurnsDriver struct {
	spec harness.AttachSpec
	err  error
}

func (d *fakeDaemonTurnsDriver) Start(context.Context, harness.SessionHandle) error { return nil }
func (d *fakeDaemonTurnsDriver) Inject(context.Context, harness.SessionHandle, string) (*harness.Result, error) {
	return nil, nil
}
func (d *fakeDaemonTurnsDriver) Attach(harness.SessionHandle) (harness.AttachSpec, error) {
	return d.spec, d.err
}

// fakeDaemonTurnsHarness is a minimal harness.Harness wrapping a
// fakeDaemonTurnsDriver.
type fakeDaemonTurnsHarness struct {
	name   string
	driver harness.SessionDriver
}

func (h fakeDaemonTurnsHarness) Name() string                              { return h.name }
func (h fakeDaemonTurnsHarness) Binary() string                            { return h.name }
func (h fakeDaemonTurnsHarness) Args(harness.LaunchSpec) ([]string, error) { return nil, nil }
func (h fakeDaemonTurnsHarness) SessionArgs(harness.SessionState) []string { return nil }
func (h fakeDaemonTurnsHarness) ValidateModel(string) error                { return nil }
func (h fakeDaemonTurnsHarness) DecodeOptions(map[string]any) (any, error) { return nil, nil }
func (h fakeDaemonTurnsHarness) OptionsSchema() []harness.OptionField      { return nil }
func (h fakeDaemonTurnsHarness) SupportsChannels() bool                    { return false }
func (h fakeDaemonTurnsHarness) ParseEvents(io.Reader) (harness.Result, error) {
	return harness.Result{}, nil
}
func (h fakeDaemonTurnsHarness) Env(harness.LaunchSpec) (map[string]string, error) { return nil, nil }
func (h fakeDaemonTurnsHarness) SupportsKind(harness.Kind) bool                    { return true }
func (h fakeDaemonTurnsHarness) Driver() harness.SessionDriver                     { return h.driver }

const fakeDaemonTurnsHarnessName = "faketurns-daemontest"

var registerFakeDaemonTurnsHarnessOnce sync.Once
var fakeDaemonTurnsDriverInstance = &fakeDaemonTurnsDriver{}

// registerFakeDaemonTurnsHarness registers fakeDaemonTurnsHarnessName once
// (the harness registry panics on duplicate registration) and returns the
// shared driver so each test can point it at a fresh AttachSpec.
func registerFakeDaemonTurnsHarness() *fakeDaemonTurnsDriver {
	registerFakeDaemonTurnsHarnessOnce.Do(func() {
		harness.Register(fakeDaemonTurnsHarness{name: fakeDaemonTurnsHarnessName, driver: fakeDaemonTurnsDriverInstance})
	})
	return fakeDaemonTurnsDriverInstance
}

// TestAgentAttachSpecHandlerNonClaude verifies GET /agents/{name}/attach-spec
// surfaces the driver's resolved TmuxSession for a non-claude agent.
func TestAgentAttachSpecHandlerNonClaude(t *testing.T) {
	drv := registerFakeDaemonTurnsHarness()
	drv.spec = harness.AttachSpec{TmuxSession: "leo-foo"}
	drv.err = nil

	mgr := &fakeAgentManager{
		records: []agent.Record{{Name: "foo"}},
		handles: map[string]struct {
			harnessName string
			handle      harness.SessionHandle
		}{
			"foo": {harnessName: fakeDaemonTurnsHarnessName, handle: harness.SessionHandle{Name: "foo"}},
		},
	}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/foo/attach-spec")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var env Response
	json.NewDecoder(resp.Body).Decode(&env) //nolint:errcheck
	var out AgentAttachSpecResponse
	json.Unmarshal(env.Data, &out) //nolint:errcheck
	if out.Harness != fakeDaemonTurnsHarnessName {
		t.Errorf("harness = %q, want %q", out.Harness, fakeDaemonTurnsHarnessName)
	}
	if out.TmuxSession != "leo-foo" {
		t.Errorf("TmuxSession = %q, want %q", out.TmuxSession, "leo-foo")
	}
}

// TestAgentAttachSpecHandlerClaudeEmptyHarness verifies a claude agent (no
// resolved handle, or a resolved "claude" harness) gets back an empty
// Harness/TmuxSession so the CLI falls back to its tmux attach flow.
func TestAgentAttachSpecHandlerClaudeEmptyHarness(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{{Name: "bar"}}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/bar/attach-spec")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var env Response
	json.NewDecoder(resp.Body).Decode(&env) //nolint:errcheck
	var out AgentAttachSpecResponse
	json.Unmarshal(env.Data, &out) //nolint:errcheck
	if out.Harness != "" {
		t.Errorf("harness = %q, want empty", out.Harness)
	}
	if out.TmuxSession != "" {
		t.Errorf("TmuxSession = %q, want empty", out.TmuxSession)
	}
}

// TestAgentAttachSpecHandlerNotFound verifies an unknown agent 404s.
func TestAgentAttachSpecHandlerNotFound(t *testing.T) {
	mgr := &fakeAgentManager{records: []agent.Record{}}
	_, client := startTestServerWithAgent(t, mgr)

	resp, err := client.Get("http://localhost/agents/missing/attach-spec")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("want 404, got %d", resp.StatusCode)
	}
}
