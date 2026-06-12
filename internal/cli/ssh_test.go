package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSSHControlOptsLocalhost(t *testing.T) {
	if got := sshControlOpts(config.HostResolution{Localhost: true}); got != nil {
		t.Errorf("localhost control opts = %v, want nil", got)
	}
}

func TestSSHControlOptsRemoteCreatesDirAndReturnsOpts(t *testing.T) {
	dir := t.TempDir()
	ctl := filepath.Join(dir, "remotes", "prod.ctl")
	res := config.HostResolution{Name: "prod", ControlPath: ctl}

	got := sshControlOpts(res)
	want := []string{"-o", "ControlMaster=auto", "-o", "ControlPath=" + ctl}
	if !equalStrings(got, want) {
		t.Errorf("control opts = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Dir(ctl)); err != nil {
		t.Errorf("expected control dir to be created: %v", err)
	}
}
