package consult

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	rosterDefaultStyle = "#[default]"
	rosterIdleStyle    = "#[fg=yellow]"
	rosterDoneStyle    = "#[fg=green]"
	rosterFailedStyle  = "#[fg=red]"
)

// RenderRoster renders already-resolved records for one tmux session.
// Session/window discovery intentionally stays outside this pure function.
func RenderRoster(records []Record, now time.Time) string {
	eligible := make([]Record, 0, len(records))
	for _, rec := range records {
		if !rosterEligible(rec, now) {
			continue
		}
		eligible = append(eligible, rec)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].StartedAt.Equal(eligible[j].StartedAt) {
			return eligible[i].ID < eligible[j].ID
		}
		return eligible[i].StartedAt.Before(eligible[j].StartedAt)
	})

	entries := make([]string, 0, len(eligible))
	for _, rec := range eligible {
		glyph, style := rosterAppearance(rec.Status)
		entries = append(entries, fmt.Sprintf("%s%s %s %s%s%s", style, glyph, rosterLabel(rec), formatActiveSeconds(rec.LiveActiveSeconds(now)), rosterUsage(rec), rosterDefaultStyle))
	}
	return strings.Join(entries, "   ")
}

// HasRosterEligibleRecord reports whether records contains a dispatch that
// RenderRoster would render at now.
func HasRosterEligibleRecord(records []Record, now time.Time) bool {
	for _, rec := range records {
		if rosterEligible(rec, now) {
			return true
		}
	}
	return false
}

func rosterEligible(rec Record, now time.Time) bool {
	if rec.Status == StatusReleased {
		return false
	}
	return rec.Kind == "dispatch" && (!rec.Status.Terminal() || rec.EndedAt.IsZero() || now.Before(rec.EndedAt.Add(viewerGraceAfterEnd)))
}

func rosterAppearance(status Status) (string, string) {
	switch status {
	case StatusIdle, StatusSettling:
		return "⏸", rosterIdleStyle
	case StatusDone, StatusClosed:
		return "✓", rosterDoneStyle
	case StatusFailed, StatusCanceled, StatusTimeout:
		return "✗", rosterFailedStyle
	case StatusQueued:
		return "…", rosterDefaultStyle
	default:
		return "⟳", rosterDefaultStyle
	}
}

func rosterLabel(rec Record) string {
	label := sanitizeViewerLabel(rec.Name)
	if label == "" {
		label = sanitizeViewerLabel(rec.Template)
	}
	if label == "" {
		label = "dispatch"
	}
	runes := []rune(label)
	if len(runes) > 16 {
		label = string(runes[:16])
	}
	return strings.ReplaceAll(label, "#", "##")
}

func formatActiveSeconds(seconds float64) string {
	if seconds < 0 {
		seconds = 0
	}
	total := int64(seconds)
	if total >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", total/3600, total%3600/60, total%60)
	}
	return fmt.Sprintf("%d:%02d", total/60, total%60)
}
