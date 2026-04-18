package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveURLBind(t *testing.T) {
	cases := []struct {
		name     string
		flag     string
		cfg      string
		allowed  []string
		wantHost string
		wantNote bool
		wantErr  bool
	}{
		{"flag_wins", "10.1.1.1", "0.0.0.0", nil, "10.1.1.1", false, false},
		{"loopback_cfg", "", "127.0.0.1", nil, "127.0.0.1", false, false},
		{"loopback_cfg_with_hosts", "", "127.0.0.1", []string{"leo.local"}, "127.0.0.1", false, false},
		{"nonloopback_uses_allowed_hosts_0", "", "0.0.0.0", []string{"10.0.4.16", "leo.local"}, "10.0.4.16", true, false},
		{"nonloopback_no_hosts_errors", "", "0.0.0.0", nil, "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host, note, err := resolveURLBind(tc.flag, tc.cfg, tc.allowed)
			if tc.wantErr && err == nil {
				t.Fatal("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantErr && host != tc.wantHost {
				t.Fatalf("host = %q, want %q", host, tc.wantHost)
			}
			if tc.wantNote && note == "" {
				t.Fatal("expected a note, got empty string")
			}
			if !tc.wantNote && note != "" {
				t.Fatalf("expected no note, got %q", note)
			}
		})
	}
}

func TestWebLoginURL(t *testing.T) {
	dir := t.TempDir()
	// Tighten perms so EnsureAPIToken is happy.
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(dir, "api.token")
	if err := os.WriteFile(tokenPath, []byte("abc123\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := newWebLoginURLCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--state-dir", dir, "--bind", "10.0.4.16", "--port", "8370"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.TrimSpace(out.String())
	want := "http://10.0.4.16:8370/login?token=abc123"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestWebLoginURL_TokenGeneratedIfMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	cmd := newWebLoginURLCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--state-dir", dir, "--bind", "127.0.0.1", "--port", "8370"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	got := strings.TrimSpace(out.String())
	if !strings.HasPrefix(got, "http://127.0.0.1:8370/login?token=") {
		t.Fatalf("unexpected output: %q", got)
	}
	// Token file should now exist with 64 hex chars.
	data, err := os.ReadFile(filepath.Join(dir, "api.token"))
	if err != nil {
		t.Fatalf("token file not created: %v", err)
	}
	tok := strings.TrimSpace(string(data))
	if len(tok) != 64 {
		t.Fatalf("token len = %d, want 64", len(tok))
	}
}
