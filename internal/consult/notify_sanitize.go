package consult

import (
	"strings"
	"unicode/utf8"
)

const (
	escByte = 0x1b
	belByte = 0x07
	// 8-bit C1 introducers, as a raw byte or as the UTF-8 rune of the same
	// code point.
	c1DCS, c1SOS, c1CSI, c1ST, c1OSC, c1PM, c1APC = 0x90, 0x98, 0x9b, 0x9c, 0x9d, 0x9e, 0x9f
)

// sanitizeSubagentText makes subagent output safe to put in front of a
// terminal-attached model: it drops every ESC-introduced sequence (CSI, OSC,
// DCS, SOS/PM/APC with their terminators, and two-byte sequences), the 8-bit
// C1 forms of the same, invalid UTF-8, and every control character other than
// newline and tab. An unterminated sequence is dropped to the end of the text.
func sanitizeSubagentText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out strings.Builder
	out.Grow(len(s))
	for i := 0; i < len(s); {
		b := s[i]
		switch {
		case b == escByte:
			i = skipEscape(s, i+1)
		case b >= 0x80:
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == utf8.RuneError && size == 1 {
				if b >= 0x80 && b <= 0x9f {
					i = skipC1(s, i+1, rune(b)) // a raw 8-bit C1 byte
					continue
				}
				i++ // invalid UTF-8
				continue
			}
			i += size
			switch {
			case r >= 0x80 && r <= 0x9f:
				i = skipC1(s, i, r)
			default:
				out.WriteString(s[i-size : i])
			}
		case b == '\n' || b == '\t' || (b >= 0x20 && b != 0x7f):
			out.WriteByte(b)
			i++
		default:
			i++ // other C0 and DEL
		}
	}
	return out.String()
}

// skipEscape returns the index after the sequence whose ESC precedes i.
func skipEscape(s string, i int) int {
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[':
		return skipCSI(s, i+1)
	case ']', 'P', 'X', '^', '_':
		return skipString(s, i+1)
	}
	// nF sequences: intermediates (0x20-0x2f) then a final byte; any other
	// ESC sequence is ESC plus one byte.
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x30 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipC1 returns the index after the sequence a C1 control r introduces; i is
// just past r.
func skipC1(s string, i int, r rune) int {
	switch r {
	case c1CSI:
		return skipCSI(s, i)
	case c1OSC, c1DCS, c1SOS, c1PM, c1APC:
		return skipString(s, i)
	}
	return i
}

// skipCSI skips parameters, intermediates and the final byte; a byte that
// cannot be part of a CSI ends it unconsumed.
func skipCSI(s string, i int) int {
	for i < len(s) && s[i] >= 0x30 && s[i] <= 0x3f {
		i++
	}
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
		i++
	}
	return i
}

// skipString skips a control string up to and including its terminator: BEL,
// ESC \, or the 8-bit ST (raw or as a rune). A new ESC that does not form ST
// aborts the string and is left for the caller.
func skipString(s string, i int) int {
	for i < len(s) {
		if s[i] == escByte {
			if i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
			return i
		}
		if s[i] == belByte {
			return i + 1
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		// A lone 0x9c byte is the 8-bit ST; inside a multi-byte rune it is
		// only a continuation byte.
		if r == c1ST || (r == utf8.RuneError && size == 1 && s[i-1] == c1ST) {
			return i
		}
	}
	return i
}
