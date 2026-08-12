package picker

import (
	"github.com/charmbracelet/bubbles/key"
)

// keyMap is the picker's custom keybindings. It implements help.KeyMap so the
// bubbles help component can render the footer.
type keyMap struct {
	Attach   key.Binding
	Suspend  key.Binding
	Resume   key.Binding
	Stop     key.Binding
	Rename   key.Binding
	Template key.Binding
	Filter   key.Binding
	Quit     key.Binding
}

func defaultKeys() keyMap {
	return keyMap{
		Attach:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "attach")),
		Suspend:  key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "suspend")),
		Resume:   key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "resume")),
		Stop:     key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "stop")),
		Rename:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "rename")),
		Template: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "template")),
		Filter:   key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "filter")),
		Quit:     key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	}
}

// ShortHelp / FullHelp satisfy help.KeyMap.
func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Attach, k.Suspend, k.Resume, k.Stop, k.Rename, k.Template, k.Filter, k.Quit}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Attach, k.Suspend, k.Resume},
		{k.Stop, k.Rename, k.Template},
		{k.Filter, k.Quit},
	}
}
