//go:build e2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/daemon"
	"github.com/blackpaw-studio/leo/internal/harness"
	"github.com/blackpaw-studio/leo/internal/hosts"
	"github.com/blackpaw-studio/leo/internal/observe"
)

type hubAgentManager struct {
	bus          *observe.Bus
	records      []agent.Record
	tmux, socket string
}

func (m *hubAgentManager) Spawn(_ context.Context, s agent.SpawnSpec) (agent.Record, error) {
	if out, err := exec.Command(m.tmux, "-L", m.socket, "new-session", "-d", "-s", "leo-"+s.Name, "sleep 120").CombinedOutput(); err != nil {
		return agent.Record{}, fmt.Errorf("tmux spawn: %w: %s", err, out)
	}
	r := agent.Record{Name: s.Name, Template: s.Template, Status: "running"}
	m.records = append(m.records, r)
	m.bus.Publish(observe.Event{Type: observe.EventAgentSpawned, Payload: &observe.AgentSpawnedPayload{Agent: observe.Agent{Name: r.Name}}})
	return r, nil
}
func (m *hubAgentManager) Stop(string, agent.StopOptions) error { return nil }
func (m *hubAgentManager) Start(string) error                   { return nil }
func (m *hubAgentManager) Reset(string) error                   { return nil }
func (m *hubAgentManager) Restart(string) error                 { return nil }
func (m *hubAgentManager) SwitchTemplate(string, string) (agent.SwitchResult, error) {
	return agent.SwitchResult{}, nil
}
func (m *hubAgentManager) RestartAll() agent.RestartResult                           { return agent.RestartResult{} }
func (m *hubAgentManager) StaleAgents() []agent.StaleAgent                           { return nil }
func (m *hubAgentManager) Delete(context.Context, string, agent.DeleteOptions) error { return nil }
func (m *hubAgentManager) DeletePlan(string) (agent.DeletePlan, error) {
	return agent.DeletePlan{}, nil
}
func (m *hubAgentManager) List() []agent.Record             { return append([]agent.Record(nil), m.records...) }
func (m *hubAgentManager) Logs(string, int) (string, error) { return "", nil }
func (m *hubAgentManager) SessionName(n string) string      { return "leo-" + n }
func (m *hubAgentManager) Resolve(q string) (agent.Record, error) {
	for _, r := range m.records {
		if r.Name == q {
			return r, nil
		}
	}
	return agent.Record{}, fmt.Errorf("not found")
}
func (m *hubAgentManager) Rename(string, string) (agent.Record, error) { return agent.Record{}, nil }
func (m *hubAgentManager) ResolveHandle(string) (string, harness.SessionHandle, bool) {
	return "", harness.SessionHandle{}, false
}

