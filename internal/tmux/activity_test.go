package tmux

import (
	"testing"
	"time"
)

func TestParseSessionActivity(t *testing.T) {
	out := "leo-a|0|1700000000\nleo-b|2|1700000600\nmalformed-line\nleo-c|x|notanumber\n"
	got := parseSessionActivity(out)

	if len(got) != 2 {
		t.Fatalf("want 2 valid sessions, got %d: %+v", len(got), got)
	}
	a, ok := got["leo-a"]
	if !ok || a.Attached != 0 || !a.LastActivity.Equal(time.Unix(1700000000, 0)) {
		t.Fatalf("leo-a parsed wrong: %+v ok=%v", a, ok)
	}
	b := got["leo-b"]
	if b.Attached != 2 || !b.LastActivity.Equal(time.Unix(1700000600, 0)) {
		t.Fatalf("leo-b parsed wrong: %+v", b)
	}
	if _, bad := got["leo-c"]; bad {
		t.Fatal("leo-c had unparseable epoch and should be skipped")
	}
}

func TestParseSessionActivityEmpty(t *testing.T) {
	if got := parseSessionActivity("\n  \n"); len(got) != 0 {
		t.Fatalf("want empty, got %+v", got)
	}
}
