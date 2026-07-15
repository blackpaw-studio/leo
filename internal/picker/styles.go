package picker

import "github.com/charmbracelet/lipgloss"

// styles holds the lipgloss styles for the status bar and table header. Row
// selection/highlight styling lives in the tableDelegate.
type styles struct {
	statusOK  lipgloss.Style
	statusErr lipgloss.Style
	prompt    lipgloss.Style
	header    lipgloss.Style
}

func newStyles() styles {
	return styles{
		statusOK:  lipgloss.NewStyle().Foreground(lipgloss.Color("42")),  // green
		statusErr: lipgloss.NewStyle().Foreground(lipgloss.Color("196")), // red
		prompt:    lipgloss.NewStyle().Foreground(lipgloss.Color("221")), // yellow
		header:    lipgloss.NewStyle().Faint(true),                       // dim column titles
	}
}
