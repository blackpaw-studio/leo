package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/harness"
)

func TestSessionEnvArgsCarriesTheLaunchBriefSwitch(t *testing.T) {
	on := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, brief: true}, nil), " ")
	off := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent}, nil), " ")

	if !strings.Contains(on, "-e LEO_BRIEF=1") {
		t.Errorf("env with briefs on = %s, want LEO_BRIEF=1", on)
	}
	if strings.Contains(off, "LEO_BRIEF") {
		t.Errorf("env with briefs off = %s, want no LEO_BRIEF", off)
	}
}

func TestSessionEnvArgsIgnoresSpecEnvBriefVar(t *testing.T) {
	args := strings.Join(sessionEnvArgs("/tmux", ProcessSpec{Name: "leo-a", Kind: harness.KindAgent, Env: map[string]string{"LEO_BRIEF": "1"}}, nil), " ")
	if strings.Contains(args, "LEO_BRIEF") {
		t.Errorf("env = %s, want leo's own switch to be the only source", args)
	}
}

func TestSupervisorReadsTheBriefSwitchFromConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "leo.yaml")
	sv := &Supervisor{configPath: path}
	if sv.briefEnabled() {
		t.Error("no config must read as off")
	}
	if err := os.WriteFile(path, []byte("brief:\n  enabled: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !sv.briefEnabled() {
		t.Error("brief.enabled: true must read as on")
	}
}
