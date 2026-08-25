package picker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/agent"
	tea "github.com/charmbracelet/bubbletea"
)

// fakeBackend records calls so tests can assert an action was dispatched.
type fakeBackend struct {
	agents        []Agent
	calls         []string
	renameOld     string
	renameNew     string
	templates     []string
	templatesErr  error
	switchErr     error
	deletePlan    agent.DeletePlan
	deletePlanErr error
}

func (f *fakeBackend) Templates(context.Context) ([]string, error) {
	if f.templatesErr != nil {
		return nil, f.templatesErr
	}
	return f.templates, nil
}

func (f *fakeBackend) SwitchTemplate(_ context.Context, name, template string) error {
	f.calls = append(f.calls, "set-template:"+name+"->"+template)
	return f.switchErr
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
func (f *fakeBackend) Start(_ context.Context, name string) error {
	f.calls = append(f.calls, "start:"+name)
	return nil
}
func (f *fakeBackend) DeletePlan(_ context.Context, name string) (agent.DeletePlan, error) {
	f.calls = append(f.calls, "delete-plan:"+name)
	if f.deletePlanErr != nil {
		return agent.DeletePlan{}, f.deletePlanErr
	}
	return f.deletePlan, nil
}
func (f *fakeBackend) Delete(_ context.Context, name string, deleteBranch bool) error {
	call := "delete:" + name
	if deleteBranch {
		call += ":branch"
	}
	f.calls = append(f.calls, call)
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

// TestStopKeyDispatchesStopImmediately is the regression guard for stop
// losing its confirmation: stop is reversible now (Start brings the agent
// back), so 's' must fire Stop directly with no y/n gate.
func TestStopKeyDispatchesStopImmediately(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	_, _ = drive(t, m, keyRunes("s"))

	if len(fb.calls) != 1 || fb.calls[0] != "stop:alpha" {
		t.Fatalf("calls = %v, want [stop:alpha]", fb.calls)
	}
}

// TestStartKeyDispatchesStart covers the 'u' binding against a dormant row.
func TestStartKeyDispatchesStart(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "stopped"}}, nil)

	_, _ = drive(t, m, keyRunes("u"))

	if len(fb.calls) != 1 || fb.calls[0] != "start:alpha" {
		t.Fatalf("calls = %v, want [start:alpha]", fb.calls)
	}
}

// TestDeleteKeyArmsConfirmThenDeletes is the regression guard for D: it must
// fetch the DeletePlan, arm a y/n confirm naming exactly what will be
// removed, and only call Delete after 'y'.
func TestDeleteKeyArmsConfirmThenDeletes(t *testing.T) {
	fb := &fakeBackend{deletePlan: agent.DeletePlan{Name: "leo-pretty-sky", HasWorktree: true, Branch: "feat/foo"}}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "leo-pretty-sky", Host: LocalHost, Status: "stopped"}}, nil)

	// D fetches the plan (async) and arms the confirm — drive to let the
	// deletePlanMsg round-trip complete.
	m, _ = drive(t, m, keyRunes("D"))
	if m.confirming == nil {
		t.Fatalf("D should arm confirm once the plan resolves")
	}
	if m.confirming.message != "delete pretty-sky? removes worktree + branch feat/foo (y/n)" {
		t.Fatalf("confirm message = %q", m.confirming.message)
	}
	for _, call := range fb.calls {
		if call == "delete:leo-pretty-sky:branch" || call == "delete:leo-pretty-sky" {
			t.Fatalf("Delete must not fire before confirm; calls = %v", fb.calls)
		}
	}

	m, _ = drive(t, m, keyRunes("y"))
	if len(fb.calls) == 0 || fb.calls[len(fb.calls)-1] != "delete:leo-pretty-sky:branch" {
		t.Fatalf("calls = %v, want trailing delete:leo-pretty-sky:branch", fb.calls)
	}
}

// TestDeleteKeySharedAgentConfirmText covers the non-worktree confirm copy.
func TestDeleteKeySharedAgentConfirmText(t *testing.T) {
	fb := &fakeBackend{deletePlan: agent.DeletePlan{Name: "rocket", HasWorktree: false}}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "rocket", Host: LocalHost, Status: "stopped"}}, nil)

	m, _ = drive(t, m, keyRunes("D"))
	if m.confirming == nil {
		t.Fatalf("D should arm confirm once the plan resolves")
	}
	if m.confirming.message != "delete rocket? removes the agent record only (y/n)" {
		t.Fatalf("confirm message = %q", m.confirming.message)
	}

	m, _ = drive(t, m, keyRunes("n"))
	if m.confirming != nil {
		t.Fatalf("n should dismiss the confirm")
	}
	for _, call := range fb.calls {
		if call == "delete:rocket" {
			t.Fatalf("Delete must not fire after n; calls = %v", fb.calls)
		}
	}
}

