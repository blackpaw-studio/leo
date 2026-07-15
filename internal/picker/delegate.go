package picker

import (
	"io"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// tableDelegate renders each row as a single, pre-aligned line (see rows.go),
// giving the picker a compact table layout instead of bubbles' two-line
// title+description default. It owns no per-item state — every row already
// carries its fully-formatted line — so Update is a no-op.
type tableDelegate struct {
	selected lipgloss.Style
}

// newTableDelegate builds a tableDelegate with the picker's selected-row
// highlight style.
func newTableDelegate() tableDelegate {
	return tableDelegate{
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212")),
	}
}

// Height is fixed at one terminal row per item — the whole point of the
// table layout is dropping the default delegate's description row.
func (d tableDelegate) Height() int { return 1 }

// Spacing is zero: rows are already dense; an extra blank line between them
// would defeat the compact layout.
func (d tableDelegate) Spacing() int { return 0 }

// Update is a no-op — rows carry no per-item interactive state of their own.
func (d tableDelegate) Update(tea.Msg, *list.Model) tea.Cmd { return nil }

// Render writes the row's precomputed line, prefixed to indicate selection.
// The prefix is applied (and the whole line styled) AFTER the row's columns
// are already padded, so the ANSI escapes added by the style never throw off
// column alignment.
func (d tableDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	r, ok := item.(row)
	if !ok {
		return
	}
	line := r.line
	if index == m.Index() {
		_, _ = io.WriteString(w, d.selected.Render("❯ "+line))
		return
	}
	_, _ = io.WriteString(w, "  "+line)
}
