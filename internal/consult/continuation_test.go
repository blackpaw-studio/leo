package consult

import (
	"context"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestHeadlessContinuationAppendsTurnResumesAndAccumulatesUsage(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	var mu sync.Mutex
	var calls [][]string
	d.ExecCommandContext = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string(nil), args...))
		n := len(calls)
		mu.Unlock()
		output := `{"type":"result","session_id":"sid-1","result":"one","is_error":false,"usage":{"input_tokens":2,"output_tokens":3},"num_turns":1}`
		if n == 2 {
			output = `{"type":"result","session_id":"sid-1","result":"two","is_error":false,"usage":{"input_tokens":5,"output_tokens":7},"num_turns":1}`
		}
		return exec.CommandContext(ctx, "printf", "%s", output)
	}
	cfg := testConfig()
	cfg.Web.Enabled = true
	cfg.Delegation = &config.DelegationConfig{ActiveProfile: "p", Profiles: map[string]config.Profile{"p": {Roles: map[string]config.RoleTarget{"implement": {Template: "claude"}}}}}
	cwd := t.TempDir()
	started, err := d.Start(context.Background(), cfg, Request{Template: "claude", Prompt: "first", Cwd: cwd, Kind: "dispatch", Effort: "high"})
	if err != nil {
		t.Fatal(err)
	}
	first := d.Wait(context.Background(), []string{started.ID}, 5*time.Second)[0]
	if first.Text != "one" || first.TurnID != started.ID+"#1" {
		t.Fatalf("first = %+v", first)
	}
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "follow up")
	if err != nil {
		t.Fatal(err)
	}
	if sent.TurnID != started.ID+"#2" {
		t.Fatalf("turn = %q", sent.TurnID)
	}
	second := d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)[0]
	if second.Text != "two" || second.Outcome != TurnFinished {
		t.Fatalf("second = %+v", second)
	}
	rec, err := d.Get(started.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rec.Turns) != 2 || len(rec.UsageInvocations) != 2 {
		t.Fatalf("record = %+v", rec)
	}
	if rec.InputTokens == nil || *rec.InputTokens != 7 || rec.OutputTokens == nil || *rec.OutputTokens != 10 {
		t.Fatalf("usage = %+v", rec)
	}
	mu.Lock()
	argv := strings.Join(calls[1], " ")
	mu.Unlock()
	if !strings.Contains(argv, "--resume sid-1") || !strings.Contains(argv, "follow up") {
		t.Fatalf("resume argv = %q", argv)
	}
	want := []string{"-p", "follow up", "--model", "opus", "--max-turns", "15", "--output-format", "stream-json", "--verbose", "--effort", "high", "--resume", "sid-1", "--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`, "--add-dir", cwd, "--disallowed-tools", "Agent", "--settings", `{"enabledPlugins":{}}`}
	if !reflect.DeepEqual(calls[1], want) {
		t.Fatalf("resume argv = %#v, want %#v", calls[1], want)
	}
	for _, args := range calls {
		if strings.Contains(strings.Join(args, " "), "--append-system-prompt") {
			t.Fatalf("dispatch leaked system context into argv: %q", args)
		}
	}
}

func TestHeadlessContinuationRejectsBeforeMutation(t *testing.T) {
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	cfg := testConfig()
	state := &runState{record: Record{ID: "d-x", Mode: ModeHeadless, Status: StatusDone, Template: "claude", Harness: "claude", Cwd: t.TempDir()}, done: closedTestChannel()}
	d.runs[state.record.ID] = state
	if _, err := d.SendWithConfig(context.Background(), cfg, state.record.ID, "next"); err == nil || !strings.Contains(err.Error(), "session") {
		t.Fatalf("error = %v", err)
	}
	if len(state.record.Turns) != 0 || state.record.Status != StatusDone {
		t.Fatalf("mutated record: %+v", state.record)
	}
}

func closedTestChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }
