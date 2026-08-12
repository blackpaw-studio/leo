package picker

import (
	"context"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// beginTemplateMenu opens the template chooser for the selected row and asks
// that row's host for its templates. The menu renders immediately (empty,
// while the host answers) so the keypress never looks dropped on a slow SSH
// backend.
func (m model) beginTemplateMenu() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	b, ok := m.backends[r.host]
	if !ok {
		m.status = statusLine{text: "unknown host " + r.host, isErr: true}
		return m, nil
	}
	m.templates = &templateMenu{host: r.host, agent: r.ag.Name, current: r.ag.Template}
	m.status = statusLine{}
	return m, templatesCmd(m.ctx, r.host, b)
}

// onTemplatesLoaded fills the open menu with its host's templates, or closes it
// with the error — an empty menu the user can only escape from would be worse
// than being told why it is empty.
func (m model) onTemplatesLoaded(msg templatesMsg) (tea.Model, tea.Cmd) {
	if m.templates == nil || m.templates.host != msg.host {
		return m, nil // the menu was cancelled while its host was answering
	}
	if msg.err != nil {
		m.templates = nil
		m.status = statusLine{text: "templates: " + msg.err.Error(), isErr: true}
		return m, nil
	}
	if len(msg.names) == 0 {
		m.templates = nil
		m.status = statusLine{text: "no templates configured on " + msg.host, isErr: true}
		return m, nil
	}

	menu := *m.templates
	menu.options = msg.names
	menu.cursor = templateIndex(msg.names, menu.current)
	m.templates = &menu
	return m, nil
}

// updateTemplateMenu owns every key while the chooser is open.
func (m model) updateTemplateMenu(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	menu := *m.templates
	switch msg.String() {
	case "esc", "ctrl+c", "q":
		m.templates = nil
		return m, nil
	case "up", "k":
		if menu.cursor > 0 {
			menu.cursor--
		}
		m.templates = &menu
		return m, nil
	case "down", "j":
		if menu.cursor < len(menu.options)-1 {
			menu.cursor++
		}
		m.templates = &menu
		return m, nil
	case "enter":
		if len(menu.options) == 0 {
			return m, nil // still loading
		}
		chosen := menu.options[menu.cursor]
		m.templates = nil
		if chosen == menu.current {
			// Answered here rather than at the daemon: a switch to the
			// template the agent already runs would bounce the process for no
			// change at all.
			m.status = statusLine{text: menu.agent + " is already on " + chosen, isErr: false}
			return m, nil
		}
		m.switchTo = chosen
		return m.dispatch(menu.host, menu.agent, actionSwitchTemplate)
	}
	return m, nil
}

// templateMenuView renders the chooser in place of the agent list, padded to
// the height of the list's RENDERED view so the footer does not jump as the
// menu opens and closes. Measuring the render rather than m.list.Height() is
// what makes that hold: once there are enough agents to paginate, the list
// draws its pagination line beyond its content height.
func (m model) templateMenuView() string {
	menu := m.templates
	height := strings.Count(m.list.View(), "\n") + 1

	lines := make([]string, 0, height)
	if len(menu.options) == 0 {
		lines = append(lines, "  loading templates…")
	}
	for i, name := range menu.options {
		label := "  " + name
		if name == menu.current {
			label += " (current)"
		}
		if i == menu.cursor {
			lines = append(lines, m.styles.prompt.Render("❯ "+strings.TrimPrefix(label, "  ")))
			continue
		}
		lines = append(lines, label)
	}

	// Keep the window around the cursor when there are more templates than
	// rows to show them in.
	if len(lines) > height && height > 0 {
		start := menu.cursor - height/2
		if start < 0 {
			start = 0
		}
		if start+height > len(lines) {
			start = len(lines) - height
		}
		lines = lines[start : start+height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// templatesCmd fetches one host's template names with the same per-host
// timeout the list and action calls use.
func templatesCmd(ctx context.Context, host string, b Backend) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, hostTimeout)
		defer cancel()
		names, err := b.Templates(cctx)
		return templatesMsg{host: host, names: names, err: err}
	}
}

// templateIndex returns the position of want in names, or 0 when it is absent
// — an agent whose template has since been deleted from config opens the menu
// at the top rather than nowhere.
func templateIndex(names []string, want string) int {
	for i, n := range names {
		if n == want {
			return i
		}
	}
	return 0
}
