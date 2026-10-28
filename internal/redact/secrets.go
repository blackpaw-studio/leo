package redact

import (
	"bytes"
	"cmp"
	"errors"
	"io"
	"maps"
	"slices"
	"strings"
)

// MinSecretLen is the shortest secret value Secrets replaces. A shorter one
// (a PIN, "on", "1") would match ordinary output everywhere and shred it, so
// it is left as is.
const MinSecretLen = 4

// Secrets replaces the resolved secret values of a launch, wherever they
// appear in what its child printed, with "<secret KEY>". It is built from
// the values themselves and holds them only in memory. A nil *Secrets
// changes nothing.
type Secrets struct {
	masks    map[string]string // value → "<secret KEY>"
	replacer *strings.Replacer
	values   [][]byte // longest first
}

// NewSecrets builds a Secrets over values (env key → resolved value).
// Values shorter than MinSecretLen are skipped; a value under several keys
// reads as the first key in sorted order.
func NewSecrets(values map[string]string) *Secrets {
	return (*Secrets)(nil).With(values)
}

// With is s plus values, for a run whose later turn resolved again. A key
// whose value rotated keeps its old value masked too, since earlier output
// may still be replayed.
func (s *Secrets) With(values map[string]string) *Secrets {
	masks := map[string]string{}
	if s != nil {
		maps.Copy(masks, s.masks)
	}
	for _, k := range slices.Sorted(maps.Keys(values)) {
		if v := values[k]; len(v) >= MinSecretLen {
			if _, seen := masks[v]; !seen {
				masks[v] = "<secret " + k + ">"
			}
		}
	}
	return newSecrets(masks)
}

func newSecrets(masks map[string]string) *Secrets {
	s := &Secrets{masks: masks}
	ordered := slices.SortedFunc(maps.Keys(masks), func(a, b string) int {
		return cmp.Or(cmp.Compare(len(b), len(a)), cmp.Compare(a, b))
	})
	pairs := make([]string, 0, 2*len(ordered))
	for _, v := range ordered {
		pairs = append(pairs, v, masks[v])
		s.values = append(s.values, []byte(v))
	}
	// strings.Replacer tries old strings in argument order at each position,
	// so the longest value wins where one value is a prefix of another.
	s.replacer = strings.NewReplacer(pairs...)
	return s
}

func (s *Secrets) empty() bool { return s == nil || len(s.values) == 0 }

// String is text with every secret value replaced.
func (s *Secrets) String(text string) string {
	if s.empty() {
		return text
	}
	return s.replacer.Replace(text)
}

// Bytes is b with every secret value replaced, as a new slice.
func (s *Secrets) Bytes(b []byte) []byte {
	if s.empty() {
		return b
	}
	return []byte(s.replacer.Replace(string(b)))
}

// Error is err with its message redacted. errors.Is still sees err's chain,
// but nothing unwraps to the unredacted error.
func (s *Secrets) Error(err error) error {
	if err == nil || s.empty() {
		return err
	}
	return &redactedError{msg: s.String(err.Error()), cause: err}
}

type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string        { return e.msg }
func (e *redactedError) Is(target error) bool { return errors.Is(e.cause, target) }

// Writer redacts a stream into w. It holds back just enough of the stream's
// tail that a value split across writes is still caught; Flush writes the
// rest.
func (s *Secrets) Writer(w io.Writer) *SecretsWriter {
	return &SecretsWriter{s: s, w: w}
}

// SecretsWriter is a redacting io.Writer; see Secrets.Writer.
type SecretsWriter struct {
	s       *Secrets
	w       io.Writer
	pending []byte
}

// Write redacts and forwards all of p that can no longer be the start of a
// split value. It reports len(p) once that is accepted.
func (sw *SecretsWriter) Write(p []byte) (int, error) {
	if sw.s.empty() {
		return sw.w.Write(p)
	}
	buf := append(sw.pending, p...)
	cut := sw.s.safeCut(buf)
	if cut > 0 {
		if _, err := sw.w.Write(sw.s.Bytes(buf[:cut])); err != nil {
			return 0, err
		}
	}
	sw.pending = bytes.Clone(buf[cut:])
	return len(p), nil
}

// Flush redacts and writes whatever Write held back.
func (sw *SecretsWriter) Flush() error {
	if len(sw.pending) == 0 {
		return nil
	}
	_, err := sw.w.Write(sw.s.Bytes(sw.pending))
	sw.pending = nil
	return err
}

// safeCut is how much of buf can be redacted now: everything but a tail
// short enough to be the start of a value, moved back further so no value
// occurrence straddles the cut.
func (s *Secrets) safeCut(buf []byte) int {
	cut := len(buf) - (len(s.values[0]) - 1)
	for moved := true; moved && cut > 0; {
		moved = false
		for _, v := range s.values {
			lo, hi := max(0, cut-len(v)+1), min(len(buf), cut+len(v)-1)
			if lo >= hi {
				continue
			}
			if i := bytes.Index(buf[lo:hi], v); i >= 0 && lo+i < cut {
				cut, moved = lo+i, true
			}
		}
	}
	return max(cut, 0)
}
