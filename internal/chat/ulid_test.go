package chat

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestULIDsSortByTimeThenSequence(t *testing.T) {
	now := time.Date(2026, 10, 12, 18, 0, 0, 0, time.UTC)
	ids := NewULIDSource(func() time.Time { return now }, bytes.NewReader(bytes.Repeat([]byte{0xff}, 64)))
	a, b := ids.New(), ids.New()
	if len(a) != 26 || len(b) != 26 {
		t.Fatalf("ULIDs must be 26 chars: %q %q", a, b)
	}
	if a >= b {
		t.Fatalf("ids within one millisecond must still increase: %q !< %q", a, b)
	}
	now = now.Add(time.Millisecond)
	c := ids.New()
	if b >= c {
		t.Fatalf("a later millisecond must sort after: %q !< %q", b, c)
	}
	if strings.ToUpper(c) != c {
		t.Fatalf("ULIDs are upper-case Crockford base32: %q", c)
	}
}

func TestULIDTimePrefixIsTheTimestamp(t *testing.T) {
	at := time.UnixMilli(1791829334123).UTC()
	id := NewULIDSource(func() time.Time { return at }, bytes.NewReader(make([]byte, 64))).New()
	got, ok := ulidTime(id)
	if !ok || !got.Equal(at) {
		t.Fatalf("ulidTime(%q) = %v, %v; want %v", id, got, ok, at)
	}
}
