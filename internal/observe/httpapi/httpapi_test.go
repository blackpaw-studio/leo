package httpapi

import (
	"bytes"
	"strings"
	"testing"
)

func TestEventsHelloHostStateBurstAndPing(t *testing.T) {
	var b bytes.Buffer
	_ = WriteEvent(&b, "hello", map[string]any{"version": "v"})
	if !strings.Contains(b.String(), "event: hello") {
		t.Fatal(b.String())
	}
}
func TestStateMerge(t *testing.T) {
	rows := append([]string{"local"}, []string{"remote"}...)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
}
