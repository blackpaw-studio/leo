package daemon

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blackpaw-studio/leo/internal/config"
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

func TestHandleTaskAdd(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskAddRequest{
		Name:       "daily",
		Schedule:   "0 9 * * *",
		PromptFile: "daily.md",
		Enabled:    true,
	})

	resp, err := client.Post("http://localhost/task/add", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/add error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	task, ok := cfg.Tasks["daily"]
	if !ok {
		t.Fatal("expected task 'daily' to exist in config")
	}
	if task.Schedule != "0 9 * * *" {
		t.Errorf("expected schedule %q, got %q", "0 9 * * *", task.Schedule)
	}
	if task.PromptFile != "daily.md" {
		t.Errorf("expected prompt_file %q, got %q", "daily.md", task.PromptFile)
	}
	if !task.Enabled {
		t.Error("expected task to be enabled")
	}
}

func TestHandleTaskRemove(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if _, ok := cfg.Tasks["heartbeat"]; ok {
		t.Error("expected task 'heartbeat' to be removed from config")
	}
}

func TestHandleTaskRemoveNotFound(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "nonexistent"})

	resp, err := client.Post("http://localhost/task/remove", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/remove error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected status 404, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if result.OK {
		t.Error("expected OK=false for not found task")
	}
}

func TestHandleTaskEnable(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	// Write config with heartbeat disabled
	cfgYAML := `
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
    enabled: false
`
	cfgPath := filepath.Join(dir, "leo.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfgYAML), 0600); err != nil {
		t.Fatalf("writing test config: %v", err)
	}

	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/enable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/enable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if !cfg.Tasks["heartbeat"].Enabled {
		t.Error("expected task 'heartbeat' to be enabled after POST /task/enable")
	}
}

func TestHandleTaskDisable(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir) // heartbeat is enabled: true
	_, client := startTestServer(t, cfgPath)

	body, _ := json.Marshal(TaskNameRequest{Name: "heartbeat"})

	resp, err := client.Post("http://localhost/task/disable", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST /task/disable error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("loading config: %v", err)
	}
	if cfg.Tasks["heartbeat"].Enabled {
		t.Error("expected task 'heartbeat' to be disabled after POST /task/disable")
	}
}

func TestHandleTaskList(t *testing.T) {
	dir, err := os.MkdirTemp("", "leo-handler-test-*")
	if err != nil {
		t.Fatalf("creating temp dir: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })

	cfgPath := writeTestConfig(t, dir)
	_, client := startTestServer(t, cfgPath)

	resp, err := client.Get("http://localhost/task/list")
	if err != nil {
		t.Fatalf("GET /task/list error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("expected status 200, got %d", resp.StatusCode)
	}

	var result Response
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decoding response: %v", err)
	}
	if !result.OK {
		t.Errorf("expected OK=true, got OK=false (error: %s)", result.Error)
	}

	var tasks map[string]config.TaskConfig
	if err := json.Unmarshal(result.Data, &tasks); err != nil {
		t.Fatalf("unmarshaling tasks: %v", err)
	}

	if _, ok := tasks["heartbeat"]; !ok {
		t.Error("expected 'heartbeat' task in list response")
	}
}
