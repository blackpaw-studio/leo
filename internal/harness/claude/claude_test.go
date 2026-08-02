package claude

import (
	"fmt"
	"reflect"
	"testing"
	"time"

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

// TestValidateModel: claude accepts aliases and full model IDs, and that set
// moves server-side faster than leo ships, so validation is a format check
// only. Anything without whitespace passes; the claude CLI is the authority
// on whether the name resolves.
func TestValidateModel(t *testing.T) {
	tests := []struct {
		model   string
		wantErr bool
	}{
		{"", false}, {"sonnet", false}, {"opus", false}, {"haiku", false},
		{"sonnet[1m]", false}, {"opus[1m]", false},
		// Aliases and full IDs released after any given leo build.
		{"fable", false}, {"claude-fable-5", false}, {"claude-opus-5", false},
		{"claude-sonnet-4-5-20250929", false},
		// Third-party endpoints (ANTHROPIC_BASE_URL) name models freely.
		{"qwen/qwen3.6-35b-a3b", false},
		// Only shapes that can never be a model name are rejected.
		{"claude opus", true}, {"opus\t1m", true},
	}
	for _, tt := range tests {
		err := Claude{}.ValidateModel(tt.model)
		if (err != nil) != tt.wantErr {
			t.Errorf("ValidateModel(%q) err=%v, wantErr=%v", tt.model, err, tt.wantErr)
		}
		if tt.wantErr && err.Error() != fmt.Sprintf("%q is not valid (must not contain whitespace)", tt.model) {
			t.Errorf("ValidateModel(%q) wrong message: %v", tt.model, err)
		}
	}
}

// TestSuggestedModels: the datalist is a hint, not a gate. It must stay
// non-empty and every entry must pass ValidateModel.
func TestSuggestedModels(t *testing.T) {
	got := SuggestedModels()
	if len(got) == 0 {
		t.Fatal("SuggestedModels() is empty")
	}
	var hasFable bool
	for _, m := range got {
		if err := (Claude{}).ValidateModel(m); err != nil {
			t.Errorf("SuggestedModels() offers %q but ValidateModel rejects it: %v", m, err)
		}
		if m == "fable" {
			hasFable = true
		}
	}
	if !hasFable {
		t.Error("SuggestedModels() does not include \"fable\"")
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
		opts Options
		want map[string]string
	}{
		{"task", harness.KindTask, Options{}, map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli"}},
		{"agent", harness.KindAgent, Options{}, nil},
		{
			// The leo MCP server hosts tools (leo_consult) that legitimately
			// outrun Claude Code's default per-tool MCP deadline, so a wired
			// bridge raises the ceiling. The knob is process-global — Claude
			// Code has no per-server form.
			name: "agent with leo MCP bridge raises the tool ceiling",
			kind: harness.KindAgent,
			opts: Options{LeoMCPArgs: []string{"--mcp-config", "/leo/state/leo-mcp.json"}, LeoMCPToolTimeout: 32 * time.Minute},
			want: map[string]string{"MCP_TOOL_TIMEOUT": "1920000"},
		},
		{
			name: "task with leo MCP bridge keeps the entrypoint marker",
			kind: harness.KindTask,
			opts: Options{LeoMCPArgs: []string{"--mcp-config", "/leo/state/leo-mcp.json"}, LeoMCPToolTimeout: 32 * time.Minute},
			want: map[string]string{"CLAUDE_CODE_ENTRYPOINT": "cli", "MCP_TOOL_TIMEOUT": "1920000"},
		},
		{
			// No bridge wired (e.g. a consultant subprocess): leave Claude
			// Code's own default alone rather than raising it globally.
			name: "no bridge leaves the ceiling untouched",
			kind: harness.KindAgent,
			opts: Options{LeoMCPToolTimeout: 32 * time.Minute},
			want: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Claude{}.Env(harness.LaunchSpec{Kind: tt.kind, Options: tt.opts})
			if err != nil {
				t.Fatalf("Env(%v): unexpected error %v", tt.kind, err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Env(%v) = %v, want %v", tt.kind, got, tt.want)
			}
		})
	}
}

// TestClaudeEnvRejectsForeignOptions mirrors Args: a spec carrying another
// adapter's options is a wiring bug, not something to silently ignore.
func TestClaudeEnvRejectsForeignOptions(t *testing.T) {
	if _, err := (Claude{}).Env(harness.LaunchSpec{Kind: harness.KindTask, Options: struct{}{}}); err == nil {
		t.Fatal("Env with foreign options: want error, got nil")
	}
}

func TestClaudeSupportsKind(t *testing.T) {
	for _, k := range []harness.Kind{harness.KindAgent, harness.KindTask} {
		if !(Claude{}.SupportsKind(k)) {
			t.Errorf("SupportsKind(%v) = false, want true", k)
		}
	}
}
