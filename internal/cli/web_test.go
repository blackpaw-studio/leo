package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
