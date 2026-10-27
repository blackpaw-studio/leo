package daemon

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/agentstore"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

// Every config change reaches the daemon as a reload (the config API, the
// rules route, the web forms and POST /config/reload all call it), so the
// reload is where appearance is republished.
func TestReloadConfigRepublishesAppearance(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	saveClients(t, path, nil)
	s := New(filepath.Join(dir, "leo.sock"), path, nil)
	mgr := &fakeAgentManager{}
	s.SetAgentManager(mgr)

	if err := s.ReloadConfig(); err != nil {
		t.Fatal(err)
	}
	if mgr.republished != 1 {
		t.Fatalf("RepublishAppearance called %d times, want 1", mgr.republished)
	}
}

// nopSupervisor is an agent.Supervisor with no live agents.
type nopSupervisor struct{}

func (nopSupervisor) ReserveAgent(string) error                      { return nil }
func (nopSupervisor) ReleaseAgent(string)                            {}
func (nopSupervisor) SpawnAgent(agent.SpawnRequest) error            { return nil }
func (nopSupervisor) StopAgent(string, bool) error                   { return nil }
func (nopSupervisor) RenameAgent(string, string) error               { return nil }
func (nopSupervisor) EphemeralAgents() map[string]agent.ProcessState { return nil }

type appearanceRecorder struct {
	mu     sync.Mutex
	events []*observe.AgentAppearanceChangedPayload
}

func (r *appearanceRecorder) Publish(ev observe.Event) {
	if p, ok := ev.Payload.(*observe.AgentAppearanceChangedPayload); ok {
		r.mu.Lock()
		r.events = append(r.events, p)
		r.mu.Unlock()
	}
}

func (r *appearanceRecorder) last(t *testing.T, name string) *observe.AgentAppearanceChangedPayload {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Agent == name {
			return r.events[i]
		}
	}
	t.Fatalf("no agent_appearance_changed for %q", name)
	return nil
}

// The appearance routes answer identically on the socket and under /api/v1,
// persist (rules to leo.yaml, manual values to agents.json) and publish.
func TestAppearanceRoutesSocketWebParity(t *testing.T) {
	rec := &appearanceRecorder{}
	var home string
	p := newParityServerWithAgents(t, func(cfgPath string) *agent.Manager {
		home = filepath.Dir(cfgPath)
		if err := agentstore.Save(home, agentstore.Record{Name: "leo-x", Template: "coder", Stopped: true}); err != nil {
			t.Fatal(err)
		}
		m := agent.New(func() (*config.Config, error) { return config.Load(cfgPath) }, nopSupervisor{}, "", "")
		m.SetPublisher(rec)
		return m
	})
	same := func(label string, a, b *httptest.ResponseRecorder) {
		t.Helper()
		if a.Code != b.Code || a.Body.String() != b.Body.String() {
			t.Errorf("%s differs:\n socket %d %s\n web    %d %s", label, a.Code, a.Body, b.Code, b.Body)
		}
	}

	a, b := p.do(http.MethodGet, "/appearance/rules", "", nil)
	same("GET rules", a, b)
	a, b = p.do(http.MethodPut, "/appearance/rules", "", map[string]any{"rules": []any{}})
	same("428", a, b)
	bad := map[string]any{"rules": []any{map[string]any{"when": map[string]any{"field": "nope", "op": "is", "value": "x"}, "color": "red"}}}
	a, b = p.do(http.MethodPut, "/appearance/rules", p.revision(), bad)
	same("400", a, b)
	if a.Code != http.StatusBadRequest {
		t.Fatalf("invalid rule = %d %s", a.Code, a.Body)
	}

	rules := map[string]any{"rules": []any{map[string]any{"when": map[string]any{"field": "template", "op": "is", "value": "coder"}, "color": "teal"}}}
	a, _ = p.do(http.MethodPut, "/appearance/rules", p.revision(), rules)
	if a.Code != http.StatusOK {
		t.Fatalf("socket rules PUT = %d %s", a.Code, a.Body)
	}
	if got := rec.last(t, "leo-x").Appearance; got == nil || got.Color != "teal" || got.ColorSource != "rule:0" {
		t.Fatalf("republished after the rules PUT = %+v, want teal from rule:0", got)
	}
	a, b = p.do(http.MethodGet, "/appearance/rules", "", nil)
	same("GET rules after a write", a, b)
	if !strings.Contains(b.Body.String(), `"color":"teal"`) {
		t.Fatalf("web does not see the socket's rules: %s", b.Body)
	}

	body := map[string]any{"emoji": "🏛️"}
	a, b = p.do(http.MethodPut, "/agents/leo-x/appearance", "", body)
	same("agent PUT", a, b)
	if a.Code != http.StatusOK || !strings.Contains(a.Body.String(), `"emoji_source":"manual"`) {
		t.Fatalf("agent PUT = %d %s", a.Code, a.Body)
	}
	records, err := agentstore.Load(agentstore.FilePath(home))
	if err != nil {
		t.Fatal(err)
	}
	if got := records["leo-x"].Appearance; got == nil || got.Emoji != "🏛️" {
		t.Fatalf("agents.json appearance = %+v", got)
	}
	if got := rec.last(t, "leo-x").Appearance; got == nil || got.Emoji != "🏛️" || got.Color != "teal" {
		t.Fatalf("published after the agent PUT = %+v", got)
	}

	a, b = p.do(http.MethodPut, "/agents/leo-ghost/appearance", "", map[string]any{"color": "red"})
	same("agent 404", a, b)
	if a.Code != http.StatusNotFound {
		t.Fatalf("unknown agent = %d %s", a.Code, a.Body)
	}
	a, b = p.do(http.MethodPut, "/agents/leo-x/appearance", "", map[string]any{"color": "magenta"})
	same("agent 400", a, b)
	if a.Code != http.StatusBadRequest {
		t.Fatalf("bad color = %d %s", a.Code, a.Body)
	}
}