func TestRemoteHostHub(t *testing.T) {
	preflight := exec.Command("ssh", "-o", "BatchMode=yes", "localhost", "true")
	if out, err := preflight.CombinedOutput(); err != nil {
		t.Skipf("ssh localhost server unavailable for remote-host-hub e2e: %s", strings.TrimSpace(string(out)))
	}
	root, err := os.MkdirTemp("/tmp", "leo-hub-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	remoteHome := filepath.Join(root, "remote")
	localHome := filepath.Join(root, "local")
	for _, d := range []string{filepath.Join(remoteHome, "state"), filepath.Join(localHome, "state")} {
		if err := os.MkdirAll(d, 0700); err != nil {
			t.Fatal(err)
		}
	}
	remoteCfgPath := filepath.Join(remoteHome, "leo.yaml")
	localCfgPath := filepath.Join(localHome, "leo.yaml")
	if err := os.WriteFile(remoteCfgPath, []byte("defaults:\n  model: sonnet\n"), 0600); err != nil {
		t.Fatal(err)
	}
	remoteBus := observe.NewBus()
	remote := daemon.New(filepath.Join(remoteHome, "state", "leo.sock"), remoteCfgPath, nil)
	remote.SetObservability(remoteBus, nil, nil, nil, "e2e")
	remoteCfg, err := config.Load(remoteCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	remote.SetHostHub(hosts.New(remoteCfg, remoteBus, remote.Handler()))
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatal(err)
	}
	loopSocket := "leo-loop-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	remote.SetAgentManager(&hubAgentManager{bus: remoteBus, tmux: tmuxPath, socket: loopSocket})
	t.Cleanup(func() { _ = exec.Command(tmuxPath, "-L", loopSocket, "kill-server").Run() })
	if err := remote.Start(); err != nil {
		t.Fatal(err)
	}
	defer remote.Shutdown()
	// Keep the production forward a real ssh process while making the socket
	// discovery probe deterministic for this isolated remote home.
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(bin, "ssh")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"$1\" = localhost ] && [ \"$2\" != -N ]; then printf %%s %q; exit 0; fi\nexec /usr/bin/ssh \"$@\"\n", filepath.Join(remoteHome, "state", "leo.sock"))
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	if err := os.WriteFile(localCfgPath, []byte("client:\n  hosts:\n    loop:\n      ssh: localhost\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(localCfgPath)
	if err != nil {
		t.Fatal(err)
	}
	localBus := observe.NewBus()
	local := daemon.New(filepath.Join(localHome, "state", "leo.sock"), localCfgPath, nil)
	local.SetObservability(localBus, nil, nil, nil, "e2e")
	hub := hosts.New(cfg, localBus, local.Handler())
	local.SetHostHub(hub)
	if err := local.Start(); err != nil {
		t.Fatal(err)
	}
	defer local.Shutdown()
	defer hub.Close()
	events, cancel := openHubEvents(t, localHome)
	defer cancel()
	t.Log("events open")
	waitEvent(t, events, "hello", "")
	t.Log("hello received")
	postHost(t, localHome, "/hosts/loop/connect")
	waitHostState(t, localHome, "connected")
	t.Log("connected")
	time.Sleep(200 * time.Millisecond)
	resp := rawUnix(t, localHome, "GET", "/hosts/loop/agents/list", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("proxy status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	spawn := strings.NewReader(`{"template":"worker","name":"fake-loop"}`)
	resp = rawUnix(t, localHome, "POST", "/hosts/loop/agents/spawn", spawn)
	if resp.StatusCode != 200 {
		t.Fatalf("spawn status=%d", resp.StatusCode)
	}
	resp.Body.Close()
	if out, err := exec.Command(tmuxPath, "-L", loopSocket, "has-session", "-t", "leo-fake-loop").CombinedOutput(); err != nil {
		t.Fatalf("loop tmux session missing: %v: %s", err, out)
	}
	waitEvent(t, events, "agent_spawned", "loop")
	t.Log("tagged event received")
	postHost(t, localHome, "/hosts/loop/disconnect")
	waitHostState(t, localHome, "disconnected")
	t.Log("disconnected")
	postHost(t, localHome, "/hosts/loop/connect")
	waitHostState(t, localHome, "connected")
	t.Log("reconnected explicitly")
	waitHostEventState(t, events, "connected")
	ctl := cfg.HostControlPath("loop")
	pid := masterPID(t, ctl)
	if err := exec.Command("kill", strconv.Itoa(pid)).Run(); err != nil {
		t.Fatal(err)
	}
	waitHostEventState(t, events, "disconnected")
	t.Log("drop event received")
	waitHostEventState(t, events, "connected")
	waitHostState(t, localHome, "connected")
	if next := masterPID(t, ctl); next == pid {
		t.Fatalf("ssh master pid did not change: %d", pid)
	}
	if !daemon.SocketHealthy(context.Background(), cfg.HostForwardSocket("loop")) {
		t.Fatal("reconnected forwarded socket is unhealthy")
	}
	t.Log("reconnected after kill")
}

func masterPID(t *testing.T, ctl string) int {
	t.Helper()
	out, err := exec.Command("/usr/bin/ssh", "-o", "BatchMode=yes", "-o", "ControlPath="+ctl, "-O", "check", "localhost").CombinedOutput()
	if err != nil {
		t.Fatalf("finding master pid: %v: %s", err, out)
	}
	match := regexp.MustCompile(`pid=([0-9]+)`).FindStringSubmatch(string(out))
	if len(match) != 2 {
		t.Fatalf("master pid missing: %s", out)
	}
	pid, _ := strconv.Atoi(match[1])
	return pid
}

func rawUnix(t *testing.T, home, method, path string, body *strings.Reader) *http.Response {
	t.Helper()
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return net.Dial("unix", filepath.Join(home, "state", "leo.sock"))
	}}
	var req *http.Request
	if body == nil {
		req, _ = http.NewRequest(method, "http://daemon"+path, nil)
	} else {
		req, _ = http.NewRequest(method, "http://daemon"+path, body)
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
func postHost(t *testing.T, home, path string) {
	t.Helper()
	r := rawUnix(t, home, "POST", path, nil)
	defer r.Body.Close()
	if r.StatusCode != 200 {
		t.Fatalf("%s status=%d", path, r.StatusCode)
	}
}
func waitHostState(t *testing.T, home, want string) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	last := ""
	for time.Now().Before(deadline) {
		r := rawUnix(t, home, "GET", "/hosts", nil)
		var env struct {
			Data []struct{ Name, State, Error, Code string }
		}
		_ = json.NewDecoder(r.Body).Decode(&env)
		r.Body.Close()
		for _, h := range env.Data {
			if h.Name == "loop" {
				last = fmt.Sprintf("state=%s code=%s error=%s", h.State, h.Code, h.Error)
			}
			if h.Name == "loop" && h.State == want {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("host never reached %s: %s", want, last)
}
func openHubEvents(t *testing.T, home string) (*bufio.Reader, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	tr := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		return net.Dial("unix", filepath.Join(home, "state", "leo.sock"))
	}}
	req, _ := http.NewRequestWithContext(ctx, "GET", "http://daemon/events", nil)
	resp, err := (&http.Client{Transport: tr}).Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return bufio.NewReader(resp.Body), cancel
}
func waitEvent(t *testing.T, r *bufio.Reader, want, host string) {
	t.Helper()
	for {
		event := ""
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") && event == want {
				if host == "" {
					return
				}
				var p map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p)
				if p["host"] == host {
					return
				}
			}
			if line == "" {
				break
			}
		}
	}
}

func waitHostEventState(t *testing.T, r *bufio.Reader, want string) {
	t.Helper()
	for {
		event := ""
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				t.Fatal(err)
			}
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") && event == "host_state_changed" {
				var p map[string]any
				_ = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &p)
				if p["host"] == "loop" && p["state"] == want {
					return
				}
			}
			if line == "" {
				break
			}
		}
	}
}
