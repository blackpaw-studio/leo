//go:build e2e

package e2e

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	claudeharness "github.com/blackpaw-studio/leo/internal/harness/claude"
	"github.com/blackpaw-studio/leo/internal/harness/claude/bridgemod"
	"github.com/blackpaw-studio/leo/internal/session"
)

// The real-claude bridge suite runs the installed claude (haiku) under a
// real `leo service`, so it costs a few cents of model usage per run. It
// skips when claude is missing, predates the mods API, or is not logged in.

const (
	bridgeTemplate = "bridged"
	// bridgeTurnTimeout bounds one haiku turn, including claude's boot.
	bridgeTurnTimeout = 2 * time.Minute
)

// realClaude returns the installed claude, skipping the test unless it can
// load the leo-bridge mod and is logged in.
func realClaude(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("claude")
	if err != nil {
		t.Skip("claude is not installed")
	}
	out, err := exec.Command(path, "--version").Output()
	if err != nil {
		t.Skipf("claude --version: %v", err)
	}
	v, err := bridgemod.ParseVersion(string(out))
	if err != nil || !bridgemod.SupportsMods(v) {
		t.Skipf("claude %q cannot load the leo-bridge mod (needs %s)", strings.TrimSpace(string(out)), bridgemod.MinClaudeVersion)
	}
	auth := exec.Command(path, "auth", "status")
	auth.Env = withUser(os.Environ())
	out, err = auth.Output()
	var status struct {
		LoggedIn bool `json:"loggedIn"`
	}
	if err != nil || json.Unmarshal(out, &status) != nil || !status.LoggedIn {
		t.Skip("claude is not logged in")
	}
	return path
}

// withUser adds USER when env lacks it: `make e2e` runs under `env -i`, and
// claude cannot read its macOS keychain credentials without USER.
func withUser(env []string) []string {
	for _, kv := range env {
		if strings.HasPrefix(kv, "USER=") {
			return env
		}
	}
	u, err := user.Current()
	if err != nil {
		return env
	}
	return append(env, "USER="+u.Username)
}

// bridgeWorkspace is a claude workspace kept across runs and trusted once,
// so the suite does not add a trust entry to ~/.claude.json per run.
func bridgeWorkspace(t *testing.T) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(cache, "leo-e2e", "claude-bridge")
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := (claudeharness.Claude{}).PrepareInteractive(home, ws); err != nil {
		t.Fatal(err)
	}
	return ws
}

// lockedBuffer collects the daemon's output while tests read it.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// bridgeE2E is a `leo service` whose claude agents load the leo bridge, on
// a disposable tmux server.
type bridgeE2E struct {
	t       *testing.T
	home    string
	ws      string
	cfgPath string
	port    int
	token   string
	service *exec.Cmd
	output  *lockedBuffer
}

type bridgeOptions struct {
	// breakBridge launches claude with an unusable LEO_BRIDGE_BIN, so the
	// mod loads but never connects.
	breakBridge bool
}

func newBridgeE2E(t *testing.T, opts bridgeOptions) *bridgeE2E {
	t.Helper()
	claudePath := realClaude(t)
	tmuxPath, socket := isolatedLeoTmux(t, "leo-e2e-bridge")
	ws := bridgeWorkspace(t)
	home := mkTempE2EDir(t, "leo-e2e-bridge-*")

	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(tmuxPath, filepath.Join(bin, "tmux")); err != nil {
		t.Fatal(err)
	}
	if opts.breakBridge {
		wrapper := "#!/bin/sh\nexport LEO_BRIDGE_BIN=/usr/bin/false\nexec '" + claudePath + "' \"$@\"\n"
		if err := os.WriteFile(filepath.Join(bin, "claude"), []byte(wrapper), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	port := freeTCPPort(t)
	cfgPath := filepath.Join(home, "leo.yaml")
	cfg := fmt.Sprintf(`web:
  enabled: true
  bind: 127.0.0.1
  port: %d
templates:
  %s:
    harness: claude
    model: haiku
    workspace: %s
    harness_options:
      # Agents ignore bypass_permissions; pre-approve the one tool the
      # suite uses so no approval prompt stalls a turn.
      allowed_tools: [Bash]
`, port, bridgeTemplate, ws)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	s := &bridgeE2E{t: t, home: home, ws: ws, cfgPath: cfgPath, port: port, output: &lockedBuffer{}}
	cmd := exec.Command(leoBin, "service", "--supervised", "-c", cfgPath)
	cmd.Dir = home
	cmd.Env = append(withUser(os.Environ()), "PATH="+bin+":"+os.Getenv("PATH"))
	cmd.Stdout, cmd.Stderr = s.output, s.output
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting service: %v", err)
	}
	s.service = cmd
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("panes:\n%s", capturePanes(socket))
		}
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		if t.Failed() {
			t.Logf("service output:\n%s\nservice.log:\n%s", s.output.String(), s.serviceLog())
		}
	})
	s.awaitReady()
	return s
}

// capturePanes renders every pane on the test's tmux server, for a failed
// run's log: a stalled turn usually shows why (a dialog, a prompt).
func capturePanes(socket string) string {
	out, err := exec.Command("tmux", "-L", socket, "list-panes", "-a", "-F", "#{pane_id} #{session_name}").Output()
	if err != nil {
		return "listing panes: " + err.Error()
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		id, _, _ := strings.Cut(line, " ")
		pane, _ := exec.Command("tmux", "-L", socket, "capture-pane", "-p", "-t", id).Output()
		fmt.Fprintf(&b, "=== %s\n%s\n", line, pane)
	}
	return b.String()
}

