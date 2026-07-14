package picker

import (
	"fmt"
	"regexp"
	"sort"
	"time"

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

// row is one list item. ag is nil for synthetic error rows (a host whose List
// failed); those cannot be acted on or attached.
type row struct {
	title  string
	desc   string
	filter string
	host   string
	ag     *Agent
}

func (r row) Title() string       { return r.title }
func (r row) Description() string { return r.desc }
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

// sortAgents orders agents by name in place.
func sortAgents(a []Agent) {
	sort.Slice(a, func(i, j int) bool { return a[i].Name < a[j].Name })
}

// buildRows flattens the per-host agent map into list items, one host group at
// a time (hosts sorted, agents sorted within each host). A host with a fetch
// error contributes a single non-selectable error row. Rows whose action is
// in flight (present in pending) render a spinner in place of the glyph.
func buildRows(byHost map[string][]Agent, byHostErr map[string]error, pending map[string]struct{}, frame int) []list.Item {
	hosts := sortedHosts(byHost, byHostErr)
	var items []list.Item
	for _, h := range hosts {
		if err := byHostErr[h]; err != nil {
			items = append(items, row{
				title:  glyphStopped + "  " + h,
				desc:   "error: " + err.Error(),
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
			items = append(items, row{
				title:  g + "  " + a.Name,
				desc:   fmt.Sprintf("%s · %s · %s", dash(a.Template), h, ageLabel(a)),
				filter: a.Name + " " + a.Template + " " + h,
				host:   h,
				ag:     &ac,
			})
		}
	}
	return items
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
