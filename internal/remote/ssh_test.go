package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
)

func TestSSHArgvShape(t *testing.T) {
	ctl := filepath.Join(t.TempDir(), "remotes", "prod.ctl")
	res := config.HostResolution{Name: "prod", ControlPath: ctl,
		Host: config.HostConfig{SSH: "alice@prod", SSHArgs: []string{"-p", "2222"}}}

	got := SSHArgv(res, false, "leo", "agent", "list")
	want := []string{"alice@prod", "-p", "2222", "-o", "ControlMaster=auto", "-o", "ControlPath=" + ctl, "leo", "agent", "list"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SSHArgv = %v, want %v", got, want)
	}
	if _, err := os.Stat(filepath.Dir(ctl)); err != nil {
		t.Fatalf("control dir not created: %v", err)
	}
	batch := SSHArgv(config.HostResolution{Host: config.HostConfig{SSH: "u@h"}}, true, "x")
	if want := []string{"-o", "BatchMode=yes", "u@h", "x"}; !reflect.DeepEqual(batch, want) {
		t.Fatalf("batch SSHArgv = %v, want %v", batch, want)
	}
	// The returned slice never aliases the config's SSHArgs.
	got[1] = "mutated"
	if res.Host.SSHArgs[0] != "-p" {
		t.Fatal("SSHArgv aliased HostConfig.SSHArgs")
	}
}
