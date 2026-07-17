package picker

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/agent"
	"github.com/charmbracelet/bubbles/list"
)

// Status glyphs.
const (
	glyphRunning   = "●"
	glyphStarting  = "⟳"
	glyphSuspended = "◌"
	glyphStopped   = "✖"
)

// spinnerFrames animates a pending (in-flight action) row.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// nameRe is the client-side validation for a rename target: a leading
// alphanumeric followed by alphanumerics, dashes, or underscores. The daemon
// re-normalizes to a leo- slug; this only guards against empty/whitespace input.
var nameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Column caps: the NAME/TEMPLATE columns are truncated past this width so one
// long agent name or template can't blow out the whole table's alignment.
const (
	maxNameColumn     = 28
	maxTemplateColumn = 28
)

// Column headers, also used as the floor for their column widths.
const (
	headerName     = "NAME"
	headerTemplate = "TEMPLATE"
	headerHost     = "HOST"
	headerUptime   = "UPTIME"
)

// prefixWidth is the width reserved for the selection cursor / glyph column
// ("❯ " or "  ", then a one-rune status glyph and a following space) that
// every row and the header must both leave room for so columns line up.
const prefixWidth = 4

// row is one list item. ag is nil for synthetic error rows (a host whose List
// failed); those cannot be acted on or attached. line is the fully-rendered,
// pre-padded table row (minus the selection cursor, which the delegate adds).
type row struct {
	line   string
	filter string
	host   string
	ag     *Agent
}

// FilterValue satisfies list.Item — the only method the list actually needs
// from a row.
func (r row) FilterValue() string { return r.filter }

// glyph maps a status string to its display glyph. Unknown statuses render as
// stopped so a row is never blank.
func glyph(status string) string {
	switch status {
	case "running":
		return glyphRunning
	case "starting", "restarting":
		return glyphStarting
	case "suspended":
		return glyphSuspended
	default:
		return glyphStopped
	}
}

// rowKey is the stable per-agent key used to track in-flight actions.
func rowKey(host, name string) string { return host + "/" + name }

// validName reports whether a rename target is acceptable client-side.
func validName(name string) bool { return nameRe.MatchString(name) }

// sortAgents orders agents by their display name in place — the leo- prefix
// that a renamed agent's canonical name carries is a tmux implementation
// detail, so "leo-vitals" must sort as "vitals", not under "l".
func sortAgents(a []Agent) {
	sort.Slice(a, func(i, j int) bool {
		return agent.DisplayName(a[i].Name) < agent.DisplayName(a[j].Name)
	})
}

// columnWidths computes the NAME/TEMPLATE/HOST column widths from the
// current agent data: the max of each column's header and its content,
// capped for NAME and TEMPLATE so one long value can't blow out alignment.
// Widths count runes, matching cell's rune-based truncation and padding.
func columnWidths(hosts []string, byHost map[string][]Agent) (nameW, templateW, hostW int) {
	nameW = len(headerName)
	templateW = len(headerTemplate)
	hostW = len(headerHost)
	for _, h := range hosts {
		if w := utf8.RuneCountInString(h); w > hostW {
			hostW = w
		}
		for _, a := range byHost[h] {
			if w := utf8.RuneCountInString(agent.DisplayName(a.Name)); w > nameW {
				nameW = w
			}
			if w := utf8.RuneCountInString(dash(a.Template)); w > templateW {
				templateW = w
			}
		}
	}
	if nameW > maxNameColumn {
		nameW = maxNameColumn
	}
	if templateW > maxTemplateColumn {
		templateW = maxTemplateColumn
	}
	return nameW, templateW, hostW
}

// cell truncates s to width runes (appending an ellipsis when it doesn't fit)
// and right-pads it with trailing spaces so every row's columns line up.
// Operates on runes rather than bytes so multi-byte characters — including
// the ellipsis itself — count as a single display column.
func cell(s string, width int) string {
	r := []rune(s)
	if len(r) > width {
		if width <= 1 {
			return string(r[:width])
		}
		r = append(r[:width-1], '…')
	}
	return fmt.Sprintf("%-*s", width, string(r))
}

