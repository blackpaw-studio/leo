package mcp

import (
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in         string
		wantAgent  string
		wantHost   string
		wantRemote bool
		wantErr    string
	}{
		{in: "rocket", wantAgent: "rocket"},
		{in: "dispatch:abc123", wantAgent: "dispatch:abc123"},
		{in: "rocket@laptop", wantAgent: "rocket", wantHost: "laptop", wantRemote: true},
		{in: "a-b.c_d@my-host", wantAgent: "a-b.c_d", wantHost: "my-host", wantRemote: true},
		{in: "@laptop", wantErr: "agent name"},
		{in: "rocket@", wantErr: "host name"},
		{in: "@", wantErr: "agent name"},
		{in: "a@b@c", wantErr: "exactly one '@'"},
		{in: "rocket@lap top", wantErr: "whitespace"},
		{in: " rocket@laptop", wantErr: "whitespace"},
		{in: "rocket@laptop\n", wantErr: "whitespace"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			agent, host, remote, err := parseTarget(tc.in)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("parseTarget(%q) err = %v, want containing %q", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseTarget(%q) unexpected error: %v", tc.in, err)
			}
			if agent != tc.wantAgent || host != tc.wantHost || remote != tc.wantRemote {
				t.Fatalf("parseTarget(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, agent, host, remote, tc.wantAgent, tc.wantHost, tc.wantRemote)
			}
		})
	}
}
