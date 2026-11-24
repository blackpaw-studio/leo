package consult

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/harness"
)

// shellWords splits a launch command built from shellQuote'd words (and the
// bare words launchCommandWords adds) back into words.
func shellWords(t *testing.T, cmd string) []string {
	t.Helper()
	var words []string
	var cur strings.Builder
	inWord := false
	for i := 0; i < len(cmd); i++ {
		switch c := cmd[i]; c {
		case '\'':
			end := strings.IndexByte(cmd[i+1:], '\'')
			if end < 0 {
				t.Fatalf("unterminated quote in %q", cmd)
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case '"':
			end := strings.IndexByte(cmd[i+1:], '"')
			if end < 0 {
				t.Fatalf("unterminated double quote in %q", cmd)
			}
			cur.WriteString(cmd[i+1 : i+1+end])
			i += end + 1
			inWord = true
		case ' ':
			if inWord {
				words = append(words, cur.String())
				cur.Reset()
				inWord = false
			}
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	if inWord {
		words = append(words, cur.String())
	}
	return words
}

// grantedRecordStart starts a headless granted claude (or codex) explore run
// whose children print a session-carrying result, returning the dispatcher,
// the run and the recorded argv.
func grantedHeadless(t *testing.T, template, output string) (*Dispatcher, *config.Config, Started, func() [][]string, string) {
	t.Helper()
	h := newTargetHarness(t)
	var mu sync.Mutex
	var calls [][]string
	h.d.ExecCommandContext = func(ctx context.Context, _ string, args ...string) *exec.Cmd {
		mu.Lock()
		calls = append(calls, append([]string(nil), args...))
		mu.Unlock()
		return exec.CommandContext(ctx, "printf", "%s", output)
	}
	cfg := targetConfig("enforce")
	started, err := h.d.Start(context.Background(), cfg, Request{Template: template, Role: "explore", Prompt: "q", Kind: "dispatch", Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatal(err)
	}
	h.d.Wait(context.Background(), []string{started.ID}, 5*time.Second)
	return h.d, cfg, started, func() [][]string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(calls)
	}, h.leoWS
}

func TestHeadlessContinuationKeepsTheReadOnlySandbox(t *testing.T) {
	d, cfg, started, calls, ws := grantedHeadless(t, "claude", `{"type":"result","session_id":"sid-1","result":"ok","is_error":false}`)
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	argv := calls()[1]
	if !hasFlagValueIn(argv, "--resume", "sid-1") || !claudeSandboxed(t, argv, ws) {
		t.Fatalf("continuation argv = %q, want --resume sid-1 under the read-only sandbox", argv)
	}
}

func TestCodexContinuationKeepsReadOnlySandbox(t *testing.T) {
	d, cfg, started, calls, _ := grantedHeadless(t, "codex", `{"type":"thread.started","thread_id":"thr-1"}
{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`)
	sent, err := d.SendWithConfig(context.Background(), cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)
	argv := strings.Join(calls()[1], " ")
	if !strings.Contains(argv, "resume thr-1") || !strings.Contains(argv, "--sandbox read-only") || strings.Contains(argv, "writable_roots") {
		t.Fatalf("codex continuation argv = %s", argv)
	}
}

func hasFlagValueIn(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func TestInteractiveGrantedLaunchAndReopenCarryReadOnly(t *testing.T) {
	h := newTargetHarness(t)
	rt := &fakeInteractiveRuntime{arm: true, empty: true, alive: true}
	h.d.SetInteractiveRuntime(rt)
	started, err := h.d.Start(context.Background(), targetConfig("enforce"), Request{Template: "claude", Role: "explore", Prompt: "q", Kind: "dispatch", Mode: ModeInteractive, Caller: "mk-1", TargetAgent: "leo"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, h.d, started.ID)
	launches := rt.launchRequests()
	if len(launches) != 1 || launches[0].ReadOnly == nil || !slices.Contains(launches[0].ReadOnly.Workspaces, h.leoWS) {
		t.Fatalf("first launch ReadOnly = %+v", launches)
	}
}

func TestInteractiveReopenOfGrantedRunCarriesReadOnly(t *testing.T) {
	configDir := claudeConfigDir(t)
	h := newTargetHarness(t)
	rt := newPaneRuntime(true)
	rt.arm, rt.empty = true, true
	h.d.SetInteractiveRuntime(rt)
	h.d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	cfg := targetConfig("enforce")
	started, err := h.d.Start(context.Background(), cfg, Request{Template: "claude", Role: "explore", Prompt: "hello", Kind: "dispatch", Mode: ModeInteractive, Caller: "mk-1", TargetAgent: "leo", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1"})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, h.d, started.ID)
	if err := h.d.Report(started.ID, claudeHook(t, "submit-1", "UserPromptSubmit", "the opening brief")); err != nil {
		t.Fatal(err)
	}
	if err := h.d.Report(started.ID, claudeStop(t, "stop-1", "done", nil)); err != nil {
		t.Fatal(err)
	}
	h.d.SweepNotifications(context.Background())
	parked := requireParked(t, h.d, started.ID, started.ID+"#1")
	writeTranscript(t, configDir, parked.Cwd, parked.SessionID, nil)
	if _, err := h.d.SendWithConfig(context.Background(), cfg, started.ID, "more"); err != nil {
		t.Fatal(err)
	}
	launches := rt.launchRequests()
	if len(launches) != 2 || !launches[1].Reopen || launches[1].ReadOnly == nil || !slices.Contains(launches[1].ReadOnly.Workspaces, h.leoWS) {
		t.Fatalf("reopen launch = %+v", launches)
	}
}

// The runtime renders LaunchRequest.ReadOnly into the pane's real claude
// argv: one merged --settings with the sandbox beside the dispatch hooks, and
// --setting-sources user, on a first launch and on a --resume reopen.
func TestInteractiveRuntimeLaunchRendersReadOnlySandbox(t *testing.T) {
	dir := t.TempDir()
	ws := t.TempDir()
	cfg := &config.Config{HomePath: dir, Templates: map[string]config.TemplateConfig{"claude": {Harness: "claude", Model: "sonnet"}}}
	for _, resume := range []string{"", "sess-1"} {
		t.Run("resume="+resume, func(t *testing.T) {
			r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return cfg, nil }, nil, "tmux", "/opt/leo")
			var launch []string
			r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
				if slices.Contains(args, "new-window") {
					launch = append([]string(nil), args...)
					return exec.Command("echo", "%42")
				}
				return exec.Command("true")
			}
			req := LaunchRequest{ID: "d-ro" + resume, Harness: "claude", Template: "claude", Cwd: ws, Name: "work", Dispatched: true, Prompt: "go",
				ReadOnly: &harness.ReadOnlySandbox{Workspaces: []string{ws}}, ResumeSessionID: resume, Reopen: resume != ""}
			if _, _, err := r.Launch(context.Background(), req); err != nil {
				t.Fatal(err)
			}
			if len(launch) == 0 {
				t.Fatal("new-window was never called")
			}
			words := shellWords(t, launch[len(launch)-1])
			if strings.Count(launch[len(launch)-1], "'--settings'") != 1 {
				t.Fatalf("launch carries more than one --settings: %q", launch[len(launch)-1])
			}
			if !claudeSandboxed(t, words, ws) || !strings.Contains(launch[len(launch)-1], "UserPromptSubmit") {
				t.Fatalf("launch argv not sandboxed with hooks merged: %q", words)
			}
			if resume != "" && !hasFlagValueIn(words, "--resume", resume) {
				t.Fatalf("reopen argv lacks --resume %s: %q", resume, words)
			}
		})
	}
	_ = os.RemoveAll(dir)
}
