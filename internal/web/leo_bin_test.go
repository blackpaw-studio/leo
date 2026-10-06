package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/blackpaw-studio/leo/internal/config"
	"github.com/blackpaw-studio/leo/internal/cron"
	"github.com/blackpaw-studio/leo/internal/leomcp"
)

// fakeLeo writes an executable stub, under a directory with a space in its
// name, that records its argv (one per line) to the returned file.
func fakeLeo(t *testing.T) (bin, argvFile string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "my leo")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	argvFile = filepath.Join(dir, "argv")
	bin = filepath.Join(dir, "leo")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argvFile + ".tmp' && mv '" + argvFile + ".tmp' '" + argvFile + "'\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, argvFile
}

func waitArgv(t *testing.T, argvFile string) []string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(argvFile); err == nil {
			return strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("injected leo binary never ran (no %s)", argvFile)
	return nil
}

// TestManualTaskRunExecsInjectedLeoBin asserts both manual-run endpoints
// spawn `leo run` from the daemon's own binary, not a leo found on PATH.
func TestManualTaskRunExecsInjectedLeoBin(t *testing.T) {
	for _, tc := range []struct {
		name    string
		handler func(*Server) http.HandlerFunc
	}{
		{"web", func(s *Server) http.HandlerFunc { return s.handleTaskRun }},
		{"api", func(s *Server) http.HandlerFunc { return s.handleAPITaskRun }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, argvFile := fakeLeo(t)
			dir := t.TempDir()
			cfgPath := filepath.Join(dir, "leo.yaml")
			cfg := &config.Config{Tasks: map[string]config.TaskConfig{
				"nightly": {Schedule: "0 3 * * *", PromptFile: "nightly.md"},
			}}
			if err := config.Save(cfgPath, cfg); err != nil {
				t.Fatal(err)
			}
			s := New(cfgPath, &mockProcesses{states: map[string]ProcessStateInfo{}},
				&mockScheduler{entries: []cron.EntryInfo{}}, &mockReloader{}, nil,
				Options{Port: testPort, APIToken: testAPIToken, LeoMCP: leomcp.Server{Bin: bin}})

			req := httptest.NewRequest(http.MethodPost, "/", nil)
			req.SetPathValue("name", "nightly")
			rec := httptest.NewRecorder()
			tc.handler(s)(rec, req)

			want := []string{"run", "nightly", "--config", cfgPath}
			if got := waitArgv(t, argvFile); !reflect.DeepEqual(got, want) {
				t.Errorf("spawned leo argv = %q, want %q", got, want)
			}
		})
	}
}
