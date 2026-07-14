package picker

import (
	"context"
	"errors"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeBackend records calls so tests can assert an action was dispatched.
type fakeBackend struct {
	agents    []Agent
	calls     []string
	renameOld string
	renameNew string
}

func (f *fakeBackend) List(context.Context) ([]Agent, error) {
	return f.agents, nil
}
func (f *fakeBackend) Rename(_ context.Context, oldName, newName string) error {
	f.calls = append(f.calls, "rename:"+oldName+"->"+newName)
	f.renameOld, f.renameNew = oldName, newName
	return nil
}
func (f *fakeBackend) Stop(_ context.Context, name string) error {
	f.calls = append(f.calls, "stop:"+name)
	return nil
}
func (f *fakeBackend) Suspend(_ context.Context, name string) error {
	f.calls = append(f.calls, "suspend:"+name)
	return nil
}
func (f *fakeBackend) Resume(_ context.Context, name string) error {
	f.calls = append(f.calls, "resume:"+name)
	return nil
}

// drive feeds a message to the model and, if it returns a command, executes
// that command and recursively feeds its result back in (except tea.Quit,
// which stops the drive and reports quit=true).
//
// The real Bubble Tea runtime never delivers a raw tea.BatchMsg to
// model.Update — it intercepts Batch commands itself (tea.go) and schedules
// each sub-command independently, in its own goroutine, with no ordering
// guarantee and no deadline. This harness has no such runtime underneath it,
// so every command — whether standalone or a sub-command unpacked from a
// tea.BatchMsg — is executed via driveCmd, which time-boxes the call. That
// matters because a single, non-batched command can itself resolve into a
// slow chain: e.g. bubbles' textinput cursor accepts an initialBlinkMsg and
// replies with a BlinkCmd that blocks for its ~530ms blink interval. Test
// assertions in this package only depend on sub-millisecond synchronous
// commands (action dispatch, filter matching, spinner ticks), so anything
// slower than driveCmdTimeout is simply abandoned rather than awaited.
func drive(t *testing.T, m model, msg tea.Msg) (model, bool) {
	t.Helper()
	next, cmd := m.Update(msg)
	nm := next.(model)
	if cmd == nil {
		return nm, false
	}
	return driveCmd(t, nm, cmd)
}

// driveCmdTimeout bounds how long driveCmd waits for a command to resolve.
// It must comfortably exceed every command this package's model actually
// depends on for its assertions (all synchronous, sub-millisecond) while
// staying well under bubbles' cursor-blink interval (~530ms) so that chain
// is reliably abandoned rather than awaited.
const driveCmdTimeout = 50 * time.Millisecond

// driveCmd executes cmd on its own goroutine and waits up to driveCmdTimeout
// for a result, discarding (never blocking on) anything slower.
func driveCmd(t *testing.T, m model, cmd tea.Cmd) (model, bool) {
	t.Helper()
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case out := <-ch:
		return driveMsg(t, m, out)
	case <-time.After(driveCmdTimeout):
		// The command didn't resolve promptly (e.g. cursor blink); not
		// needed for these assertions, so move on without it.
		return m, false
	}
}

// driveMsg feeds a single already-produced message back into the model. A
// tea.BatchMsg is unpacked sub-command by sub-command instead of being
// handed to model.Update directly, since the model (like the real runtime)
// does not know how to interpret a raw batch.
func driveMsg(t *testing.T, m model, out tea.Msg) (model, bool) {
	t.Helper()
	if out == nil {
		return m, false
	}
	if _, isQ := out.(tea.QuitMsg); isQ {
		return m, true
	}
	if batch, ok := out.(tea.BatchMsg); ok {
		quit := false
		for _, c := range batch {
			if c == nil {
				continue
			}
			var q bool
			m, q = driveCmd(t, m, c)
			if q {
				quit = true
			}
		}
		return m, quit
	}
	return drive(t, m, out)
}

func sized(m model) model {
	next, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return next.(model)
}

