package consult

import "testing"

func TestSanitizeSubagentText(t *testing.T) {
	for name, tc := range map[string]struct{ in, want string }{
		"plain text keeps newlines and tabs": {"a\n\tb", "a\n\tb"},
		"CRLF becomes LF":                    {"a\r\nb", "a\nb"},
		"CSI color":                          {"\x1b[31mred\x1b[0m", "red"},
		"CSI with intermediates":             {"a\x1b[2 qb", "ab"},
		"OSC title ended by BEL":             {"a\x1b]0;evil title\x07b", "ab"},
		"OSC hyperlink ended by ST":          {"a\x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\b", "alinkb"},
		"DCS":                                {"a\x1bP1$r0m\x1b\\b", "ab"},
		"SOS PM APC":                         {"a\x1bXs\x1b\\b\x1b^p\x1b\\c\x1b_apc\x1b\\d", "abcd"},
		"two-byte ESC sequences":             {"a\x1bcb\x1b7c\x1b=d", "abcd"},
		"ESC with intermediate (charset)":    {"a\x1b(Bb", "ab"},
		"unterminated OSC at end":            {"ok\x1b]0;title with no end", "ok"},
		"unterminated DCS at end":            {"ok\x1bPdata", "ok"},
		"unterminated CSI at end":            {"ok\x1b[31", "ok"},
		"lone ESC at end":                    {"ok\x1b", "ok"},
		"a new ESC aborts an OSC":            {"\x1b]0;t\x1b[31mred", "red"},
		"8-bit CSI byte 0x9b":                {"a\x9b31mb", "ab"},
		"ST byte inside a multi-byte rune does not end a string": {"a\x1b]0;\u025c secret\x07b", "ab"},
		"C1 CSI as the UTF-8 rune U+009B":                        {"a\u009b31mb", "ab"},
		"8-bit OSC 0x9d ended by 0x9c":                           {"a\x9d0;title\x9cb", "ab"},
		"8-bit OSC as runes":                                     {"a\u009d0;title\u009cb", "ab"},
		"other C1 controls":                                      {"a\u0085b\u0090q\u009cc", "abc"},
		"C0 controls":                                            {"a\x00b\x07c\x08d\re\x7ff", "abcdef"},
		"invalid UTF-8 is dropped":                               {"a\xffb\xc3", "ab"},
		"non-ASCII text survives":                                {"línea €", "línea €"},
	} {
		if got := sanitizeSubagentText(tc.in); got != tc.want {
			t.Errorf("%s: sanitize(%q) = %q, want %q", name, tc.in, got, tc.want)
		}
	}
}
