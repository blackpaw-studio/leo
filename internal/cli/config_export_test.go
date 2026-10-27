package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func runConfigCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newConfigCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

// TestConfigExportFollowRunsLocally: the export describes this machine even
// when the CLI would otherwise target a remote (default_host, LEO_HOST).
func TestConfigExportFollowRunsLocally(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "leo.yaml")
	yaml := "client:\n  default_host: remote\n  hosts:\n    remote: {ssh: evan@10.0.0.9}\ntemplates:\n  coder:\n    workspace: ~/work\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	t.Setenv("LEO_HOST", "remote")
	stub := withStubExec(t)

	out, err := runConfigCmd(t, "export", "--follow")
	if err != nil {
		t.Fatalf("export: %v\n%s", err, out)
	}
	if len(stub.calls) != 0 {
		t.Fatalf("export ran a remote command: %v", stub.calls)
	}
	var exp config.FollowExport
	if err := json.Unmarshal([]byte(out), &exp); err != nil {
		t.Fatalf("stdout is not one export: %v\n%s", err, out)
	}
	if exp.Kind != config.FollowExportKind || exp.Templates["coder"] == nil {
		t.Fatalf("export = %+v", exp)
	}
}

func TestConfigExportRequiresFollowFlag(t *testing.T) {
	_, err := runConfigCmd(t, "export")
	if err == nil || !strings.Contains(err.Error(), "--follow") {
		t.Fatalf("err = %v, want a --follow hint", err)
	}
}

func TestConfigExportFollowRefusalCarriesCode(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, "leo.yaml")
	if err := os.WriteFile(path, []byte("client:\n  hosts:\n    up: {ssh: evan@10.0.0.1}\nfollow:\n  host: up\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	old := cfgFile
	cfgFile = path
	t.Cleanup(func() { cfgFile = old })
	_, err := runConfigCmd(t, "export", "--follow")
	if err == nil || !strings.HasPrefix(err.Error(), "lead_is_follower: ") {
		t.Fatalf("err = %v, want lead_is_follower", err)
	}
}
