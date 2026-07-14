package claude

import (
	"fmt"
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
	_, err := Claude{}.Args(harness.LaunchSpec{Kind: harness.KindAgent, Options: "nope"})
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

func TestValidateModel(t *testing.T) {
	tests := []struct {
		model   string
		wantErr bool
	}{
		{"", false}, {"sonnet", false}, {"opus", false}, {"haiku", false},
		{"sonnet[1m]", false}, {"opus[1m]", false},
		{"gpt-5", true}, {"claude-3-opus", true},
	}
	for _, tt := range tests {
		err := Claude{}.ValidateModel(tt.model)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateModel(%q) err=%v, wantErr=%v", tt.model, err, tt.wantErr)
		}
		if tt.wantErr && err.Error() != fmt.Sprintf("%q is not valid (use sonnet, opus, haiku, sonnet[1m], or opus[1m])", tt.model) {
			t.Errorf("ValidateModel(%q) wrong message: %v", tt.model, err)
		}
	}
}

func TestSupportsChannels(t *testing.T) {
	if !(Claude{}.SupportsChannels()) {
		t.Fatal("SupportsChannels() = false, want true")
	}
}

func TestClaudeEnv(t *testing.T) {
	tests := []struct {
		name string
		kind harness.Kind
		want map[string]string
	}{
		{"task", harness.KindTask, map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}},
		{"agent", harness.KindAgent, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Claude{}.Env(harness.LaunchSpec{Kind: tt.kind})
			if err != nil {
				t.Fatalf("Env(%v): unexpected error %v", tt.kind, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Env(%v) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

func TestClaudeSupportsKind(t *testing.T) {
	for _, k := range []harness.Kind{harness.KindAgent, harness.KindTask} {
		if !(Claude{}.SupportsKind(k)) {
			t.Errorf("SupportsKind(%v) = false, want true", k)
		}
	}
}
