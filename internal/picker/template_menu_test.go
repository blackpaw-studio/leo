package picker

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyMsg(s string) tea.KeyMsg {
	if len(s) == 1 {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
	switch s {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "up":
		return tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		return tea.KeyMsg{Type: tea.KeyDown}
	}
	panic("unhandled key " + s)
}

// menuModel returns a model with one loaded local agent and the template menu
// already open on it.
func menuModel(t *testing.T, b *fakeBackend) model {
	t.Helper()
	m := newModel(context.Background(), map[string]Backend{LocalHost: b})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = drive(t, m, rowsMsg{host: LocalHost, agents: b.agents})
	m, _ = drive(t, m, keyMsg("t"))
	return m
}

func TestTemplateMenuOpensAndMarksCurrent(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex", "review"},
	}
	m := menuModel(t, b)

	if m.templates == nil {
		t.Fatal("pressing t did not open the template menu")
	}
	if m.templates.agent != "leo-coding-fetch" || m.templates.current != "coding" {
		t.Fatalf("menu = %+v, want it bound to the selected row and its current template", m.templates)
	}
	view := m.View()
	if !strings.Contains(view, "codex") || !strings.Contains(view, "review") {
		t.Errorf("menu view missing template options:\n%s", view)
	}
	if !strings.Contains(view, "current") {
		t.Errorf("menu view does not mark the agent's current template:\n%s", view)
	}
}

func TestTemplateMenuEnterDispatchesSwitch(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex", "review"},
	}
	m := menuModel(t, b)

	// Move off the current template ("coding") onto "codex", then confirm.
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
		t.Fatalf("calls = %v, want a set-template dispatch for codex", b.calls)
	}
}

func TestTemplateMenuEscCancels(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex"},
	}
	m := menuModel(t, b)
	m, _ = drive(t, m, keyMsg("j"))
	m, _ = drive(t, m, keyMsg("esc"))

	if m.templates != nil {
		t.Error("esc should close the template menu")
	}
	if len(b.calls) != 0 {
		t.Errorf("esc dispatched something: %v", b.calls)
	}
}

// Choosing the template the agent already runs is a no-op the picker answers
// locally: bouncing an agent to land it exactly where it already is would cost
// a conversation-free restart for nothing.
func TestTemplateMenuCurrentTemplateIsNoOp(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex"},
	}
	m := menuModel(t, b)
	m, _ = drive(t, m, keyMsg("enter")) // cursor starts on the current template

	if m.templates != nil {
		t.Error("menu should close")
	}
	if len(b.calls) != 0 {
		t.Errorf("selecting the current template dispatched %v, want nothing", b.calls)
	}
	if !strings.Contains(m.status.text, "already") {
		t.Errorf("status = %q, want it to say the agent is already on that template", m.status.text)
	}
}

func TestTemplateMenuRefusesAttachOnlyRows(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-remote", Host: LocalHost, Status: "running", AttachOnly: true}},
		templates: []string{"coding"},
	}
	m := newModel(context.Background(), map[string]Backend{LocalHost: b})
	m, _ = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 24})
	m, _ = drive(t, m, rowsMsg{host: LocalHost, agents: b.agents})
	m, _ = drive(t, m, keyMsg("t"))

	if m.templates != nil {
		t.Error("attach-only rows have no template to switch")
	}
	if !m.status.isErr {
		t.Errorf("status = %+v, want an error explaining the row is attach-only", m.status)
	}
}

func TestTemplateMenuReportsLoadFailure(t *testing.T) {
	b := &fakeBackend{
		agents:       []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templatesErr: errors.New("ssh: connect timed out"),
	}
	m := menuModel(t, b)

	if m.templates != nil {
		t.Error("menu should close when its templates cannot be loaded")
	}
	if !m.status.isErr || !strings.Contains(m.status.text, "ssh") {
		t.Errorf("status = %+v, want the backend error surfaced", m.status)
	}
}

// The menu replaces the agent list, so it must occupy exactly the same number
// of lines — otherwise the status bar and help footer jump as it opens, closes,
// or scrolls. Both directions matter: a short template list has to be padded
// out to the list's height, and a long one windowed down to it.
func TestTemplateMenuKeepsLayoutHeightStable(t *testing.T) {
	long := make([]string, 0, 30)
	for i := 0; i < 30; i++ {
		long = append(long, "template-"+string(rune('a'+i%26))+string(rune('0'+i/26)))
	}

	// Enough agents to make the list paginate, which adds a rendered line
	// beyond its content height.
	manyAgents := make([]Agent, 0, 50)
	for i := 0; i < 50; i++ {
		manyAgents = append(manyAgents, Agent{
			Name: fmt.Sprintf("leo-a%02d", i), Template: "coding", Host: LocalHost, Status: "running",
		})
	}

	tests := []struct {
		name      string
		templates []string
		agents    []Agent
	}{
		{name: "fewer templates than rows", templates: []string{"coding", "codex", "review"}},
		{name: "more templates than rows", templates: long},
		{name: "paginated agent list", templates: []string{"coding", "codex"}, agents: manyAgents},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			agents := tc.agents
			if agents == nil {
				agents = []Agent{{Name: "leo-coding-fetch", Template: tc.templates[0], Host: LocalHost, Status: "running"}}
			}
			b := &fakeBackend{
				agents:    agents,
				templates: tc.templates,
			}
			base := newModel(context.Background(), map[string]Backend{LocalHost: b})
			base, _ = drive(t, base, tea.WindowSizeMsg{Width: 100, Height: 24})
			base, _ = drive(t, base, rowsMsg{host: LocalHost, agents: b.agents})
			listHeight := strings.Count(base.View(), "\n")

			m, _ := drive(t, base, keyMsg("t"))
			if got := strings.Count(m.View(), "\n"); got != listHeight {
				t.Errorf("view height with the menu open = %d lines, want %d (same as the list it replaces)", got, listHeight)
			}

			// Scrolling to the end must not change it either.
			for i := 0; i < len(tc.templates); i++ {
				m, _ = drive(t, m, keyMsg("j"))
			}
			if got := strings.Count(m.View(), "\n"); got != listHeight {
				t.Errorf("view height after scrolling to the end = %d lines, want %d", got, listHeight)
			}
		})
	}
}

// The picker is a second door onto the same action, so it has to honor the same
// can_spawn allowlist the CLI verb enforces — otherwise a template forbidden
// from spawning codex could reach codex by pressing t.
func TestTemplateMenuHonorsTheSwitchGate(t *testing.T) {
	b := &fakeBackend{
		agents:    []Agent{{Name: "leo-coding-fetch", Template: "coding", Host: LocalHost, Status: "running"}},
		templates: []string{"coding", "codex"},
		switchErr: errors.New("not permitted to spawn template \"codex\""),
	}
	m := menuModel(t, b)
	m, _ = drive(t, m, keyMsg("j"))
	m, _ = drive(t, m, keyMsg("enter"))

	if !m.status.isErr || !strings.Contains(m.status.text, "not permitted") {
		t.Errorf("status = %+v, want the refusal surfaced to the user", m.status)
	}
}
