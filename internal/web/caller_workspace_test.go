package web

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/consult"
)

func TestCallerWorkspace(t *testing.T) {
	s, _, svc := newTestServerWithAgents(t)
	svc.records = append(svc.records,
		agent.Record{Name: "marketing", Template: "coding", Workspace: "/ws/marketing", Status: "running", StartedAt: time.Now()},
		agent.Record{Name: "homeless", Template: "coding", Status: "running", StartedAt: time.Now()},
	)
	cfg := &config.Config{
		Defaults: config.DefaultsConfig{Harness: "claude"},
		Templates: map[string]config.TemplateConfig{
			"coding": {Harness: "codex"},
		},
		Tasks: map[string]config.TaskConfig{
			"nightly": {Workspace: "/ws/nightly", Harness: "opencode"},
			"plain":   {},
		},
	}
	tests := []struct {
		name   string
		caller string
		want   consult.CallerHome
		ok     bool
	}{
		{"exact agent name", "marketing", consult.CallerHome{Workspace: "/ws/marketing", Harness: "codex"}, true},
		{"fuzzy suffix does not resolve", "market", consult.CallerHome{}, false},
		{"agent without a workspace", "homeless", consult.CallerHome{}, false},
		{"task with its own workspace", "task:nightly", consult.CallerHome{Workspace: "/ws/nightly", Harness: "opencode"}, true},
		{"task on the default workspace", "task:plain", consult.CallerHome{Workspace: cfg.DefaultWorkspace(), Harness: "claude"}, true},
		{"unknown task", "task:ghost", consult.CallerHome{}, false},
		{"unknown agent", "ghost", consult.CallerHome{}, false},
		{"empty caller", "", consult.CallerHome{}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := s.CallerWorkspace(cfg, tc.caller)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("CallerWorkspace(%q) = %+v, %v; want %+v, %v", tc.caller, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// confinementServer serves a config whose defaults carry mode, with agent
// "alpha" homed in its own workspace.
func confinementServer(t *testing.T, mode string) (s *Server, ws string) {
	t.Helper()
	s, dir, svc := newTestServerWithAgents(t)
	yaml := strings.Replace(testConfigWithTemplatesYAML, "defaults:\n", "defaults:\n  dispatch_confinement: "+mode+"\n", 1)
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	ws = filepath.Join(dir, "agents", "alpha")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.records = append(svc.records, agent.Record{Name: "alpha", Template: "coding", Workspace: ws, Status: "running", StartedAt: time.Now()})
	return s, ws
}

func TestAPIDispatchConfinementEnforce(t *testing.T) {
	s, ws := confinementServer(t, "enforce")
	outside := t.TempDir()
	w := startDispatch(t, s, `{"from":"alpha","template":"coding","prompt":"work","cwd":"`+outside+`","mode":"headless"}`)
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "allowed roots") {
		t.Fatalf("outside cwd: %d %s, want 400 naming the allowed roots", w.Code, w.Body.String())
	}
	w = startDispatch(t, s, `{"from":"alpha","template":"coding","prompt":"work","cwd":"`+ws+`","mode":"headless"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("workspace cwd: %d %s", w.Code, w.Body.String())
	}
}

func TestAPIDispatchConfinementWarn(t *testing.T) {
	s, _ := confinementServer(t, "warn")
	outside := t.TempDir()
	w := startDispatch(t, s, `{"from":"alpha","template":"coding","prompt":"work","cwd":"`+outside+`","mode":"headless"}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "dispatch_confinement=enforce") {
		t.Fatalf("outside cwd: %d %s, want 200 with the confinement warning", w.Code, w.Body.String())
	}
}
