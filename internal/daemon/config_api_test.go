package daemon

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type socketResult struct {
	status int
	body   map[string]any
}

func (r socketResult) data() map[string]any {
	d, _ := r.body["data"].(map[string]any)
	return d
}

func socketDo(t *testing.T, c *http.Client, method, path, ifMatch string, body any) socketResult {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, "http://localhost"+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if ifMatch != "" {
		req.Header.Set("If-Match", `"`+ifMatch+`"`)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out := socketResult{status: resp.StatusCode}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func newConfigAPIServer(t *testing.T) (*Server, *http.Client, string) {
	t.Helper()
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir)
	s, client := startTestServer(t, cfgPath)
	return s, client, cfgPath
}

func TestSocketServesTheConfigAPI(t *testing.T) {
	_, client, cfgPath := newConfigAPIServer(t)

	res := socketDo(t, client, http.MethodGet, "/config/schema", "", nil)
	if res.status != http.StatusOK || res.body["ok"] != true {
		t.Fatalf("schema = %d %v", res.status, res.body)
	}

	raw, _ := os.ReadFile(cfgPath)
	rev := config.RevisionOf(raw)
	res = socketDo(t, client, http.MethodPut, "/config/defaults", rev, map[string]any{"values": map[string]any{"model": "opus"}})
	if res.status != http.StatusOK {
		t.Fatalf("PUT defaults = %d %v", res.status, res.body)
	}
	if rr, _ := res.data()["restart_required"].(map[string]any); rr["agents"] != true {
		t.Errorf("restart_required = %v, want agents", res.data()["restart_required"])
	}
	cfg, err := config.Load(cfgPath)
	if err != nil || cfg.Defaults.Model != "opus" {
		t.Fatalf("saved model = %q, %v", cfg.Defaults.Model, err)
	}

	stale := socketDo(t, client, http.MethodPut, "/config/defaults", rev, map[string]any{"values": map[string]any{"model": "haiku"}})
	if stale.status != http.StatusConflict {
		t.Errorf("stale write = %d, want 409", stale.status)
	}
}

func TestSocketReloadReturnsTheRevision(t *testing.T) {
	_, client, cfgPath := newConfigAPIServer(t)
	raw, _ := os.ReadFile(cfgPath)
	res := socketDo(t, client, http.MethodPost, "/config/reload", "", nil)
	if res.status != http.StatusOK || res.data()["revision"] != config.RevisionOf(raw) {
		t.Fatalf("reload = %d %v", res.status, res.body)
	}
}

func TestAgentsRestartClearsTheSharedRestartFlag(t *testing.T) {
	s, client, cfgPath := newConfigAPIServer(t)
	s.SetAgentManager(&fakeAgentManager{restartAll: agent.RestartResult{Restarted: []string{"leo-a"}}})
	raw, _ := os.ReadFile(cfgPath)
	socketDo(t, client, http.MethodPut, "/config/defaults", config.RevisionOf(raw), map[string]any{"values": map[string]any{"model": "opus"}})
	if !s.configSvc.RestartFlags().Agents {
		t.Fatal("precondition: defaults save should raise the agents flag")
	}
	resp, err := client.Post("http://localhost/agents/restart", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if s.configSvc.RestartFlags().Agents {
		t.Error("a clean restart-all did not clear the shared agents flag")
	}
}

func TestVersionAdvertisesTheConfigAPI(t *testing.T) {
	_, client, _ := newConfigAPIServer(t)
	res := socketDo(t, client, http.MethodGet, "/version", "", nil)
	caps, _ := res.data()["capabilities"].([]any)
	found := false
	for _, c := range caps {
		if c == observe.FeatureConfigAPI {
			found = true
		}
	}
	if !found {
		t.Errorf("capabilities = %v, want %q", caps, observe.FeatureConfigAPI)
	}
}