// TestXKeyIsInert is the regression guard for the old stop-then-confirm
// muscle memory: x used to arm a confirm (with y finishing the stop). It must
// now do nothing at all, since a stray "x" "y" from habit must never delete
// or stop an agent.
func TestXKeyIsInert(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)

	m, _ = drive(t, m, keyRunes("x"))
	if m.confirming != nil {
		t.Fatalf("x must not arm any confirm")
	}
	m, _ = drive(t, m, keyRunes("y"))
	if len(fb.calls) != 0 {
		t.Fatalf("x-then-y must not dispatch anything; calls = %v", fb.calls)
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

// TestEnterOnDormantStartsThenAttaches is the regression guard for enter's
// generalized dormant handling: any dormant status starts the agent first,
// then attaches.
func TestEnterOnDormantStartsThenAttaches(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "stopped"}}, nil)

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.calls) != 1 || fb.calls[0] != "start:alpha" {
		t.Fatalf("calls = %v, want [start:alpha]", fb.calls)
	}
	if !quit {
		t.Fatalf("start-then-attach should quit the program")
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

// TestLifecycleGateBlocksBeforeAnyBackendCall is the parity regression guard
// for the picker's five lifecycle actions (stop/start/delete/rename/start-
// attach): a refusing CanLifecycle gate must stop dispatch before the backend
// is ever called, and surface the refusal as an error status line — exactly
// like the existing template-switch gate.
func TestLifecycleGateBlocksBeforeAnyBackendCall(t *testing.T) {
	tests := []struct {
		name   string
		status string
		key    string
		setup  func(t *testing.T, m model) model // extra steps before the gated key, e.g. arming delete confirm
	}{
		{name: "stop", status: "running", key: "s"},
		{name: "start", status: "stopped", key: "u"},
		{name: "rename", status: "running", key: "enter-rename"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fb := &fakeBackend{}
			m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
			m = sized(m)
			m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: tc.status}}, nil)
			m.canLifecycle = func(verb string) error {
				return errors.New("not permitted: " + verb)
			}

			switch tc.key {
			case "enter-rename":
				next, _ := m.Update(keyRunes("r"))
				m = next.(model)
				m.rename.SetValue("beta")
				m, _ = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
			default:
				m, _ = drive(t, m, keyRunes(tc.key))
			}

			if len(fb.calls) != 0 {
				t.Fatalf("a refused %s still reached the backend: %v", tc.name, fb.calls)
			}
			if !m.status.isErr || !contains(m.status.text, "not permitted") {
				t.Fatalf("status = %+v, want the refusal surfaced", m.status)
			}
		})
	}
}

// TestLifecycleGateBlocksDelete covers delete specifically, since it requires
// arming the confirm dialog first (a DeletePlan round-trip) before the gate
// is consulted on 'y'.
func TestLifecycleGateBlocksDelete(t *testing.T) {
	fb := &fakeBackend{deletePlan: agent.DeletePlan{Name: "alpha"}}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "stopped"}}, nil)
	m.canLifecycle = func(verb string) error {
		return errors.New("not permitted: " + verb)
	}

	m, _ = drive(t, m, keyRunes("D"))
	if m.confirming == nil {
		t.Fatalf("D should still arm the confirm dialog")
	}
	m, _ = drive(t, m, keyRunes("y"))

	// beginDelete's DeletePlan fetch is not gated (it mutates nothing), but
	// the actual Delete call must never fire.
	for _, call := range fb.calls {
		if strings.HasPrefix(call, "delete:") {
			t.Fatalf("a refused delete still reached the backend: %v", fb.calls)
		}
	}
	if !m.status.isErr || !contains(m.status.text, "not permitted") {
		t.Fatalf("status = %+v, want the refusal surfaced", m.status)
	}
}

// TestLifecycleGateBlocksStartAttach covers enter-on-dormant, which dispatches
// actionStartAttach rather than actionStart.
func TestLifecycleGateBlocksStartAttach(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "stopped"}}, nil)
	m.canLifecycle = func(verb string) error {
		return errors.New("not permitted: " + verb)
	}

	m, quit := drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})

	if len(fb.calls) != 0 {
		t.Fatalf("a refused start-attach still reached the backend: %v", fb.calls)
	}
	if quit {
		t.Fatalf("a refused start-attach must not quit the picker")
	}
	if !m.status.isErr || !contains(m.status.text, "not permitted") {
		t.Fatalf("status = %+v, want the refusal surfaced", m.status)
	}
}

// TestLifecycleGateAllowsPermittedActions guards against gating everything
// into uselessness: a permitting gate must still let each action reach the
// backend.
func TestLifecycleGateAllowsPermittedActions(t *testing.T) {
	fb := &fakeBackend{}
	m := newModel(context.Background(), map[string]Backend{LocalHost: fb})
	m = sized(m)
	m = loaded(m, LocalHost, []Agent{{Name: "alpha", Host: LocalHost, Status: "running"}}, nil)
	var asked []string
	m.canLifecycle = func(verb string) error {
		asked = append(asked, verb)
		return nil
	}

	_, _ = drive(t, m, keyRunes("s"))

	if len(fb.calls) != 1 || fb.calls[0] != "stop:alpha" {
		t.Fatalf("calls = %v, want [stop:alpha]", fb.calls)
	}
	if len(asked) != 1 {
		t.Fatalf("gate consulted %d times, want 1", len(asked))
	}
}

// TestLifecycleGateDoesNotBlockTemplateSwitch is the parity regression test:
// `leo agent set-template` is governed by gateTemplateSwitch alone, NOT
// leo_stop_agent, so a refusing CanLifecycle gate must never block a template
// switch — only CanSwitchTemplate governs that action.
func TestLifecycleGateDoesNotBlockTemplateSwitch(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex"},
	}
	m := menuModel(t, b)
	m.canLifecycle = func(verb string) error {
		return errors.New("not permitted: " + verb)
	}

	m, _ = drive(t, m, keyMsg("j"))
	m, _ = drive(t, m, keyMsg("enter"))

	if m.templates != nil {
		t.Error("menu should close once a template is confirmed")
	}
	var found bool
	for _, c := range b.calls {
		if c == "set-template:leo-coding-fetch->codex" {
			found = true
		}
	}
	if !found {
		t.Fatalf("calls = %v, want a set-template dispatch for codex despite the refusing lifecycle gate", b.calls)
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
	if !contains(r.line, "error:") || !contains(r.line, "connection refused") {
		t.Fatalf("error row line = %q", r.line)
	}
}
