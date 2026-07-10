package claude

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestIdentity(t *testing.T) {
	c := Claude{}
	if c.Name() != "claude" {
		t.Fatalf("Name() = %q", c.Name())
	}
	if c.Binary() != "claude" {
		t.Fatalf("Binary() = %q", c.Binary())
	}
}

func TestRegisteredInRegistry(t *testing.T) {
	h, err := harness.Get("claude")
	if err != nil {
		t.Fatalf("harness.Get(claude): %v", err)
	}
	if h.Name() != "claude" {
		t.Fatalf("registered harness Name() = %q", h.Name())
	}
}

func TestSessionArgs(t *testing.T) {
	tests := []struct {
		name  string
		state harness.SessionState
		want  []string
	}{
		{"none", harness.SessionState{Mode: harness.SessionNone}, nil},
		{"zero value", harness.SessionState{}, nil},
		{"resume", harness.SessionState{Mode: harness.SessionResume, ID: "abc-123"}, []string{"--resume", "abc-123"}},
		{"pinned", harness.SessionState{Mode: harness.SessionPinned, ID: "def-456"}, []string{"--session-id", "def-456"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Claude{}.SessionArgs(tt.state)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("SessionArgs(%+v) = %v, want %v", tt.state, got, tt.want)
			}
		})
	}
}

func TestArgsRejectsWrongOptionsType(t *testing.T) {
	_, err := Claude{}.Args(harness.LaunchSpec{Kind: harness.KindProcess, Options: "nope"})
	if err == nil {
		t.Fatal("Args with non-claude.Options: expected error")
	}
}

func TestArgsRejectsUnknownKind(t *testing.T) {
	_, err := Claude{}.Args(harness.LaunchSpec{Kind: harness.Kind("bogus"), Options: Options{}})
	if err == nil {
		t.Fatal("Args with unknown kind: expected error")
	}
}
