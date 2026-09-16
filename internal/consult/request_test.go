package consult

import (
	"strings"
	"testing"
)

func TestDispatchPreambleReleaseGuidance(t *testing.T) {
	if !strings.Contains(requestPrompt(Request{Prompt: "work"}), "orchestrator has finished with you, it releases this pane") {
		t.Fatal("release guidance missing")
	}
}

func TestRequestPrompt(t *testing.T) {
	tests := []struct {
		name string
		req  Request
		want string
	}{
		{name: "dispatch", req: Request{Prompt: "implement it"}, want: dispatchPreamble + " implement it"},
		{name: "consult retains existing preamble", req: Request{Prompt: "inspect it", Preamble: true}, want: preamble + "\n\ninspect it"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := requestPrompt(tt.req); got != tt.want {
				t.Fatalf("requestPrompt() = %q, want %q", got, tt.want)
			}
		})
	}
}
