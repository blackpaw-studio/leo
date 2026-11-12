package observe

import (
	"bytes"
	"io"
	"os"
)

const (
	// maxRolloutLine bounds one rollout line the tail will hold; codex lines
	// reach ~150 KB, so a line this long is not one it needs.
	maxRolloutLine = 4 << 20
	// maxRolloutRead bounds the bytes one Read takes, so a first look at a
	// large rollout is spread over several ticks.
	maxRolloutRead = 8 << 20
)

var rolloutWanted = [][]byte{[]byte(`"token_count"`), []byte(`"turn_context"`)}

// rolloutTail reads a growing rollout file incrementally: each Read returns
// only the complete lines added since the last one, and only those the usage
// fold can use (the bulk of a rollout is message and tool output).
// Not safe for concurrent use.
type rolloutTail struct {
	path   string
	offset int64
	// partial is the unterminated line at the end of what was read so far.
	partial []byte
	// skipping is set while discarding the rest of an over-long line.
	skipping bool
	// info is the file as last read, to notice a replacement.
	info os.FileInfo
}

// rolloutRead is the result of one Read.
type rolloutRead struct {
	// Lines are the complete wanted lines read, in order.
	Lines [][]byte
	// OK is false when the file cannot be read (missing, unreadable).
	OK bool
	// Reset reports the file was truncated or replaced: the lines are its
	// content from the start, and earlier state is stale.
	Reset bool
	// More reports the cap stopped the read short of the end of the file.
	More bool
}

// Read returns what was appended since the last call, taking at most
// maxBytes of the file.
func (t *rolloutTail) Read(maxBytes int64) rolloutRead {
	f, err := os.Open(t.path) // #nosec G304 -- path is a rollout located under codex's own sessions dir
	if err != nil {
		return rolloutRead{}
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return rolloutRead{}
	}
	res := rolloutRead{OK: true}
	if t.info != nil && (!os.SameFile(t.info, info) || info.Size() < t.offset) {
		t.offset, t.partial, t.skipping = 0, nil, false
		res.Reset = true
	}
	t.info = info
	if info.Size() == t.offset {
		return res
	}
	if _, err := f.Seek(t.offset, io.SeekStart); err != nil {
		return rolloutRead{}
	}
	buf, err := io.ReadAll(io.LimitReader(f, min(maxBytes, info.Size()-t.offset)))
	if err != nil {
		return rolloutRead{}
	}
	t.offset += int64(len(buf))
	res.More = t.offset < info.Size()
	res.Lines = t.consume(buf)
	return res
}

// consume splits buf into lines, completing the held partial line and
// holding the new one, and returns the wanted complete lines.
func (t *rolloutTail) consume(buf []byte) [][]byte {
	var lines [][]byte
	for len(buf) > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			t.hold(buf)
			break
		}
		seg := buf[:i]
		buf = buf[i+1:]
		if t.skipping {
			t.skipping = false // the end of the over-long line
			continue
		}
		if len(t.partial)+len(seg) > maxRolloutLine {
			t.partial = nil
			continue
		}
		line := seg
		if len(t.partial) > 0 {
			line = append(t.partial, seg...)
			t.partial = nil
		}
		if rolloutWorthDecoding(line) {
			lines = append(lines, bytes.Clone(line))
		}
	}
	return lines
}

// hold keeps the unterminated tail of a read, or drops it for good (and
// starts skipping to the next newline) once the line is longer than the cap.
func (t *rolloutTail) hold(tail []byte) {
	if t.skipping {
		return
	}
	if len(t.partial)+len(tail) > maxRolloutLine {
		t.partial, t.skipping = nil, true
		return
	}
	t.partial = append(t.partial, tail...)
}

func rolloutWorthDecoding(line []byte) bool {
	for _, w := range rolloutWanted {
		if bytes.Contains(line, w) {
			return true
		}
	}
	return false
}
