//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

// TestInteractiveDispatchLifecycle is deliberately black-box: the service,
// HTTP bearer auth, tmux pane, hook subprocess, and dispatch CLI all run for
// real.  The only fake is the Codex-shaped TUI binary built in TestMain.
func TestInteractiveDispatchLifecycle(t *testing.T) {
	s := newInteractiveE2E(t)
	started := s.dispatch(t, "opening prompt")

	if !s.windowExists(started.Window) {
		t.Fatalf("interactive window %q was not created", started.Window)
	}
	first := s.wait(t, started.ID+"#1")
	if first.Outcome != consult.TurnFinished || first.Text != "FAKE-REPLY: opening prompt" {
		t.Fatalf("opening turn = %+v", first)
	}

	send := s.send(t, started.ID, "follow up")
	if send.TurnID != started.ID+"#2" {
		t.Fatalf("send turn_id = %q", send.TurnID)
	}
	second := s.wait(t, send.TurnID)
	if second.Outcome != consult.TurnFinished || second.Text != "FAKE-REPLY: follow up" {
		t.Fatalf("follow-up turn = %+v", second)
	}

	rec := s.record(t, started.ID)
	if err := exec.Command(s.tmux, tmux.Args("send-keys", "-t", rec.PaneID, "human steer", "Enter")...).Run(); err != nil {
		t.Fatalf("typing into interactive pane: %v", err)
	}
	s.waitFor(t, func() bool { return len(s.record(t, started.ID).Turns) == 3 })
	rec = s.record(t, started.ID)
	if !rec.Steered || rec.Turns[2].Source != consult.TurnSourceUser || rec.Turns[2].Outcome != consult.TurnFinished {
		t.Fatalf("human steering record = %+v", rec)
	}

	s.cancel(t, started.ID)
	s.waitFor(t, func() bool { return s.record(t, started.ID).Status == consult.StatusCanceled })
	if s.paneAlive(rec.PaneID) {
		t.Fatal("interactive pane survived cancel")
	}
}

func TestInteractivePaneDeath(t *testing.T) {
	t.Run("after finished turn closes", func(t *testing.T) {
		s := newInteractiveE2E(t)
		started := s.dispatch(t, "complete first")
		_ = s.wait(t, started.ID+"#1")
		rec := s.record(t, started.ID)
		s.killPane(t, rec.PaneID)
		s.waitForStatus(t, started.ID, consult.StatusClosed, 45*time.Second)
	})
	t.Run("before a turn finishes fails", func(t *testing.T) {
		s := newInteractiveE2EWithDelay(t, 3000)
		started := s.dispatch(t, "kill before reply")
		rec := s.record(t, started.ID)
		s.killPane(t, rec.PaneID)
		s.waitForStatus(t, started.ID, consult.StatusFailed, 45*time.Second)
	})
}

type interactiveE2E struct {
	t       *testing.T
	ws      string
	cfgPath string
	port    int
	token   string
	tmux    string
	service *exec.Cmd
	output  *bytes.Buffer
}

func newInteractiveE2E(t *testing.T) *interactiveE2E {
	return newInteractiveE2EWithDelay(t, 0)
}

