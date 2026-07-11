package opencode

import (
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness/schematest"
)

func TestDecodeOptionsPermissionFlat(t *testing.T) {
	raw := map[string]any{
		"permission": map[string]any{
			"bash":     "deny",
			"edit":     "allow",
			"webfetch": "ask",
		},
	}
	got, err := Opencode{}.DecodeOptions(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts, ok := got.(Options)
	if !ok {
		t.Fatalf("got %T, want Options", got)
	}
	if opts.Permission["bash"] != "deny" || opts.Permission["edit"] != "allow" || opts.Permission["webfetch"] != "ask" {
		t.Errorf("permission = %+v", opts.Permission)
	}
}

func TestDecodeOptionsPermissionPatternMap(t *testing.T) {
	raw := map[string]any{
		"permission": map[string]any{
			"bash": map[string]any{"git *": "allow", "*": "ask"},
		},
	}
	got, err := Opencode{}.DecodeOptions(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	opts := got.(Options)
	patterns, ok := opts.Permission["bash"].(map[string]any)
	if !ok {
		t.Fatalf("got %T, want map[string]any", opts.Permission["bash"])
	}
	if patterns["git *"] != "allow" || patterns["*"] != "ask" {
		t.Errorf("patterns = %+v", patterns)
	}
}

func TestDecodeOptionsErrors(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]any
		wantErr string
	}{
		{
			name:    "bad leaf value",
			raw:     map[string]any{"permission": map[string]any{"bash": "maybe"}},
			wantErr: `permission value "maybe" for "bash" is not valid (use allow, ask, or deny)`,
		},
		{
			name:    "bad nested leaf value",
			raw:     map[string]any{"permission": map[string]any{"bash": map[string]any{"git *": "maybe"}}},
			wantErr: `permission value "maybe" for "git *" is not valid (use allow, ask, or deny)`,
		},
		{
			name:    "non-string non-map leaf",
			raw:     map[string]any{"permission": map[string]any{"bash": 1}},
			wantErr: `option "permission" values must be "allow"/"ask"/"deny" or a pattern map, got int for "bash"`,
		},
		{
			name:    "permission not a map",
			raw:     map[string]any{"permission": "x"},
			wantErr: `option "permission" must be a map, got string`,
		},
		{
			name:    "append_system_prompt unsupported",
			raw:     map[string]any{"append_system_prompt": "x"},
			wantErr: `option "append_system_prompt" is not supported: opencode has no append-system-prompt equivalent (use AGENTS.md or the instructions config)`,
		},
		{
			name:    "unknown key",
			raw:     map[string]any{"foo": "bar"},
			wantErr: `unknown option "foo" (valid: permission)`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Opencode{}.DecodeOptions(tt.raw)
			if err == nil {
				t.Fatalf("expected error %q, got nil", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestDecodeOptionsNilMap(t *testing.T) {
	got, err := Opencode{}.DecodeOptions(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reflect.DeepEqual(got.(Options), Options{}) {
		t.Errorf("got %+v, want zero Options", got)
	}
}

func TestOptionsSchemaMatchesDecodeOptions(t *testing.T) {
	schematest.Run(t, Opencode{}, optionKeys, map[string]any{
		"permission": map[string]any{"bash": "allow"},
	})
}
