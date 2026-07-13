package codex

import (
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness/schematest"
)

func TestDecodeOptions(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]any
		want    Options
		wantErr string
	}{
		{
			name: "sandbox read-only",
			raw:  map[string]any{"sandbox": "read-only"},
			want: Options{Sandbox: "read-only"},
		},
		{
			name: "sandbox workspace-write",
			raw:  map[string]any{"sandbox": "workspace-write"},
			want: Options{Sandbox: "workspace-write"},
		},
		{
			name: "sandbox danger-full-access",
			raw:  map[string]any{"sandbox": "danger-full-access"},
			want: Options{Sandbox: "danger-full-access"},
		},
		{
			name:    "sandbox invalid value",
			raw:     map[string]any{"sandbox": "yolo"},
			wantErr: `sandbox "yolo" is not valid (use read-only, workspace-write, or danger-full-access)`,
		},
		{
			name:    "sandbox wrong type",
			raw:     map[string]any{"sandbox": 5},
			wantErr: `option "sandbox" must be a string, got int`,
		},
		{
			name:    "approval unsupported",
			raw:     map[string]any{"approval": "never"},
			wantErr: `option "approval" is not supported: leo always launches codex with approval policy "never" (unattended sessions)`,
		},
		{
			name:    "append_system_prompt unsupported",
			raw:     map[string]any{"append_system_prompt": "x"},
			wantErr: `option "append_system_prompt" is not supported: codex has no append-system-prompt equivalent (use the workspace AGENTS.md)`,
		},
		{
			name:    "unknown key",
			raw:     map[string]any{"foo": "bar"},
			wantErr: `unknown option "foo" (valid: sandbox)`,
		},
		{
			name:    "two bad keys fail on lexicographically first",
			raw:     map[string]any{"zzz": "bar", "approval": "never"},
			wantErr: `option "approval" is not supported: leo always launches codex with approval policy "never" (unattended sessions)`,
		},
		{
			name: "nil map",
			raw:  nil,
			want: Options{},
		},
		{
			name: "empty map",
			raw:  map[string]any{},
			want: Options{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Codex{}.DecodeOptions(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error %q, got nil", tt.wantErr)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("got error %q, want %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			opts, ok := got.(Options)
			if !ok {
				t.Fatalf("got type %T, want Options", got)
			}
			if opts != tt.want {
				t.Errorf("got %+v, want %+v", opts, tt.want)
			}
		})
	}
}

func TestOptionsSchemaMatchesDecodeOptions(t *testing.T) {
	schematest.Run(t, Codex{}, optionKeys, nil)
}
