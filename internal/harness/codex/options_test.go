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
			name: "permission_mode read-only",
			raw:  map[string]any{"permission_mode": "read-only"},
			want: Options{PermissionMode: "read-only"},
		},
		{
			name: "permission_mode workspace-write",
			raw:  map[string]any{"permission_mode": "workspace-write"},
			want: Options{PermissionMode: "workspace-write"},
		},
		{
			name: "permission_mode danger-full-access",
			raw:  map[string]any{"permission_mode": "danger-full-access"},
			want: Options{PermissionMode: "danger-full-access"},
		},
		{
			name: "permission_mode approve-for-me",
			raw:  map[string]any{"permission_mode": "approve-for-me"},
			want: Options{PermissionMode: "approve-for-me"},
		},
		{
			name:    "permissions invalid value",
			raw:     map[string]any{"permission_mode": "yolo"},
			wantErr: `permission_mode "yolo" is not valid (use read-only, workspace-write, danger-full-access, or approve-for-me)`,
		},
		{
			name:    "permissions wrong type",
			raw:     map[string]any{"permission_mode": 5},
			wantErr: `option "permission_mode" must be a string, got int`,
		},
		{
			name:    "sandbox renamed",
			raw:     map[string]any{"sandbox": "workspace-write"},
			wantErr: `option "sandbox" is not supported: renamed to "permission_mode" (use read-only, workspace-write, danger-full-access, or approve-for-me)`,
		},
		{
			name:    "approval unsupported",
			raw:     map[string]any{"approval": "never"},
			wantErr: `option "approval" is not supported: use "permission_mode: approve-for-me" for auto-reviewed approvals, or leave unset for approval policy "never" (unattended sessions)`,
		},
		{
			name:    "append_system_prompt unsupported",
			raw:     map[string]any{"append_system_prompt": "x"},
			wantErr: `option "append_system_prompt" is not supported: codex has no append-system-prompt equivalent (use the workspace AGENTS.md)`,
		},
		{
			name:    "unknown key",
			raw:     map[string]any{"foo": "bar"},
			wantErr: `unknown option "foo" (valid: permission_mode)`,
		},
		{
			name:    "two bad keys fail on lexicographically first",
			raw:     map[string]any{"zzz": "bar", "approval": "never"},
			wantErr: `option "approval" is not supported: use "permission_mode: approve-for-me" for auto-reviewed approvals, or leave unset for approval policy "never" (unattended sessions)`,
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
