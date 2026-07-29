package consult

import (
	"testing"
	"time"
)

func TestDecodeEvent(t *testing.T) {
	tests := []struct {
		name       string
		line       string
		ok         bool
		wantOffset time.Duration
		wantData   string
		wantRaw    string
	}{
		{
			name: "harness event", line: `{"t":1.25,"d":{"type":"result"}}`,
			ok: true, wantOffset: 1250 * time.Millisecond, wantData: `{"type":"result"}`,
		},
		{
			name: "non-JSON line", line: `{"t":0.5,"raw":"panic: boom"}`,
			ok: true, wantOffset: 500 * time.Millisecond, wantRaw: "panic: boom",
		},
		{
			name: "torn trailing line", line: `{"t":2.0,"d":{"typ`,
		},
		{name: "empty", line: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, ok := DecodeEvent([]byte(tt.line))
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if ev.Offset != tt.wantOffset {
				t.Errorf("offset = %v, want %v", ev.Offset, tt.wantOffset)
			}
			if string(ev.Data) != tt.wantData {
				t.Errorf("data = %s, want %s", ev.Data, tt.wantData)
			}
			if ev.Raw != tt.wantRaw {
				t.Errorf("raw = %q, want %q", ev.Raw, tt.wantRaw)
			}
		})
	}
}
