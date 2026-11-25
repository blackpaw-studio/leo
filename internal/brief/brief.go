// Package brief is an agent's "where was I" summary: a few short plain-text
// lines the agent writes about its current work through leo_brief, shown to
// the user in Leo's apps. This is the agent brief, not the opening prompt
// ("brief") an agent is spawned with.
package brief

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"
)

const (
	// MaxLineRunes bounds every field and every list item, in runes.
	MaxLineRunes = 140
	// MaxItems bounds each of the two lists.
	MaxItems = 5
)

// ErrInvalid wraps every validation failure, so callers answer 400.
var ErrInvalid = errors.New("invalid brief")

// Brief is one agent's stored brief. All text is plain, single-line and
// bounded (see Normalize); UpdatedAt is the daemon's clock at the write.
type Brief struct {
	Goal      string    `json:"goal,omitempty"`
	Status    string    `json:"status,omitempty"`
	Decisions []string  `json:"decisions,omitempty"`
	Open      []string  `json:"open,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Input is what an agent submits: a brief without the daemon's timestamp.
type Input struct {
	Goal      string   `json:"goal"`
	Status    string   `json:"status"`
	Decisions []string `json:"decisions"`
	Open      []string `json:"open"`
}

// Clone is a deep copy of b, nil for nil.
func (b *Brief) Clone() *Brief {
	if b == nil {
		return nil
	}
	c := *b
	c.Decisions, c.Open = slices.Clone(b.Decisions), slices.Clone(b.Open)
	return &c
}

// IsZero reports a cleared brief: no text anywhere.
func (b Brief) IsZero() bool {
	return b.Goal == "" && b.Status == "" && len(b.Decisions) == 0 && len(b.Open) == 0
}

// Normalize validates in, returning the brief to store stamped with now. An
// input with no text at all yields the zero Brief, which clears. Control
// characters are stripped and newlines read as spaces first, so the limits
// apply to what is stored; a field over MaxLineRunes or a list over MaxItems
// is an error the agent reads, never a silent cut.
func Normalize(in Input, now time.Time) (Brief, error) {
	out := Brief{Goal: clean(in.Goal), Status: clean(in.Status), Decisions: cleanList(in.Decisions), Open: cleanList(in.Open)}
	if err := out.validate(); err != nil {
		return Brief{}, err
	}
	if out.IsZero() {
		return Brief{}, nil
	}
	out = out.Clamped()
	out.UpdatedAt = now.UTC()
	return out, nil
}

func (b Brief) validate() error {
	for _, f := range []struct{ name, text string }{{"goal", b.Goal}, {"status", b.Status}} {
		if n := len([]rune(f.text)); n > MaxLineRunes {
			return fmt.Errorf("%w: %s is %d characters, at most %d", ErrInvalid, f.name, n, MaxLineRunes)
		}
	}
	for _, l := range []struct {
		name  string
		items []string
	}{{"decisions", b.Decisions}, {"open", b.Open}} {
		if len(l.items) > MaxItems {
			return fmt.Errorf("%w: %s has %d items, at most %d", ErrInvalid, l.name, len(l.items), MaxItems)
		}
		for i, item := range l.items {
			if n := len([]rune(item)); n > MaxLineRunes {
				return fmt.Errorf("%w: %s item %d is %d characters, at most %d", ErrInvalid, l.name, i+1, n, MaxLineRunes)
			}
		}
	}
	return nil
}

// Clamped is b cut to the limits, a backstop for text that did not pass
// through Normalize (a stored or relayed brief). It never mutates b.
func (b Brief) Clamped() Brief {
	b.Goal, b.Status = clampLine(b.Goal), clampLine(b.Status)
	b.Decisions, b.Open = clampList(b.Decisions), clampList(b.Open)
	return b
}

func clampLine(s string) string {
	if r := []rune(s); len(r) > MaxLineRunes {
		return string(r[:MaxLineRunes])
	}
	return s
}

func clampList(items []string) []string {
	if len(items) == 0 {
		return nil
	}
	if len(items) > MaxItems {
		items = items[:MaxItems]
	}
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = clampLine(item)
	}
	return out
}

// clean is s as one trimmed line: whitespace runs collapse to a space, other
// control and format characters (escapes, bidi overrides) are dropped.
func clean(s string) string {
	return strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			return ' '
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
			return -1
		}
		return r
	}, s)), " ")
}

func cleanList(items []string) []string {
	var out []string
	for _, item := range items {
		if c := clean(item); c != "" {
			out = append(out, c)
		}
	}
	return out
}
