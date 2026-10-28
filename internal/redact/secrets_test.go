package redact

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

var errSentinelCause = errors.New("cause")

func TestSecretsReplaceValuesByKey(t *testing.T) {
	s := NewSecrets(map[string]string{"OP_TOKEN": "tok-abcdef", "SHORT": "abc", "PREFIX": "tok-ab", "EMPTY": ""})
	got := s.String("a tok-abcdef b tok-ab c abc d")
	if want := "a <secret OP_TOKEN> b <secret PREFIX> c abc d"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
	if got := string(s.Bytes([]byte("x tok-abcdef"))); got != "x <secret OP_TOKEN>" {
		t.Errorf("Bytes = %q", got)
	}
	// One value under two keys reads as the first key.
	dup := NewSecrets(map[string]string{"B": "same-value", "A": "same-value"})
	if got := dup.String("same-value"); got != "<secret A>" {
		t.Errorf("shared value = %q", got)
	}
	var none *Secrets
	if got := none.String("tok-abcdef"); got != "tok-abcdef" {
		t.Errorf("nil Secrets changed text: %q", got)
	}
}

func TestSecretsWithAddsValues(t *testing.T) {
	s := NewSecrets(map[string]string{"OLD": "first-value"}).With(map[string]string{"NEW": "second-value"})
	if got := s.String("first-value second-value"); got != "<secret OLD> <secret NEW>" {
		t.Errorf("String = %q", got)
	}
	rotated := s.With(map[string]string{"OLD": "rotated-value"})
	if got := rotated.String("first-value rotated-value"); got != "<secret OLD> <secret OLD>" {
		t.Errorf("rotated key lost its old value: %q", got)
	}
	var none *Secrets
	if got := none.With(map[string]string{"K": "a-value"}).String("a-value"); got != "<secret K>" {
		t.Errorf("nil.With = %q", got)
	}
}

func TestSecretsErrorKeepsIsButNotTheValue(t *testing.T) {
	s := NewSecrets(map[string]string{"OP_TOKEN": "tok-abcdef"})
	err := s.Error(fmt.Errorf("child said tok-abcdef: %w", errSentinelCause))
	if err.Error() != "child said <secret OP_TOKEN>: cause" {
		t.Errorf("Error() = %q", err)
	}
	if !errors.Is(err, errSentinelCause) {
		t.Error("errors.Is lost the cause")
	}
	if errors.Unwrap(err) != nil {
		t.Error("Unwrap exposes the unredacted error")
	}
	if s.Error(nil) != nil {
		t.Error("Error(nil) != nil")
	}
}

// The writer gives the same bytes as String however the stream is chunked,
// a value split across writes included.
func TestSecretsWriterMatchesStringAcrossChunks(t *testing.T) {
	s := NewSecrets(map[string]string{"OP_TOKEN": "tok-abcdef", "PREFIX": "tok-ab", "OTHER": "zzzz"})
	text := "tok-abtok-abcdef..zzzztok-abcdefzzz tok-ab end"
	want := s.String(text)
	for size := 1; size <= len(text); size++ {
		var out bytes.Buffer
		w := s.Writer(&out)
		for i := 0; i < len(text); i += size {
			end := min(i+size, len(text))
			if n, err := w.Write([]byte(text[i:end])); err != nil || n != end-i {
				t.Fatalf("Write = %d, %v", n, err)
			}
		}
		if err := w.Flush(); err != nil {
			t.Fatal(err)
		}
		if out.String() != want {
			t.Fatalf("chunk %d: %q, want %q", size, out.String(), want)
		}
	}
}

func TestSecretsWriterWithoutValuesPassesThrough(t *testing.T) {
	var out bytes.Buffer
	w := NewSecrets(map[string]string{"S": "ab"}).Writer(&out)
	_, _ = w.Write([]byte("abc"))
	if out.String() != "abc" {
		t.Errorf("unbuffered pass-through = %q", out.String())
	}
}