func (s *bridgeE2E) serviceLog() string {
	b, _ := os.ReadFile(filepath.Join(s.home, "state", "service.log"))
	return string(b)
}

// awaitReady waits for the API token and an authenticated state response.
func (s *bridgeE2E) awaitReady() {
	s.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(filepath.Join(s.home, "state", "api.token")); err == nil && len(bytes.TrimSpace(data)) > 0 {
			s.token = strings.TrimSpace(string(data))
			if code, _ := s.call(http.MethodGet, "/api/v1/state", nil); code == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	s.t.Fatalf("daemon not ready on port %d", s.port)
}

// call sends an authenticated request and returns the status and body.
func (s *bridgeE2E) call(method, path string, body any) (int, []byte) {
	s.t.Helper()
	var r io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			s.t.Fatal(err)
		}
		r = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, fmt.Sprintf("http://127.0.0.1:%d%s", s.port, path), r)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+s.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := (&http.Client{Timeout: 2 * time.Minute}).Do(req)
	if err != nil {
		return 0, []byte(err.Error())
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// spawn starts an agent from the bridged template with an opening prompt.
func (s *bridgeE2E) spawn(name, prompt string) {
	s.t.Helper()
	code, body := s.call(http.MethodPost, "/api/agent/spawn", map[string]string{"template": bridgeTemplate, "name": name, "prompt": prompt})
	if code/100 != 2 {
		s.t.Fatalf("spawn %s: %d %s", name, code, body)
	}
}

// message sends text to agent name the way leo_send_message does.
func (s *bridgeE2E) message(name, text, from string) (int, string) {
	s.t.Helper()
	code, body := s.call(http.MethodPost, "/web/agent/"+name+"/message", map[string]string{"text": text, "from": from})
	return code, string(body)
}

func (s *bridgeE2E) agent(name string) agent.Record {
	s.t.Helper()
	stdout, stderr, code := runLeo(s.t, s.home, nil, "agent", "list", "--json", "-c", s.cfgPath)
	if code != 0 {
		s.t.Fatalf("agent list: %s", stderr)
	}
	var recs []agent.Record
	if err := json.Unmarshal([]byte(stdout), &recs); err != nil {
		s.t.Fatalf("agent list: %v\n%s", err, stdout)
	}
	for _, r := range recs {
		if r.Name == name {
			return r
		}
	}
	s.t.Fatalf("agent %s not listed: %s", name, stdout)
	return agent.Record{}
}

// awaitBridge waits for agent name's bridge to report want.
func (s *bridgeE2E) awaitBridge(name, want string) {
	s.t.Helper()
	deadline := time.Now().Add(bridgeTurnTimeout)
	for time.Now().Before(deadline) {
		if s.agent(name).Bridge == want {
			return
		}
		time.Sleep(time.Second)
	}
	s.t.Fatalf("agent %s bridge never became %q (now %q)", name, want, s.agent(name).Bridge)
}

// transcript is agent name's claude session transcript.
func (s *bridgeE2E) transcript(name string) *transcript {
	s.t.Helper()
	id := pollAgentstoreSessionID(s.t, s.home, name, 30*time.Second)
	path, err := session.JSONLPath(s.ws, id)
	if err != nil {
		s.t.Fatal(err)
	}
	return &transcript{t: s.t, path: path}
}

// transcript reads a claude session JSONL.
type transcript struct {
	t    *testing.T
	path string
}

// with returns the transcript reporting failures to t.
func (tr *transcript) with(t *testing.T) *transcript { return &transcript{t: t, path: tr.path} }

type transcriptLine struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message struct {
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// lines returns the transcript's parsed lines (none while it is unwritten).
func (tr *transcript) lines() []transcriptLine {
	f, err := os.Open(tr.path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []transcriptLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var l transcriptLine
		if json.Unmarshal(sc.Bytes(), &l) == nil {
			out = append(out, l)
		}
	}
	return out
}

// assistantText is the text of an assistant line's text blocks.
func assistantText(l transcriptLine) string {
	if l.Type != "assistant" {
		return ""
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(l.Message.Content, &blocks) != nil {
		return ""
	}
	var b strings.Builder
	for _, blk := range blocks {
		if blk.Type == "text" {
			b.WriteString(blk.Text)
		}
	}
	return b.String()
}

// index is the line number of the first line matching, or -1.
func (tr *transcript) index(match func(transcriptLine) bool) int {
	for i, l := range tr.lines() {
		if match(l) {
			return i
		}
	}
	return -1
}

// await waits for a line matching and returns its line number.
func (tr *transcript) await(what string, match func(transcriptLine) bool) int {
	tr.t.Helper()
	deadline := time.Now().Add(bridgeTurnTimeout)
	for time.Now().Before(deadline) {
		if i := tr.index(match); i >= 0 {
			return i
		}
		time.Sleep(500 * time.Millisecond)
	}
	tr.t.Fatalf("transcript %s never showed %s", tr.path, what)
	return -1
}

// said matches an assistant reply containing needle.
func said(needle string) func(transcriptLine) bool {
	return func(l transcriptLine) bool { return strings.Contains(assistantText(l), needle) }
}

// ranTool matches an assistant tool call whose input mentions needle.
func ranTool(needle string) func(transcriptLine) bool {
	return func(l transcriptLine) bool {
		return l.Type == "assistant" && strings.Contains(string(l.Message.Content), `"tool_use"`) && strings.Contains(string(l.Message.Content), needle)
	}
}

// compacted matches claude's compaction boundary.
func compacted(l transcriptLine) bool { return l.Subtype == "compact_boundary" }
