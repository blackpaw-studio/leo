package claude

import (
	"reflect"
	"testing"
)

func TestDecodeOptions(t *testing.T) {
	tests := []struct {
		name    string
		raw     map[string]any
		want    Options
		wantErr string
	}{
		{
			name: "nil map decodes to zero Options",
			raw:  nil,
			want: Options{},
		},
		{
			name: "empty map decodes to zero Options",
			raw:  map[string]any{},
			want: Options{},
		},
		{
			name: "permission_mode",
			raw:  map[string]any{"permission_mode": "plan"},
			want: Options{PermissionMode: "plan"},
		},
		{
			name: "bypass_permissions",
			raw:  map[string]any{"bypass_permissions": true},
			want: Options{BypassPermissions: true},
		},
		{
			name: "remote_control",
			raw:  map[string]any{"remote_control": true},
			want: Options{RemoteControl: true},
		},
		{
			name: "agent maps to AgentFile",
			raw:  map[string]any{"agent": "reviewer"},
			want: Options{AgentFile: "reviewer"},
		},
		{
			name: "allowed_tools converts []any to []string",
			raw:  map[string]any{"allowed_tools": []any{"a", "b"}},
			want: Options{AllowedTools: []string{"a", "b"}},
		},
		{
			name: "disallowed_tools converts []any to []string",
			raw:  map[string]any{"disallowed_tools": []any{"a", "b"}},
			want: Options{DisallowedTools: []string{"a", "b"}},
		},
		{
			name: "append_system_prompt",
			raw:  map[string]any{"append_system_prompt": "be nice"},
			want: Options{AppendSystemPrompt: "be nice"},
		},
		{
			name:    "unknown key",
			raw:     map[string]any{"bogus": "x"},
			wantErr: `unknown option "bogus" (valid: agent, allowed_tools, append_system_prompt, bypass_permissions, disallowed_tools, permission_mode, remote_control)`,
		},
		{
			name:    "permission_mode wrong type",
			raw:     map[string]any{"permission_mode": 5},
			wantErr: `option "permission_mode" must be a string, got int`,
		},
		{
			name:    "allowed_tools wrong type",
			raw:     map[string]any{"allowed_tools": "x"},
			wantErr: `option "allowed_tools" must be a list of strings, got string`,
		},
		{
			name:    "remote_control wrong type",
			raw:     map[string]any{"remote_control": "yes"},
			wantErr: `option "remote_control" must be a boolean, got string`,
		},
		{
			name:    "invalid permission_mode value",
			raw:     map[string]any{"permission_mode": "bogus"},
			wantErr: `permission_mode "bogus" is not valid (use acceptEdits, auto, bypassPermissions, default, dontAsk, or plan)`,
		},
		{
			name: "two bad keys errors on lexicographically first",
			raw: map[string]any{
				"zzz_bogus":     "x",
				"allowed_tools": "x",
			},
			wantErr: `option "allowed_tools" must be a list of strings, got string`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Claude{}.DecodeOptions(tt.raw)
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("DecodeOptions(%v): expected error, got nil", tt.raw)
				}
				if err.Error() != tt.wantErr {
					t.Fatalf("DecodeOptions(%v) err = %q, want %q", tt.raw, err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("DecodeOptions(%v): unexpected error: %v", tt.raw, err)
			}
			gotOpts, ok := got.(Options)
			if !ok {
				t.Fatalf("DecodeOptions(%v) returned %T, want Options", tt.raw, got)
			}
			if !reflect.DeepEqual(gotOpts, tt.want) {
				t.Fatalf("DecodeOptions(%v) = %+v, want %+v", tt.raw, gotOpts, tt.want)
			}
		})
	}
}
