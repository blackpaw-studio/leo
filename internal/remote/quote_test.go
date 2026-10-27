package remote

import "testing"

func TestQuote(t *testing.T) {
	cases := map[string]string{
		"":          `''`,
		"plain":     `'plain'`,
		"a b":       `'a b'`,
		"it's":      `'it'\''s'`,
		"$(reboot)": `'$(reboot)'`,
	}
	for in, want := range cases {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestLeoWord(t *testing.T) {
	cases := map[string]string{
		"$HOME/.local/bin/leo": `"$HOME"/'.local/bin/leo'`,
		"~/bin/my leo":         `"$HOME"/'bin/my leo'`,
		"/opt/leo dev/bin/leo": `'/opt/leo dev/bin/leo'`,
		"/usr/bin/le'o":        `'/usr/bin/le'\''o'`,
		"/tmp/$(id)/leo":       `'/tmp/$(id)/leo'`,
		"$HOMEX/leo":           `'$HOMEX/leo'`,
		"leo":                  `'leo'`,
	}
	for in, want := range cases {
		if got := LeoWord(in); got != want {
			t.Errorf("LeoWord(%q) = %s, want %s", in, got, want)
		}
	}
}