// buildRows flattens the per-host agent map into list items — one row per
// agent, plus a single-line row for any host whose List call failed — and
// the column-aligned header line to render above them. Hosts are sorted with
// LocalHost first; agents are sorted by name within each host. Rows whose
// action is in flight (present in pending) render a spinner in place of the
// status glyph.
func buildRows(byHost map[string][]Agent, byHostErr map[string]error, pending map[string]struct{}, frame int) (string, []list.Item) {
	hosts := sortedHosts(byHost, byHostErr)
	nameW, templateW, hostW := columnWidths(hosts, byHost)
	header := buildHeader(nameW, templateW, hostW)

	var items []list.Item
	for _, h := range hosts {
		if err := byHostErr[h]; err != nil {
			items = append(items, row{
				line:   glyphStopped + " " + cell(h, nameW) + " error: " + err.Error(),
				filter: h,
				host:   h,
			})
			continue
		}
		ags := append([]Agent(nil), byHost[h]...)
		sortAgents(ags)
		for i := range ags {
			a := ags[i]
			g := glyph(a.Status)
			if _, ok := pending[rowKey(h, a.Name)]; ok {
				g = spinnerFrames[frame%len(spinnerFrames)]
			}
			ac := a // stable pointer for the selected-row result
			display := agent.DisplayName(a.Name)
			line := g + " " + cell(display, nameW) + " " + cell(dash(a.Template), templateW) + " " + cell(h, hostW) + " " + ageLabel(a)
			items = append(items, row{
				line:   line,
				filter: display + " " + a.Template + " " + h,
				host:   h,
				ag:     &ac,
			})
		}
	}
	return header, items
}

// buildHeader renders the column-title line, indented to align under the
// data rows' NAME column (past the cursor/glyph prefix both selected and
// unselected rows reserve).
func buildHeader(nameW, templateW, hostW int) string {
	pad := strings.Repeat(" ", prefixWidth)
	return pad + cell(headerName, nameW) + " " + cell(headerTemplate, templateW) + " " + cell(headerHost, hostW) + " " + headerUptime
}

// sortedHosts returns the union of host keys from both maps, sorted, with
// LocalHost first so local agents lead the list.
func sortedHosts(byHost map[string][]Agent, byHostErr map[string]error) []string {
	seen := map[string]struct{}{}
	for h := range byHost {
		seen[h] = struct{}{}
	}
	for h := range byHostErr {
		seen[h] = struct{}{}
	}
	hosts := make([]string, 0, len(seen))
	for h := range seen {
		hosts = append(hosts, h)
	}
	sort.Slice(hosts, func(i, j int) bool {
		if hosts[i] == LocalHost {
			return true
		}
		if hosts[j] == LocalHost {
			return false
		}
		return hosts[i] < hosts[j]
	})
	return hosts
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// ageLabel renders the right-hand column: uptime for live agents, a
// "suspended … ago" hint for suspended agents, and a plain label otherwise.
func ageLabel(a Agent) string {
	switch a.Status {
	case "stopped":
		return "stopped"
	case "suspended":
		if a.StartedAt.IsZero() {
			return "suspended"
		}
		return "suspended " + humanDuration(time.Since(a.StartedAt)) + " ago"
	default:
		if a.StartedAt.IsZero() {
			return a.Status
		}
		return humanDuration(time.Since(a.StartedAt))
	}
}

// humanDuration renders a compact duration: "2d4h", "3h", "5m", "10s".
func humanDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		h := int(d.Hours())
		m := int(d.Minutes()) % 60
		if m == 0 {
			return fmt.Sprintf("%dh", h)
		}
		return fmt.Sprintf("%dh%dm", h, m)
	}
	days := int(d.Hours()) / 24
	h := int(d.Hours()) % 24
	if h == 0 {
		return fmt.Sprintf("%dd", days)
	}
	return fmt.Sprintf("%dd%dh", days, h)
}
