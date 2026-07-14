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
	listErr   error
	calls     []string
	renameOld string
	renameNew string
}

func (f *fakeBackend) List(context.Context) ([]Agent, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
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

// drive feeds a message to the model, runs the returned command to completion
// (recursively feeding produced messages except tea.Quit), and returns the
// resulting model. Batched/tick commands other than Quit are executed once.
func drive(t *testing.T, m model, msg tea.Msg) (model, bool) {
	t.Helper()
	next, cmd := m.Update(msg)
	nm := next.(model)
	quit := false
	if cmd != nil {
		if isQuit(cmd) {
			return nm, true
		}
		// Execute the command and feed non-quit messages back in.
		out := cmd()
		if out != nil {
			if _, isQ := out.(tea.QuitMsg); isQ {
				return nm, true
			}
			nm, quit = drive(t, nm, out)
		}
	}
	return nm, quit
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
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

	// Enter filter mode and type "alp".
	next, _ := m.Update(keyRunes("/"))
	m = next.(model)
	for _, ch := range []string{"a", "l", "p"} {
		next, _ = m.Update(keyRunes(ch))
		m = next.(model)
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

	m, _ = drive(t, m, keyRunes("s"))

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
	_ = time.Now
}