func newInteractiveE2EWithDelay(t *testing.T, delayMS int) *interactiveE2E {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux not available; skipping interactive dispatch e2e")
	}
	_ = tmuxPath // TestMain's wrapper rewrites production's fixed -L leo.
	tmuxPath = faketmux
	t.Setenv("FAKECLAUDE_TMUX_SOCKET", fmt.Sprintf("leo-e2e-%d", time.Now().UnixNano()))
	t.Cleanup(func() { _ = exec.Command(tmuxPath, tmux.Args("kill-server")...).Run() })

	ws := mkTempE2EDir(t, "leo-e2e-interactive-*")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()
	cfgPath := filepath.Join(ws, "leo.yaml")
	cfg := fmt.Sprintf(`web:
  enabled: true
  bind: 127.0.0.1
  port: %d
templates:
  interactive:
    harness: codex
    model: gpt-5
    workspace: %s
    env:
      CODEX_HOME: %s
      FAKECLAUDE_DISPATCH_INTERACTIVE: "1"
      FAKECLAUDE_REPLY_DELAY_MS: "%d"
`, port, ws, ws, delayMS)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(leoBin, "service", "--supervised", "-c", cfgPath)
	cmd.Dir = ws
	cmd.Env = append(os.Environ(), "PATH="+filepath.Dir(fakeclaude)+":"+os.Getenv("PATH"))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting service: %v", err)
	}
	s := &interactiveE2E{t: t, ws: ws, cfgPath: cfgPath, port: port, tmux: tmuxPath, service: cmd, output: &output}
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	s.waitFor(t, func() bool {
		data, err := os.ReadFile(filepath.Join(ws, "state", "api.token"))
		if err != nil {
			return false
		}
		s.token = strings.TrimSpace(string(data))
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", port))
		if err == nil {
			resp.Body.Close()
		}
		return s.token != ""
	})
	return s
}

func (s *interactiveE2E) waitFor(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition did not become true")
}
func (s *interactiveE2E) waitForStatus(t *testing.T, id string, want consult.Status, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.record(t, id).Status == want {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("dispatch %s did not reach %s", id, want)
}

func (s *interactiveE2E) request(t *testing.T, method, path string, in, out any) {
	t.Helper()
	var body *bytes.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(raw)
	} else {
		body = bytes.NewReader(nil)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path), body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v\nservice output:\n%s", method, path, err, s.output.String())
	}
	defer resp.Body.Close()
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error string          `json:"error"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode/100 != 2 || !envelope.OK {
		t.Fatalf("%s %s: status=%d error=%q", method, path, resp.StatusCode, envelope.Error)
	}
	if out != nil {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			t.Fatal(err)
		}
	}
}

func (s *interactiveE2E) dispatch(t *testing.T, prompt string) consult.Started {
	t.Helper()
	var out consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]string{"template": "interactive", "prompt": prompt, "cwd": s.ws, "mode": "interactive"}, &out)
	return out
}
func (s *interactiveE2E) wait(t *testing.T, id string) consult.Entry {
	t.Helper()
	var out []consult.Entry
	s.request(t, http.MethodGet, "/api/dispatch/wait?id="+id+"&timeout=5", nil, &out)
	if len(out) != 1 {
		t.Fatalf("wait %s = %+v", id, out)
	}
	return out[0]
}
func (s *interactiveE2E) send(t *testing.T, id, message string) consult.SendResult {
	t.Helper()
	var out consult.SendResult
	s.request(t, http.MethodPost, "/api/dispatch/"+id+"/send", map[string]string{"message": message}, &out)
	return out
}
func (s *interactiveE2E) record(t *testing.T, id string) consult.Record {
	t.Helper()
	var out consult.Record
	s.request(t, http.MethodGet, "/api/dispatch/"+id, nil, &out)
	return out
}
func (s *interactiveE2E) cancel(t *testing.T, id string) {
	t.Helper()
	s.request(t, http.MethodPost, "/api/dispatch/"+id+"/cancel", nil, &struct{}{})
}
func (s *interactiveE2E) paneAlive(pane string) bool {
	out, err := exec.Command(s.tmux, tmux.Args("display-message", "-p", "-t", pane, "#{pane_dead}")...).Output()
	return err == nil && strings.TrimSpace(string(out)) == "0"
}
func (s *interactiveE2E) killPane(t *testing.T, pane string) {
	t.Helper()
	if err := exec.Command(s.tmux, tmux.Args("kill-pane", "-t", pane)...).Run(); err != nil {
		t.Fatalf("kill pane %s: %v", pane, err)
	}
}
func (s *interactiveE2E) windowExists(window string) bool {
	return exec.Command(s.tmux, tmux.Args("list-panes", "-t", tmux.Target("leo-dispatch")+":="+window)...).Run() == nil
}
