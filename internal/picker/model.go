package picker

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

const (
	// hostTimeout bounds each per-host List/action call so one unreachable
	// remote cannot hang the picker.
	hostTimeout = 5 * time.Second
	// spinnerInterval is the tick period for the in-flight action spinner.
	spinnerInterval = 120 * time.Millisecond
	// footerLines is the vertical space reserved below the list for the status
	// bar and help footer.
	footerLines = 3
	// headerLines is the vertical space reserved above the list for the
	// column-header line (see rows.go's buildHeader).
	headerLines = 1
)

// actionKind identifies which Backend method a dispatch invokes.
type actionKind int

const (
	actionSuspend actionKind = iota
	actionResume
	actionStop
	actionRename
	actionResumeAttach // resume a suspended agent, then quit and attach
)

// rowsMsg carries the result of a host's List call.
type rowsMsg struct {
	host   string
	agents []Agent
	err    error
}

// actionMsg carries the result of a dispatched lifecycle action.
type actionMsg struct {
	host string
	name string
	kind actionKind
	err  error
}

// tickMsg advances the spinner.
type tickMsg struct{}

// confirmState holds the target of a pending stop confirmation.
type confirmState struct {
	host string
	name string
}

// statusLine is the transient status-bar message.
type statusLine struct {
	text  string
	isErr bool
}

type model struct {
	ctx      context.Context
	backends map[string]Backend

	list   list.Model
	header string
	help   help.Model
	keys   keyMap
	styles styles

	byHost    map[string][]Agent
	byHostErr map[string]error
	pending   map[string]struct{}
	frame     int

	confirming *confirmState
	renaming   bool
	rename     textinput.Model
	renameHost string
	renameOld  string

	status statusLine
	result Result
}

func newModel(ctx context.Context, backends map[string]Backend) model {
	delegate := newTableDelegate()
	l := list.New(nil, delegate, 0, 0)
	l.SetShowTitle(false)
	l.SetShowStatusBar(false)
	l.SetShowHelp(false)
	l.SetFilteringEnabled(true)

	ti := textinput.New()
	ti.Prompt = ""

	return model{
		ctx:       ctx,
		backends:  backends,
		list:      l,
		help:      help.New(),
		keys:      defaultKeys(),
		styles:    newStyles(),
		byHost:    map[string][]Agent{},
		byHostErr: map[string]error{},
		pending:   map[string]struct{}{},
		rename:    ti,
	}
}

func (m model) Init() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.backends))
	for host, b := range m.backends {
		cmds = append(cmds, loadCmd(m.ctx, host, b))
	}
	return tea.Batch(cmds...)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.list.SetSize(msg.Width, msg.Height-footerLines-headerLines)
		return m, nil

	case rowsMsg:
		m.byHost[msg.host] = msg.agents
		if msg.err != nil {
			m.byHostErr[msg.host] = msg.err
		} else {
			delete(m.byHostErr, msg.host)
		}
		cmd := m.rebuild()
		return m, cmd

	case actionMsg:
		return m.onActionDone(msg)

	case tickMsg:
		if len(m.pending) == 0 {
			return m, nil // stop animating when nothing is in flight
		}
		m.frame++
		return m, tea.Batch(m.rebuild(), tickCmd())

	case tea.KeyMsg:
		// While the user is typing a filter, the list owns every key.
		if m.list.SettingFilter() {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		if m.renaming {
			return m.updateRename(msg)
		}
		if m.confirming != nil {
			return m.updateConfirm(msg)
		}
		return m.updateKey(msg)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

// updateKey handles the top-level keybindings when not filtering/renaming/confirming.
func (m model) updateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Esc clears an applied filter first; only quits when the list is unfiltered.
	if msg.String() == "esc" {
		if m.list.FilterState() != list.Unfiltered {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}
		return m, tea.Quit
	}

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m, tea.Quit
	case key.Matches(msg, m.keys.Attach):
		return m.enterSelected()
	case key.Matches(msg, m.keys.Suspend):
		return m.startAction(actionSuspend)
	case key.Matches(msg, m.keys.Resume):
		return m.startAction(actionResume)
	case key.Matches(msg, m.keys.Stop):
		return m.beginConfirm()
	case key.Matches(msg, m.keys.Rename):
		return m.beginRename()
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m model) selectedRow() (row, bool) {
	it := m.list.SelectedItem()
	if it == nil {
		return row{}, false
	}
	r, ok := it.(row)
	return r, ok
}

// enterSelected implements Enter semantics: running/starting attach immediately;
// suspended resumes first then attaches; stopped shows a hint.
func (m model) enterSelected() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	switch r.ag.Status {
	case "suspended":
		return m.dispatch(r.host, r.ag.Name, actionResumeAttach)
	case "stopped":
		m.status = statusLine{text: "stopped — press u to resume", isErr: true}
		return m, nil
	default: // running / starting / restarting
		agentCopy := *r.ag
		m.result = Result{Agent: &agentCopy}
		return m, tea.Quit
	}
}

// startAction dispatches a lifecycle action against the selected row.
func (m model) startAction(kind actionKind) (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	return m.dispatch(r.host, r.ag.Name, kind)
}

// dispatch marks the row pending and fires the async action command, animating
// the spinner if this is the first in-flight action.
func (m model) dispatch(host, name string, kind actionKind) (tea.Model, tea.Cmd) {
	b, ok := m.backends[host]
	if !ok {
		m.status = statusLine{text: "unknown host " + host, isErr: true}
		return m, nil
	}
	startTick := len(m.pending) == 0

	newPending := make(map[string]struct{}, len(m.pending)+1)
	for k := range m.pending {
		newPending[k] = struct{}{}
	}
	var newName string
	if kind == actionRename {
		newName = strings.TrimSpace(m.rename.Value())
	}
	newPending[rowKey(host, name)] = struct{}{}
	m.pending = newPending

	cmds := []tea.Cmd{actionCmd(m.ctx, host, b, kind, name, newName), m.rebuild()}
	if startTick {
		cmds = append(cmds, tickCmd())
	}
	return m, tea.Batch(cmds...)
}

