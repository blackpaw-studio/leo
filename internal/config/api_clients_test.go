package config

import (
	"strings"
	"testing"
)

// TestAPIClientsValidation: a client token lives outside Leo's trust boundary,
// so a malformed entry must fail loudly at load rather than resolve to
// something permissive at request time.
func TestAPIClientsValidation(t *testing.T) {
	tests := []struct {
		name    string
		clients map[string]APIClientConfig
		wantErr string
	}{
		{
			name:    "valid",
			clients: map[string]APIClientConfig{"docker-scout": {CanMessage: []string{"rocket", "scout-*"}}},
		},
		{
			name:    "empty can_message denies everything, which is never what was meant",
			clients: map[string]APIClientConfig{"docker-scout": {}},
			wantErr: "can_message",
		},
		{
			name:    "post-only client is valid",
			clients: map[string]APIClientConfig{"pipe": {CanPost: []string{"pipe-*"}}},
		},
		{
			name:    "neither scope names both fields",
			clients: map[string]APIClientConfig{"pipe": {}},
			wantErr: "can_message or can_post",
		},
		{
			name:    "blank can_post entry",
			clients: map[string]APIClientConfig{"pipe": {CanPost: []string{"ok", " "}}},
			wantErr: "can_post[1] must not be empty",
		},
		{
			name:    "malformed can_post glob",
			clients: map[string]APIClientConfig{"pipe": {CanPost: []string{"pipe-["}}},
			wantErr: "can_post[0]",
		},
		{
			name:    "malformed glob",
			clients: map[string]APIClientConfig{"docker-scout": {CanMessage: []string{"scout-["}}},
			wantErr: "not a valid pattern",
		},
		{
			name:    "blank target",
			clients: map[string]APIClientConfig{"docker-scout": {CanMessage: []string{"  "}}},
			wantErr: "must not be empty",
		},
		{
			name:    "name with a path separator would break its token file",
			clients: map[string]APIClientConfig{"../../etc/passwd": {CanMessage: []string{"rocket"}}},
			wantErr: "api_clients",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &Config{APIClients: tt.clients, Tasks: map[string]TaskConfig{}}
			err := cfg.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Validate() = nil, want an error mentioning %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Validate() = %q, want it to mention %q", err.Error(), tt.wantErr)
			}
		})
	}
}
