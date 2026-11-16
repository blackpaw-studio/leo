package consult

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestHeadlessContinuationKeepsAdditionalMCPServers(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	d, calls, mu, started, err := startMCPDispatchWith(t, f, []string{"chrome"})
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{started.ID}, 5*time.Second)

	sent, err := d.SendWithConfig(context.Background(), f.cfg, started.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	d.Wait(context.Background(), []string{sent.TurnID}, 5*time.Second)

	mu.Lock()
	second := (*calls)[1]
	mu.Unlock()
	assertArgvHasOnlyMCPPath(t, second.args, f.path(started.ID))
	if names := configServerNames(t, second.file); len(names) != 2 || names[0] != "chrome" || names[1] != "leo" {
		t.Fatalf("continuation file servers = %v, want [chrome leo]", names)
	}
	if _, err := os.Stat(f.path(started.ID)); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after the continuation: %v", err)
	}
}

func TestInteractiveStartUsesAdditionalMCPServers(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	d := NewDispatcher(newFakeRecorder())
	rt := &fakeInteractiveRuntime{arm: true, empty: true, alive: true}
	d.SetInteractiveRuntime(rt)

	started, err := d.Start(context.Background(), f.cfg, Request{Template: "claude", Prompt: "x", Cwd: t.TempDir(), Mode: ModeInteractive, Kind: "dispatch", AdditionalMCPServers: []string{"chrome", "chrome", "other"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)

	launches := rt.launchRequests()
	if len(launches) != 1 || !slices.Equal(launches[0].AdditionalMCPServers, []string{"chrome", "other"}) {
		t.Fatalf("launches = %+v", launches)
	}
}

func TestInteractiveReopenKeepsAdditionalMCPServers(t *testing.T) {
	configDir := claudeConfigDir(t)
	d := NewDispatcher(NewFileRecorder(t.TempDir()))
	rt := newPaneRuntime(true)
	rt.arm, rt.empty = true, true
	d.SetInteractiveRuntime(rt)
	d.SetNotificationDelivery(&fakeNotificationDelivery{ready: true})
	started, err := d.Start(context.Background(), testConfig(), Request{Template: "claude", Prompt: "hello", Cwd: gitTestRepo(t), Isolation: "worktree", Mode: ModeInteractive, Kind: "dispatch", Role: "implement", CallerPaneID: "%9", CallerHarness: "claude", CallerSessionID: "$1", AdditionalMCPServers: []string{"chrome"}})
	if err != nil {
		t.Fatal(err)
	}
	waitForArmed(t, d, started.ID)
	if err := d.Report(started.ID, claudeHook(t, "submit-1", "UserPromptSubmit", "the opening brief")); err != nil {
		t.Fatal(err)
	}
	if err := d.Report(started.ID, claudeStop(t, "stop-1", "done", nil)); err != nil {
		t.Fatal(err)
	}
	d.SweepNotifications(context.Background())
	parkedRec := requireParked(t, d, started.ID, started.ID+"#1")
	writeTranscript(t, configDir, parkedRec.Worktree, parkedRec.SessionID, nil)

	if _, err := d.SendWithConfig(context.Background(), testConfig(), started.ID, "more"); err != nil {
		t.Fatal(err)
	}

	launches := rt.launchRequests()
	if len(launches) != 2 || !launches[1].Reopen || !slices.Equal(launches[1].AdditionalMCPServers, []string{"chrome"}) {
		t.Fatalf("launches = %+v", launches)
	}
}

// The runtime resolves the request's additional servers into the private
// strict config it writes for the pane's claude.
func TestInteractiveLaunchWritesAdditionalMCPServers(t *testing.T) {
	f := newMCPDispatchFixture(t, nil)
	r := NewInteractiveRuntime("/tmp/leo.yaml", func() (*config.Config, error) { return f.cfg, nil }, nil, "tmux", "/opt/leo")
	r.ExecCommandContext = func(_ context.Context, _ string, args ...string) *exec.Cmd {
		if slices.Contains(args, "new-window") {
			return exec.Command("echo", "%42")
		}
		return exec.Command("true")
	}
	req := LaunchRequest{ID: "d-addl", Harness: "claude", Template: "claude", Cwd: t.TempDir(), Name: "work", Dispatched: true, Prompt: "go", AdditionalMCPServers: []string{"chrome"}}
	if _, _, err := r.Launch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.path("d-addl"))
	if err != nil {
		t.Fatal(err)
	}
	if names := configServerNames(t, data); !slices.Equal(names, []string{"chrome", "leo"}) {
		t.Fatalf("file servers = %v, want [chrome leo]", names)
	}
}
