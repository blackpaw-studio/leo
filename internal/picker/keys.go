package picker

import (
	"github.com/charmbracelet/bubbles/key"
)

// keyMap is the picker's custom keybindings. It implements help.KeyMap so the
// bubbles help component can render the footer.
//
// "x" is deliberately left unbound (see defaultKeys): it used to be the stop
// key, and stop is now reversible with no confirm, so the old x-then-y
// muscle memory must do nothing rather than delete an agent.
type keyMap struct {
	Attach   key.Binding
	Stop     key.Binding
	Start    key.Binding
	Delete   key.Binding
	Rename   key.Binding
	Template key.Binding
	Filter   key.Binding
	Quit     key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Attach:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
		Stop:     key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stop")),
		Start:    key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "start")),
		Delete:   key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete")),
		Rename:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rename")),
		Template: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "template")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp / FullHelp satisfy help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Attach, k.Stop, k.Start, k.Delete, k.Rename, k.Template, k.Filter, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Attach, k.Stop, k.Start},
		{k.Delete, k.Rename, k.Template},
		{k.Filter, k.Quit},
	}
}
