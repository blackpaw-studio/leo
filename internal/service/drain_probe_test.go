package service

import (
	"context"
	"os/exec"
	"reflect"
	"slices"
	"testing"

	"github.com/blackpaw-studio/leo/internal/drain"
)

const drainProbeBusyCapture = `
❯ Describe the history of the Roman Empire exhaustively.

✽ Grooving…

────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────
  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`

const drainProbeIdleCapture = `
────────────────────────────────────────────────────────────────────────────────
❯ 
────────────────────────────────────────────────────────────────────────────────

  ⏵⏵ auto mode on (shift+tab to cycle) · ← 6 agents           ● high · /effort
`

func addEphemeral(sv *Supervisor, name, harnessName string) {
	id := newProcIdentity(name, nil)
	id.harness = harnessName
	id.setLegacy()
	sv.mu.Lock()
	sv.identities[name] = id
	sv.states[name] = &ProcessState{Name: name, Status: "running", Ephemeral: true}
	sv.mu.Unlock()
}

// fakeTmux answers list-panes with pane and capture-pane with the capture
// recorded for that pane, recording every argv.
type fakeTmux struct {
	panes    map[string]string // session target -> pane id
	captures map[string]string // pane id -> capture
	calls    [][]string
}

func (f *fakeTmux) command(_ context.Context, name string, args ...string) *exec.Cmd {
	f.calls = append(f.calls, append([]string{name}, args...))
	if i := slices.Index(args, "list-panes"); i >= 0 {
		return exec.Command("printf", "%s\n", f.panes[args[i+2]])
	}
	if i := slices.Index(args, "capture-pane"); i >= 0 {
		return exec.Command("printf", "%s", f.captures[args[i+3]])
	}
	return exec.Command("false")
}

func TestDrainProbeCapturesThePanesExactArgv(t *testing.T) {
	sv := NewSupervisor(context.Background())
	addEphemeral(sv, "alpha", "")
	fake := &fakeTmux{
		panes:    map[string]string{"=leo-alpha:": "%7"},
		captures: map[string]string{"%7": drainProbeBusyCapture},
	}
	p := drainProber{sup: sv, tmuxPath: "/opt/tmux", command: fake.command}

	got, err := p.Probe(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	want := drain.Probe{Name: "alpha", Exists: true, Busy: true, Transport: drain.TransportComposer}
	if got != want {
		t.Fatalf("Probe = %+v, want %+v", got, want)
	}
	wantCalls := [][]string{
		{"/opt/tmux", "-L", "leo", "list-panes", "-t", "=leo-alpha:", "-F", "#{pane_id}"},
		{"/opt/tmux", "-L", "leo", "capture-pane", "-p", "-t", "%7"},
	}
	if !reflect.DeepEqual(fake.calls, wantCalls) {
		t.Fatalf("tmux calls = %q, want %q", fake.calls, wantCalls)
	}
}

func TestDrainProbeMissingAgentDoesNotExist(t *testing.T) {
	sv := NewSupervisor(context.Background())
	fake := &fakeTmux{}
	p := drainProber{sup: sv, tmuxPath: "tmux", command: fake.command}
	got, err := p.Probe(context.Background(), "ghost")
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if got.Exists || got.Busy || got.Name != "ghost" {
		t.Fatalf("Probe(ghost) = %+v, want a non-existent probe", got)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("tmux called for a missing agent: %q", fake.calls)
	}
}

func TestDrainBusyAgentsReturnsOnlyBusy(t *testing.T) {
	sv := NewSupervisor(context.Background())
	addEphemeral(sv, "alpha", "")
	addEphemeral(sv, "beta", "claude")
	fake := &fakeTmux{
		panes:    map[string]string{"=leo-alpha:": "%1", "=leo-beta:": "%2"},
		captures: map[string]string{"%1": drainProbeIdleCapture, "%2": drainProbeBusyCapture},
	}
	p := drainProber{sup: sv, tmuxPath: "tmux", command: fake.command}
	got, err := p.BusyAgents(context.Background())
	if err != nil {
		t.Fatalf("BusyAgents: %v", err)
	}
	want := []drain.Probe{{Name: "beta", Exists: true, Busy: true, Transport: drain.TransportComposer}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("BusyAgents = %+v, want %+v", got, want)
	}
}
