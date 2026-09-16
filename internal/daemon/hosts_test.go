package daemon

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/hosts"
	"github.com/blackpaw-studio/leo/internal/observe"
)

func TestHostRoutesUseEnvelope(t *testing.T) {
	s := New(t.TempDir()+"/leo.sock", t.TempDir()+"/leo.yaml", nil)
	w := httptest.NewRecorder()
	s.handleHosts(w, httptest.NewRequest("GET", "/hosts", nil))
	var r Response
	if json.Unmarshal(w.Body.Bytes(), &r) != nil || r.OK {
		t.Fatalf("/hosts response is not the daemon envelope: %s", w.Body.String())
	}
}

func TestHostStateMergeTwoRemotes(t *testing.T) {
	home, err := os.MkdirTemp("/tmp", "leo-hub-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(home) })
	cfgPath := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte("client:\n  hosts:\n    alpha:\n      ssh: alpha\n    beta:\n      ssh: beta\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	for name, agentName := range map[string]string{"alpha": "agent-a", "beta": "agent-b"} {
		sock := cfg.HostForwardSocket(name)
		if err := os.MkdirAll(filepath.Dir(sock), 0700); err != nil {
			t.Fatal(err)
		}
		ln, err := net.Listen("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/health":
				w.WriteHeader(200)
			case "/state":
				if r.URL.RawQuery != "scope=local" {
					t.Errorf("remote state query = %q", r.URL.RawQuery)
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "data": map[string]any{"agents": []map[string]any{{"name": agentName, "host": "localhost"}}}})
			default:
				http.NotFound(w, r)
			}
		})}
		go func() { _ = srv.Serve(ln) }()
		t.Cleanup(func() { _ = srv.Close() })
	}
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	ssh := filepath.Join(bin, "ssh")
	script := "#!/bin/sh\nif [ \"$1\" = -N ]; then trap 'exit 0' TERM INT; while :; do sleep 1; done; fi\nprintf '/remote.sock\\n'\n"
	if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	bus := observe.NewBus()
	hub := hosts.New(cfg, bus, nil)
	t.Cleanup(func() { _ = hub.Close() })
	s := New(filepath.Join(home, "daemon.sock"), cfgPath, nil)
	s.SetHostHub(hub)
	s.SetObservability(bus, nil, nil, nil, "test")
	s.SetAgentManager(&fakeAgentManager{records: []agent.Record{{Name: "local-agent", Status: "running"}}})
	for _, name := range []string{"alpha", "beta"} {
		if _, err := hub.Connect(context.Background(), name); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rows := hub.Rows()
		if rows[1].State == hosts.StateConnected && rows[2].State == hosts.StateConnected {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/hosts", nil))
	var hostEnv struct {
		OK   bool        `json:"ok"`
		Data []hosts.Row `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &hostEnv); err != nil || !hostEnv.OK {
		t.Fatalf("/hosts response is not the daemon envelope: %s", w.Body.String())
	}
	if len(hostEnv.Data) != 3 || hostEnv.Data[1].State != hosts.StateConnected || hostEnv.Data[2].State != hosts.StateConnected {
		t.Fatalf("/hosts rows = %#v, want localhost plus two connected remotes", hostEnv.Data)
	}
	w = httptest.NewRecorder()
	s.Handler().ServeHTTP(w, httptest.NewRequest("GET", "/state", nil))
	var stateEnv struct {
		OK   bool `json:"ok"`
		Data struct {
			Agents []map[string]any `json:"agents"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &stateEnv); err != nil || !stateEnv.OK {
		t.Fatalf("/state response is not the daemon envelope: %s", w.Body.String())
	}
	got := make([]string, 0, len(stateEnv.Data.Agents))
	for _, a := range stateEnv.Data.Agents {
		got = append(got, a["host"].(string)+":"+a["name"].(string))
	}
	sort.Strings(got)
	want := []string{"alpha:agent-a", "beta:agent-b", "localhost:local-agent"}
	if len(got) != len(want) {
		t.Fatalf("/state agents = %v, want %v (remote merge missing)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("/state agents = %v, want %v (remote merge missing)", got, want)
		}
	}
}
