package observe

import (
	"regexp"
	"strings"
)

// oscEscapeRegexp matches an OSC sequence (e.g. a terminal title set),
// terminated by BEL or ST. ansiEscapeRegexp covers CSI.
var oscEscapeRegexp = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// ClampDetail sanitises one mod-supplied display string (a tool summary,
// attention detail, tool name, or session-end reason) to MaxActionDetail
// runes. See clampText.
func ClampDetail(s string) string { return clampText(s, MaxActionDetail) }

// ClampPreview sanitises a turn's final-message preview to MaxTurnPreview
// runes. See clampText.
func ClampPreview(s string) string { return clampText(s, MaxTurnPreview) }

// ClampAttentionReason returns a copy of r with its untrusted strings
// clamped. Kind is left alone: the producer sets it from a closed set.
func ClampAttentionReason(r AttentionReason) AttentionReason {
	r.Tool = ClampDetail(r.Tool)
	r.Detail = ClampDetail(r.Detail)
	return r
}

// clampText is the untrusted-text discipline for strings the claude mod
// bridge supplies: strip ANSI escapes, fold every control character
// (newlines included) into a space, collapse whitespace, and truncate to max
// runes, never mid-rune. Unlike sanitizePaneLine it folds rather than drops
// controls, so a multi-line message keeps its word breaks.
func clampText(s string, max int) string {
	s = oscEscapeRegexp.ReplaceAllString(s, "")
	s = ansiEscapeRegexp.ReplaceAllString(s, "")
	folded := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, s)
	return truncateRunes(strings.Join(strings.Fields(folded), " "), max)
}
