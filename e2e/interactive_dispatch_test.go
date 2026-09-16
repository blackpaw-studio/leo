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

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/blackpaw-studio/leo/internal/consult"
	"github.com/blackpaw-studio/leo/internal/tmux"
)

func TestInteractiveReleasePane(t *testing.T) {
	s := newInteractiveE2E(t)
	session := "release-caller"
	out, err := exec.Command(s.tmux, tmux.Args("new-session", "-d", "-P", "-F", "#{pane_id} #{window_id}", "-s", session, "sleep", "60")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	ids := strings.Fields(string(out))
	if len(ids) != 2 {
		t.Fatalf("identity=%q", out)
	}
	caller, window := ids[0], ids[1]
	otherOut, err := exec.Command(s.tmux, tmux.Args("new-window", "-d", "-P", "-F", "#{window_id}", "-t", "="+session, "sleep", "60")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	other := strings.TrimSpace(string(otherOut))
	before, _ := exec.Command(s.tmux, tmux.Args("display-message", "-p", "-t", other, "#{window_layout}")...).Output()
	var started consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]string{"template": "interactive", "prompt": "release me", "cwd": s.ws, "mode": "interactive", "caller_pane_id": caller}, &started)
	_ = s.wait(t, started.ID+"#1")
	rec := s.record(t, started.ID)
	paneWindow, err := exec.Command(s.tmux, tmux.Args("display-message", "-p", "-t", rec.PaneID, "#{window_id}")...).Output()
	if err != nil || strings.TrimSpace(string(paneWindow)) != window {
		t.Fatalf("pane window=%q want=%q err=%v", paneWindow, window, err)
	}
	beforePanes, err := exec.Command(s.tmux, tmux.Args("list-panes", "-t", window, "-F", "#{pane_id}")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	s.request(t, http.MethodPost, "/api/dispatch/"+started.ID+"/release", nil, &struct{}{})
	s.waitFor(t, func() bool { return !s.paneAlive(rec.PaneID) })
	if got := s.record(t, started.ID); got.Status != consult.StatusReleased {
		t.Fatalf("status=%s", got.Status)
	}
	afterPanes, err := exec.Command(s.tmux, tmux.Args("list-panes", "-t", window, "-F", "#{pane_id}")...).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(afterPanes), rec.PaneID) || len(strings.Fields(string(afterPanes))) != len(strings.Fields(string(beforePanes)))-1 {
		t.Fatalf("release panes before=%q after=%q released=%q", beforePanes, afterPanes, rec.PaneID)
	}
	after, _ := exec.Command(s.tmux, tmux.Args("display-message", "-p", "-t", other, "#{window_layout}")...).Output()
	if string(after) != string(before) {
		t.Fatalf("other layout changed: %q -> %q", before, after)
	}
}

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
	openingPrompt := strings.Join([]string{
		"You are a subagent dispatched by an orchestrator. Do not spawn agents or run your own code review; the orchestrator reviews your work.",
		"opening prompt",
	}, " ")
	wantOpening := "FAKE-REPLY: " + truncate80(strings.TrimSpace(openingPrompt))
	if first.Outcome != consult.TurnFinished || first.Text != wantOpening {
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
	s.waitFor(t, func() bool {
		turns := s.record(t, started.ID).Turns
		return len(turns) == 3 && turns[2].Outcome == consult.TurnFinished
	})
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

func TestInteractiveDispatchConfirmsLongMultilinePreamble(t *testing.T) {
	s := newInteractiveE2E(t)
	lines := make([]string, 160)
	for i := range lines {
		lines[i] = "\tplaceholder line " + strings.Repeat("x", 40)
	}
	prompt := strings.Join(lines, "\n\n")
	started := s.dispatch(t, prompt)
	first := s.wait(t, started.ID+"#1")
	if !strings.HasPrefix(first.Text, "You are a subagent dispatched by an orchestrator.") {
		t.Fatalf("first dispatched turn = %+v", first)
	}
}

func TestInteractiveDispatchRosterWithNoLocale(t *testing.T) {
	s := newInteractiveE2EWithoutLocale(t)
	s.dispatch(t, "roster without locale")

	s.waitFor(t, func() bool {
		out, err := exec.Command(s.tmux, tmux.Args("show-options", "-qv", "-t", tmux.Target("leo-dispatch")+":", "@leo_roster")...).Output()
		return err == nil && strings.Contains(string(out), "interactive")
	})
}

func TestInteractivePaneDeath(t *testing.T) {
	t.Run("after finished turn closes", func(t *testing.T) {
		s := newInteractiveE2E(t)
		started := s.dispatch(t, "complete first")
		_ = s.wait(t, started.ID+"#1")
		rec := s.record(t, started.ID)
		s.killPane(t, rec.PaneID)
		// Bound: 5s detection + 30s final-report grace + 5s sweep tick.
		s.waitForStatus(t, started.ID, consult.StatusClosed, 75*time.Second)
	})
	t.Run("before a turn finishes fails", func(t *testing.T) {
		s := newInteractiveE2EWithDelay(t, 3000)
		started := s.dispatch(t, "kill before reply")
		rec := s.record(t, started.ID)
		s.killPane(t, rec.PaneID)
		// Bound: 5s detection + 30s final-report grace + 5s sweep tick.
		s.waitForStatus(t, started.ID, consult.StatusFailed, 75*time.Second)
	})
}

func TestInteractiveDaemonRestart(t *testing.T) {
	s := newInteractiveE2E(t)
	started := s.dispatch(t, "complete before daemon restart")
	_ = s.wait(t, started.ID+"#1")
	rec := s.record(t, started.ID)
	if !s.paneAlive(rec.PaneID) {
		t.Fatal("interactive pane died before daemon restart")
	}

	s.restartDaemon(t)
	s.waitForStatus(t, started.ID, consult.StatusClosed, 10*time.Second)
	if s.paneAlive(rec.PaneID) {
		t.Fatal("orphan interactive pane survived daemon restart")
	}
}

func TestInteractiveCallerRestart(t *testing.T) {
	t.Skip("fake ephemeral agent does not stay up under supervision in the e2e daemon; caller-restart is covered live (tracked as a follow-up issue)")
	s := newInteractiveE2E(t)
	caller := s.spawnAgent(t, "caller")
	session := agent.SessionName(caller)
	s.waitForSession(t, session)
	primaryPane := s.sessionPane(t, session)

	started := s.dispatchFrom(t, "caller restart", caller)
	s.waitFor(t, func() bool { return s.windowExistsInSession(session, started.Window) })

	s.killPane(t, primaryPane)
	s.waitFor(t, func() bool { return !s.sessionExists(session) })
	s.waitFor(t, func() bool { return s.sessionExists(session) })
	if s.windowExistsInSession(session, started.Window) {
		t.Fatal("subagent window survived caller primary-pane death")
	}
}

type interactiveE2E struct {
	t           *testing.T
	ws          string
	cfgPath     string
	port        int
	token       string
	tmux        string
	service     *exec.Cmd
	output      *bytes.Buffer
	stripLocale bool
}

func newInteractiveE2E(t *testing.T) *interactiveE2E {
	return newInteractiveE2EWithDelay(t, 0)
}

func newInteractiveE2EWithDelay(t *testing.T, delayMS int) *interactiveE2E {
	return newInteractiveE2EWithOptions(t, delayMS, false)
}

func newInteractiveE2EWithoutLocale(t *testing.T) *interactiveE2E {
	return newInteractiveE2EWithOptions(t, 0, true)
}

func newInteractiveE2EWithOptions(t *testing.T, delayMS int, stripLocale bool) *interactiveE2E {
	tmuxPath, err := exec.LookPath("tmux")
	if err != nil {
		t.Fatalf("tmux is required for interactive e2e: %v", err)
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
  caller:
    harness: codex
    model: gpt-5
    workspace: %s
    env:
      CODEX_HOME: %s
      FAKECLAUDE_DISPATCH_INTERACTIVE: "1"
      FAKECLAUDE_ARGLOG: %s
`, port, ws, ws, delayMS, ws, ws, filepath.Join(ws, "caller-args.json"))
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &interactiveE2E{t: t, ws: ws, cfgPath: cfgPath, port: port, tmux: tmuxPath, stripLocale: stripLocale}
	s.startDaemon(t)
	t.Cleanup(func() {
		if s.service != nil && s.service.Process != nil {
			_ = s.service.Process.Kill()
			_ = s.service.Wait()
		}
	})
	// api.token is written before the HTTP listener is necessarily accepting
	// requests. Treat an authenticated state response as the daemon readiness
	// boundary so callers cannot race startup with their first mutation.
	deadline := time.Now().Add(30 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(ws, "state", "api.token"))
		if err != nil {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		s.token = strings.TrimSpace(string(data))
		if s.token == "" {
			time.Sleep(50 * time.Millisecond)
			continue
		}
		req, err := http.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", port), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+s.token)
		resp, err := client.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("daemon did not become ready on port %d\nservice output:\n%s", port, s.output.String())
	return s
}

func (s *interactiveE2E) startDaemon(t *testing.T) {
	t.Helper()
	cmd := exec.Command(leoBin, "service", "--supervised", "-c", s.cfgPath)
	cmd.Dir = s.ws
	cmd.Env = append(s.daemonEnv(), "PATH="+filepath.Dir(fakeclaude)+":"+os.Getenv("PATH"))
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting service: %v", err)
	}
	s.service, s.output = cmd, &output
}

func (s *interactiveE2E) daemonEnv() []string {
	base := os.Environ()
	if !s.stripLocale {
		return base
	}
	filtered := make([]string, 0, len(base))
	for _, entry := range base {
		if strings.HasPrefix(entry, "LANG=") || strings.HasPrefix(entry, "LC_ALL=") || strings.HasPrefix(entry, "LC_CTYPE=") {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func (s *interactiveE2E) restartDaemon(t *testing.T) {
	t.Helper()
	if err := s.service.Process.Kill(); err != nil {
		t.Fatalf("killing service: %v", err)
	}
	_ = s.service.Wait()
	s.startDaemon(t)
	s.waitFor(t, func() bool {
		resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/api/v1/state", s.port))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	})
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
	return s.dispatchFrom(t, prompt, "")
}
func (s *interactiveE2E) dispatchFrom(t *testing.T, prompt, from string) consult.Started {
	t.Helper()
	var out consult.Started
	s.request(t, http.MethodPost, "/api/dispatch", map[string]string{"template": "interactive", "prompt": prompt, "cwd": s.ws, "mode": "interactive", "from": from}, &out)
	return out
}
func (s *interactiveE2E) spawnAgent(t *testing.T, name string) string {
	t.Helper()
	var out struct {
		Name string `json:"name"`
	}
	s.request(t, http.MethodPost, "/api/agent/spawn", map[string]string{"template": "caller", "name": name, "prompt": "stay alive"}, &out)
	if out.Name == "" {
		t.Fatal("agent spawn returned no name")
	}
	return out.Name
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
	return s.windowExistsInSession("leo-dispatch", window)
}
func (s *interactiveE2E) windowExistsInSession(session, window string) bool {
	return exec.Command(s.tmux, tmux.Args("list-panes", "-t", tmux.Target(session)+":="+window)...).Run() == nil
}
func (s *interactiveE2E) sessionExists(session string) bool {
	return exec.Command(s.tmux, tmux.Args("has-session", "-t", tmux.Target(session))...).Run() == nil
}
func (s *interactiveE2E) waitForSession(t *testing.T, session string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if s.sessionExists(session) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	sessions, _ := exec.Command(s.tmux, tmux.Args("list-sessions")...).CombinedOutput()
	var agents json.RawMessage
	s.request(t, http.MethodGet, "/api/agent/list", nil, &agents)
	processFiles, _ := os.ReadDir(filepath.Join(s.ws, "state", "processes"))
	args, _ := os.ReadFile(filepath.Join(s.ws, "caller-args.json"))
	var stderr string
	for _, file := range processFiles {
		if strings.HasSuffix(file.Name(), ".stderr") {
			body, _ := os.ReadFile(filepath.Join(s.ws, "state", "processes", file.Name()))
			stderr += file.Name() + ": " + string(body)
		}
	}
	t.Fatalf("session %q did not start\ntmux sessions:\n%sagents: %s\ncaller args: %s\nprocess stderr: %s\nservice output:\n%s", session, sessions, agents, args, stderr, s.output.String())
}
func (s *interactiveE2E) sessionPane(t *testing.T, session string) string {
	t.Helper()
	out, err := exec.Command(s.tmux, tmux.Args("list-panes", "-t", tmux.Target(session), "-F", "#{pane_id}")...).Output()
	if err != nil || strings.TrimSpace(string(out)) == "" {
		args, _ := os.ReadFile(filepath.Join(s.ws, "caller-args.json"))
		t.Fatalf("listing panes for %q: %v (%s); caller args: %s; service output: %s", session, err, out, args, s.output.String())
	}
	return strings.Fields(string(out))[0]
}
