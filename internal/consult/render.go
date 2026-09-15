package consult

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/blackpaw-studio/leo/internal/harness"
)

// FeedIndent aligns continuation lines under the body column of a feed row.
const FeedIndent = 18

// Renderer renders recorded stream events using the harness's live-feed
// mapping. It intentionally owns no terminal styling, so callers can choose
// whether to preserve raw text (watch) or sanitize it (output snapshots).
type Renderer struct{ renderer harness.EventRenderer }

// NewRendererFor permits callers with a preselected adapter renderer (notably
// watch's compatibility wrapper) to use the same row rendering path.
func NewRendererFor(renderer harness.EventRenderer) Renderer { return Renderer{renderer: renderer} }

func NewRenderer(name string) Renderer {
	h, err := harness.Get(name)
	if err != nil {
		return Renderer{}
	}
	r, _ := h.(harness.EventRenderer)
	return Renderer{renderer: r}
}

// Render returns physical feed rows for one stream event.
func (r Renderer) Render(event StreamEvent) []string {
	var rows []string
	row := func(label, body string) { rows = append(rows, renderRow(event.Offset, label, body)...) }
	if event.Raw != "" {
		row("raw", event.Raw)
		return rows
	}
	if len(event.Data) == 0 {
		return nil
	}
	var raw map[string]json.RawMessage
	if json.Unmarshal(event.Data, &raw) == nil {
		var typ string
		_ = json.Unmarshal(raw["type"], &typ)
		switch typ {
		case "turn":
			var turn Turn
			_ = json.Unmarshal(raw["data"], &turn)
			row("turn", fmt.Sprintf("%s %s %s\n%s", turn.TurnID, turn.Source, turn.Outcome, turn.Text))
			return rows
		case "status":
			var status string
			_ = json.Unmarshal(raw["data"], &status)
			row("status", status)
			return rows
		}
	}
	if r.renderer == nil {
		row("raw", string(event.Data))
		return rows
	}
	for _, rendered := range r.renderer.RenderEvent(event.Data) {
		label := string(rendered.Kind)
		if rendered.Kind == harness.EventTool {
			label = strings.ToLower(rendered.Tool)
		}
		row(label, rendered.Summary)
	}
	return rows
}

func renderRow(offset time.Duration, label, body string) []string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	rows := []string{fmt.Sprintf("%7s  %-8s %s", FormatOffset(offset), label, lines[0])}
	for _, extra := range lines[1:] {
		rows = append(rows, strings.Repeat(" ", FeedIndent)+extra)
	}
	return rows
}

// FormatOffset renders a duration as m:ss, growing to h:mm:ss only when needed.
func FormatOffset(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	total := int(d.Seconds())
	hours, minutes, seconds := total/3600, (total%3600)/60, total%60
	if hours > 0 {
		return fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	}
	return fmt.Sprintf("%d:%02d", minutes, seconds)
}

// StripControlSequences removes terminal controls from non-interactive output.
func StripControlSequences(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			i += 2
			for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
				i++
			}
			if i < len(s) {
				i++
			}
			continue
		}
		r, size := rune(s[i]), 1
		if r >= 0x80 {
			r, size = utf8.DecodeRuneInString(s[i:])
		}
		if r >= 0x20 && r != 0x7f && !unicode.IsControl(r) {
			b.WriteRune(r)
		}
		i += size
	}
	return b.String()
}
