package observe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func strs(lines [][]byte) []string {
	var out []string
	for _, l := range lines {
		out = append(out, string(l))
	}
	return out
}

func TestRolloutTail_RealFile(t *testing.T) {
	const tokenLine = `{"type":"event_msg","payload":{"type":"token_count"}}`
	const ctxLine = `{"type":"turn_context"}`
	const other = `{"type":"response_item","payload":{"type":"message"}}`
	const big = 1 << 20

	newTail := func(t *testing.T) (*rolloutTail, string) {
		path := filepath.Join(t.TempDir(), "rollout.jsonl")
		return &rolloutTail{path: path}, path
	}

	t.Run("a missing file is not ok", func(t *testing.T) {
		tail, _ := newTail(t)
		if tail.Read(big).OK {
			t.Fatal("ok for a missing file")
		}
	})

	t.Run("an append reads only the new bytes and only the lines worth decoding", func(t *testing.T) {
		tail, path := newTail(t)
		appendFile(t, path, tokenLine+"\n"+other+"\n")
		res := tail.Read(big)
		if !res.OK || len(res.Lines) != 1 || string(res.Lines[0]) != tokenLine {
			t.Fatalf("first read = %v", strs(res.Lines))
		}
		appendFile(t, path, ctxLine+"\n")
		res = tail.Read(big)
		if got := strs(res.Lines); len(got) != 1 || got[0] != ctxLine || res.Reset {
			t.Fatalf("second read = %v reset=%v", got, res.Reset)
		}
		if res = tail.Read(big); len(res.Lines) != 0 || !res.OK {
			t.Fatalf("unchanged file = %+v", res)
		}
	})

	t.Run("a partial last line is held back until its newline", func(t *testing.T) {
		tail, path := newTail(t)
		half := len(tokenLine) / 2
		appendFile(t, path, tokenLine[:half])
		if res := tail.Read(big); len(res.Lines) != 0 {
			t.Fatalf("partial delivered: %v", strs(res.Lines))
		}
		appendFile(t, path, tokenLine[half:]+"\n")
		if res := tail.Read(big); len(res.Lines) != 1 || string(res.Lines[0]) != tokenLine {
			t.Fatalf("completed line = %v", strs(res.Lines))
		}
	})

	t.Run("a line over the cap is skipped without being held", func(t *testing.T) {
		tail, path := newTail(t)
		huge := `{"type":"event_msg","payload":{"type":"token_count","x":"` + strings.Repeat("a", maxRolloutLine+10) + `"}}`
		// The oversized line arrives across several reads, then a normal one.
		appendFile(t, path, huge[:maxRolloutLine/2])
		tail.Read(maxRolloutRead)
		appendFile(t, path, huge[maxRolloutLine/2:])
		tail.Read(maxRolloutRead)
		if len(tail.partial) != 0 {
			t.Fatalf("held %d bytes of an oversized line", len(tail.partial))
		}
		appendFile(t, path, "\n"+tokenLine+"\n")
		res := tail.Read(maxRolloutRead)
		if got := strs(res.Lines); len(got) != 1 || got[0] != tokenLine {
			t.Fatalf("after the oversized line = %v", got)
		}
	})

	t.Run("truncation resets to the start", func(t *testing.T) {
		tail, path := newTail(t)
		appendFile(t, path, tokenLine+"\n"+ctxLine+"\n")
		tail.Read(big)
		if err := os.WriteFile(path, []byte(ctxLine+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		res := tail.Read(big)
		if !res.Reset || len(res.Lines) != 1 || string(res.Lines[0]) != ctxLine {
			t.Fatalf("after truncation = reset=%v %v", res.Reset, strs(res.Lines))
		}
	})

	t.Run("a replaced file resets to the start", func(t *testing.T) {
		tail, path := newTail(t)
		appendFile(t, path, tokenLine+"\n")
		tail.Read(big)
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		// Longer than the old one, so only the identity check can notice.
		appendFile(t, path, ctxLine+"\n"+ctxLine+"\n"+ctxLine+"\n")
		res := tail.Read(big)
		if !res.Reset || len(res.Lines) != 3 {
			t.Fatalf("after replacement = reset=%v %v", res.Reset, strs(res.Lines))
		}
	})

	t.Run("a read is capped per call and resumes where it stopped", func(t *testing.T) {
		tail, path := newTail(t)
		appendFile(t, path, tokenLine+"\n"+ctxLine+"\n"+tokenLine+"\n")
		limit := int64(len(tokenLine) + 1 + 3) // the first line plus part of the second
		res := tail.Read(limit)
		if len(res.Lines) != 1 || !res.More {
			t.Fatalf("capped read = %v more=%v", strs(res.Lines), res.More)
		}
		res = tail.Read(big)
		if got := strs(res.Lines); len(got) != 2 || got[0] != ctxLine || got[1] != tokenLine || res.More {
			t.Fatalf("resumed read = %v more=%v", got, res.More)
		}
	})
}
