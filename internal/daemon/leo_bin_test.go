package daemon

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// TestSchedulerExecsInjectedLeoBin fires a real cron entry and asserts the
// spawned `leo run` is the daemon's own binary (WithLeoMCP), not a leo
// found on PATH.
func TestSchedulerExecsInjectedLeoBin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my leo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile := filepath.Join(dir, "argv")
	bin := filepath.Join(dir, "leo")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argvFile + ".tmp' && mv '" + argvFile + ".tmp' '" + argvFile + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(dir, "leo.yaml")

	s := New(tmpSockPath(t, "bin.sock"), cfgPath, nil, WithLeoMCP(leomcp.Server{Bin: bin}))
	cfg := &config.Config{Tasks: map[string]config.TaskConfig{
		"tick": {Schedule: "@every 1s", PromptFile: "tick.md", Enabled: true},
	}}
	if err := s.scheduler.Install(cfg); err != nil {
		t.Fatalf("Install: %v", err)
	}
	s.scheduler.Start()
	t.Cleanup(s.scheduler.Stop)

	var raw []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		if raw, err = os.ReadFile(argvFile); err == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if raw == nil {
		t.Fatalf("cron never ran the injected leo binary (no %s)", argvFile)
	}
	got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
	if want := []string{"run", "tick", "--config", cfgPath}; !reflect.DeepEqual(got, want) {
		t.Errorf("cron leo argv = %q, want %q", got, want)
	}
}