// onActionDone clears the pending marker and either quits-and-attaches (resume
// attach), or shows the result and reloads that host.
func (m model) onActionDone(msg actionMsg) (tea.Model, tea.Cmd) {
	newPending := make(map[string]struct{}, len(m.pending))
	for k := range m.pending {
		if k != rowKey(msg.host, msg.name) {
			newPending[k] = struct{}{}
		}
	}
	m.pending = newPending

	if msg.kind == actionResumeAttach && msg.err == nil {
		// Template/StartedAt are intentionally left zero: the attach path only
		// consumes Name+Host, and this Agent is synthesized here rather than
		// refetched from the backend.
		m.result = Result{Agent: &Agent{Name: msg.name, Host: msg.host, Status: "running"}}
		return m, tea.Quit
	}

	if msg.err != nil {
		m.status = statusLine{text: verbLabel(msg.kind) + " " + msg.name + ": " + msg.err.Error(), isErr: true}
	} else {
		m.status = statusLine{text: verbLabel(msg.kind) + " " + msg.name + " ok", isErr: false}
	}
	// Refresh the acted-on host so its rows reflect the new state.
	return m, tea.Batch(m.rebuild(), loadCmd(m.ctx, msg.host, m.backends[msg.host]))
}

// beginConfirm arms the inline stop confirmation for the selected row.
func (m model) beginConfirm() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	m.confirming = &confirmState{host: r.host, name: r.ag.Name}
	return m, nil
}

func (m model) updateConfirm(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "y", "Y":
		c := m.confirming
		m.confirming = nil
		return m.dispatch(c.host, c.name, actionStop)
	case "n", "N", "esc", "ctrl+c":
		m.confirming = nil
		return m, nil
	}
	return m, nil
}

// beginRename opens the inline text input pre-filled with the current name.
func (m model) beginRename() (tea.Model, tea.Cmd) {
	r, ok := m.selectedRow()
	if !ok || r.ag == nil {
		return m, nil
	}
	if r.ag.AttachOnly {
		m.status = statusLine{text: "remote fallback row — lifecycle actions unavailable", isErr: true}
		return m, nil
	}
	m.renaming = true
	m.renameHost = r.host
	m.renameOld = r.ag.Name
	m.rename.SetValue(r.ag.Name)
	m.rename.CursorEnd()
	return m, m.rename.Focus()
}

func (m model) updateRename(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		newName := strings.TrimSpace(m.rename.Value())
		if !validName(newName) {
			m.status = statusLine{text: "invalid name — use letters, digits, dash, underscore", isErr: true}
			return m, nil
		}
		host, old := m.renameHost, m.renameOld
		m.renaming = false
		m.rename.Blur()
		return m.dispatch(host, old, actionRename)
	case "esc", "ctrl+c":
		m.renaming = false
		m.rename.Blur()
		return m, nil
	}
	var cmd tea.Cmd
	m.rename, cmd = m.rename.Update(msg)
	return m, cmd
}

// rebuild refreshes the list items and column header from the current
// per-host state.
func (m *model) rebuild() tea.Cmd {
	header, items := buildRows(m.byHost, m.byHostErr, m.pending, m.frame)
	m.header = header
	return m.list.SetItems(items)
}

func (m model) View() string {
	var b strings.Builder
	b.WriteString(m.styles.header.Render(m.header))
	b.WriteString("\n")
	b.WriteString(m.list.View())
	b.WriteString("\n")
	switch {
	case m.renaming:
		b.WriteString(m.styles.prompt.Render("rename "+m.renameOld+" to: ") + m.rename.View())
	case m.confirming != nil:
		b.WriteString(m.styles.statusErr.Render(fmt.Sprintf("stop %s? (y/n)", m.confirming.name)))
	case m.status.text != "":
		st := m.styles.statusOK
		if m.status.isErr {
			st = m.styles.statusErr
		}
		b.WriteString(st.Render(m.status.text))
	}
	b.WriteString("\n")
	b.WriteString(m.help.View(m.keys))
	return b.String()
}

// loadCmd fetches one host's agents with a per-host timeout.
func loadCmd(ctx context.Context, host string, b Backend) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, hostTimeout)
		defer cancel()
		ags, err := b.List(cctx)
		return rowsMsg{host: host, agents: ags, err: err}
	}
}

// actionCmd runs one lifecycle action with a per-host timeout.
func actionCmd(ctx context.Context, host string, b Backend, kind actionKind, name, newName string) tea.Cmd {
	return func() tea.Msg {
		cctx, cancel := context.WithTimeout(ctx, hostTimeout)
		defer cancel()
		var err error
		switch kind {
		case actionSuspend:
			err = b.Suspend(cctx, name)
		case actionResume, actionResumeAttach:
			err = b.Resume(cctx, name)
		case actionStop:
			err = b.Stop(cctx, name)
		case actionRename:
			err = b.Rename(cctx, name, newName)
		}
		return actionMsg{host: host, name: name, kind: kind, err: err}
	}
}

func tickCmd() tea.Cmd {
	return tea.Tick(spinnerInterval, func(time.Time) tea.Msg { return tickMsg{} })
}

func verbLabel(k actionKind) string {
	switch k {
	case actionSuspend:
		return "suspend"
	case actionResume, actionResumeAttach:
		return "resume"
	case actionStop:
		return "stop"
	case actionRename:
		return "rename"
	default:
		return "action"
	}
}
