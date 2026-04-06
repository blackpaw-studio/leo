package daemon

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/cron"
)

const testConfigYAML = `
agent:
  name: test-agent
  workspace: /tmp/test-workspace
defaults:
  model: sonnet
  max_turns: 10
tasks:
  heartbeat:
    schedule: "0 * * * *"
    prompt_file: heartbeat.md
    enabled: true
`

func writeTestConfig(t *testing.T, dir string) string {
	t.Helper()
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(testConfigYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}
	return cfgPath
}

func startTestServer(t *testing.T, cfgPath string) (*Server, *http.Client) {
	t.Helper()
	sockPath := tmpSockPath(t, "h.sock")
	s := New(sockPath, cfgPath)
	if err := s.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	t.Cleanup(func() { s.Shutdown() }) //nolint:errcheck
	return s, socketHTTPClient(sockPath)
}

func TestHandleCronInstall(t *testing.T) {
	// Stub cron read/write
	origRead := cron.ExportReadCrontab()
	origWrite := cron.ExportWriteCrontab()
	t.Cleanup(func() {
		cron.SetReadCrontab(origRead)
		cron.SetWriteCrontab(origWrite)
	})

	var written string
	cron.SetReadCrontab(func() (string, error) { return "", nil })
	cron.SetWriteCrontab(func(content string) error {
		written = content
		return nil
	})

	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)

	// Override PATH so exec.LookPath("leo") resolves to a dummy binary.
	leoFake := filepath.Join(dir, "leo")
	if err := os.WriteFile(leoFake, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("creating fake leo binary: %v", err)
	}
	origPATH := os.Getenv("PATH")
	os.Setenv("PATH", dir+":"+origPATH)
	t.Cleanup(func() { os.Setenv("PATH", origPATH) })

	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/cron/install", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/install error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}

	if !strings.Contains(written, "LEO:test-agent") {
		t.Errorf("expected crontab to contain LEO:test-agent marker, got: %q", written)
	}
}

func TestHandleCronRemove(t *testing.T) {
	origRead := cron.ExportReadCrontab()
	origWrite := cron.ExportWriteCrontab()
	t.Cleanup(func() {
		cron.SetReadCrontab(origRead)
		cron.SetWriteCrontab(origWrite)
	})

	initialCrontab := "# === LEO:test-agent — DO NOT EDIT ===\n0 * * * * leo run heartbeat\n# === END LEO:test-agent ===\n"
	var written string
	cron.SetReadCrontab(func() (string, error) { return initialCrontab, nil })
	cron.SetWriteCrontab(func(content string) error {
		written = content
		return nil
	})

	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Post("http://localhost/cron/remove", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /cron/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}

	if strings.Contains(written, "LEO:test-agent") {
		t.Errorf("expected crontab marker to be removed, but still present in: %q", written)
	}
}

func TestHandleCronList(t *testing.T) {
	origRead := cron.ExportReadCrontab()
	origWrite := cron.ExportWriteCrontab()
	t.Cleanup(func() {
		cron.SetReadCrontab(origRead)
		cron.SetWriteCrontab(origWrite)
	})

	existingBlock := "# === LEO:test-agent — DO NOT EDIT ===\n0 * * * * leo run heartbeat\n# === END LEO:test-agent ==="
	cron.SetReadCrontab(func() (string, error) { return existingBlock + "\n", nil })
	cron.SetWriteCrontab(func(content string) error { return nil })

	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Get("http://localhost/cron/list")
	if err != nil {
		t.Fatalf("GET /cron/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var body Response
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !body.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", body.Error)
	}

	var data map[string]string
	if err := json.Unmarshal(body.Data, &data); err != nil {
		t.Fatalf("unmarshaling data: %v", err)
	}

	entries, ok := data["entries"]
	if !ok {
		t.Fatal("expected 'entries' key in response data")
	}
	if !strings.Contains(entries, "LEO:test-agent") {
		t.Errorf("expected entries to contain LEO:test-agent, got: %q", entries)
	}
}
