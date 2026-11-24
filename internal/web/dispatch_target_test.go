package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
)

// targetServer serves a config where template leo grants agent alpha read
// access, explore routes to research, and agents alpha, beta and leo exist.
func targetServer(t *testing.T) (*Server, string) {
	t.Helper()
	s, dir, svc := newTestServerWithAgents(t)
	yaml := testConfigWithTemplatesYAML + `  leo:
    model: sonnet
    share:
      readable_by: [agent:alpha]
delegation:
  active_profile: p
  profiles:
    p:
      roles:
        explore:
          template: research
`
	if err := os.WriteFile(filepath.Join(dir, "leo.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	leoWS := filepath.Join(dir, "agents", "leo")
	if err := os.MkdirAll(leoWS, 0o755); err != nil {
		t.Fatal(err)
	}
	svc.records = append(svc.records,
		agent.Record{Name: "alpha", Template: "coding", Workspace: filepath.Join(dir, "agents", "alpha"), Status: "running", StartedAt: time.Now()},
		agent.Record{Name: "beta", Template: "coding", Workspace: filepath.Join(dir, "agents", "beta"), Status: "running", StartedAt: time.Now()},
		agent.Record{Name: "leo", Template: "leo", Workspace: leoWS, Status: "stopped"},
	)
	s.consults.ExecCommandContext = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", `{"type":"result","result":"done","is_error":false}`)
	}
	return s, leoWS
}

func TestAPIDispatchTargetAgentNeedsNoCwd(t *testing.T) {
	s, leoWS := targetServer(t)
	w := startDispatch(t, s, `{"from":"alpha","role":"explore","prompt":"read","target_agent":"leo","mode":"headless"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("granted dispatch: %d %s", w.Code, w.Body.String())
	}
	var res struct {
		Data struct {
			ID  string `json:"id"`
			Cwd string `json:"cwd"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Data.Cwd != leoWS {
		t.Fatalf("cwd = %q, want the target workspace %q", res.Data.Cwd, leoWS)
	}
	s.consults.Wait(context.Background(), []string{res.Data.ID}, 5*time.Second)
	rec, err := s.consults.Get(res.Data.ID)
	if err != nil || rec.TargetAgent != "leo" || rec.TargetGrant != "agent:alpha" {
		t.Fatalf("record = %+v, %v", rec, err)
	}
}

func TestAPIDispatchTargetAgentRefusals(t *testing.T) {
	s, _ := targetServer(t)
	for name, body := range map[string]string{
		"no grant":  `{"from":"beta","role":"explore","prompt":"read","target_agent":"leo","mode":"headless"}`,
		"with cwd":  `{"from":"alpha","role":"explore","prompt":"read","target_agent":"leo","cwd":"/tmp","mode":"headless"}`,
		"unknown":   `{"from":"alpha","role":"explore","prompt":"read","target_agent":"ghost","mode":"headless"}`,
		"template":  `{"from":"alpha","template":"research","prompt":"read","target_agent":"leo","mode":"headless"}`,
		"no cwd ok": `{"from":"alpha","role":"explore","prompt":"read","mode":"headless"}`,
	} {
		t.Run(name, func(t *testing.T) {
			w := startDispatch(t, s, body)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("%d %s, want 400", w.Code, w.Body.String())
			}
			if name == "no cwd ok" && !strings.Contains(w.Body.String(), "cwd is required") {
				t.Fatalf("plain dispatch without cwd: %s", w.Body.String())
			}
		})
	}
}