func loaded(m model, host string, ags []Agent, err error) model {
	next, _ := m.Update(rowsMsg{host: host, agents: ags, err: err})
	return next.(model)
}

func keyRunes(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func TestFilterNarrowsRows(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{
		{Name: "alpha", Host: LocalHost, Status: "running"},
		{Name: "beta", Host: LocalHost, Status: "running"},
	}, nil)

	if got := len(m.list.VisibleItems()); got != 2 {
		t.Fatalf("pre-filter visible = %d, want 2", got)
	}

	// Enter filter mode and type "alp". Filtering is async in bubbles v1
	// (list.filterItems runs as a Cmd), so each keystroke's Cmd must be
	// driven to completion for VisibleItems to reflect it.
	m, _ = drive(t, m, keyRunes("/"))
	for _, ch := range []string{"a", "l", "p"} {
		m, _ = drive(t, m, keyRunes(ch))
	}

	if got := len(m.list.VisibleItems()); got != 1 {
		t.Fatalf("filtered visible = %d, want 1", got)
	}
}

func TestSuspendKeyDispatchesSuspend(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	_, _ = drive(t, m, keyRunes("s"))

	if len(fb.calls) != 1 || fb.calls[0] != "suspend:alpha" {
		t.Fatalf("calls = %v, want [suspend:alpha]", fb.calls)
	}
}

func TestStopRequiresConfirm(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	// x arms the confirm prompt but does NOT call Stop yet.
	next, _ := m.Update(keyRunes("x"))
	m = next.(model)
	if m.confirming == nil {
		t.Fatalf("x should arm confirm")
	}
	if len(fb.calls) != 0 {
		t.Fatalf("Stop must not fire before confirm; calls = %v", fb.calls)
	}

	// y confirms and dispatches Stop.
	m, _ = drive(t, m, keyRunes("y"))
	if len(fb.calls) != 1 || fb.calls[0] != "stop:alpha" {
		t.Fatalf("calls = %v, want [stop:alpha]", fb.calls)
	}
}

func TestRenameRoundTrip(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	// r opens the rename input, pre-filled with the current name.
	next, _ := m.Update(keyRunes("r"))
	m = next.(model)
	if !m.renaming {
		t.Fatalf("r should enter rename mode")
	}

	// Clear and type a new name.
	m.rename.SetValue("auth-refactor")

	m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if fb.renameNew != "auth-refactor" || fb.renameOld != "alpha" {
		t.Fatalf("rename old=%q new=%q, want alpha->auth-refactor", fb.renameOld, fb.renameNew)
	}
}

func TestEnterOnSuspendedResumesThenAttaches(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "suspended"}}, nil)

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.calls) != 1 || fb.calls[0] != "resume:alpha" {
		t.Fatalf("calls = %v, want [resume:alpha]", fb.calls)
	}
	if !quit {
		t.Fatalf("resume-then-attach should quit the program")
	}
	if m.result.Agent == nil || m.result.Agent.Name != "alpha" {
		t.Fatalf("result = %+v, want alpha selected", m.result)
	}
}

func TestEnterOnRunningSelectsAndQuits(t *testing.T) {
	m := newModel(context.Background(), map[string]Backend{LocalHost: &fakeBackend{}})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if !quit || m.result.Agent == nil || m.result.Agent.Name != "alpha" {
		t.Fatalf("running Enter should quit with result; quit=%v result=%+v", quit, m.result)
	}
}

func TestHostFetchFailureRendersErrorRow(t *testing.T) {
	m := newModel(context.Background(), map[string]Backend{"hestia": &fakeBackend{}})
	m = sized(m)
	m = loaded(m, "hestia", nil, errors.New("connection refused"))

	items := m.list.Items()
	if len(items) != 1 {
		t.Fatalf("want 1 error row, got %d", len(items))
	}
	r := items[0].(row)
	if r.ag != nil {
		t.Fatalf("error row must have nil agent")
	}
	if !contains(r.desc, "connection refused") {
		t.Fatalf("error row desc = %q", r.desc)
	}
}
